# Connect component registration

[`internal/benthos/env.go`](../../internal/benthos/env.go) blank-imports these Redpanda Connect component packages:

- `crypto`
- `ffi`
- `io`
- `msgpack`
- `pure`
- `pure/extended`
- `sql/base`
- `text`

Component packages register implementations during Go package initialization. Imported packages finish initialization before their importer is initialized. Keeping the imports beside `NewEnvironment` is an organizational choice; Go does not require them to appear in the same file as an environment constructor.

A component may register processors or other pipeline components without adding Bloblang functions. Build tags can also affect registrations. Inspect `Environment.WalkFunctions` and `WalkMethods` to determine the mapping functions actually available in the built binary. Do not infer registrations from a component's name. Bloblang keywords such as `this`, `root`, `if` and `match` are syntax, not component registrations.

`bloblang.GlobalEnvironment()` refers to the global environment, while `bloblang.NewEnvironment()` clones it. This repository's `benthos.NewEnvironment` derives an environment with an explicit custom importer; callers configure source-relative import resolution for their operation.

When changing component imports, check the resulting registry and existing mappings, then run tests and `go mod tidy` as needed. Removing one direct import does not guarantee a registration disappears: another dependency may import the same component transitively.
