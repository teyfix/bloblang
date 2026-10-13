package lsp

import (
	"context"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang-lsp/internal/fileuri"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"sort"
	"strings"
)

type symbol struct {
	name, kind  string
	node, scope *tree_sitter.Node
	declaration bool
}

func walkNodes(n *tree_sitter.Node, f func(*tree_sitter.Node)) {
	f(n)
	for i := uint(0); i < n.ChildCount(); i++ {
		walkNodes(n.Child(i), f)
	}
}
func scopeOf(n *tree_sitter.Node) *tree_sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == "map_declaration" || p.Kind() == "lambda" || p.Kind() == "source" {
			return p
		}
	}
	return nil
}
func symbols(root *tree_sitter.Node, text string) []symbol {
	var out []symbol
	walkNodes(root, func(n *tree_sitter.Node) {
		switch n.Kind() {
		case "let_assignment", "map_declaration":
			if name := n.ChildByFieldName("name"); name != nil {
				kind := "variable"
				if n.Kind() == "map_declaration" {
					kind = "map"
				}
				out = append(out, symbol{strings.Trim(name.Utf8Text([]byte(text)), "\"`"), kind, name, scopeOf(n), true})
			}
		case "variable_ref":
			name := n.ChildByFieldName("name")
			if name == nil {
				for i := uint(0); i < n.ChildCount(); i++ {
					if n.Child(i).Kind() == "identifier" {
						name = n.Child(i)
						break
					}
				}
			}
			if name != nil {
				out = append(out, symbol{name.Utf8Text([]byte(text)), "variable", n, scopeOf(n), false})
			}
		case "lambda":
			if name := n.ChildByFieldName("param"); name != nil {
				out = append(out, symbol{name.Utf8Text([]byte(text)), "lambda", name, n, true})
			}
		case "identifier":
			// Bare identifiers are lambda references only in expression positions;
			// method names, object keys and field names belong to other namespaces.
			p := n.Parent()
			if p == nil {
				return
			}
			for _, field := range []string{"name", "param", "method", "function", "field", "key", "path"} {
				if c := p.ChildByFieldName(field); c != nil && c.Id() == n.Id() {
					return
				}
			}
			if p.Kind() != "variable_ref" && p.Kind() != "meta_ref" {
				out = append(out, symbol{n.Utf8Text([]byte(text)), "lambda", n, scopeOf(n), false})
			}
		case "method_call":
			method := n.ChildByFieldName("method")
			if method != nil && method.Utf8Text([]byte(text)) == "apply" {
				for i := uint(0); i < n.NamedChildCount(); i++ {
					arg := n.NamedChild(i)
					if obj := n.ChildByFieldName("object"); obj != nil && obj.Id() == arg.Id() {
						continue
					}
					if arg.Id() == method.Id() {
						continue
					}
					if arg.Kind() == "named_argument" {
						arg = arg.ChildByFieldName("value")
					}
					if arg != nil && arg.Kind() == "string" {
						out = append(out, symbol{strings.Trim(arg.Utf8Text([]byte(text)), "\"`"), "map", arg, scopeOf(n), false})
						break
					}
				}
			}

		}
	})
	return out
}
func symbolAt(ss []symbol, offset uint) *symbol {
	for i := range ss {
		if ss[i].node.StartByte() <= offset && offset <= ss[i].node.EndByte() {
			return &ss[i]
		}
	}
	return nil
}
func nodeLocation(uri protocol.DocumentURI, text string, n *tree_sitter.Node) protocol.Location {
	return protocol.Location{URI: uri, Range: protocol.Range{Start: bytePosition(text, int(n.StartByte())), End: bytePosition(text, int(n.EndByte()))}}
}
func sameScope(a, b *tree_sitter.Node) bool { return a != nil && b != nil && a.Id() == b.Id() }
func visibleDefinition(ss []symbol, s *symbol) *symbol {
	if s.declaration {
		return s
	}
	// Prefer the nearest scope before considering declaration order.
	if s.kind != "map" {
		for scope := s.scope; scope != nil; scope = scopeOf(scope) {
			var best *symbol
			for i := range ss {
				d := &ss[i]
				if !d.declaration || d.name != s.name || d.kind != s.kind || !sameScope(scope, d.scope) || d.node.StartByte() > s.node.StartByte() {
					continue
				}
				// A let binding isn't in scope inside its own initializer.
				if p := d.node.Parent(); p.Kind() == "let_assignment" && s.node.EndByte() <= p.EndByte() {
					continue
				}
				if best == nil || d.node.StartByte() > best.node.StartByte() {
					best = d
				}
			}
			if best != nil {
				return best
			}
			if scope.Kind() == "source" || scope.Kind() == "map_declaration" {
				break
			}
		}
		return nil
	}
	for i := range ss {
		d := &ss[i]
		if !d.declaration || d.name != s.name || d.kind != s.kind {
			continue
		}
		return d
	}
	return nil
}
func fileURI(path string) protocol.DocumentURI {
	return protocol.DocumentURI(fileuri.FromPath(path))
}
func (h *Handler) Definition(_ context.Context, p *protocol.DefinitionParams) ([]protocol.Location, error) {
	uri := p.TextDocument.URI
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		host, _ := h.documents.Text(uri)
		if paths := h.yamlPathDefinition(uri, host, p.Position); len(paths) > 0 {
			return paths, nil
		}
		r, pos, ok := h.regionAt(uri, p.Position)
		if !ok {
			return []protocol.Location{}, nil
		}
		cp := *p
		cp.TextDocument.URI = r.uri
		cp.Position = pos
		vals, err := h.Definition(context.Background(), &cp)
		for i := range vals {
			if vals[i].URI == r.uri {
				vals[i].URI = uri
				vals[i].Range = r.hostRange(host, vals[i].Range)
			}
		}
		return vals, err
	}
	text, ok := h.documents.Text(uri)
	if !ok {
		return []protocol.Location{}, nil
	}
	return h.definitionForText(uri, text, p.Position)
}
func (h *Handler) definitionForText(uri protocol.DocumentURI, text string, position protocol.Position) ([]protocol.Location, error) {
	tree, err := h.parser.Parse(string(uri), text)
	if err != nil {
		return nil, nil
	}
	defer tree.Close()
	offset := uint(positionByte(text, position))
	if path, ok := directivePathAt(text, int(offset)); ok {
		return []protocol.Location{{URI: fileURI(h.resolveMappingPath(uri, path)), Range: protocol.Range{}}}, nil
	}
	ss := symbols(tree.RootNode(), text)
	s := symbolAt(ss, offset)
	// Import paths navigate directly to the file.
	var imports []string
	var direct []protocol.Location
	walkNodes(tree.RootNode(), func(n *tree_sitter.Node) {
		if n.Kind() != "import_statement" {
			return
		}
		walkNodes(n, func(ch *tree_sitter.Node) {
			if ch.Kind() == "string" {
				path := decodeImportPath(ch.Utf8Text([]byte(text)))
				abs := h.resolveImportPath(uri, path)
				imports = append(imports, abs)
				if ch.StartByte() <= offset && offset <= ch.EndByte() {
					direct = append(direct, protocol.Location{URI: fileURI(abs), Range: protocol.Range{}})
				}
			}
		})
	})
	if len(direct) > 0 {
		return direct, nil
	}
	if s == nil {
		return []protocol.Location{}, nil
	}
	if s.declaration {
		return []protocol.Location{nodeLocation(uri, text, s.node)}, nil
	}
	if d := visibleDefinition(ss, s); d != nil {
		return []protocol.Location{nodeLocation(uri, text, d.node)}, nil
	}
	if s.kind == "map" {
		visited := map[string]bool{}
		var find func(string) *protocol.Location
		find = func(path string) *protocol.Location {
			if visited[path] {
				return nil
			}
			visited[path] = true
			txt, err := h.navigationText(fileURI(path))
			if err != nil {
				return nil
			}
			t, err := h.parser.Parse(path, txt)
			if err != nil {
				return nil
			}
			defer t.Close()
			for _, sym := range symbols(t.RootNode(), txt) {
				if sym.declaration && sym.kind == "map" && sym.name == s.name {
					loc := nodeLocation(fileURI(path), txt, sym.node)
					return &loc
				}
			}
			var next []string
			walkNodes(t.RootNode(), func(n *tree_sitter.Node) {
				if n.Kind() == "import_statement" {
					for i := uint(0); i < n.ChildCount(); i++ {
						c := n.Child(i)
						if c.Kind() == "string" {
							next = append(next, h.resolveImportPath(fileURI(path), decodeImportPath(c.Utf8Text([]byte(txt)))))
						}
					}
				}
			})
			for _, n := range next {
				if loc := find(n); loc != nil {
					return loc
				}
			}
			return nil
		}
		for _, path := range imports {
			if loc := find(path); loc != nil {
				return []protocol.Location{*loc}, nil
			}
		}
	}
	return []protocol.Location{}, nil
}

