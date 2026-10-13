# Sample schema summaries

`blobl schema` summarizes a sample with [JSON Type Definition (JTD, RFC 8927)](https://jsontypedef.com/), using the existing [jtd-infer-go](https://github.com/bombsimon/jtd-infer-go) library. It requires no JavaScript runtime. This is a sample inspection aid, not a schema validator or a promise about future data.

```sh
blobl schema --input-file response.json
blobl schema --sample mapping.sample.yaml --format yaml
blobl schema --input-file response.json --map-path '/data/watch~1providers/results'
```

`--input-file` reads raw JSON or YAML. `--sample` reads the `input` from an LSP `$bloblang` envelope; `root`, metadata, and environment fixtures are not part of the summarized value. Without a file, stdin accepts one JSON or YAML value. The output contains `schema` and `meta`; `--format` selects JSON or YAML serialization, with JTD used in both. Extract `schema` when passing the result to another JTD tool.

In the editor, truncated assignment previews offer **Show Input Schema** and **Show Output Schema** beside their full-value lenses. Clicking a schema lens infers the corresponding captured preview and opens `schema` plus `meta` using the document's normal JSON/YAML preview setting. Merely requesting code lenses performs no inference. These editor summaries use no map hints; use the CLI or MCP tool for explicit map paths.

Use repeated `--map-path` hints for objects whose keys are dynamic entries, such as country codes. Each hint is a JSON Pointer: `~1` escapes `/`, `~0` escapes `~`, and an empty pointer selects the root. The library additionally interprets a component `-` as a wildcard. Keys are preserved without hints; map shapes are not guessed.

Enums and literal constants are not inferred. All numbers use `float64`, avoiding narrow integer bounds inferred from a small sample. The library can infer timestamps from strings. Optional fields reflect missing observations; required fields merely appeared in every observed object. Nullability is tracked separately. Empty objects, null-only fields, empty arrays, and heterogeneous values can become unconstrained (`{}`); JTD does not retain arbitrary mixed-type unions.

Every generated schema is checked against its source sample. Unsupported inference cases return an error rather than a schema that contradicts the input. In particular, the pinned library has limitations with some array-first mixed values.

Inference rejects rather than truncates samples exceeding 4 MiB of JSON, depth 128, 100,000 values, or 256 null values; output is limited to 4 MiB. A conservative work bound also requires `valueCount * (nullCount + 1) <= 1,000,000`, because the upstream library repeatedly wraps nullable observations. There may be at most 128 map hints. For larger samples, select the relevant subtree or use fewer representative records. These bounds apply to inspection, not to the mapping runner.
