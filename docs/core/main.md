# Command entrypoints and server startup

[`cmd/blobl`](../../cmd/blobl/main.go) calls `internal/cli.Run`. The compatibility commands [`cmd/bloblang-lsp`](../../cmd/bloblang-lsp/main.go) and [`cmd/language-server`](../../cmd/language-server/main.go) call `RunLegacy`, which starts LSP when invoked without arguments. Build version and date values are supplied through linker flags.

For `blobl lsp`, [`runLSP`](../../internal/cli/cli.go) loads startup configuration, constructs a `slog` logger on stderr, creates `lsp.NewHandler`, and calls `lsp.Run` with the supplied stdin/stdout transport. `Run` closes the handler when the transport ends; LSP shutdown also cancels document work and releases native parser resources.

The handler owns the open-document store, Bloblang environment, Tree-sitter parser, documentation/completion data, sample state and bounded execution cache. Document generations are monotonic across close/reopen; each edit cancels work for the preceding generation. Validation uses the configured debounce. See [settings](settings.md) for startup and workspace configuration.

[`Handler.Initialize`](../../internal/lsp/handler.go) advertises capabilities and records workspace roots and client refresh support. [`NewServer`](../../internal/lsp/server.go) uses go-lsp's handler interfaces to register protocol methods. Completion triggers are `.`, `@`, `$`; signature help triggers are `(`, `,`, `:`. Existing command IDs remain `bloblang-lsp.showResult` and `bloblang-lsp.openFile`.

Stdout is reserved for protocol messages while serving LSP. Logs and startup failures go to stderr. Feature behavior lives in `internal/lsp`; the entrypoint does not inspect syntax trees or evaluate mappings itself.