// References and rename share binding resolution and candidate discovery, so
// unopened mappings, YAML regions and every workspace root behave consistently.
func (h *Handler) References(ctx context.Context, p *protocol.ReferenceParams) ([]protocol.Location, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	units, index, selected := h.renameTarget(p.TextDocumentPositionParams)
	defer closeRenameUnits(units)
	if selected == nil {
		text, err := h.navigationText(p.TextDocument.URI)
		if err != nil {
			return []protocol.Location{}, nil
		}
		if yamlDocument(p.TextDocument.URI) {
			if paths := h.yamlPathDefinition(p.TextDocument.URI, text, p.Position); len(paths) > 0 {
				return h.fileReferences(ctx, paths[0].URI)
			}
		}
		for _, unit := range units {
			pos := p.Position
			if unit.region != nil {
				var ok bool
				pos, ok = unit.region.localPosition(text, pos)
				if !ok {
					continue
				}
			}
			defs, _ := h.definitionForText(unit.uri, unit.text, pos)
			if len(defs) > 0 {
				return h.fileReferences(ctx, defs[0].URI)
			}
		}
		return []protocol.Location{}, nil
	}
	unit := &units[index]
	out := []protocol.Location{}
	if selected.kind != "map" {
		target := visibleDefinition(unit.symbols, selected)
		for i := range unit.symbols {
			candidate := &unit.symbols[i]
			if candidate.declaration && !p.Context.IncludeDeclaration {
				continue
			}
			if sameRenameBinding(visibleDefinition(unit.symbols, candidate), target) {
				out = append(out, unit.referenceLocation(candidate.node))
			}
		}
		return out, nil
	}
	defs, _ := h.definitionForText(unit.uri, unit.text, bytePosition(unit.text, int(selected.node.StartByte())))
	if len(defs) != 1 {
		return out, nil
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
			continue
		}
		func() {
			other := h.renameUnits(uri, text)
			defer closeRenameUnits(other)
			for _, unit := range other {
				for _, candidate := range unit.symbols {
					if candidate.kind != "map" || candidate.name != selected.name || candidate.declaration && !p.Context.IncludeDeclaration {
						continue
					}
					found, _ := h.definitionForText(unit.uri, unit.text, bytePosition(unit.text, int(candidate.node.StartByte())))
					if len(found) == 1 && found[0] == target {
						out = append(out, unit.referenceLocation(candidate.node))
					}
				}
			}
		}()
	}
	return out, nil
}

