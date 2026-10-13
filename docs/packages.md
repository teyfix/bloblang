# Selected dependency references

These notes describe the main runtime dependencies. The pinned versions are in [`go.mod`](../go.mod), and current source is authoritative when behavior differs from a note.

| Package | Reference | Role |
| --- | --- | --- |
| `github.com/owenrumney/go-lsp` | [go-lsp](packages/go-lsp.md) | JSON-RPC transport, LSP handler interfaces, protocol types and document store |
| `github.com/tree-sitter/go-tree-sitter` | [Go bindings](packages/go-tree-sitter.md) | Native syntax tree parsing and traversal |
| `github.com/teyfix/tree-sitter-bloblang/bindings/go` | [Bloblang grammar](packages/tree-sitter-bloblang.md) | Generated Bloblang parser language |
| `github.com/redpanda-data/benthos/v4/public/bloblang` | [Bloblang API](packages/benthos-bloblang.md) | Environments, parsing, execution and function/method metadata |
| `github.com/redpanda-data/benthos/v4/public/service` | [Bloblang API](packages/benthos-bloblang.md) | Message execution with input/output metadata and optional root targets |
| `github.com/redpanda-data/connect/v4/public/components/*` | [Component registration](packages/connect-components.md) | Additional registered runtime components |

Tree-sitter supplies source ranges and editor structure. Benthos provides the language semantics and execution. Documentation, completion and signature help read the installed runtime's registry instead of maintaining a second function catalog. Embedded YAML is decoded with `go.yaml.in/yaml/v3` and mapped back to host positions.
