# Repository migration

The repository is now [teyfix/bloblang](https://github.com/teyfix/bloblang). It contains the `blobl` CLI, MCP server and language server. Update remotes explicitly instead of relying on GitHub's redirect:

```sh
git remote set-url origin https://github.com/teyfix/bloblang.git
```

Release downloads and the public CLI manifest now live under `https://github.com/teyfix/bloblang/releases`. The Go module path is `github.com/teyfix/bloblang`; update source imports when building dependent Go code. Source builds still require CGO for Tree-sitter. Downloaded binaries do not require Go, a C compiler or Node.js.

The executable stays `blobl`; launch LSP with `blobl lsp`, MCP with `blobl mcp`. Legacy `bloblang-lsp` and `language-server` binaries/archives remain available. LSP command IDs, `.bloblangrc` configuration locations and `BLOBLANG_LSP_*` environment overrides retain their names. Local checkout directories can retain their current name.

VS Code extension 0.3.1 uses the new repository's download URLs directly. Its schema-generation script accepts `BLOBLANG_REPO` and supports both local checkout directory names.
