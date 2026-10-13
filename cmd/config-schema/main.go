// config-schema writes the schema from the workspace configuration registry.
package main

import (
	"encoding/json"
	"github.com/teyfix/bloblang/internal/editorconfig"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--rules" {
		_, _ = os.Stdout.WriteString(editorconfig.RuleReference())
		return
	}
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(editorconfig.Schema()); err != nil {
		panic(err)
	}
}
