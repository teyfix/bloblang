# Bloblang developer tools

The `cmd/blobl` CLI provides `lsp`, `lint`, `fmt`, `run`, `test`, `schema` and `mcp`. The shipped legacy server is `cmd/bloblang-lsp`; the older `cmd/language-server` command uses the same implementation. `internal/lsp` provides the shared analysis and formatting. One handler tracks open documents and answers LSP requests; it uses the Benthos public parser/runtime and Tree-sitter for source boundaries.

See the repository [README](../README.md) for features, samples, packaging and validation. Source is the reference for behavior. YAML mappings are extracted into independent document regions and mapped back to host positions; the server does not simulate a YAML processor pipeline. Named map definitions and lambda bodies have no unique runtime invocation context, so ambiguous sampled hovers are omitted.

Logs go to stderr. Stdout is reserved for JSON-RPC. Diagnostics publish asynchronously because the go-lsp transport must continue reading client responses. Client refresh requests are sent only when the client advertises support. go-lsp 0.2.5 includes the required `result: null` JSON-RPC response fix.

## Current feature documentation

- [Configuration](core/settings.md): startup server settings and live workspace editor JSON.
- [CLI reference](cli.md): formatting, static linting, stdin, configuration and exit status.
- [Standalone evaluation and fixtures](features/evaluation.md), [sample schemas](features/schema.md), and [MCP tools](mcp.md).
- [Batched lexical and fuzzy documentation search](features/search.md).
- [Completion](features/completion.md), [hovers and previews](features/hover.md), [samples](features/sample.md), and [diagnostics](features/diagnostic.md).
- [Lint behavior and fixes](features/lint.md) and [generated rule reference](features/lint-rules.md).
- [Signature help, outline, folding and references](features/editor.md).
- [Rename](features/rename.md): F2, scope handling, imports and embedded YAML.
- [Server startup](core/main.md) and [selected dependency references](packages.md).

The shipped implementation uses document generations, cancellation contexts and mutex-protected caches. Remaining historical notes are labeled explicitly; they are not implementation requirements.
