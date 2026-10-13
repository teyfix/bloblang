# Configuration

The server has two configuration readers: startup settings in [`internal/config`](../../internal/config/config.go) and workspace editor settings in [`internal/editorconfig`](../../internal/editorconfig/config.go). The VS Code `.bloblangrc.json` schema describes workspace editor settings. `blobl fmt` and `blobl lint` use the same editor settings schema, with the CLI lookup behavior below.

## Workspace editor settings

Each document uses the nearest ancestor `.bloblangrc.json`, starting in its directory and stopping at the deepest containing workspace root, inclusive. Embedded mappings use their host YAML document's directory. A nested configuration replaces ancestor settings rather than merging them. For loose documents outside all workspace roots, lookup stops at the nearest Git project root (a `.git` directory or worktree marker); outside Git it continues to the filesystem root.

| Setting | Default | Accepted values |
| --- | --- | --- |
| `files.exclude` | `[]` | Array of Gitignore-style patterns relative to the configuration directory |
| `files.respectGitignore` | `true` | Boolean; controls `.gitignore` processing in CLI directory/glob scans |
| `formatter.printWidth` | `80` | Integer from 20 through 1000 |
| `preview.format` | `"yaml"` | `"yaml"` or `"json"` |
| `lint.enabled` | `true` | Boolean |
| `lint.rules` | Registry defaults | Flat slash-separated rule IDs |

Rules accept a severity string or an object containing `severity`. Severities are `off`, `hint`, `info`, `warn`, and `error`. Only `style/assignments/prefer-grouped` accepts `minAssignments`, an integer of at least 3. See [lint configuration](../features/lint.md) and the [generated rule reference](../features/lint-rules.md).

The file is read on demand. Client file-watch events revalidate diagnostics and refresh previews; requests also see current settings without restarting. Invalid JSON, unknown properties or rule IDs, and invalid values cause the entire workspace editor configuration to fall back to defaults. The extension associates the generated schema for completion and validation. Invalid editor configuration does not prevent server startup.

Registry metadata drives both the schema and generated rule reference:

```sh
go run ./cmd/config-schema > schemas/bloblangrc.schema.json
go run ./cmd/config-schema --rules > docs/features/lint-rules.md
```

## CLI settings

For each input file, `blobl fmt` and `blobl lint` search from its directory upward for the nearest `.bloblangrc.json`, stopping at the nearest Git project root, inclusive. Outside Git, lookup continues to the filesystem root. Embedded mappings use the YAML file's location. The nearest file is used on its own; ancestor files are not merged. Without a configuration file, the registry defaults apply. Stdin uses `--stdin-filepath` for this lookup and for import resolution, or the current working directory when no filepath is supplied.

Unlike editor requests, invalid CLI configuration is reported as a failure with exit status `2`. `formatter.printWidth` controls formatting; `lint.enabled` and `lint.rules` control static lint findings. Preview settings have no effect on CLI lint because it does not load samples or execute mappings. See the [CLI reference](../cli.md).

Directory and glob discovery honors `.gitignore`, `.bloblangignore` and `files.exclude`; explicitly named regular files bypass exclusions. `files.respectGitignore: false` disables only `.gitignore`, while `--no-ignore` bypasses all ignore and configured exclusion rules. Built-in generated-directory and symlink skips remain in effect. File discovery settings do not suppress diagnostics for open editor documents.

## Source paths

Bloblang imports resolve relative to the source file's directory; absolute paths are accepted. External YAML `mapping: from "path"` references try the host YAML file's directory first, then the selected project root. The editor uses its deepest containing workspace root; the CLI and loose editor documents discover the nearest Git root. Without a project root, YAML references use only the host file's directory. Missing references retain the local path in diagnostics.

## Legacy server startup settings

Viper searches for the `.bloblangrc` base name with supported file extensions, for example `.bloblangrc.yaml`, in the process working directory, home directory, then `$XDG_CONFIG_HOME/bloblang-lsp` (or `~/.config/bloblang-lsp`). The first discovered file is loaded; files from those locations are not merged. Environment variables override file values and defaults.

```yaml
# .bloblangrc.yaml
log_level: info
max_inline_document_bytes: 150000
max_inline_result_bytes: 100
```

| Key | Default | Current use |
| --- | --- | --- |
| `bloblang_docs_url` | `https://docs.redpanda.com/redpanda-connect/guides/bloblang` | Documentation links |
| `log_level` | `info` | Server logging |
| `max_inline_document_bytes` | `150000` | Execution size limit |
| `max_inline_result_bytes` | `100` | Inline label truncation; does not set preview print width |
| `diagnostics_debounce` | `200ms` | Coalesce validation after edits; zero disables the delay |
| `partial_exec_cache_size` | `1000` | Maximum cached evaluation results; zero disables caching |
| `partial_exec_cache_ttl` | `5m` | Maximum result age; zero disables caching |

Evaluation results are reused between hints, hovers and code lenses for unchanged source and fixtures. Edits, document close and watched dependency changes invalidate them. Cache retention is additionally capped at 16 MiB of encoded results, with entries over 1 MiB omitted. Inlay requests evaluate only requested positions; canceled or superseded requests stop between runtime calls. A synchronous Benthos function already running cannot be interrupted. The former unused `inline_result_debounce` option has been removed.

Every key has an environment override named `BLOBLANG_LSP_<UPPERCASE_KEY>`, for example `BLOBLANG_LSP_MAX_INLINE_RESULT_BYTES=200`. Startup settings are loaded once; changing them requires restarting the server.

Viper can also discover `.bloblangrc.json`, but its startup reader only recognizes the legacy snake_case settings. Keep editor JSON limited to its schema, and configure legacy settings through environment variables or a separate server config file. The editor configuration reader does not accept legacy server keys.
