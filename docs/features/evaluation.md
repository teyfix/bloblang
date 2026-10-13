# Standalone evaluation and fixtures

Run Bloblang directly without a Redpanda Connect pipeline:

```sh
blobl run --input-file response.json mapping.blobl
blobl run --sample mapping.sample.yaml mapping.blobl
blobl run --source 'root = this.name.uppercase()' --input '{"name":"Ada"}'
blobl run mapping.blobl
```

Put options before the mapping path. `--input-file` reads a raw JSON or YAML value; `--sample` reads the existing editor sample format, including `$bloblang.input`, metadata, environment and root fixtures. Mapping paths also discover sample directives and sibling `.sample.json`, `.sample.yaml` and `.sample.yml` files. Explicit input/sample arguments override discovery. Large fixtures can stay in files rather than tool arguments or stdin.

Output is a JSON object with `output`, `metadata`, `deleted`, and `diagnostics`. Static lint findings include one-based Unicode line/column ranges, severity, code and message. They use the mapping file's nearest project configuration and do not prevent execution. A malformed lint configuration is exposed as `lintError`. Parse/runtime failures appear in `error` with a stage, message and parser position when available. Runtime failures retain lint findings. Exit status is 0 for successful execution, 1 for execution or lint-configuration failure, and 2 for invalid arguments or file-loading errors. Use `blobl lint` to enforce diagnostic severity in CI.

`root` is an optional starting output document; `this` still refers to input. `deleted: true` distinguishes a deleted message from a JSON null. `rawOutput` preserves raw bytes as UTF-8 or base64 when the runtime does not retain a structured value. The public Benthos message API parses JSON-looking string payloads: `root = "null"` and `root = null` cannot be distinguished afterward. No source rewriting changes those semantics.

Evaluation uses Benthos's pure environment. Host I/O functions are unavailable, and `env()` reads only supplied fixtures (missing values return null). Clock/random functions remain available. File-backed mappings snapshot transitive imports relative to the mapping; explicit `--imports` or repeated `--import name=path` entries override file contents. Inline mappings can import only explicitly supplied source. Requests and files are limited to 4 MiB, imports to 256 traversals, map recursion to 100, and worker output to 4 MiB. `--timeout` sets an execution deadline up to the default five seconds; exceeding it terminates the subprocess.

## Named fixtures

```yaml
source: root = this.name.uppercase()
cases:
  - name: uppercase a name
    request:
      input: {name: Ada}
    expect:
      output: ADA
      deleted: false
  - name: missing name fails
    request:
      input: {}
    expect:
      error:
        kind: execute
```

```sh
blobl test suite.yaml
blobl test --format json suite.yaml
```

Suites accept JSON or YAML and reject unknown fields, duplicate keys/names, omitted inputs and empty assertions. Every case has an explicit input (`null` is valid). Only specified expectations are checked; metadata expectations compare the entire map. `source`, `env` and `imports` at the suite root provide defaults, with per-case environment/import entries overriding by key. Cases remain self-contained: file paths are not accepted inside suites. Failed assertions return status 1; malformed suites return 2. JSON results include each evaluation's diagnostics and errors.
