# Bloblang MCP server

`blobl mcp` exposes mapping execution, linting, formatting and runtime documentation over MCP stdio. It runs individual Bloblang mappings without starting a Redpanda Connect pipeline. The server runs inside the `blobl` binary; no Node.js runtime or remote service is required.

```sh
blobl mcp
```

Configure an MCP client with executable `blobl` and argument `mcp`. For clients using the `mcpServers` configuration shape:

```json
{
  "mcpServers": {
    "bloblang": {
      "command": "blobl",
      "args": ["mcp"]
    }
  }
}
```

Use an absolute executable path when the client does not inherit your shell's `PATH`. Stdout is reserved for MCP JSON-RPC; logs go to stderr. Tool responses include structured JSON and a JSON text fallback for clients that do not display structured content.

## Tools

| Tool | Purpose |
| --- | --- |
| `bloblang_run` | Evaluate a mapping against explicit sample data, with lint diagnostics. |
| `bloblang_lint` | Return static diagnostics without applying fixes. |
| `bloblang_format` | Return formatted source without changing files. |
| `bloblang_search` | Search functions and methods using batched lexical and fuzzy queries. |
| `bloblang_docs` | Retrieve full documentation by exact function or method IDs. |
| `bloblang_schema` | Summarize sample structure as JSON Type Definition (JTD). |

All tools are read-only. File inputs are read from the machine running the server, including a remote extension host or container if that is where the client launches it. Files are not uploaded to a service. Requests, source text and individual file reads are bounded to 4 MiB.

## Evaluate a mapping

Provide exactly one of `source` and `mappingPath`. Select at most one of `input`, `inputPath` and `samplePath`:

```json
{
  "mappingPath": "/workspace/mappings/person.blobl",
  "inputPath": "/workspace/samples/person.json",
  "timeoutMs": 5000
}
```

`mappingPath` reads Bloblang source. `inputPath` reads an ordinary JSON or YAML value, so an agent can pass a large sample file without reproducing it in tool arguments. `input` accepts any inline JSON value, including `null`:

```json
{
  "source": "root.name = this.name.uppercase()",
  "input": {"name": "Ada"},
  "metadata": {"topic": "people"},
  "env": {"REGION": "eu"}
}
```

`samplePath` reads the same JSON or YAML `$bloblang` envelope used by editor sample files:

```yaml
$bloblang:
  input:
    name: Ada
  meta:
    topic: people
  env:
    REGION: eu
```

The envelope requires `input`, even when it is `null`. Its optional `meta` field is an object, `env` is a string-to-string object, and `root` seeds an output target. See [samples](features/sample.md) for the editor's sample format. Explicit input or sample paths override sample directives. When no input argument is supplied, mapping sample directives and file-backed sibling samples follow the editor's discovery rules. Inline source does not discover sibling files.

`mappingPath` and `inputPath` are relative to the server's working directory. `samplePath` is relative to the mapping file when `mappingPath` is supplied, otherwise to the working directory. Explicit `metadata`, `env`, and `root` replace the corresponding fields loaded from a sample.

The optional execution arguments are:

| Argument | Value |
| --- | --- |
| `metadata` | An object providing initial message metadata. |
| `env` | A string-to-string object providing environment fixtures. |
| `imports` | A string-to-string object mapping import paths to Bloblang source. |
| `root` | Any JSON value, used as the initial output target while `this` reads the input. |
| `timeoutMs` | Execution timeout from 1 through 5000 milliseconds; default 5000. |

Execution permits pure mappings. I/O functions are unavailable; `env()` reads only explicit fixtures and returns `null` for missing names. `imports` can supply imported mapping source explicitly by path. File-backed mappings snapshot transitive imports relative to the mapping file; explicit entries override those files. Inline source reads imports only from the supplied map. Import traversal and source bytes are bounded. This differs from editor previews that use an empty-string placeholder for missing environment values.

