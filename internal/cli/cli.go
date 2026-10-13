// Package cli implements the blobl command and compatible language-server entrypoints.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/teyfix/bloblang-lsp/internal/config"
	"github.com/teyfix/bloblang-lsp/internal/lsp"
)

type BuildInfo struct {
	Version   string
	DateBuilt string
}

func (b BuildInfo) version() string {
	if b.Version == "" {
		return "dev"
	}
	return b.Version
}

// Run executes blobl, returning 0 for success, 1 for findings or formatting drift,
// and 2 for usage, configuration, or operational errors. Streams belong to the caller.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 {
		return printHelp(stdout, stderr)
	}
	switch args[0] {
	case "--version", "-version", "version":
		if len(args) != 1 {
			return fail(stderr, errors.New("version accepts no arguments"))
		}
		version := build.version()
		if build.DateBuilt != "" {
			version += " (built " + build.DateBuilt + ")"
		}
		_, err := fmt.Fprintln(stdout, version)
		if err != nil {
			return fail(stderr, err)
		}
		return 0
	case "help", "--help", "-h":
		if len(args) == 2 && args[0] == "help" {
			return Run(ctx, []string{args[1], "--help"}, stdin, stdout, stderr, build)
		}
		if len(args) != 1 {
			return fail(stderr, errors.New("help accepts at most one command"))
		}
		return printHelp(stdout, stderr)
	case "lsp":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, err := fmt.Fprintln(stdout, "Usage: blobl lsp\nServe the Language Server Protocol on stdin/stdout.")
			if err != nil {
				return fail(stderr, err)
			}
			return 0
		}
		if len(args) != 1 {
			return fail(stderr, errors.New("lsp accepts no arguments"))
		}
		return runLSP(ctx, stdin, stdout, stderr, build)
	case "fmt", "lint":
		return runTool(ctx, args[0], args[1:], stdin, stdout, stderr)
	default:
		return fail(stderr, fmt.Errorf("unknown command %q; use blobl --help", args[0]))
	}
}

// RunLegacy preserves the historical no-argument stdio server startup.
func RunLegacy(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 {
		return runLSP(ctx, stdin, stdout, stderr, build)
	}
	return Run(ctx, args, stdin, stdout, stderr, build)
}

func printHelp(stdout, stderr io.Writer) int {
	_, err := io.WriteString(stdout, `Usage: blobl <command> [options] [paths...]

Commands:
  lsp    Serve LSP over stdin/stdout
  fmt    Format one file to stdout, or use --write/--check for files/directories
  lint   Report static diagnostics; --fix applies safe fixes

Use blobl <command> --help for command options, or blobl --version.
`)
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "blobl: %v\n", err)
	return 2
}

type commandOptions struct {
	write, check, fix bool
	noIgnore          bool
	format, stdinPath string
	tabSize           int
}

func parseOptions(command string, args []string, stdout io.Writer) (commandOptions, []string, error) {
	var opts commandOptions
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.stdinPath, "stdin-filepath", "", "Virtual file path for stdin configuration, imports, and YAML detection")
	fs.BoolVar(&opts.noIgnore, "no-ignore", false, "Bypass ignore files and configured exclusions (built-in skips remain)")
	if command == "fmt" {
		fs.BoolVar(&opts.write, "write", false, "Update files in place")
		fs.BoolVar(&opts.check, "check", false, "List files needing formatting and exit 1")
		fs.IntVar(&opts.tabSize, "tab-size", 2, "Indentation width (1 to 8)")
	} else {
		fs.BoolVar(&opts.fix, "fix", false, "Apply safe fixes in place and report remaining diagnostics")
		fs.StringVar(&opts.format, "format", "text", "Diagnostic output: text or json")
	}
	fs.Usage = func() {}
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		var help bytes.Buffer
		_, _ = fmt.Fprintf(&help, "Usage: blobl %s [options] [paths...]\n", command)
		_, _ = fmt.Fprintln(&help, "No paths or '-' reads stdin. Put options before paths.")
		_, _ = fmt.Fprintln(&help, "Quoted glob patterns are expanded, including ** for recursive matches.")
		_, _ = fmt.Fprintln(&help, "Directories recurse through .blobl, .bloblang, .yaml and .yml files.")
		_, _ = fmt.Fprintln(&help, "Skip symlinks and .git, .cache, .state, .worktrees, .volumes, node_modules, vendor, target directories.")
		fs.SetOutput(&help)
		fs.PrintDefaults()
		if _, writeErr := io.Copy(stdout, &help); writeErr != nil {
			return opts, nil, writeErr
		}
	}
	if err != nil {
		return opts, nil, err
	}
	if opts.write && opts.check {
		return opts, nil, errors.New("--write and --check are mutually exclusive")
	}
	if command == "fmt" && (opts.tabSize < 1 || opts.tabSize > 8) {
		return opts, nil, errors.New("--tab-size must be between 1 and 8")
	}
	if command == "lint" && opts.format != "text" && opts.format != "json" {
		return opts, nil, errors.New("--format must be text or json")
	}
	paths := fs.Args()
	stdin := len(paths) == 0 || len(paths) == 1 && paths[0] == "-"
	for _, path := range paths {
		if path == "-" && !stdin {
			return opts, nil, errors.New("stdin '-' cannot be combined with other paths")
		}
	}
	if stdin && (opts.write || opts.fix) {
		return opts, nil, errors.New("--write and --fix require file paths; stdin cannot be updated")
	}
	if !stdin && opts.stdinPath != "" {
		return opts, nil, errors.New("--stdin-filepath requires stdin input")
	}
	return opts, paths, nil
}

