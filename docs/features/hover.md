# Hovers and previews

[`Handler.Hover`](../../internal/lsp/handler.go) uses Tree-sitter to identify the selected token and Benthos to evaluate sample values. Function and method names show documentation, examples, and a documentation link. `deleted()` has documentation even though it is a special grammar node; its marker removes a field or filters a message according to where it is assigned. Expression-only YAML fields such as `check: !errored()` also support documentation.

With a valid sample, hovers can show fields, expressions, variable references, metadata, and the value assigned by a `let` declaration. Hovering the left-hand `root` of an assignment shows the preceding output state. Before any assignment, that preview shows the input or explicit sample root. Right-hand `root` retains Benthos's actual output semantics and may be uninitialized.

Reached conditional branches preserve preceding statements and conditions. Unreached branches and named-map or lambda contexts without an unambiguous runtime invocation omit dynamic values. Missing or invalid samples leave documentation available.

Values, inlay tooltips, and Show Input/Output use the same renderer in [`preview.go`](../../internal/lsp/preview.go). YAML is the default; `preview.format: "json"` selects JSON. Both use `formatter.printWidth` to collapse small collections and expand larger values. Inline labels have a separate `max_inline_result_bytes` startup limit. Show Input/Output opens a temporary file with the selected format's extension.

Starting with v0.2.3, hover and inlay tooltip bodies are limited to **4 KiB / 60 lines**. Before formatting, previews also limit the value to 128 nodes, eight nesting levels and 512 bytes per string. Large values show a **Preview truncated** notice at the top, the full value's JSON size, and guidance to use **Show Input / Show Output** above the mapping. Those lenses open the complete message; a hovered subexpression can represent only part of that message. Small values retain their normal presentation.

Limiting happens before YAML conversion, reducing both server formatting work and the amount of Markdown sent to the editor. Full previews and Bloblang evaluation still use the complete sample. Inlay requests evaluate only statements whose result positions were requested. Hints, hovers and code lenses reuse unchanged results within the configured cache limits. Edits, close and watched dependency changes invalidate results, and superseded requests cannot restore stale execution diagnostics. Cancellation stops work between runtime calls; an already-running synchronous Bloblang function cannot be interrupted. Tooltip limits do not change mapping results.

Positions use LSP UTF-16 columns and embedded YAML regions map ranges back to the host document. See [sample configuration](sample.md) and [workspace settings](../core/settings.md).

Environment expressions use preview-only fixtures: unspecified names evaluate to an empty string, while a whole sample's `env` map can supply explicit string values. No process environment values are exposed. Hovering a binary operator evaluates its enclosing binary expression when a valid sample exists. See [environment fixtures](sample.md#environment-fixtures) for differences from production fallback behavior.
