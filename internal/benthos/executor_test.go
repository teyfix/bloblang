package benthos

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang/internal/config"
)

func testExecutorConfig() *config.Config {
	return &config.Config{
		MaxInlineDocumentBytes: 150000,
		MaxInlineResultBytes:   100,
		PartialExecCacheSize:   100,
		PartialExecCacheTTL:    time.Minute,
	}
}

func newTestParser(t *testing.T) *Bloblang {
	t.Helper()
	p, err := NewBloblang()
	require.NoError(t, err)
	t.Cleanup(func() { p.Close("test") })
	return p
}

func TestExecuteCumulativeSingleLine(t *testing.T) {
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	doc := "#!sample {\"name\":\"alice\",\"age\":30}\nroot.name = this.name"
	sample := map[string]interface{}{"name": "alice", "age": 30}

	// through line 1 (the only root assignment) → {\"name\":\"alice\"}
	result, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, 1)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, `{"name":"alice"}`, result.Text)
}

func TestExecuteCumulativeMultiAssignment(t *testing.T) {
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	doc := "#!sample {}\nroot.name = this.name\nroot.age = this.age"
	sample := map[string]interface{}{"name": "alice", "age": 30}

	// through line 1 (first assignment) → only name field
	result1, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, 1)
	require.NoError(t, err)
	require.NotNil(t, result1)
	assert.Equal(t, `{"name":"alice"}`, result1.Text)

	// through line 2 (both assignments) → both fields
	result2, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, 2)
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Equal(t, `{"age":30,"name":"alice"}`, result2.Text)
}

func TestExecuteCumulativeBeforeFirstLine(t *testing.T) {
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	doc := "#!sample {}\nroot.name = this.name\nroot.age = this.age"
	sample := map[string]interface{}{"name": "alice", "age": 30}

	// throughLine = -1 means "before any assignment" → raw sample
	result, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, -1)
	require.NoError(t, err)
	require.NotNil(t, result)
	// raw sample marshalled
	assert.Equal(t, `{"age":30,"name":"alice"}`, result.Text)
}

func TestExecuteCumulativeInvalidatesCache(t *testing.T) {
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	uri := "file:///map.blobl"
	doc := "#!sample {}\nroot.name = this.name"
	sample := map[string]interface{}{"name": "alice"}

	result, err := executor.ExecuteCumulative(parser, uri, sample, doc, 1)
	require.NoError(t, err)
	require.Equal(t, `{"name":"alice"}`, result.Text)

	executor.InvalidateDocument(uri)
	result, err = executor.ExecuteCumulative(parser, uri, map[string]interface{}{"name": "bob"}, doc, 1)
	require.NoError(t, err)
	require.Equal(t, `{"name":"bob"}`, result.Text)
}

func TestExecuteCumulativeMultilineStatement(t *testing.T) {
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	// Multi-line assignment: root.msg spans lines 1-3
	doc := "#!sample {\"message\":\"hello world\"}\nroot.msg = this.\n  message.\n  replace(\"hello\", \"good morning\")"
	sample := map[string]interface{}{"message": "hello world"}

	// throughLine=1 (start of the multi-line statement) should include all continuation lines
	result, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, 1)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, `{"msg":"good morning world"}`, result.Text)
}

func TestExecuteCumulativeMultilineMultiStatement(t *testing.T) {
	parser := newTestParser(t)
	executor := NewExecutor(NewEnvironment(), testExecutorConfig())
	// Two multi-line assignments
	doc := "#!sample {\"message\":\"hello world\"}\nroot.msg = this.\n  message.\n  replace(\"hello\", \"good morning\")\nroot.upper = this.\n  message.\n  uppercase()"
	sample := map[string]interface{}{"message": "hello world"}

	// throughLine=1 → only first statement
	result1, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, 1)
	require.NoError(t, err)
	require.NotNil(t, result1)
	assert.Equal(t, `{"msg":"good morning world"}`, result1.Text)

	// throughLine=4 → both statements
	result2, err := executor.ExecuteCumulative(parser, "file:///map.blobl", sample, doc, 4)
	require.NoError(t, err)
	require.NotNil(t, result2)
	assert.Equal(t, `{"msg":"good morning world","upper":"HELLO WORLD"}`, result2.Text)
}
