# Tree-sitter Go bindings

[`internal/benthos/parser.go`](../../internal/benthos/parser.go) wraps `github.com/tree-sitter/go-tree-sitter`. One `benthos.Bloblang` instance owns a native parser guarded by a mutex and cached syntax trees keyed by document URI. Derived parsing contexts use URI suffixes, including embedded YAML regions.

`Parse(uri, text)` returns an independently owned tree clone. Callers close that clone after finishing traversal; nodes remain valid only while their owning tree is alive. Cache invalidation closes the retained originals without invalidating returned clones. Unchanged source reuses a cached tree, while edits update the tree supplied for incremental parsing.

`InvalidateDocument(uri)` removes a document and its derived entries. `Clear()` frees cached trees while retaining a reusable parser. `Close()` frees both caches and the native parser and is safe to repeat. Document close and completed requests for closed/unopened sources clean up retained trees; server shutdown closes the parser.

Tree-sitter byte offsets and `Point.Column` count UTF-8 bytes. LSP `Position.Character` counts UTF-16 code units. Use the conversion helpers in [`internal/lsp/positions.go`](../../internal/lsp/positions.go), and map embedded-region positions to their YAML host before returning protocol ranges.

The grammar's node names and named fields are documented alongside the wrapper. Use `Kind()`, `ChildByFieldName` and named-child traversal for structure; check `HasError()` when an operation requires a complete syntax tree. Navigation and outlines can still use recoverable structure while the user is typing.
