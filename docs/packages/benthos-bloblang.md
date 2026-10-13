# Benthos Bloblang API

`github.com/redpanda-data/benthos/v4/public/bloblang` supplies the language environment, parser, executor and function/method metadata. [`internal/benthos/env.go`](../../internal/benthos/env.go) loads the project's component registrations and returns an environment requiring explicit import configuration.

`Environment.Parse` checks syntax and statically detectable errors. Runtime-dependent type and data errors require execution. Parse errors expose `*bloblang.ParseError` line/column information; [`convertErrorToDiagnostics`](../../internal/lsp/diagnostics.go) converts that structured information to editor positions. Do not extract locations by parsing error-message strings.

`WalkFunctions` and `WalkMethods` expose runtime metadata, including names, descriptions, parameters, examples and release status. Completion, hover documentation and signature help use this metadata. Signature help recognizes incomplete calls without requiring a successfully parsed mapping.

Static CLI analysis uses a deactivated environment so parsing does not execute registered function/method constructors. Preview environments additionally install explicit fixture behavior for `env()`. Unspecified preview environment names evaluate to an empty string; this differs from ordinary Bloblang's null for unset names and is documented in [sample fixtures](../features/sample.md#environment-fixtures).

The public `Executor.Query(any)` API supports direct value transformations. Editor previews use `service.Message.BloblangQuery` or `BloblangMutateFrom` to preserve message metadata and support an explicit initial output root. A deleted result is distinct from JSON null. Imports resolve through an operation-specific importer using the source file or workspace context.

Benthos evaluation is synchronous and does not take a context. Editor requests check cancellation between calls and discard superseded work, but cannot interrupt a function already running. Reusable preview results are bounded by entry count, age and encoded size; see [runtime settings](../core/settings.md#legacy-server-startup-settings).
