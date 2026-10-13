package main

import (
	"context"
	"os"

	"github.com/teyfix/bloblang-lsp/internal/cli"
)

var Version = "dev"
var DateBuilt string

func main() {
	os.Exit(cli.RunLegacy(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, cli.BuildInfo{Version: Version, DateBuilt: DateBuilt}))
}
