# Documentation search

Bloblang documentation is indexed directly from the installed Benthos/Connect
function and method registry. Search does not execute mappings, plugin
constructors or examples, and does not fetch documentation from the network.
The package used by the MCP adapter is `internal/help`.

Search supports exact names, name prefixes, identifier tokens, small name typos
(including adjacent transpositions), and weighted full-text matches in
descriptions, parameter documentation, categories and examples. Exact names
rank first. Name and signature matches outweigh prose; repeating a word in a
long example does not increase its weight. Simple English plural normalization
matches `field` with `fields`. Equal ranks are resolved by canonical ID.

This is lexical and fuzzy search. It does not use embeddings or provide semantic
search. Responses advertise `meta.capabilities.semantic: false`; adapters also
describe this limitation in their tool descriptions. There is no semantic mode
request option and no silent fallback from a semantic mode.

## Batched search

```json
{
  "queries": [
    {"id": "remove", "text": "remove object fields"},
    {"id": "case", "text": "uppercaes"}
  ],
  "limitPerQuery": 5
}
```

Every query has a caller-supplied, unique ID. Results use canonical IDs such as
`method:without` and `function:env`; functions and methods sharing a name remain
distinct. A response is an object with `results` and `meta`:

- `results` contains the deduplicated union, in first-seen query/rank order. Each
  item includes its name, kind, signature, short summary and `matches` array.
- Each match contains `queryId`, its one-based `rank` for that query, and up to
  three evidence records. Evidence includes `field`, `matchType`, `queryTerms`,
  `matchedTerms` and a short source `excerpt`. Fuzzy name evidence also includes
  `editDistance`. Search scores are internal and are not comparable API values.
- `meta.queries` includes every input query, in input order, with ordered
  `resultIds` and `hasMore`. Empty matches have an empty `resultIds` array.
  Invalid text instead includes an `error` with `code` and `message`; other
  queries in the batch still run.
- `meta.capabilities` reports lexical, fuzzy-name and batch support, and the
  absence of semantic search. `meta.runtime` reports the linked Benthos and
  Connect module versions from build metadata. Unavailable versions are
  `unknown`; local module replacements are identified as such.
- `meta.truncated` indicates the response byte budget omitted results. Normal
  per-query limiting is indicated by `hasMore`, without setting `truncated`.

Descriptions and evidence are the installed runtime's documentation. Finding a
function does not imply that it is enabled in the mapping execution profile.

## Exact documentation retrieval

Retrieve several complete documents in one request using the IDs from search:

```json
{"ids": ["method:without", "method:uppercase"]}
```

The result includes the full signature, description, parameters and defaults,
variadic status, introductory version, examples with input/output pairs, and
category-specific documentation and examples. Example `skipTesting` flags are
preserved. Documentation is returned verbatim, without executing examples.

Retrieval also returns `{results, meta}`. `meta.queries` uses each requested
canonical document ID as its ID. Unknown or excluded IDs receive individual
`not_found` errors. A document that exceeds the remaining response budget
receives `response_limit`; it is not returned as a silently incomplete document.
Retry a smaller batch when the combined documentation exceeds the limit.

Hidden entries are always excluded. Both operations exclude deprecated entries
by default and accept `includeDeprecated: true` when they are needed.

## Limits

Search accepts one through eight queries, each with up to 256 UTF-8 bytes of
text. Query IDs are nonempty, unique and at most 64 UTF-8 bytes. The default limit
is five results per query; the maximum is ten. Retrieval accepts one through
sixteen distinct canonical IDs. Empty batches, invalid IDs, duplicate IDs and
out-of-range limits reject the whole request. Invalid search text is a per-query
error. Responses are bounded to 512 KiB; excerpts and search summaries have
separate smaller limits. No filesystem paths are accepted by these operations.
