package editorconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

// SourceContext gives a source directory its configuration search boundary and
// external YAML mapping fallback. Workspace roots take precedence over Git roots.
type SourceContext struct {
	Dir  string
	Root string
}

// ResolveSource selects the deepest containing workspace root. Sources outside
// all workspace roots, including standalone CLI files, use their nearest .git
// marker (a directory or worktree file). With neither, config lookup reaches the
// filesystem root and external mappings have no project-root fallback.
func ResolveSource(dir string, workspaceRoots []string) SourceContext {
	dir = filepath.Clean(dir)
	context := SourceContext{Dir: dir}
	for _, root := range workspaceRoots {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if contains(root, dir) && len(root) > len(context.Root) {
			context.Root = root
		}
	}
	if context.Root != "" {
		return context
	}
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			context.Root = current
			break
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return context
}

func contains(root, dir string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}

// Config reads the nearest .bloblangrc.json, including the selected root. A
// nearer file replaces ancestor settings rather than merging with them. Errors
// return defaults so editor callers can retain static features; CLI callers
// should report the error instead of silently continuing.
func (s SourceContext) Config() (Config, error) {
	path, err := s.ConfigPath()
	if err != nil {
		return Default(), err
	}
	if path == "" {
		return Default(), nil
	}
	cfg, err := Load(path)
	if err != nil {
		return Default(), fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// ConfigPath returns the nearest settings filename, or empty when none exists.
// Callers can anchor file exclusion patterns to the containing directory.
func (s SourceContext) ConfigPath() (string, error) {
	for dir := s.Dir; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".bloblangrc.json")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		rel, err := filepath.Rel(s.Root, dir)
		if (s.Root != "" && err == nil && rel == ".") || filepath.Dir(dir) == dir {
			return "", nil
		}
	}
}

// ImportPath resolves Bloblang imports from the source file's directory.
func (s SourceContext) ImportPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.Dir, path)
}

// MappingPath resolves external YAML mappings locally first, then at the root.
// Missing paths keep their local spelling for diagnostics.
func (s SourceContext) MappingPath(path string) string {
	local := s.ImportPath(path)
	if filepath.IsAbs(path) {
		return local
	}
	if _, err := os.Stat(local); err == nil {
		return local
	}
	if s.Root != "" {
		fallback := filepath.Join(s.Root, path)
		if _, err := os.Stat(fallback); err == nil {
			return fallback
		}
	}
	return local
}
