# Samples and runtime previews

[`internal/benthos/sample.go`](../../internal/benthos/sample.go) extracts samples, and [`executor.go`](../../internal/benthos/executor.go) evaluates complete statement prefixes or selected expressions through the public Benthos runtime. Each uncached evaluation starts from a fresh input and optional root target. The server reuses bounded results for identical statement prefixes and fixtures until an edit, dependency change or cache expiry; it does not simulate processor pipelines. Before and after previews share the same cached prefix result, but generating all distinct prefixes still replays the mapping from its input.

For `mapping.blobl`, automatic discovery looks for `mapping.sample.json`, `.yaml`, or `.yml`. File samples require the `$bloblang` envelope:

```yaml
$bloblang:
  input:
    name: Ada
  meta:
    topic: people
```

`input` is required, including when its value is `null`. `meta` is optional and must be an object. Optional `env` is an object whose values must be strings; it supplies explicit environment fixtures for whole-sample loading. Optional `root` seeds a separate output target for overlay mappings while `this` continues to read `input`.

Inline directives before mapping statements accept JSON or YAML values:

```bloblang
#!input {"name":"Ada"}
#!meta {"topic":"people"}
root.name = this.name.uppercase()
```

`#!sample` accepts an object containing `input` and optional `meta`, `root`, and `env`, without a `$bloblang` envelope. It overrides automatic sibling discovery. `#!input`, `#!meta`, and `#!root` override their respective fields of a valid automatically selected sample. The corresponding `_from` directives load file values from a mandatory `$bloblang` envelope. Paths resolve relative to the mapping or host YAML file.

Multiline directives use a `|` header followed by `#|` continuation comments. Empty arguments, duplicate directive names, unknown directives, malformed data, and missing explicit files produce errors. A valid directive without mapping statements is a valid editor state.

## Embedded YAML selection

Inline mappings are numbered from 001 in document order; external `from` mappings and `${! ... }` interpolations do not consume numbers. For a host named `config.yaml`, selection prefers an explicit adjacent comment, then `config.sample-001.json` (also `.yaml` or `.yml`), then `config.sample.json` (also `.yaml` or `.yml`). The comment must immediately precede the mapping key and have the same indentation:

```yaml
processor:
  # bloblang-sample: selected.sample.yaml
  mapping: |
    root = this
```

Multiple candidates at an automatically selected sibling level are ambiguous; use an explicit whole-sample source. Inline whole-sample directives can override file selection. Missing automatic samples produce informational diagnostics; malformed samples and missing explicit paths produce errors. Sample errors disable dynamic values and leave static features available.

File-watch notifications reload samples for open documents, including file creation, modification, and deletion. Valid samples enable before/after assignment inlays, input/output lenses for truncated values, sample-file navigation lenses, and supported value hovers. See the [README](../../README.md#samples) for directive examples and [hover rendering](hover.md) for preview configuration.

## Environment fixtures

Sample files must be regular files no larger than 4 MiB. The same limit applies to sibling samples and all file-backed sample directives.

IDE validation and execution replace `env(name)` in a private Benthos environment. Whole-sample `env` overrides take precedence; unspecified names return an empty string. Neither path reads or mutates the server's process environment. Imported mappings and dynamic environment names use the same resolver.

```bloblang
#!sample {"input":{},"env":{"ARTIFACT_DIR":"/tmp/fixture-artifacts"}}
root.artifact_dir = env("ARTIFACT_DIR")
```

The shared production parser/runtime and `correctness/environment/require-fallback` lint rule retain the real null-on-unset semantics. An empty placeholder can bypass a real `.or(...)` or null-check fallback, so preview output may follow a different branch. Whole-sample `env` overrides do not load through partial `#!input_from`, `#!meta_from`, or `#!root_from` directives.
