# Rename

Available starting with bloblang-lsp **v0.2.2**. In VS Code, place the cursor on a binding or reference and press **F2** (Rename Symbol). The server implements standard `textDocument/prepareRename` and `textDocument/rename`; other LSP clients can use the same support.

## Supported bindings

- `let movie = ...` and its `$movie` references within the mapping. Repeated assignments to that variable are renamed together; separate named maps keep their own variables.
- Lambda parameters such as `c -> c.name`. Nested lambdas with the same parameter name remain separate; the receiver of a nested call still belongs to the enclosing scope.
- Named `map` declarations and literal `.apply("map_name")` references that resolve to them. Imported maps are followed through local imports. Every workspace root is searched for `.blobl`, `.bloblang`, `.yaml` and `.yml` files, including unopened files; open buffers take precedence over disk contents.
- Recognized embedded YAML mappings, preserving scalar quoting, escapes, indentation and adjacent YAML. Separate mappings within a YAML file remain independent.

Reference search shares this discovery and binding resolution, including YAML consumers and unsaved buffers. Generated `.cache`, `.state` and `.worktrees` directories are excluded from scans.

The rename field contains only the name. `$` prefixes and string delimiters stay in the source. New names must be identifiers: letters, digits and underscores, with a letter or underscore first. Invalid names, existing bindings and changes that would capture another binding are rejected before returning edits.

## Limits

Field paths (`this.title`, `root.title`), metadata keys, built-in functions, methods and filenames are not rename targets. Computed map names such as `.apply($map_name)` cannot be resolved statically and are not changed. Consumers outside the open workspace are not searched, although a selected map's imported definition is included. Documents or embedded regions with syntax errors do not offer rename.

The existing VS Code extension uses the server's advertised rename capability; no extension update or custom keybinding is required. Run **Bloblang: Restart Language Server** to resolve the latest server release, unless `bloblang.server.path` selects a manually managed binary.
