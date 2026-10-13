package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBinaryStartup(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		args         []string
		graceful     bool
		version      string
	}{
		{name: "bloblang-lsp", source: ".", graceful: true, version: "v9.8.7"},
		{name: "language-server", source: "../language-server", graceful: true, version: "v9.8.7"},
		{name: "blobl", source: "../blobl", args: []string{"lsp"}, graceful: true, version: "v9.8.7"},
		{name: "legacy-stdin-close", source: "."},
	} {
		t.Run(tc.name, func(t *testing.T) { testBinaryStartup(t, tc.source, tc.args, tc.graceful, tc.version) })
	}
}

func testBinaryStartup(t *testing.T, source string, args []string, graceful bool, version string) {
	bin := filepath.Join(t.TempDir(), "bloblang-lsp.exe")
	buildArgs := []string{"build", "-o", bin}
	if version != "" {
		buildArgs = append(buildArgs, "-ldflags", "-X main.Version="+version+" -X main.DateBuilt=2026-10-13")
	}
	build := exec.Command("go", append(buildArgs, ".")...)
	build.Dir = source
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "BLOBLANG_LSP_LOG_LEVEL=info")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	writeMessage(t, stdin, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"processId":    nil,
			"capabilities": map[string]interface{}{},
		},
	})
	response := readMessage(t, stdout)
	require.Contains(t, response, "capabilities")
	var initialized struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(response), &initialized))
	wantVersion := version
	if wantVersion == "" {
		wantVersion = "dev"
	}
	require.Equal(t, wantVersion, initialized.Result.ServerInfo.Version)

	writeMessage(t, stdin, map[string]interface{}{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	_ = readMessage(t, stdout)
	if graceful {
		writeMessage(t, stdin, map[string]interface{}{"jsonrpc": "2.0", "method": "exit"})
	} else {
		require.NoError(t, stdin.Close())
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(2 * time.Second):
		t.Fatal("server did not terminate after shutdown")
	case err := <-done:
		if graceful {
			require.NoError(t, err, stderr.String())
		}
	}
	if graceful {
		_ = stdin.Close() // Wait may already have closed the parent pipe.
	}
	var startup string
	for _, line := range strings.Split(stderr.String(), "\n") {
		if strings.Contains(line, `msg="server starting"`) {
			startup = line
			break
		}
	}
	require.NotEmpty(t, startup, stderr.String())
	require.Contains(t, startup, "version="+wantVersion)
	if version != "" {
		require.Contains(t, startup, "dateBuilt=2026-10-13")
	}
}

func writeMessage(t *testing.T, w io.Writer, msg map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	require.NoError(t, err)
}

func readMessage(t *testing.T, r io.Reader) string {
	t.Helper()
	br := bufio.NewReader(r)
	contentLength := 0
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		_, _ = fmt.Sscanf(line, "Content-Length: %d", &contentLength)
	}
	require.Positive(t, contentLength)
	body := make([]byte, contentLength)
	_, err := io.ReadFull(br, body)
	require.NoError(t, err)
	return string(body)
}
