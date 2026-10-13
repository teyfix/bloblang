package benthos

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/redpanda-data/benthos/v4/public/service"
	"github.com/teyfix/bloblang/internal/config"
	"github.com/teyfix/bloblang/internal/fileuri"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type PartialResult struct {
	Text      string
	Truncated bool
	Full      string
	Value     any
	Meta      map[string]any
	Deleted   bool
}
type Executor struct {
	benv     *bloblang.Environment
	config   *config.Config
	cache    executionCache
	importer func(string, string) ([]byte, error)
}

func NewExecutor(benv *bloblang.Environment, cfg *config.Config) *Executor {
	return &Executor{benv: benv, config: cfg}
}
func isStatementKind(kind string) bool {
	switch kind {
	case "root_assignment", "let_assignment", "meta_assignment", "map_declaration", "import_statement", "if_statement":
		return true
	}
	return false
}
func (e *Executor) result(value any, meta map[string]any, deleted bool) (*PartialResult, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	full := string(encoded)
	if deleted {
		full = "deleted()"
	}
	short := full
	truncated := false
	if n := e.config.MaxInlineResultBytes; n >= 0 && len(short) > n {
		for n > 0 && !utf8.RuneStart(short[n]) {
			n--
		}
		short = short[:n] + "…"
		truncated = true
	}
	return &PartialResult{Text: short, Full: full, Truncated: truncated, Value: value, Meta: meta, Deleted: deleted}, nil
}
func sampleOf(v any) *Sample {
	if s, ok := v.(*Sample); ok {
		return s
	}
	return &Sample{Value: v}
}
func (e *Executor) execute(uri string, sample *Sample, snippet string) (*PartialResult, error) {
	return e.executeContext(context.Background(), uri, sample, snippet)
}

// SetImporter shares the caller's source-path rules with runtime evaluation.
// Configure it before serving requests.
func (e *Executor) SetImporter(importer func(string, string) ([]byte, error)) {
	e.importer = importer
}

func (e *Executor) executeContext(ctx context.Context, uri string, sample *Sample, snippet string) (*PartialResult, error) {
	return e.cachedExecution(ctx, uri, sample, snippet, func() (*PartialResult, error) {
		return e.executeUncached(ctx, uri, sample, snippet)
	})
}

func (e *Executor) executeUncached(ctx context.Context, uri string, sample *Sample, snippet string) (*PartialResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	preview, err := PreviewEnvironment(e.benv, sample)
	if err != nil {
		return nil, err
	}
	env := preview.WithCustomImporter(func(name string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.importer != nil {
			return e.importer(uri, name)
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(documentDir(uri), name)
		}
		return os.ReadFile(name)
	})
	snippet += "\nroot = match { root.type() == \"delete\" => deleted(), root.type() == \"nothing\" => {\"__bloblang_lsp_output\": this}, _ => {\"__bloblang_lsp_output\": root} }"
	parsed, err := env.Parse(snippet)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msg := service.NewMessage(nil)
	msg.SetStructuredMut(cloneValue(sample.Value))
	for k, v := range sample.Meta {
		msg.MetaSetMut(k, cloneValue(v))
	}
	var out *service.Message
	if sample.HasRoot {
		target := service.NewMessage(nil)
		target.SetStructuredMut(cloneValue(sample.Root))
		out, err = target.BloblangMutateFrom(parsed, msg)
	} else {
		out, err = msg.BloblangQuery(parsed)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if out == nil {
		return e.result(nil, nil, true)
	}
	value, err := out.AsStructured()
	if err != nil {
		return nil, err
	}
	if obj, ok := value.(map[string]any); ok {
		value = obj["__bloblang_lsp_output"]
	}
	meta := map[string]any{}
	_ = out.MetaWalkMut(func(k string, v any) error { meta[k] = v; return nil })
	return e.result(value, meta, false)
}
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, c := range x {
			out[k] = cloneValue(c)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = cloneValue(c)
		}
		return out
	case []byte:
		return append([]byte(nil), x...)
	}
	return v
}

func documentDir(uri string) string {
	if i := strings.Index(uri, "#"); i >= 0 {
		uri = uri[:i]
	}
	u, err := urlPath(uri)
	if err == nil {
		return filepath.Dir(u)
	}
	return "."
}

// ExecuteThrough executes complete statements ending before the byte cutoff.
func (e *Executor) ExecuteThrough(parser *Bloblang, uri string, sample *Sample, text string, cutoff uint) (*PartialResult, error) {
	return e.ExecuteThroughContext(context.Background(), parser, uri, sample, text, cutoff)
}

