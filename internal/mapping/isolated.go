package mapping

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const (
	DefaultTimeout  = 5 * time.Second
	MaxRequestBytes = 4 << 20
	MaxResultBytes  = 4 << 20
)

// Execute runs a mapping in a short-lived copy of blobl. Killing the worker
// actually stops execution, unlike abandoning a goroutine in the evaluator.
// A shorter caller deadline takes precedence over the default five seconds.
func Execute(ctx context.Context, request Request) Result {
	executable, err := os.Executable()
	if err != nil {
		return workerError("worker", err.Error())
	}
	return executeWorker(ctx, request, executable, []string{"__mapping-worker"})
}

func workerError(kind, message string) Result {
	return Result{Metadata: map[string]any{}, Error: &Error{Kind: kind, Message: message}}
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, fmt.Errorf("mapping result exceeds %d bytes", b.limit)
	}
	return b.Buffer.Write(p)
}

func executeWorker(ctx context.Context, request Request, executable string, args []string) Result {
	if len(request.Input) == 0 {
		return workerError("input", "input is required; use null for an explicit null input")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return workerError("input", err.Error())
	}
	if len(data) > MaxRequestBytes {
		return workerError("input", "mapping request exceeds 4 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = bytes.NewReader(data)
	output := &boundedBuffer{limit: MaxResultBytes}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return workerError("timeout", "mapping execution deadline exceeded")
		}
		if ctx.Err() != nil {
			return workerError("canceled", ctx.Err().Error())
		}
		return workerError("worker", err.Error())
	}
	var result Result
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return workerError("worker", "invalid mapping worker response: "+err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return workerError("worker", "unexpected content after mapping worker response")
	}
	return result
}

// ServeWorker handles exactly one request. It is an internal subprocess
// protocol, not a second public server or a persistent execution session.
func ServeWorker(input io.Reader, output io.Writer) int {
	data, err := io.ReadAll(io.LimitReader(input, MaxRequestBytes+1))
	var result Result
	if err != nil {
		result = workerError("input", err.Error())
	} else if len(data) > MaxRequestBytes {
		result = workerError("input", "mapping request exceeds 4 MiB")
	} else {
		var request Request
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			result = workerError("input", err.Error())
		} else if err := decoder.Decode(new(any)); err != io.EOF {
			result = workerError("input", "unexpected content after mapping request")
		} else {
			result = Run(request)
		}
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return 2
	}
	return 0
}
