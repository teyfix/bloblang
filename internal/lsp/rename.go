package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"go.yaml.in/yaml/v3"
)

func (h *Handler) navigationText(uri protocol.DocumentURI) (string, error) {
	if text, ok := h.documents.Text(uri); ok {
		return text, nil
	}
	path, err := uriToPath(uri)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	return string(b), err
}

type renameUnit struct {
	uri        protocol.DocumentURI
	text, host string
	region     *embeddedRegion
	tree       *tree_sitter.Tree
	symbols    []symbol
}

func sameRenameBinding(a, b *symbol) bool {
	if a == nil || b == nil || a.kind != b.kind {
		return false
	}
	if a.kind == "variable" {
		// Repeated let assignments (including conditional branches) update the
		// same runtime variable within a mapping.
		return a.name == b.name && sameScope(a.scope, b.scope)
	}
	return a.node.Id() == b.node.Id()
}

func (u renameUnit) location(s *symbol) protocol.Location {
	start, end := int(s.node.StartByte()), int(s.node.EndByte())
	raw := u.text[start:end]
	if strings.HasPrefix(raw, "$") {
		start++
	}
	if strings.HasPrefix(raw, "\"") || strings.HasPrefix(raw, "`") {
		start++
		end--
	}
	r := protocol.Range{Start: bytePosition(u.text, start), End: bytePosition(u.text, end)}
	uri := u.uri
	if u.region != nil {
		uri = protocol.DocumentURI(strings.Split(string(uri), "#bloblang-")[0])
		r = u.region.hostRange(u.host, r)
		if end > start {
			// The next decoded position can skip a folded newline or block
			// terminator. End at the last name character, preserving that gap.
			last := u.region.offsets[end-1]
			length := 1
			if u.host[last] == '\\' && u.region.style&yaml.DoubleQuotedStyle != 0 && last+1 < len(u.host) {
				length = 2
				switch u.host[last+1] {
				case 'x':
					length = 4
				case 'u':
					length = 6
				case 'U':
					length = 10
				}
			}
			r.End = bytePosition(u.host, last+length)
		}
	}
	return protocol.Location{URI: uri, Range: r}
}

func (h *Handler) renameUnits(uri protocol.DocumentURI, text string) []renameUnit {
	return h.navigationUnits(uri, text, false)
}

func (h *Handler) navigationUnits(uri protocol.DocumentURI, text string, allowErrors bool) []renameUnit {
	var units []renameUnit
	if yamlDocument(uri) {
		for _, region := range extractRegions(uri, text) {
			units = append(units, renameUnit{uri: region.uri, text: region.text, host: text, region: &region})
		}
	} else {
		units = append(units, renameUnit{uri: uri, text: text})
	}
	for i := range units {
		u := &units[i]
		u.tree, _ = h.parser.Parse(string(u.uri)+":rename", u.text)
		if u.tree != nil && (allowErrors || !u.tree.RootNode().HasError()) {
			u.symbols = symbols(u.tree.RootNode(), u.text)
		}
	}
	return units
}
func closeRenameUnits(units []renameUnit) {
	for _, u := range units {
		if u.tree != nil {
			u.tree.Close()
		}
	}
}
func (h *Handler) renameTarget(p protocol.TextDocumentPositionParams) ([]renameUnit, int, *symbol) {
	return h.navigationTarget(p, false)
}

func (h *Handler) navigationTarget(p protocol.TextDocumentPositionParams, allowErrors bool) ([]renameUnit, int, *symbol) {
	text, err := h.navigationText(p.TextDocument.URI)
	if err != nil {
		return nil, 0, nil
	}
	units := h.navigationUnits(p.TextDocument.URI, text, allowErrors)
	for i := range units {
		u := &units[i]
		pos := p.Position
		if u.region != nil {
			var ok bool
			pos, ok = u.region.localPosition(text, pos)
			if !ok {
				continue
			}
		}
		s := symbolAt(u.symbols, uint(positionByte(u.text, pos)))
		if s == nil {
			continue
		}
		if s.kind == "map" {
			defs, _ := h.definitionForText(u.uri, u.text, pos)
			if len(defs) == 1 {
				return units, i, s
			}
		} else if visibleDefinition(u.symbols, s) != nil {
			return units, i, s
		}
	}
	return units, 0, nil
}
func (h *Handler) PrepareRename(_ context.Context, p *protocol.PrepareRenameParams) (*protocol.PrepareRenameResult, error) {
	units, index, s := h.renameTarget(p.TextDocumentPositionParams)
	defer closeRenameUnits(units)
	if s == nil {
		return nil, nil
	}
	return &protocol.PrepareRenameResult{Range: units[index].location(s).Range, Placeholder: s.name}, nil
}

