package lsp

import (
	"context"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang-lsp/internal/fileuri"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"os"
	"path/filepath"
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
				path := strings.Trim(ch.Utf8Text([]byte(text)), "\"`")
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
							next = append(next, h.resolveImportPath(fileURI(path), strings.Trim(c.Utf8Text([]byte(txt)), "\"`")))
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
func (h *Handler) References(ctx context.Context, p *protocol.ReferenceParams) ([]protocol.Location, error) {
	uri := p.TextDocument.URI
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		host, _ := h.documents.Text(uri)
		r, pos, ok := h.regionAt(uri, p.Position)
		if !ok {
			if paths := h.yamlPathDefinition(uri, host, p.Position); len(paths) > 0 {
				return h.fileReferences(paths[0].URI), nil
			}
			return []protocol.Location{}, nil
		}
		cp := *p
		cp.TextDocument.URI = r.uri
		cp.Position = pos
		vals, err := h.References(context.Background(), &cp)
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
	tree, err := h.parser.Parse(string(uri), text)
	if err != nil {
		return nil, nil
	}
	defer tree.Close()
	ss := symbols(tree.RootNode(), text)
	s := symbolAt(ss, uint(positionByte(text, p.Position)))
	if s == nil {
		defs, _ := h.definitionForText(uri, text, p.Position)
		if len(defs) > 0 {
			return h.fileReferences(defs[0].URI), nil
		}
		return []protocol.Location{}, nil
	}
	target := visibleDefinition(ss, s)
	if s.declaration {
		target = s
	}
	var out []protocol.Location
	for _, candidate := range ss {
		if candidate.name != s.name || candidate.kind != s.kind || candidate.declaration && !p.Context.IncludeDeclaration {
			continue
		}
		if s.kind != "map" {
			d := visibleDefinition(ss, &candidate)
			if candidate.declaration {
				d = &candidate
			}
			if d == nil || target == nil || d.node.Id() != target.node.Id() {
				continue
			}
		}
		out = append(out, nodeLocation(uri, text, candidate.node))
	}
	if s.kind == "map" && h.workspaceRoot != "" {
		definition, _ := h.Definition(ctx, &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{TextDocument: p.TextDocument, Position: p.Position}})
		_ = filepath.WalkDir(h.workspaceRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".volumes" || d.Name() == ".local" || d.Name() == ".history" || d.Name() == ".worktrees" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".blobl" && ext != ".bloblang" {
				return nil
			}
			other := fileURI(path)
			if other == uri {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			txt := string(b)
			if open, ok := h.documents.Text(other); ok {
				txt = open
			}

			t, err := h.parser.Parse(string(other), txt)
			if err != nil {
				return nil
			}
			defer t.Close()
			for _, candidate := range symbols(t.RootNode(), txt) {
				if candidate.name != s.name || candidate.kind != "map" || candidate.declaration && !p.Context.IncludeDeclaration {
					continue
				}
				pos := bytePosition(txt, int(candidate.node.StartByte()))
				defs, _ := h.definitionForText(other, txt, pos)
				if len(definition) > 0 && len(defs) > 0 && defs[0] == definition[0] {
					out = append(out, nodeLocation(other, txt, candidate.node))
				}
			}
			return nil
		})
	}
	return out, nil
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

func (h *Handler) fileReferences(target protocol.DocumentURI) []protocol.Location {
	if h.workspaceRoot == "" {
		return nil
	}
	var out []protocol.Location
	_ = filepath.WalkDir(h.workspaceRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".volumes", ".local", ".history", ".worktrees", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".blobl" && ext != ".bloblang" && ext != ".yaml" && ext != ".yml" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		uri := fileURI(path)
		text := string(b)
		if open, ok := h.documents.Text(uri); ok {
			text = open
		}
		if yamlDocument(uri) {
			for _, p := range yamlPaths(text) {
				if fileURI(h.resolveMappingPath(uri, p.path)) == target {
					out = append(out, protocol.Location{URI: uri, Range: protocol.Range{Start: bytePosition(text, p.start), End: bytePosition(text, p.end)}})
				}
			}
			return nil
		}
		tree, err := h.parser.Parse(string(uri)+":references", text)
		if err != nil {
			return nil
		}
		defer tree.Close()
		walkNodes(tree.RootNode(), func(n *tree_sitter.Node) {
			if n.Kind() != "import_statement" {
				return
			}
			for i := uint(0); i < n.NamedChildCount(); i++ {
				arg := n.NamedChild(i)
				if arg.Kind() == "string" {
					name := strings.Trim(arg.Utf8Text([]byte(text)), "\"`")
					if fileURI(h.resolveImportPath(uri, name)) == target {
						out = append(out, nodeLocation(uri, text, arg))
					}
				}
			}
		})
		return nil
	})
	return out
}
