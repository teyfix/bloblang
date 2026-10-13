# Signature help, outline and folding

These features use the standard LSP methods, so clients such as VS Code can expose them without custom commands. They also work in recognized embedded YAML mappings, with positions mapped back to the host file.

## Signature help

Typing `(`, `,` or `:` requests parameter guidance for a Bloblang function or method. Labels and parameter documentation come from the installed Benthos registry, including optional/default parameters and variadic arguments. The active parameter follows positional arguments or an explicit named argument, even when named arguments are reordered.

Incomplete calls are supported while typing. Nested calls, arrays and objects, comments, escaped strings and triple-quoted strings do not confuse argument counting. Unknown functions and positions outside a call return no signature. Signature help performs no mapping execution.

## Document outline

Named maps appear as functions containing their assignments. `let` declarations, `root` paths and metadata assignments provide navigation entries with separate declaration and selection ranges. Assignments in conditional statement blocks remain under the enclosing map. Independent mappings in YAML retain their host positions; synthetic interpolation wrappers are omitted from the outline.

## Folding

Multiline map bodies, conditional blocks, collections, calls and strings offer structural folds. Consecutive comment lines and import statements provide comment/import folds, and embedded YAML mappings can fold as a whole. Closing delimiters on their own lines remain visible. Single-line structures do not produce folds.

## Workspace references

Reference search and rename share source discovery and binding resolution. They search every workspace root, standalone `.blobl`/`.bloblang` files and recognized YAML mappings, including unopened files. Live buffers take precedence over disk. Named-map matches must resolve to the same declaration, so unrelated maps with the same name remain separate. Repeated `let` assignments follow the runtime variable's mapping scope.

Generated directories such as `.cache`, `.state` and `.worktrees` are excluded from workspace scans. Open documents are still available. Reference search uses recoverable syntax in incomplete mappings; rename requires valid syntax. See [rename limits](rename.md#limits).
