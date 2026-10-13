package benthos

import (
	"fmt"
	"strings"
	"sync"

	tree_sitter_bloblang "github.com/teyfix/tree-sitter-bloblang/bindings/go"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type parsedTree struct {
	tree *tree_sitter.Tree
	text string
}

type Bloblang struct {
	parser *tree_sitter.Parser
	trees  map[string]*parsedTree
	mu     sync.Mutex
}

func NewBloblang() (*Bloblang, error) {
	language := tree_sitter.NewLanguage(tree_sitter_bloblang.Language())
	parser := tree_sitter.NewParser()

	if err := parser.SetLanguage(language); err != nil {
		parser.Close()
		return nil, err
	}

	return &Bloblang{
		parser: parser,
		trees:  make(map[string]*parsedTree), // initialize the map
	}, nil
}

// Parse takes a Bloblang document string and returns its syntax tree.
//
// The returned tree's root node has kind "source" and contains zero or more
// statement nodes as direct children. Callers must call tree.Close() when done.
//
// Statement node kinds (direct children of source):
//
//   - root_assignment   root[.path]* = <expr>
//   - let_assignment    let <name> = <expr>
//   - meta_assignment   meta [key] = <expr>
//   - map_declaration   map <name> { <statement>* }
//   - import_statement  import "<path>"
//
// Expression node kinds (appear recursively as field "value" or sub-expressions):
//
//   - if_expr            if <cond> { <expr> } [else { <expr> } | else <if_expr>]
//   - match_expr         match [<expr>] { <match_case>* }
//   - match_case           <pattern|catch_all> => <expr>
//   - binary_expr        <expr> (> < >= <= == != && || + - * / % |) <expr>
//   - unary_expr         (! | -) <expr>
//   - method_call        <expr>.<method>(<args...>)
//   - field_access       <expr>.<field>
//   - call_expr          <fn>(<args...>)
//   - lambda             <param> -> <expr>
//   - object_literal     { <pair>* }   where pair holds fields "key" and "value"
//   - array_literal      [ <expr>* ]
//   - parenthesized_expr ( <expr> )
//
// Atom / primary node kinds:
//
//   - this_ref      "this"
//   - meta_ref      @<identifier>
//   - bare_meta_ref "@" with no identifier
//   - variable_ref  $<identifier>
//   - deleted       deleted()
//   - identifier    bare name (field, function, map name, …)
//   - string        "…" or """…"""
//   - number        integer or float
//   - boolean       true | false
//   - catch_all     _ (only inside match_case)
//   - comment       # … (attached as an extra, can appear anywhere)
//
// Named fields on key nodes:
//
//	root_assignment : path*, value
//	let_assignment  : name, value
//	meta_assignment : key (optional), value
//	map_declaration : name
//	if_expr         : condition, consequence, alternative (optional)
//	match_expr      : condition (optional)
//	match_case      : pattern, result
//	binary_expr     : left, operator, right
//	unary_expr      : operator, argument
//	method_call     : object, method
//	field_access    : object, field
//	call_expr       : function
//	lambda          : param, body
//	pair            : key, value
func endPoint(s string) tree_sitter.Point {
	lines := strings.Split(s, "\n")
	row := len(lines) - 1
	col := len(lines[row])
	return tree_sitter.Point{
		Row:    uint(row),
		Column: uint(col),
	}
}

func (b *Bloblang) Parse(uri string, document string) (*tree_sitter.Tree, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.parser == nil {
		return nil, fmt.Errorf("Bloblang parser is closed")
	}

	if cached, ok := b.trees[uri]; ok && cached.text == document {
		return cached.tree.Clone(), nil
	}

	var oldTree *tree_sitter.Tree
	if cached, ok := b.trees[uri]; ok {
		oldTree = cached.tree
		edit := tree_sitter.InputEdit{
			StartByte:      0,
			OldEndByte:     uint(len(cached.text)),
			NewEndByte:     uint(len(document)),
			StartPosition:  tree_sitter.Point{Row: 0, Column: 0},
			OldEndPosition: endPoint(cached.text),
			NewEndPosition: endPoint(document),
		}
		oldTree.Edit(&edit)
	}

	tree := b.parser.Parse([]byte(document), oldTree)
	if tree == nil {
		return nil, fmt.Errorf("could not parse document: tree is nil")
	}

	if oldTree != nil {
		oldTree.Close()
	}

	b.trees[uri] = &parsedTree{
		tree: tree,
		text: document,
	}
	return tree.Clone(), nil
}

func (b *Bloblang) InvalidateDocument(uri string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for key, cached := range b.trees {
		if DocumentKeyMatches(key, uri) {
			cached.tree.Close()
			delete(b.trees, key)
		}
	}
}

// DocumentKeyMatches includes the parser's derived documents and YAML regions.
func DocumentKeyMatches(key, uri string) bool {
	return key == uri || strings.HasPrefix(key, uri+":") || strings.HasPrefix(key, uri+"#bloblang-")
}

// Clear releases cached trees while keeping the parser available for reuse.
func (b *Bloblang) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clear()
}

func (b *Bloblang) clear() {
	for k, cached := range b.trees {
		if cached.tree != nil {
			cached.tree.Close()
		}
		delete(b.trees, k)
	}
}

// Close releases all native resources. The optional argument is accepted for
// compatibility with older internal callers; it never selected a document.
func (b *Bloblang) Close(_ ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clear()
	if b.parser != nil {
		b.parser.Close()
		b.parser = nil
	}
}
