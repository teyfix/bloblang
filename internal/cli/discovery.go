package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/denormal/go-gitignore"
	"github.com/teyfix/bloblang-lsp/internal/editorconfig"
)

type directorySettings struct {
	respectGitignore bool
	excludes         gitignore.GitIgnore
}

type discoveryPolicy struct {
	base        string
	noIgnore    bool
	settings    map[string]directorySettings
	ignoreFiles map[string]gitignore.GitIgnore
	pruned      bool
}

func newDiscoveryPolicy(base string, noIgnore bool) *discoveryPolicy {
	return &discoveryPolicy{base: base, noIgnore: noIgnore, settings: make(map[string]directorySettings), ignoreFiles: make(map[string]gitignore.GitIgnore)}
}

func parseIgnore(text, base, source string) (gitignore.GitIgnore, error) {
	var parseError error
	matcher := gitignore.New(strings.NewReader(text), base, func(err gitignore.Error) bool {
		parseError = fmt.Errorf("%s:%d: %w", source, err.Position().Line, err)
		return false
	})
	return matcher, parseError
}

func (p *discoveryPolicy) settingsFor(dir string) (directorySettings, error) {
	if settings, ok := p.settings[dir]; ok {
		return settings, nil
	}
	ctx := editorconfig.ResolveSource(dir, nil)
	path, err := ctx.ConfigPath()
	if err != nil {
		return directorySettings{}, err
	}
	cfg, err := ctx.Config()
	if err != nil {
		return directorySettings{}, err
	}
	settings := directorySettings{respectGitignore: cfg.Files.RespectGitignore}
	if len(cfg.Files.Exclude) > 0 {
		settings.excludes, err = parseIgnore(strings.Join(cfg.Files.Exclude, "\n")+"\n", filepath.Dir(path), path+" files.exclude")
		if err != nil {
			return directorySettings{}, err
		}
	}
	p.settings[dir] = settings
	return settings, nil
}

func (p *discoveryPolicy) ignoreFile(dir, name string) (gitignore.GitIgnore, error) {
	path := filepath.Join(dir, name)
	if matcher, ok := p.ignoreFiles[path]; ok {
		return matcher, nil
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		p.ignoreFiles[path] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		p.ignoreFiles[path] = nil
		return nil, nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: expected a regular ignore file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	matcher, err := parseIgnore(string(data), dir, path)
	if err != nil {
		return nil, err
	}
	p.ignoreFiles[path] = matcher
	return matcher, nil
}

// Validate even directories that produce no tool inputs. Config errors must not
// disappear just because their file exclusions hide all source files.
func (p *discoveryPolicy) validateDir(dir string) error {
	settings, err := p.settingsFor(dir)
	if err != nil || p.noIgnore {
		return err
	}
	if settings.respectGitignore {
		if _, err := p.ignoreFile(dir, ".gitignore"); err != nil {
			return err
		}
	}
	_, err = p.ignoreFile(dir, ".bloblangignore")
	return err
}

func within(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func ignoreMatch(matcher gitignore.GitIgnore, path string, directory bool) gitignore.Match {
	if matcher == nil || !within(matcher.Base(), path) {
		return nil
	}
	rel, err := filepath.Rel(matcher.Base(), path)
	if err != nil || rel == "." {
		return nil
	}
	return matcher.Relative(filepath.ToSlash(rel), directory)
}

func ignoredBy(matchers []gitignore.GitIgnore, path string, directory bool) bool {
	ignored := false
	for _, matcher := range matchers {
		if match := ignoreMatch(matcher, path, directory); match != nil {
			ignored = match.Ignore()
		}
	}
	return ignored
}

// Ancestors are checked before children, so negation cannot resurrect a child
// of an ignored directory, matching Git's directory pruning semantics.
func (p *discoveryPolicy) ignored(path string, directory bool) (bool, error) {
	dir := filepath.Dir(path)
	// A project root starts its own search boundary; its parent belongs to a
	// different context and must not supply configuration or ignore rules.
	if directory && editorconfig.ResolveSource(path, nil).Root == path {
		return false, nil
	}
	if p.noIgnore {
		return false, nil
	}
	// Establish the boundary using metadata only. Loading the nearest config
	// first could read settings inside an already excluded ancestor.
	boundary := editorconfig.ResolveSource(dir, nil).Root
	if boundary == "" {
		boundary = p.base
		for current := dir; ; current = filepath.Dir(current) {
			if _, err := os.Stat(filepath.Join(current, ".bloblangrc.json")); err == nil {
				boundary = current
			} else if !os.IsNotExist(err) {
				return false, err
			}
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	if !within(boundary, dir) {
		boundary = dir
	}
	var dirs []string
	for current := dir; ; current = filepath.Dir(current) {
		dirs = append(dirs, current)
		if current == boundary || filepath.Dir(current) == current {
			break
		}
	}
	var settings directorySettings
	var rules []ignoreRule
	for i := len(dirs) - 1; i >= 0; i-- {
		current := dirs[i]
		if i != len(dirs)-1 && ignoredEntry(settings, rules, current, true) {
			return true, nil
		}
		var err error
		settings, err = p.settingsFor(current)
		if err != nil {
			return false, err
		}
		for _, name := range []string{".gitignore", ".bloblangignore"} {
			if name == ".gitignore" && !settings.respectGitignore {
				continue
			}
			matcher, err := p.ignoreFile(current, name)
			if err != nil {
				return false, err
			}
			if matcher != nil {
				rules = append(rules, ignoreRule{matcher: matcher, git: name == ".gitignore"})
			}
		}
	}
	return ignoredEntry(settings, rules, path, directory), nil
}

type ignoreRule struct {
	matcher gitignore.GitIgnore
	git     bool
}

func ignoredEntry(settings directorySettings, rules []ignoreRule, path string, directory bool) bool {
	if match := ignoreMatch(settings.excludes, path, directory); match != nil && match.Ignore() {
		return true
	}
	var matchers []gitignore.GitIgnore
	for _, rule := range rules {
		if !rule.git || settings.respectGitignore {
			matchers = append(matchers, rule.matcher)
		}
	}
	return ignoredBy(matchers, path, directory)
}