func sortedNavigationFiles(files map[protocol.DocumentURI]bool) []protocol.DocumentURI {
	paths := make([]protocol.DocumentURI, 0, len(files))
	for uri := range files {
		paths = append(paths, uri)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	return paths
}

func (u renameUnit) referenceLocation(n *tree_sitter.Node) protocol.Location {
	loc := nodeLocation(u.uri, u.text, n)
	if u.region != nil {
		loc.URI = protocol.DocumentURI(strings.Split(string(u.uri), "#bloblang-")[0])
		loc.Range = u.region.hostRange(u.host, loc.Range)
	}
	return loc
}

func directivePathAt(text string, offset int) (string, bool) {
	cursor := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
		if cursor <= offset && offset <= cursor+len(line) && strings.HasPrefix(trimmed, "#!") {
			parts := strings.SplitN(trimmed, " ", 2)
			if len(parts) == 2 && strings.HasSuffix(parts[0], "_from") {
				return strings.Trim(strings.TrimSpace(parts[1]), "\"'"), true
			}
		}
		cursor += len(line) + 1
	}
	return "", false
}

func (h *Handler) fileReferences(ctx context.Context, target protocol.DocumentURI) ([]protocol.Location, error) {
	files, err := h.navigationFiles(ctx)
	if err != nil {
		return nil, err
	}
	out := []protocol.Location{}
	for _, uri := range sortedNavigationFiles(files) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := h.navigationText(uri)
		if err != nil {
			continue
		}
		if yamlDocument(uri) {
			for _, p := range yamlPaths(text) {
				if fileURI(h.resolveMappingPath(uri, p.path)) == target {
					out = append(out, protocol.Location{URI: uri, Range: protocol.Range{Start: bytePosition(text, p.start), End: bytePosition(text, p.end)}})
				}
			}
		}
		func() {
			units := h.renameUnits(uri, text)
			defer closeRenameUnits(units)
			for _, unit := range units {
				if unit.tree == nil {
					continue
				}
				walkNodes(unit.tree.RootNode(), func(n *tree_sitter.Node) {
					if n.Kind() != "import_statement" {
						return
					}
					path := n.ChildByFieldName("path")
					if path != nil && fileURI(h.resolveImportPath(unit.uri, decodeImportPath(path.Utf8Text([]byte(unit.text))))) == target {
						out = append(out, unit.referenceLocation(path))
					}
				})
			}
		}()
	}
	return out, nil
}