The result contains `output`, output `metadata`, `deleted`, and `diagnostics`, plus `error` when execution fails or `lintError` when static tooling fails. Either error marks the MCP result as an error while preserving the structured result. Diagnostics include lint findings as well as relevant source errors; their lines and columns are one-based. A successful evaluation can still have lint diagnostics. A deleted result is identified by `deleted`, so clients can distinguish deletion from JSON `null` output. Clock and random functions remain available, so repeated evaluations can produce different values.

## Lint and format

Both tools accept exactly one of inline `source` and `mappingPath`. An optional `filename` gives inline source a location for syntax selection and configuration lookup. For example, use a `.yaml` filename to lint or format recognized embedded mappings in a Redpanda Connect configuration. Optional `imports` supplies imported Bloblang source by path. Inline source resolves imports exclusively from that map; missing imports produce errors. File inputs use normal file-based import resolution when `imports` is omitted; supplying the map selects explicit imports instead.

```json
{
  "source": "root=this.name.uppercase()",
  "filename": "/workspace/mapping.blobl"
}
```

`bloblang_lint` returns `source` and `diagnostics`. It performs static analysis and does not apply fixes. Diagnostic lines and columns are one-based; diagnostics in YAML refer to the host file.

`bloblang_format` returns formatted `source` and a `changed` boolean. Its optional `tabSize` selects an indentation width from 1 through 8; the default is 2. The result is returned to the caller without writing to `mappingPath` or `filename`.

## Summarize sample structure

`bloblang_schema` uses the existing Go `jtd-infer-go` library to return a compact JTD schema and metadata explaining inference limits. It accepts exactly one of inline `input`, raw JSON/YAML `inputPath`, or a `$bloblang` envelope `samplePath`. Envelope input is summarized; metadata and environment fixtures are excluded. No mapping is executed.

```json
{
  "inputPath": "/workspace/samples/tmdb.json"
}
```

Optional `mapPaths` contains JSON Pointers for objects whose keys represent dynamic entries, for example `["/translations"]`. Use `-` for array elements and normal JSON Pointer escaping (`~1` for `/`, `~0` for `~`).

The response contains a JSON object in `schema`, and `meta` with `format: "jtd"`, `sampleBased: true`, `enumInference: false`, `numericType: "float64"`, and warnings. Enums are deliberately disabled because a narrow sample cannot establish a complete value set. Observed fields and inferred types are not an authoritative contract for future data. Empty or mixed values can be unconstrained; unsupported inference fails explicitly. See [schema summaries](features/schema.md) for bounds and limitations.

## Search documentation

`bloblang_search` performs in-process lexical search across runtime documentation and fuzzy matching of function and method names. It does **not** perform semantic or embedding search. The tool description advertises this before a call, and response metadata reports the supported capabilities.

Supply related queries in one request. This lets agents search alternate terms without repeated tool calls:

```json
{
  "queries": [
    {"id": "typo", "text": "uppercse"},
    {"id": "task", "text": "uppercase string"}
  ],
  "limitPerQuery": 5,
  "includeDeprecated": false
}
```

Each request accepts 1 through 8 queries. `limitPerQuery` is from 1 through 10 and defaults to 5; `includeDeprecated` is optional. Results are deduplicated across queries while retaining the evidence for each matching query.

The response is an object containing `results` and `meta`. Each result has a stable ID such as `method:uppercase`, documentation details, and `matches`. Each match identifies its `queryId`, query-local `rank`, and `evidence`. Evidence identifies the matched `field`, `matchType`, `queryTerms`, `matchedTerms`, and an `excerpt`; fuzzy evidence also reports `editDistance`. Rank is relative to a query, not a probability that the result answers a question.

`meta` reports capabilities (`lexical`, `fuzzyNames`, `batch`, and `semantic: false`), the installed `runtime`, per-query information in `queries`, and whether results were `truncated`. Use that information to distinguish a missing result from a limited result set.

Fetch full documentation using `bloblang_docs` after finding relevant IDs:

```json
{
  "ids": ["method:uppercase", "function:env"]
}
```

Documentation retrieval accepts 1 through 16 exact `function:` or `method:` IDs. It returns the installed runtime's signatures, parameters, descriptions and examples, so agents can inspect the APIs available to the server without another search.
