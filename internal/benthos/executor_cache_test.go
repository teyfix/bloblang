package benthos

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/stretchr/testify/require"
)

func countingExecutor(t *testing.T, function func() (any, error)) *Executor {
	t.Helper()
	env := NewEnvironment()
	require.NoError(t, env.RegisterFunctionV2("cache_tick", bloblang.NewPluginSpec().Impure(), func(*bloblang.ParsedParams) (bloblang.Function, error) { return function, nil }))
	return NewExecutor(env, testExecutorConfig())
}

func TestExecutionCacheTracksFixtureContentsAndReturnsOwnedValues(t *testing.T) {
	var calls atomic.Int64
	executor := countingExecutor(t, func() (any, error) { return calls.Add(1), nil })
	sample := &Sample{Value: map[string]any{}, Meta: map[string]any{"key": "original"}}
	run := func() *PartialResult {
		r, err := executor.execute("file:///mapping.blobl", sample, `root = {"value": cache_tick()}`)
		require.NoError(t, err)
		return r
	}
	first := run()
	first.Value.(map[string]any)["value"] = int64(99)
	first.Meta["key"] = "changed"
	second := run()
	require.Equal(t, int64(1), calls.Load())
	require.Equal(t, int64(1), second.Value.(map[string]any)["value"])
	require.Equal(t, "original", second.Meta["key"])
	for _, mutate := range []func(){
		func() { sample.Value = "new input" },
		func() { sample.Root = map[string]any{"seed": true} },
		func() { sample.HasRoot = true },
		func() { sample.Meta["key"] = "new metadata" },
		func() { sample.Env = map[string]string{"NAME": "new env"} },
	} {
		before := calls.Load()
		mutate()
		run()
		require.Equal(t, before+1, calls.Load())
	}
}

func TestExecutionCacheDoesNotRestoreInvalidatedInflightResult(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	executor := countingExecutor(t, func() (any, error) {
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			<-release
		}
		return n, nil
	})
	const uri = "file:///mapping.blobl#bloblang-0"
	sample := &Sample{Value: map[string]any{}}
	done := make(chan error, 1)
	go func() { _, err := executor.execute(uri, sample, "root = cache_tick()"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("evaluation did not start")
	}
	executor.InvalidateDocument("file:///mapping.blobl")
	close(release)
	require.NoError(t, <-done)
	r, err := executor.execute(uri, sample, "root = cache_tick()")
	require.NoError(t, err)
	require.Equal(t, int64(2), r.Value)
	require.Equal(t, int64(2), calls.Load())
}

func TestExecutionCacheBoundsAndCancellation(t *testing.T) {
	var calls atomic.Int64
	executor := countingExecutor(t, func() (any, error) { return calls.Add(1), nil })
	executor.config.PartialExecCacheSize = 1
	sample := &Sample{Value: map[string]any{}}
	for _, source := range []string{"root = cache_tick()", "root = cache_tick() + 1", "root = cache_tick()"} {
		_, err := executor.execute("file:///mapping.blobl", sample, source)
		require.NoError(t, err)
	}
	require.Equal(t, int64(3), calls.Load(), "evicted results must be evaluated again")
	executor.config.PartialExecCacheTTL = time.Nanosecond
	_, err := executor.execute("file:///mapping.blobl", sample, "root = cache_tick()")
	require.NoError(t, err)
	require.Equal(t, int64(4), calls.Load(), "expired results must be evaluated again")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executor.executeContext(ctx, "file:///mapping.blobl", sample, "root = cache_tick()")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(4), calls.Load())
}
