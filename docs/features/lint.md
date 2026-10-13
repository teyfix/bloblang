# Lint rules and fixes

The AST linter supports the seven slash-separated IDs in the [generated rule reference](lint-rules.md). All keys remain flat under `lint.rules`, which gives JSON completion the full rule names while retaining a visible namespace hierarchy.

Run the same static analysis outside an editor with `blobl lint` on files, directories or stdin. `blobl lint --fix` applies available safe fixes to files and reports the diagnostics that remain. Warnings and errors return exit status `1`; hints and informational findings do not. CLI lint does not load samples or execute mappings. See the [CLI reference](../cli.md) for configuration, JSON output and stdin handling.

```json
{
  "lint": {
    "enabled": true,
    "rules": {
      "correctness/environment/require-fallback": "warn",
      "style/assignments/prefer-grouped": {
        "severity": "hint",
        "minAssignments": 4
      },
      "style/objects/prefer-with": "off"
    }
  }
}
```

The environment rule distinguishes unset values from errors: `env()` returns null when unset, so `.catch(...)` alone is insufficient. Recognized handling includes `.or(default)`, `.or(throw(...))`, `.not_null().catch(...)`, a coalesce fallback when the lookup is the left operand, and supported explicit null checks on a local binding.

Assignment grouping, object projection/deletion, and array existence rules are advisory. Rewrites need care: `assign()` merges nested objects, `with()` omits missing fields, and `any()` changes predicate evaluation through short-circuiting. The implementation limits grouping findings based on output state and expression structure. Unused local bindings are warnings and are not automatically removed because evaluation may perform work or throw.

`style/objects/combine-without` offers an explicit `quickfix` for consecutive `without()` calls with literal arguments and no comments. Embedded YAML fixes preserve host indentation and validate the complete edited YAML. They are offered for plain and literal mappings; quoted and folded mappings retain their diagnostics without this edit. Other rules currently offer diagnostics only. The server does not implement a `source.fixAll` action or apply lint refactors during formatting.

Suppress a rule for the syntax node beginning on the next source line with its exact ID. The suppression covers that node after formatting wraps it across several lines. Multiple IDs can be separated by commas or spaces; `all` suppresses all rules for the target node. A reason after ` -- ` is optional.

```bloblang
# bloblang-lint-disable-next-line correctness/environment/require-fallback -- optional value
root.optional = env("OPTIONAL")
```

Registry descriptions, default severities, options, and safe-fix capability live in [`internal/editorconfig/config.go`](../../internal/editorconfig/config.go); diagnostics and code actions live in [`internal/lsp/lint.go`](../../internal/lsp/lint.go). See [settings](../core/settings.md) for schema generation and invalid-configuration behavior.
