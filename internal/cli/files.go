package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/teyfix/bloblang-lsp/internal/editorconfig"
)

type inputFile struct {
	path string
	text *string
}

func (f inputFile) read() (string, error) {
	if f.text != nil {
		return *f.text, nil
	}
	if err := rejectSymlinks(f.path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", f.path, err)
	}
	return string(data), nil
}

func collectInputs(paths []string, stdin io.Reader, stdinPath string, singleFile, noIgnore bool) ([]inputFile, error) {
	if len(paths) == 0 || len(paths) == 1 && paths[0] == "-" {
		if stdinPath == "" {
			stdinPath = "stdin.blobl"
		}
		path, err := absolutePath(stdinPath)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("stdin: %w", err)
		}
		text := string(data)
		return []inputFile{{path: path, text: &text}}, nil
	}
	seen := make(map[string]bool)
	expanded, err := expandPaths(paths, noIgnore)
	if err != nil {
		return nil, err
	}
	for _, target := range expanded {
		path := target.path
		policy := target.policy
		if err := rejectSymlinks(path); err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			if policy != nil {
				ignored, err := policy.ignored(path, false)
				if err != nil {
					return nil, err
				}
				if ignored {
					continue
				}
			}
			seen[path] = true
			continue
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s: expected a regular file or directory", path)
		}
		if policy != nil {
			ignored, err := policy.ignored(path, true)
			if err != nil {
				return nil, err
			}
			if ignored {
				continue
			}
		}
		if singleFile {
			return nil, errors.New("formatting a directory requires --write or --check")
		}
		if policy == nil {
			policy = newDiscoveryPolicy(path, noIgnore)
		}
		err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				if skippedDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				ignored, err := policy.ignored(current, true)
				if err != nil {
					return err
				}
				if ignored {
					return filepath.SkipDir
				}
				if err := policy.validateDir(current); err != nil {
					return err
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			ignored, err := policy.ignored(current, false)
			if err != nil {
				return err
			}
			if ignored {
				return nil
			}
			switch strings.ToLower(filepath.Ext(current)) {
			case ".blobl", ".bloblang", ".yaml", ".yml":
				seen[current] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if singleFile && len(seen) > 1 {
		return nil, errors.New("formatting multiple files requires --write or --check")
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	inputs := make([]inputFile, 0, len(names))
	for _, name := range names {
		inputs = append(inputs, inputFile{path: name})
	}
	return inputs, nil
}

func skippedDirectory(name string) bool {
	switch name {
	case ".git", ".cache", ".state", ".worktrees", ".volumes", "node_modules", "vendor", "target":
		return true
	default:
		return false
	}
}

type discoveredTarget struct {
	path   string
	policy *discoveryPolicy // nil identifies an explicitly named literal.
}

func expandPaths(paths []string, noIgnore bool) ([]discoveredTarget, error) {
	var expanded []discoveredTarget
	for _, argument := range paths {
		absolute, err := absolutePath(argument)
		if err != nil {
			return nil, err
		}
		// Existing literal names win, including filenames containing glob syntax.
		if _, err := os.Lstat(absolute); err == nil {
			expanded = append(expanded, discoveredTarget{path: absolute})
			continue
		} else if !globLookupMiss(err) || !strings.ContainsAny(argument, "*?[{") {
			return nil, err
		}
		pattern := filepath.ToSlash(absolute)
		if !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("invalid glob pattern %q", argument)
		}
		base, pattern := doublestar.SplitPattern(pattern)
		base = filepath.FromSlash(base)
		info, err := os.Lstat(base)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		var matches []string
		policy := newDiscoveryPolicy(base, noIgnore)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && !hardSkippedPrefix(base) {
			filtered := globFS{root: base, FS: os.DirFS(base), policy: policy}
			err = doublestar.GlobWalk(filtered, pattern, func(name string, entry fs.DirEntry) error {
				matches = append(matches, filepath.Join(base, filepath.FromSlash(name)))
				return nil
			}, doublestar.WithNoFollow(), doublestar.WithFailOnIOErrors())
			if err != nil {
				return nil, fmt.Errorf("glob %q: %w", argument, err)
			}
		}
		if len(matches) == 0 && !policy.pruned {
			return nil, fmt.Errorf("glob pattern %q matched no files or directories", argument)
		}
		for _, match := range matches {
			expanded = append(expanded, discoveredTarget{path: match, policy: policy})
		}
	}
	return expanded, nil
}

func hardSkippedPrefix(base string) bool {
	boundary := editorconfig.ResolveSource(base, nil).Root
	for current := base; ; current = filepath.Dir(current) {
		if skippedDirectory(filepath.Base(current)) {
			return true
		}
		if current == boundary || filepath.Dir(current) == current {
			return false
		}
	}
}

// Filter before doublestar descends: callbacks run only for matched entries,
// so callback-only filtering would still traverse excluded trees and symlinks.
type globFS struct {
	fs.FS
	root   string
	policy *discoveryPolicy
}

func (f globFS) check(name string) error {
	if !fs.ValidPath(name) {
		return fs.ErrInvalid
	}
	if name == "." {
		return nil
	}
	current := f.root
	for _, component := range strings.Split(name, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() && skippedDirectory(component) {
			return fs.ErrNotExist
		}
	}
	return nil
}

func (f globFS) Open(name string) (fs.File, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	return f.FS.Open(name)
}

func (f globFS) Stat(name string) (fs.FileInfo, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	return fs.Stat(f.FS, name)
}

func (f globFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := f.check(name); err != nil {
		return nil, err
	}
	if f.policy != nil {
		current := filepath.Join(f.root, filepath.FromSlash(name))
		ignored, err := f.policy.ignored(current, true)
		if err != nil {
			return nil, err
		}
		if ignored {
			f.policy.pruned = true
			return nil, nil
		}
		if err := f.policy.validateDir(current); err != nil {
			return nil, err
		}
	}
	entries, err := fs.ReadDir(f.FS, name)
	if err != nil {
		return nil, err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 && (!entry.IsDir() || !skippedDirectory(entry.Name())) {
			if f.policy != nil {
				ignored, err := f.policy.ignored(filepath.Join(f.root, filepath.FromSlash(name), entry.Name()), entry.IsDir())
				if err != nil {
					return nil, err
				}
				if ignored {
					f.policy.pruned = true
					continue
				}
			}
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

// Reject explicit symlink entries. Ancestor aliases are allowed, including
// platform-owned paths such as /var on macOS.
func rejectSymlinks(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s: symlinks are not supported", path)
	}
	return nil
}

func replaceFile(path, original, replacement string) error {
	// Resolve the parent before writing so an ancestor alias changing mid-write
	// cannot move the temporary file and destination into different directories.
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	path = filepath.Join(parent, filepath.Base(path))
	if err := rejectSymlinks(path); err != nil {
		return err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("%s: expected a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(data) != original {
		return fmt.Errorf("%s: file changed while processing", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blobl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.WriteString(tmp, replacement); err != nil {
		return err
	}
	if err := tmp.Chmod(before.Mode()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := rejectSymlinks(path); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("%s: file changed while processing", path)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