func (e *Executor) ExecuteThroughContext(ctx context.Context, parser *Bloblang, uri string, sample *Sample, text string, cutoff uint) (*PartialResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(text) > e.config.MaxInlineDocumentBytes {
		return nil, nil
	}
	tree, err := parser.Parse(uri, text)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	var parts []string
	root := tree.RootNode()
	for i := uint(0); i < root.ChildCount(); i++ {
		n := root.Child(i)
		if isStatementKind(n.Kind()) && (n.EndByte() <= cutoff || n.Kind() == "map_declaration" || n.Kind() == "import_statement") {
			parts = append(parts, n.Utf8Text([]byte(text)))
		}
	}
	if len(parts) == 0 {
		if sample.HasRoot {
			return e.result(sample.Root, sample.Meta, false)
		}
		return e.result(sample.Value, sample.Meta, false)
	}
	return e.executeContext(ctx, uri, sample, strings.Join(parts, "\n"))
}
func (e *Executor) ExecuteCumulative(parser *Bloblang, uri string, value any, text string, line int) (*PartialResult, error) {
	sample := sampleOf(value)
	if line < 0 {
		if sample.HasRoot {
			return e.result(sample.Root, sample.Meta, false)
		}
		return e.result(sample.Value, sample.Meta, false)
	}
	tree, err := parser.Parse(uri, text)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	var cutoff uint
	root := tree.RootNode()
	for i := uint(0); i < root.ChildCount(); i++ {
		n := root.Child(i)
		if isStatementKind(n.Kind()) && int(n.StartPosition().Row) <= line {
			cutoff = n.EndByte()
		}
	}
	return e.ExecuteThrough(parser, uri, sample, text, cutoff)
}

// EvaluateExpression appends a synthetic assignment after preceding complete statements.
// The runtime evaluates the synthetic RHS against the preceding output state.
func (e *Executor) EvaluateExpression(parser *Bloblang, uri string, sample *Sample, text string, statement, expr *tree_sitter.Node) (*PartialResult, error) {
	return e.EvaluateExpressionContext(context.Background(), parser, uri, sample, text, statement, expr)
}

func (e *Executor) EvaluateExpressionContext(ctx context.Context, parser *Bloblang, uri string, sample *Sample, text string, statement, expr *tree_sitter.Node) (*PartialResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(text) > e.config.MaxInlineDocumentBytes {
		return nil, nil
	}
	if statement == nil || expr == nil {
		return nil, nil
	}
	top := statement
	for top.Parent() != nil && top.Parent().Kind() != "source" {
		top = top.Parent()
	}
	var parts []string
	tree, err := parser.Parse(uri, text)
	if err != nil {
		return nil, err
	}
	defer tree.Close()
	root := tree.RootNode()
	for i := uint(0); i < root.ChildCount(); i++ {
		n := root.Child(i)
		if isStatementKind(n.Kind()) && (n.EndByte() <= top.StartByte() || n.Kind() == "map_declaration" || n.Kind() == "import_statement") {
			parts = append(parts, n.Utf8Text([]byte(text)))
		}
	}

	expression := expr.Utf8Text([]byte(text))
	// Benthos evaluates the assignment RHS before replacing root. Appending a
	// synthetic assignment therefore preserves current root reads without source rewrites.
	if expr.Kind() == "root" {
		expression = "if root.type() == \"nothing\" { this } else { root }"
	}
	var replacement string
	marker := "__bloblang_lsp_hover"
	for strings.Contains(text, marker) {
		marker += "_"
	}
	replacement += "root = {\"" + marker + "\": " + expression + "}"
	if top.Id() == statement.Id() {
		parts = append(parts, replacement)
	} else {
		parts = append(parts, trimBranch(top, statement, replacement, text))
	}
	result, err := e.executeContext(ctx, uri, sample, strings.Join(parts, "\n"))
	if err != nil || result == nil {
		return result, err
	}
	obj, ok := result.Value.(map[string]any)
	if !ok {
		return nil, nil
	}
	value, exists := obj[marker]
	if !exists {
		return nil, nil
	}
	return e.result(value, result.Meta, false)

}

func looksIncomplete(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") || strings.Contains(msg, "unexpected end") || strings.Contains(msg, "unterminated")
}
func urlPath(uri string) (string, error) {
	return fileuri.Path(uri)
}

func containsNode(parent, node *tree_sitter.Node) bool {
	return parent != nil && parent.StartByte() <= node.StartByte() && parent.EndByte() >= node.EndByte()
}
func trimBranch(n, target *tree_sitter.Node, replacement, text string) string {
	if n.Id() == target.Id() {
		return replacement
	}
	if n.Kind() == "if_statement" {
		condition := n.ChildByFieldName("condition")
		consequence := n.ChildByFieldName("consequence")
		alternative := n.ChildByFieldName("alternative")
		left := ""
		right := ""
		if containsNode(consequence, target) {
			left = trimBranch(consequence, target, replacement, text)
		}
		if containsNode(alternative, target) {
			right = trimBranch(alternative, target, replacement, text)
		}
		return "if " + condition.Utf8Text([]byte(text)) + " {\n" + left + "\n} else {\n" + right + "\n}"
	}
	var parts []string
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if !isStatementKind(child.Kind()) {
			continue
		}
		if child.EndByte() <= target.StartByte() {
			parts = append(parts, child.Utf8Text([]byte(text)))
		} else if containsNode(child, target) {
			parts = append(parts, trimBranch(child, target, replacement, text))
			break
		}
	}
	return strings.Join(parts, "\n")
}