type fileDiagnostic struct {
	File string `json:"file"`
	lsp.ToolDiagnostic
}

func runTool(ctx context.Context, command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, paths, err := parseOptions(command, args, stdout)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return fail(stderr, err)
	}
	inputs, err := collectInputs(paths, stdin, opts.stdinPath, command == "fmt" && !opts.write && !opts.check, opts.noIgnore)
	if err != nil {
		return fail(stderr, err)
	}
	tool, err := lsp.NewTooling()
	if err != nil {
		return fail(stderr, err)
	}
	defer tool.Close()
	exitCode := 0
	diagnostics := make([]fileDiagnostic, 0)
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return fail(stderr, err)
		}
		text, err := input.read()
		if err != nil {
			return fail(stderr, err)
		}
		if command == "fmt" {
			formatted, err := tool.Format(input.path, text, opts.tabSize)
			if err != nil {
				return fail(stderr, fmt.Errorf("%s: %w", input.path, err))
			}
			switch {
			case opts.write:
				if formatted != text {
					if err := replaceFile(input.path, text, formatted); err != nil {
						return fail(stderr, err)
					}
				}
			case opts.check:
				if formatted != text {
					exitCode = 1
					if _, err := fmt.Fprintln(stdout, input.path); err != nil {
						return fail(stderr, err)
					}
				}
			default:
				if _, err := io.WriteString(stdout, formatted); err != nil {
					return fail(stderr, err)
				}
			}
			continue
		}
		result, err := tool.Lint(input.path, text, opts.fix)
		if err != nil {
			return fail(stderr, fmt.Errorf("%s: %w", input.path, err))
		}
		if opts.fix && result.Text != text {
			if err := replaceFile(input.path, text, result.Text); err != nil {
				return fail(stderr, err)
			}
		}
		for _, diagnostic := range result.Diagnostics {
			diagnostics = append(diagnostics, fileDiagnostic{File: input.path, ToolDiagnostic: diagnostic})
			if diagnostic.Severity == "error" || diagnostic.Severity == "warn" {
				exitCode = 1
			}
		}
	}
	if command == "lint" {
		sort.SliceStable(diagnostics, func(i, j int) bool {
			a, b := diagnostics[i], diagnostics[j]
			if a.File != b.File {
				return a.File < b.File
			}
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			if a.Column != b.Column {
				return a.Column < b.Column
			}
			if a.Severity != b.Severity {
				return a.Severity < b.Severity
			}
			if a.Code != b.Code {
				return a.Code < b.Code
			}
			return a.Message < b.Message
		})
		if opts.format == "json" {
			if err := json.NewEncoder(stdout).Encode(diagnostics); err != nil {
				return fail(stderr, err)
			}
		} else {
			for _, d := range diagnostics {
				code := ""
				if d.Code != "" {
					code = " [" + d.Code + "]"
				}
				if _, err := fmt.Fprintf(stdout, "%s:%d:%d: %s%s: %s\n", d.File, d.Line, d.Column, d.Severity, code, d.Message); err != nil {
					return fail(stderr, err)
				}
			}
		}
	}
	return exitCode
}

type stdio struct {
	io.Reader
	io.Writer
}

func (s stdio) Close() error {
	if closer, ok := s.Reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func runLSP(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) int {
	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, err)
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(strings.ToUpper(cfg.LogLevel)))
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})).With("version", build.version())
	if build.DateBuilt != "" {
		logger = logger.With("dateBuilt", build.DateBuilt)
	}
	handler, err := lsp.NewHandler(cfg, logger, build.version())
	if err != nil {
		return fail(stderr, err)
	}
	if err := lsp.Run(ctx, handler, logger, stdio{Reader: stdin, Writer: stdout}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func absolutePath(path string) (string, error) {
	return filepath.Abs(filepath.Clean(path))
}
