package mapping

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMappingWorkerHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "mapping-worker":
		os.Exit(ServeWorker(os.Stdin, os.Stdout))
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	}
}

func TestIsolatedMappingExecution(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	req := Request{Source: `root = this.name.uppercase()`, Input: json.RawMessage(`{"name":"Ada"}`)}
	result := executeWorker(context.Background(), req, executable, []string{"-test.run=^TestMappingWorkerHelper$", "--", "mapping-worker"})
	require.Nil(t, result.Error)
	require.Equal(t, "ADA", result.Output)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	result = executeWorker(ctx, req, executable, []string{"-test.run=^TestMappingWorkerHelper$", "--", "hang"})
	require.NotNil(t, result.Error)
	require.Equal(t, "timeout", result.Error.Kind)
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestWorkerProtocolRejectsMissingInputAndTrailingData(t *testing.T) {
	for _, input := range []string{`{"source":"root = this"}`, `{"source":"root = this","input":null} {}`} {
		var out strings.Builder
		require.Zero(t, ServeWorker(strings.NewReader(input), &out))
		var result Result
		require.NoError(t, json.Unmarshal([]byte(out.String()), &result))
		require.NotNil(t, result.Error)
		require.Equal(t, "input", result.Error.Kind)
	}
	result := executeWorker(context.Background(), Request{Source: "root = this"}, "missing", nil)
	require.Equal(t, "input", result.Error.Kind)
}

func TestWorkerBufferBound(t *testing.T) {
	b := boundedBuffer{limit: 4}
	_, err := b.Write([]byte("1234"))
	require.NoError(t, err)
	_, err = b.Write([]byte("5"))
	require.Error(t, err)
	require.Equal(t, "1234", b.String())
}
