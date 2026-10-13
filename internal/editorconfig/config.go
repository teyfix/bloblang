// Package editorconfig owns workspace editor settings and the rule registry.
package editorconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type Rule struct {
	ID, Description, DefaultSeverity string
	Options                          map[string]any
	SafeQuickFix                     bool
}

var Rules = []Rule{
	{"correctness/environment/require-fallback", "Handle an unset environment variable with a fallback or explicit failure.", "warn", nil, false},
	{"correctness/variables/no-unused-let", "Report local bindings that are never referenced.", "warn", nil, false},
	{"style/assignments/prefer-grouped", "Group adjacent field assignments when their output state permits it.", "warn", map[string]any{"minAssignments": map[string]any{"type": "integer", "minimum": 3, "default": 3}}, false},
	{"style/objects/prefer-with", "Use with for a projection of same-named fields, accounting for missing properties.", "hint", nil, false},
	{"style/objects/prefer-without", "Use without when copying an object and deleting fields.", "warn", nil, false},
	{"style/objects/combine-without", "Combine consecutive without calls.", "warn", nil, true},
	{"style/arrays/prefer-any", "Use any for an existence check, accounting for short-circuit behavior.", "hint", nil, false},
}

type RuleSetting struct {
	Severity       string `json:"severity"`
	MinAssignments int    `json:"minAssignments,omitempty"`
}

func (r *RuleSetting) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &r.Severity)
	}
	type plain RuleSetting
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode((*plain)(r)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	if _, ok := fields["minAssignments"]; ok && r.MinAssignments < 3 {
		return fmt.Errorf("minAssignments must be at least 3")
	}
	return nil
}

type Config struct {
	Formatter struct {
		PrintWidth int `json:"printWidth"`
	} `json:"formatter"`
	Preview struct {
		Format string `json:"format"`
	} `json:"preview"`
	Lint struct {
		Enabled bool                   `json:"enabled"`
		Rules   map[string]RuleSetting `json:"rules"`
	} `json:"lint"`
	Files struct {
		Exclude          []string `json:"exclude"`
		RespectGitignore bool     `json:"respectGitignore"`
	} `json:"files"`
	Schema string `json:"$schema,omitempty"`
}

func Default() Config {
	var c Config
	c.Formatter.PrintWidth = 80
	c.Preview.Format = "yaml"
	c.Lint.Enabled = true
	c.Lint.Rules = map[string]RuleSetting{}
	c.Files.Exclude = []string{}
	c.Files.RespectGitignore = true
	return c
}
func ValidSeverity(s string) bool {
	switch s {
	case "off", "hint", "info", "warn", "error":
		return true
	}
	return false
}
func Load(path string) (Config, error) {
	c := Default()
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return Default(), e
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Default(), fmt.Errorf("unexpected content after configuration")
	}
	if c.Formatter.PrintWidth < 20 || c.Formatter.PrintWidth > 1000 {
		return Default(), fmt.Errorf("formatter.printWidth must be between 20 and 1000")
	}
	if c.Preview.Format != "yaml" && c.Preview.Format != "json" {
		return Default(), fmt.Errorf("preview.format must be yaml or json")
	}
	for _, pattern := range c.Files.Exclude {
		if strings.TrimSpace(pattern) == "" {
			return Default(), fmt.Errorf("files.exclude patterns must not be empty")
		}
	}
	for id, r := range c.Lint.Rules {
		known := false
		for _, rule := range Rules {
			if rule.ID == id {
				known = true
				if r.MinAssignments != 0 && (rule.Options == nil || r.MinAssignments < 3) {
					return Default(), fmt.Errorf("invalid minAssignments for %s", id)
				}
			}
		}
		if !known {
			return Default(), fmt.Errorf("unknown lint rule %s", id)
		}
		if !ValidSeverity(r.Severity) {
			return Default(), fmt.Errorf("invalid severity for %s", id)
		}
	}
	return c, nil
}
func (c Config) Setting(id string) RuleSetting {
	if r, ok := c.Lint.Rules[id]; ok {
		return r
	}
	for _, rule := range Rules {
		if rule.ID == id {
			return RuleSetting{Severity: rule.DefaultSeverity, MinAssignments: 3}
		}
	}
	return RuleSetting{Severity: "off"}
}
func Schema() map[string]any {
	severity := map[string]any{"type": "string", "enum": []string{"off", "hint", "info", "warn", "error"}}
	props := map[string]any{}
	for _, r := range Rules {
		opts := map[string]any{"severity": severity}
		for k, v := range r.Options {
			opts[k] = v
		}
		props[r.ID] = map[string]any{"description": r.Description, "default": r.DefaultSeverity, "oneOf": []any{severity, map[string]any{"type": "object", "additionalProperties": false, "required": []string{"severity"}, "properties": opts}}}
	}
	object := func(p map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p}
	}
	s := object(map[string]any{"$schema": map[string]any{"type": "string"}, "formatter": object(map[string]any{"printWidth": map[string]any{"type": "integer", "minimum": 20, "maximum": 1000, "default": 80}}), "preview": object(map[string]any{"format": map[string]any{"type": "string", "enum": []string{"yaml", "json"}, "default": "yaml"}}), "lint": object(map[string]any{"enabled": map[string]any{"type": "boolean", "default": true}, "rules": object(props)}), "files": object(map[string]any{
		"exclude":          map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "default": []string{}, "description": "Gitignore-style patterns relative to this configuration file's directory, with ! negation. Applied to CLI directory and glob scans; explicit filenames bypass exclusions."},
		"respectGitignore": map[string]any{"type": "boolean", "default": true, "description": "Honor .gitignore during CLI directory and glob scans. .bloblangignore and files.exclude remain active; explicit filenames bypass exclusions."},
	})})
	s["$schema"] = "http://json-schema.org/draft-07/schema#"
	s["title"] = "Bloblang workspace configuration"
	return s
}

func RuleReference() string {
	var b strings.Builder
	b.WriteString("<!-- Generated by go run ./cmd/config-schema --rules; edit internal/editorconfig instead. -->\n\n| Rule | Default | Quick Fix | Description |\n| --- | --- | --- | --- |\n")
	for _, r := range Rules {
		fix := "No"
		if r.SafeQuickFix {
			fix = "Literal arguments only"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s |\n", r.ID, r.DefaultSeverity, fix, r.Description)
	}
	return b.String()
}
