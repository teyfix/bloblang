package benthos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func TestParserDocumentCleanupAndPermanentClose(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close()
	for _, key := range []string{"file:///test.blobl", "file:///test.blobl:completion", "file:///test.blobl#bloblang-0:lint", "file:///other.blobl"} {
		tree, err := b.Parse(key, "root = this")
		require.NoError(t, err)
		tree.Close()
	}
	b.InvalidateDocument("file:///test.blobl")
	require.Len(t, b.trees, 1)
	require.Contains(t, b.trees, "file:///other.blobl")
	b.Clear()
	require.Empty(t, b.trees)
	tree, err := b.Parse("file:///next.blobl", "root = this")
	require.NoError(t, err)
	defer tree.Close()
	b.Close()
	b.Close()
	require.Nil(t, b.parser)
	_, err = b.Parse("file:///next.blobl", "root = this")
	require.ErrorContains(t, err, "closed")
}

func TestBloblangParserCaching(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	uri := "file:///test.blobl"
	doc1 := `root = this`

	// Parse first time
	tree1, err := b.Parse(uri, doc1)
	require.NoError(t, err)
	require.NotNil(t, tree1)

	// Parse second time with exact same text should return identical tree instance
	tree2, err := b.Parse(uri, doc1)
	require.NoError(t, err)
	assert.NotSame(t, tree1, tree2, "Callers own independent cloned trees")
	assert.Equal(t, tree1.RootNode().ToSexp(), tree2.RootNode().ToSexp())
	defer tree1.Close()
	defer tree2.Close()

	// Parse third time with changed text
	doc2 := `root = this.name`
	tree3, err := b.Parse(uri, doc2)
	require.NoError(t, err)
	require.NotNil(t, tree3)
	defer tree3.Close()
	assert.False(t, tree1 == tree3, "Should return a new tree instance after changes")

	// Invalidate
	b.InvalidateDocument(uri)
	_, ok := b.trees[uri]
	assert.False(t, ok, "Should remove document from cache")
}

func TestBloblangParserErrors(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	uri := "file:///test-err.blobl"
	doc := `root =`
	tree, err := b.Parse(uri, doc)
	require.NoError(t, err)
	require.NotNil(t, tree)

	root := tree.RootNode()
	assert.True(t, root.HasError(), "Root should have error status")

	var foundError bool
	var visit func(node *tree_sitter.Node)
	visit = func(node *tree_sitter.Node) {
		if node.IsError() || node.IsMissing() {
			foundError = true
		}
		for i := uint(0); i < node.ChildCount(); i++ {
			visit(node.Child(i))
		}
	}
	visit(root)
	assert.True(t, foundError, "Should find at least one error or missing node")
}

func TestBloblangImportAst(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	uri := "file:///test-import.blobl"
	doc := `import "foo.blobl"`
	tree, err := b.Parse(uri, doc)
	require.NoError(t, err)
	require.NotNil(t, tree)

	root := tree.RootNode()
	assert.False(t, root.HasError(), "Root should not have errors")

	var importFound bool
	for i := uint(0); i < root.ChildCount(); i++ {
		child := root.Child(i)
		if child.Kind() == "import_statement" {
			importFound = true
			var stringChildFound bool
			for j := uint(0); j < child.ChildCount(); j++ {
				gc := child.Child(j)
				if gc.Kind() == "string" {
					stringChildFound = true
					assert.Equal(t, `"foo.blobl"`, gc.Utf8Text([]byte(doc)))
				}
			}
			assert.True(t, stringChildFound, "import_statement should have a string child")
		}
	}
	assert.True(t, importFound, "Should find an import_statement node")
}

func TestBloblangParserOk(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	uri := "file:///test-ok.blobl"
	doc := `root = "ok"`
	tree, err := b.Parse(uri, doc)
	require.NoError(t, err)
	require.NotNil(t, tree)

	root := tree.RootNode()
	assert.False(t, root.HasError(), "Root should not have errors for root = \"ok\"")
	if root.HasError() {
		var visit func(node *tree_sitter.Node, indent string)
		visit = func(node *tree_sitter.Node, indent string) {
			t.Logf("%snode kind: %s, isError: %v, isMissing: %v, text: %q", indent, node.Kind(), node.IsError(), node.IsMissing(), node.Utf8Text([]byte(doc)))
			for i := uint(0); i < node.ChildCount(); i++ {
				visit(node.Child(i), indent+"  ")
			}
		}
		visit(root, "")
	}
}

func TestBloblangParserEdit(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	uri := "file:///test-edit.blobl"
	doc1 := `root = (`
	tree1, err := b.Parse(uri, doc1)
	require.NoError(t, err)

	// Let's call Edit
	edit := tree_sitter.InputEdit{
		StartByte:      0,
		OldEndByte:     uint(len(doc1)),
		NewEndByte:     11,
		StartPosition:  tree_sitter.Point{Row: 0, Column: 0},
		OldEndPosition: tree_sitter.Point{Row: 0, Column: uint(len(doc1))},
		NewEndPosition: tree_sitter.Point{Row: 0, Column: 11},
	}
	tree1.Edit(&edit)
}

func TestBloblangParserDescendant(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	uri := "file:///test-desc.blobl"
	doc := `root = json("name")`
	tree, err := b.Parse(uri, doc)
	require.NoError(t, err)
	require.NotNil(t, tree)

	root := tree.RootNode()
	point := tree_sitter.Point{Row: 0, Column: 8} // 'j' of json
	node1 := root.DescendantForPointRange(point, point)
	require.NotNil(t, node1)
	assert.Equal(t, "identifier", node1.Kind())

	node2 := root.NamedDescendantForPointRange(point, point)
	require.NotNil(t, node2)
	assert.Equal(t, "identifier", node2.Kind())
}

func TestBloblangParserHover(t *testing.T) {
	b, err := NewBloblang()
	require.NoError(t, err)
	defer b.Close("explore")

	// 1. Function test
	uri1 := "file:///test-fn.blobl"
	doc1 := `root = json("name")`
	tree1, err := b.Parse(uri1, doc1)
	require.NoError(t, err)
	root1 := tree1.RootNode()
	point1 := tree_sitter.Point{Row: 0, Column: 8} // 'j' of json
	node1 := root1.DescendantForPointRange(point1, point1)
	t.Logf("Function node: kind=%s, text=%s", node1.Kind(), node1.Utf8Text([]byte(doc1)))
	if node1.Parent() != nil {
		t.Logf("  Parent: kind=%s", node1.Parent().Kind())
	}

	// 2. Method test
	uri2 := "file:///test-method.blobl"
	doc2 := `root = this.string()`
	tree2, err := b.Parse(uri2, doc2)
	require.NoError(t, err)
	root2 := tree2.RootNode()
	point2 := tree_sitter.Point{Row: 0, Column: 14} // 's' of string
	node2 := root2.DescendantForPointRange(point2, point2)
	t.Logf("Method node: kind=%s, text=%s", node2.Kind(), node2.Utf8Text([]byte(doc2)))
	if node2.Parent() != nil {
		t.Logf("  Parent: kind=%s", node2.Parent().Kind())
	}
}