func (h *Handler) Rename(ctx context.Context, p *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	if !validIdentifier(p.NewName) {
		return nil, fmt.Errorf("use a Bloblang identifier: letters, digits and underscores, starting with a letter or underscore")
	}
	units, index, selected := h.renameTarget(p.TextDocumentPositionParams)
	defer closeRenameUnits(units)
	if selected == nil {
		return nil, fmt.Errorf("no renameable Bloblang binding at this position")
	}
	u := &units[index]
	probe := "let " + p.NewName + " = 1\nroot = $" + p.NewName
	if selected.kind == "lambda" {
		probe = "root = [].map_each(" + p.NewName + " -> " + p.NewName + ")"
	} else if selected.kind == "map" {
		probe = "map " + p.NewName + " { root = this }\nroot = this.apply(\"" + p.NewName + "\")"
	}
	if _, err := h.benv.Parse(probe); err != nil {
		return nil, fmt.Errorf("%q is not a valid %s name: %w", p.NewName, selected.kind, err)
	}
	edits := &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{}}
	add := func(unit *renameUnit, s *symbol) {
		loc := unit.location(s)
		edits.Changes[loc.URI] = append(edits.Changes[loc.URI], protocol.TextEdit{Range: loc.Range, NewText: p.NewName})
	}
	if selected.name == p.NewName {
		return edits, nil
	}
	if selected.kind != "map" {
		target := visibleDefinition(u.symbols, selected)
		changed := append([]symbol(nil), u.symbols...)
		bindings := make([]*symbol, len(u.symbols))
		for i := range u.symbols {
			bindings[i] = visibleDefinition(u.symbols, &u.symbols[i])
			if sameRenameBinding(bindings[i], target) {
				changed[i].name = p.NewName
			}
			if u.symbols[i].declaration && u.symbols[i].kind == target.kind && u.symbols[i].name == p.NewName && sameScope(u.symbols[i].scope, target.scope) {
				return nil, fmt.Errorf("%q is already declared in this scope", p.NewName)
			}
		}
		for i := range changed {
			after := visibleDefinition(changed, &changed[i])
			before := bindings[i]
			if (before == nil) != (after == nil) || before != nil && after != nil && before.node.Id() != after.node.Id() {
				return nil, fmt.Errorf("renaming to %q would change another binding's scope", p.NewName)
			}
			if sameRenameBinding(before, target) {
				add(u, &u.symbols[i])
			}
		}
		return edits, nil
	}
	// Named maps may be imported by unopened files and YAML mappings. Resolve each
	// literal apply target, rather than renaming every matching string in a workspace.
	defs, _ := h.definitionForText(u.uri, u.text, bytePosition(u.text, int(selected.node.StartByte())))
	if len(defs) != 1 {
		return nil, fmt.Errorf("map definition is no longer available")
	}
	target := defs[0]
	candidates, err := h.navigationFiles(ctx)
	if err != nil {
		return nil, err
	}
	candidates[p.TextDocument.URI] = true
	candidates[protocol.DocumentURI(strings.Split(string(target.URI), "#bloblang-")[0])] = true
	for _, uri := range sortedNavigationFiles(candidates) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := h.navigationText(uri)
		if err != nil {
			return nil, err
		}
		err = func() error {
			other := h.renameUnits(uri, text)
			defer closeRenameUnits(other)
			for i := range other {
				unit := &other[i]
				var matches []*symbol
				for j := range unit.symbols {
					s := &unit.symbols[j]
					if s.kind != "map" || s.name != selected.name {
						continue
					}
					found, _ := h.definitionForText(unit.uri, unit.text, bytePosition(unit.text, int(s.node.StartByte())))
					if len(found) == 1 && found[0] == target {
						matches = append(matches, s)
					}
				}
				if len(matches) == 0 {
					continue
				}
				if h.mapNameExists(unit.uri, unit.text, p.NewName, map[protocol.DocumentURI]bool{}) {
					return fmt.Errorf("map %q already exists or is referenced in %s", p.NewName, uri)
				}
				for _, s := range matches {
					add(unit, s)
				}
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}
	return edits, nil
}

func (h *Handler) navigationFiles(ctx context.Context) (map[protocol.DocumentURI]bool, error) {
	files := map[protocol.DocumentURI]bool{}
	supported := func(uri protocol.DocumentURI) bool {
		s := strings.ToLower(string(uri))
		return !strings.Contains(s, "#bloblang-") && (strings.HasSuffix(s, ".blobl") || strings.HasSuffix(s, ".bloblang") || yamlDocument(uri))
	}
	for _, uri := range h.documents.URIs() {
		if supported(uri) {
			files[uri] = true
		}
	}
	roots := append([]string(nil), h.workspaceRoots...)
	if h.workspaceRoot != "" {
		roots = append(roots, h.workspaceRoot)
	}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", ".cache", ".state", ".volumes", ".local", ".history", ".worktrees", "vendor":
					return filepath.SkipDir
				}
			} else if uri := fileURI(path); supported(uri) {
				files[uri] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (h *Handler) mapNameExists(uri protocol.DocumentURI, text, name string, seen map[protocol.DocumentURI]bool) bool {
	if seen[uri] {
		return false
	}
	seen[uri] = true
	tree, err := h.parser.Parse(string(uri)+":rename-collision", text)
	if err != nil || tree == nil {
		return true
	}
	defer tree.Close()
	for _, s := range symbols(tree.RootNode(), text) {
		if s.kind == "map" && s.name == name {
			return true
		}
	}
	collision := false
	walkNodes(tree.RootNode(), func(n *tree_sitter.Node) {
		if n.Kind() != "import_statement" {
			return
		}
		path := n.ChildByFieldName("path")
		if path == nil {
			return
		}
		imported := fileURI(h.resolveImportPath(uri, decodeImportPath(path.Utf8Text([]byte(text)))))
		source, err := h.navigationText(imported)
		if err != nil || h.mapNameExists(imported, source, name, seen) {
			collision = true
		}
	})
	return collision
}
