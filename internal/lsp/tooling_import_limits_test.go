package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolingPortableImportNames(t *testing.T) {
	tooling, err := NewToolingWithImports(map[string]string{
		"sub/a.blobl": "import \"../b.blobl\"\nmap a { root = this.apply(\"b\") }",
		"b.blobl":     "map b { root = this.uppercase() }",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tooling.Close()
	result, err := tooling.Lint(filepath.Join(t.TempDir(), "main.blobl"), "import \"sub/a.blobl\"\nroot = this.apply(\"a\")", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", result.Diagnostics)
	}
}

func TestToolingImportLimits(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name, imported, want string
		imports              map[string]string
	}{
		{name: "explicit cycle", imports: map[string]string{"cycle.blobl": "import \"cycle.blobl\""}, imported: "cycle.blobl", want: "failed to parse import"},
		{name: "oversized explicit", imports: map[string]string{"huge.blobl": strings.Repeat(" ", (4<<20)+1)}, imported: "huge.blobl", want: "exceed"},
		{name: "directory", imported: dir, want: "regular file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tooling, err := NewToolingWithImports(test.imports)
			if err != nil {
				t.Fatal(err)
			}
			defer tooling.Close()
			result, err := tooling.Lint(filepath.Join(dir, "mapping.blobl"), "import \""+filepath.ToSlash(test.imported)+"\"\nroot=this", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0].Message, test.want) {
				t.Fatalf("missing bounded import diagnostic: %+v", result)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(dir, "huge.blobl"), []byte(strings.Repeat(" ", (4<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	tooling, err := NewTooling()
	if err != nil {
		t.Fatal(err)
	}
	defer tooling.Close()
	result, err := tooling.Lint(filepath.Join(dir, "mapping.yaml"), "pipeline:\n  processors:\n    - mapping: |\n        import \"huge.blobl\"\n        root = this\n", false)
	if err != nil || len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0].Message, "exceed") {
		t.Fatalf("YAML imports were not bounded: %+v, %v", result, err)
	}
}
