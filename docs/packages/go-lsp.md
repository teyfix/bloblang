# go-lsp integration

`github.com/owenrumney/go-lsp` provides JSON-RPC framing, protocol types, handler interfaces, client notifications and a document store. [`internal/lsp/server.go`](../../internal/lsp/server.go) creates the server around `Handler` and runs it over the supplied transport.

Methods implemented by `Handler` satisfy optional go-lsp interfaces and are registered automatically. [`Handler.Initialize`](../../internal/lsp/handler.go) supplies advertised capabilities, trigger characters, server version and workspace setup. LSP requests use UTF-16 positions; tree and byte offsets must pass through the project's conversion helpers.

The handler's document state uses explicit mutexes, document generations and cancellation contexts. Each open/change/close updates the corresponding state; a close/reopen cannot reuse an old generation. Validation and preview publication check that their source generation is still current. Client refresh requests are sent only when supported.

Keep stdout reserved for LSP traffic and send logs to stderr. Execute-command IDs remain `bloblang-lsp.showResult` and `bloblang-lsp.openFile` for client compatibility. `lsp.Run` releases handler resources when the transport ends, as does the shutdown handler.

Use `servertest` for transport-level tests of initialization, requests and notifications. The pinned go-lsp version includes the required `result: null` response behavior; avoid replacing transport code with ad hoc JSON-RPC handling.
