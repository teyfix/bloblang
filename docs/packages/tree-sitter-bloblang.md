# Bloblang Tree-sitter grammar

The published [`github.com/teyfix/tree-sitter-bloblang`](https://github.com/teyfix/tree-sitter-bloblang) module supplies the generated C parser through `bindings/go`. Its revision is pinned in [`go.mod`](../../go.mod). The [`benthos.Bloblang` wrapper](../../internal/benthos/parser.go) initializes the language and documents the node kinds and named fields used by editor features.

Tree-sitter provides a concrete syntax tree with source boundaries, comments and recoverable errors. Benthos remains the semantic parser and runtime. Changes to formatting or navigation should first check whether the existing tree contains the required structure; host YAML extraction and edit preservation belong to this repository.

For grammar work, use the sibling `tree-sitter-bloblang` checkout when available. A local `go mod edit -replace github.com/teyfix/tree-sitter-bloblang=../tree-sitter-bloblang` can test a grammar change; remove the replacement before committing. Rebuild with `go build -a` after regenerating `parser.c`, because the included C file lives outside the Go package directory and ordinary build caching can miss its change.

See the [parser notes](../../internal/benthos/parser/README.md) and [build instructions](../../README.md#build-and-package).
