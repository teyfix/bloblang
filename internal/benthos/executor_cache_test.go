package benthos

import (
	"context"
	"sync"
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
	// Expire the entry explicitly: Windows' monotonic clock can report zero
	// elapsed time between adjacent calls, even with a one-nanosecond TTL.
	executor.cache.mu.Lock()
	for _, element := range executor.cache.entries {
		element.Value.(*executionEntry).created = time.Now().Add(-executor.config.PartialExecCacheTTL - time.Second)
	}
	executor.cache.mu.Unlock()
	_, err := executor.execute("file:///mapping.blobl", sample, "root = cache_tick()")
	require.NoError(t, err)
	require.Equal(t, int64(4), calls.Load(), "expired results must be evaluated again")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executor.executeContext(ctx, "file:///mapping.blobl", sample, "root = cache_tick()")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(4), calls.Load())
}

type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *observedWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func TestExecutionCacheSharesInflightWorkAndCancelsWaiters(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	executor := countingExecutor(t, func() (any, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return int64(7), nil
	})
	sample := &Sample{Value: map[string]any{}}
	run := func(ctx context.Context, done chan error) {
		_, err := executor.executeContext(ctx, "file:///mapping.blobl", sample, "root = cache_tick()")
		done <- err
	}
	first := make(chan error, 1)
	go run(context.Background(), first)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first execution did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiter := &observedWaitContext{Context: ctx, waiting: make(chan struct{})}
	second := make(chan error, 1)
	go run(waiter, second)
	select {
	case <-waiter.waiting:
	case <-time.After(time.Second):
		t.Fatal("second execution did not wait for shared work")
	}
	cancel()
	require.ErrorIs(t, <-second, context.Canceled)
	thirdContext := &observedWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	third := make(chan error, 1)
	go run(thirdContext, third)
	select {
	case <-thirdContext.waiting:
	case <-time.After(time.Second):
		t.Fatal("third execution did not wait for shared work")
	}
	close(release)
	require.NoError(t, <-first)
	require.NoError(t, <-third)
	require.Equal(t, int64(1), calls.Load())
}
