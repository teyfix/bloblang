// Package evaluation combines static diagnostics and isolated mapping execution.
package evaluation

import (
	"context"
	"github.com/teyfix/bloblang-lsp/internal/lsp"
	"github.com/teyfix/bloblang-lsp/internal/mapping"
)

// Run resolves files and sample directives, lints in the source file's project
// context, and evaluates once in a worker process. Lint findings do not prevent
// execution; configuration failures are reported separately as lintError.
func Run(ctx context.Context, req mapping.Request) mapping.Result {
	return run(ctx, req, mapping.Execute)
}

func run(ctx context.Context, req mapping.Request, execute func(context.Context, mapping.Request) mapping.Result) mapping.Result {
	result := mapping.Result{Metadata: map[string]any{}, Diagnostics: []mapping.Diagnostic{}}
	resolved, err := mapping.Resolve(req)
	if err != nil {
		result.Error = &mapping.Error{Kind: "input", Message: err.Error()}
		return result
	}
	if err := ctx.Err(); err != nil {
		result.Error = &mapping.Error{Kind: "canceled", Message: err.Error()}
		return result
	}
	imports := resolved.Imports
	if imports == nil {
		imports = map[string]string{}
	}
	tool, err := lsp.NewToolingWithImports(imports)
	if err != nil {
		result.LintError = &mapping.Error{Kind: "lint", Message: err.Error()}
	} else {
		path := resolved.Origin
		if path == "" {
			path = "mapping.blobl"
		}
		lint, err := tool.Lint(path, resolved.Source, false)
		tool.Close()
		if err != nil {
			result.LintError = &mapping.Error{Kind: "config", Message: err.Error()}
		} else {
			for _, d := range lint.Diagnostics {
				result.Diagnostics = append(result.Diagnostics, mapping.Diagnostic{
					Line: d.Line, Column: d.Column, EndLine: d.EndLine, EndColumn: d.EndColumn,
					Severity: d.Severity, Code: d.Code, Message: d.Message,
				})
			}
		}
	}
	output := execute(ctx, resolved)
	output.Diagnostics, output.LintError = result.Diagnostics, result.LintError
	return output
}
