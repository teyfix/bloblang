# blobl CLI

`blobl` provides linting, formatting, standalone evaluation, fixture tests, sample schemas, an MCP server and the Bloblang language server. Standalone Bloblang files and embedded mappings in Redpanda Connect YAML use the same analysis and formatter as the editor. Run `blobl --help` or a subcommand's `--help` for available options; `blobl --version` reports the build version. With no arguments, `blobl` prints help. Put options before input paths.

See [run and test](features/evaluation.md), [schema](features/schema.md), and [MCP](mcp.md) for those commands. The formatting/lint exit status table below applies to `fmt` and `lint`.

## Installation

Download a `blobl-v<version>-<os>-<arch>.tar.gz` archive from [Releases](https://github.com/teyfix/bloblang/releases), or `.zip` on Windows. Supported OS values are `linux`, `darwin` and `windows`; architectures are `amd64` and `arm64`. Extract `blobl` (`blobl.exe` on Windows) into a directory on your `PATH`. The binary runs without a Go toolchain or C compiler.

The [VS Code extension](https://github.com/teyfix/vscode-bloblang) installs the managed CLI on startup and exposes it to new integrated terminals when `bloblang.cli.enabled` is enabled (the default). Existing terminals may need relaunching. **Bloblang: Check for Tool Updates** checks immediately. **Bloblang: Install CLI for External Terminals** copies an independent binary into a folder you choose, initially `~/.local/bin` on Unix or `%LOCALAPPDATA%/Bloblang/bin` on Windows. Add that folder to your shell's `PATH` if needed; the command does not edit shell profiles. In remote workspaces these operations run on the remote extension host. A custom `bloblang.server.path` skips automatic managed CLI installation and terminal exposure; the explicit external installation command remains available.

Automated installers can fetch [blobl-release.json](https://github.com/teyfix/bloblang/releases/latest/download/blobl-release.json). Its `schemaVersion` is `1`, `version` is a stable tag such as `v0.3.0`, and `assets` maps `<os>-<arch>` to raw binary filenames such as `blobl-linux-amd64` or `blobl-windows-arm64.exe`. Download binaries from `https://github.com/teyfix/bloblang/releases/download/<version>/<asset>`. The manifest and binaries are public and require no GitHub API requests or authentication.

## Formatting

```sh
blobl fmt mapping.blobl
blobl fmt --write mapping.blobl config.yaml
blobl fmt --write mappings/
blobl fmt --check mappings/ config.yaml
blobl fmt --check '**/*.blobl'
```

`fmt` with a single file writes the formatted source to stdout. `--write` updates files in place and accepts multiple files or directories. `--check` accepts the same inputs, leaves files unchanged, lists files that differ on stdout, and returns `1` when formatting differs. `--write` and `--check` are mutually exclusive. Multiple files and directories require one of those modes. `--tab-size` selects an indentation width from 1 through 8; the default is 2.

The formatter preserves comments and string contents and validates its output. It formats recognized embedded YAML mappings while preserving surrounding YAML, scalar comments/properties and host indentation. Attached comments, lint suppression targets and existing line endings survive formatting; attached comments may exceed the preferred width. `${! ... }` interpolations are not formatted. Invalid host YAML or malformed Bloblang that prevents formatting returns `2`. Formatting does not apply lint refactors.

## Linting

```sh
blobl lint mapping.blobl config.yaml
blobl lint mappings/
blobl lint --fix mappings/
blobl lint --format json mappings/
```

Lint reports Bloblang syntax, semantic and configured lint diagnostics, including mappings and `${! ... }` expressions embedded in YAML. It uses static analysis: sample discovery, sample directives and runtime previews do not run. Diagnostics in YAML refer to host file positions. Text output is the default; `--format json` writes an array of diagnostics to stdout, with `file`, `line`, `column`, `severity`, optional `code`, and `message`. Paths are absolute, lines and columns are one-based, and columns count Unicode characters. No diagnostics produces `[]`.

`--fix` writes available safe fixes and then reports remaining diagnostics. Safe fixes come from the same registry as LSP Quick Fixes; currently consecutive `.without()` calls with literal arguments and no comments can be combined. Other suggestions, including unused bindings and assignment grouping, remain diagnostics. Embedded YAML fixes have the same plain/literal mapping restrictions as editor Quick Fixes. See [lint behavior and fixes](features/lint.md) and the [rule reference](features/lint-rules.md).

## Files, directories, globs and stdin

Explicit files are processed directly. Directories are scanned recursively for `.blobl`, `.bloblang`, `.yaml` and `.yml` files. Traversal skips `.git`, `.cache`, `.state`, `.worktrees`, `.volumes`, `node_modules`, `vendor`, `target` and symlink entries. An explicitly supplied symlink is rejected; regular files reached through a symlinked parent directory are accepted. Files and diagnostics are processed in deterministic order.

Quote glob patterns to let `blobl` expand them consistently across shells. `*`, `?`, character classes, brace alternatives and `**` are supported; `**` matches recursively. Glob matches and explicit paths are deduplicated. Malformed patterns return `2`. An unmatched pattern returns `2` when its scan did not prune ignored entries. Empty results are accepted when ignore rules filtered matches or pruned directories; an entirely ignored input set returns `0`. Excluded directories are pruned before reading their descendant configuration or ignore files.

```sh
blobl lint '**/*.blobl' '**/*.yaml'
blobl fmt --write 'mappings/**/*.bloblang'
blobl lint --no-ignore '**/*.{blobl,yaml}'
```

Directory and glob scans honor Gitignore-style patterns in `.gitignore` and `.bloblangignore`, including nested files. The nearest `.bloblangrc.json` can add `files.exclude` patterns relative to its directory. Set `files.respectGitignore` to `false` to disable `.gitignore` processing while keeping `.bloblangignore` and configured exclusions. `--no-ignore` bypasses all three; generated-directory and symlink skips still apply, and configuration is still validated. Explicitly named regular files bypass exclusion rules.

YAML inputs must contain a single document. Multiple-document streams produce an error diagnostic in lint and a formatting failure in fmt; mappings in later documents are not processed.

No path, or a single `-`, reads stdin:

```sh
printf 'root=this.name\n' | blobl fmt
cat mapping.blobl | blobl lint -
cat config.yaml | blobl fmt --stdin-filepath config.yaml
cat config.yaml | blobl lint --format json --stdin-filepath config.yaml -
```

`--stdin-filepath` supplies the source location for YAML detection, imports and configuration lookup; it does not read that file. Without it, stdin is treated as Bloblang and uses the current working directory for imports and configuration. Stdin cannot be combined with other input paths. `--write` and `--fix` require files and cannot be used with stdin. `fmt --check` can check stdin.

## Configuration

Each file uses the nearest ancestor `.bloblangrc.json`, beginning in the file's directory and stopping at the nearest Git project root (a `.git` directory or worktree marker). Outside Git, lookup continues to the filesystem root. Embedded YAML uses the host file's location. Stdin uses `--stdin-filepath`, or the current working directory. Configuration files are not merged. Missing configuration uses defaults; invalid JSON, unknown keys or invalid values return `2`.

Bloblang imports resolve relative to the source file; absolute paths are accepted. External YAML `mapping: from "path"` references first try the YAML file's directory, then the Git project root. Editor requests use the selected workspace root instead of Git discovery when the document belongs to a workspace.

```json
{
  "files": {
    "exclude": ["generated/**"],
    "respectGitignore": true
  },
  "formatter": { "printWidth": 100 },
  "lint": {
    "enabled": true,
    "rules": {
      "correctness/environment/require-fallback": "error",
      "style/objects/combine-without": "warn"
    }
  }
}
```

See [configuration](core/settings.md) for accepted values and editor workspace boundaries. The CLI and LSP share configuration and path resolution.

## Exit status

| Status | Meaning |
| --- | --- |
| `0` | Success; lint has no warning/error diagnostics; checked formatting is unchanged. |
| `1` | Lint has warning/error diagnostics, or `fmt --check` found formatting drift. |
| `2` | Invalid arguments or configuration, an I/O failure, or a syntax/validation failure preventing formatting. |

Hints and informational lint diagnostics return `0` on their own. After `lint --fix`, the status reflects remaining diagnostics; errors preventing processing return `2`.

## Language server

```sh
blobl lsp
```

The server speaks LSP over stdin/stdout and logs to stderr. Configure an LSP client with executable `blobl` and argument `lsp`. The separate `bloblang-lsp` and older `language-server` executables retain their no-argument LSP startup. The VS Code extension manages `blobl` automatically. For a custom CLI executable, set `bloblang.server.path` to its path and `bloblang.server.args` to `["lsp"]`; use empty arguments with a legacy server.
