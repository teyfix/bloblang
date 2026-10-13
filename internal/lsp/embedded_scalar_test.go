package lsp

import (
	"context"
	"strings"
	"testing"
	"time"

	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/servertest"
	"github.com/stretchr/testify/require"
)

func TestYAMLScalarPropertiesAndPositions(t *testing.T) {
	prefixes := []struct{ name, text string }{
		{"anchor", "&root "},
		{"tag", "!!str "},
		{"anchor-tag", "&root !!str "},
		{"tag-anchor", "!!str &root "},
		{"verbatim-tag", "!<tag:yaml.org,2002:str> &root "},
		{"non-specific-tag", "! &root "},
		{"multiline-anchor-tag", "&root # anchor\n  !!str # tag\n  "},
		{"multiline-tag-anchor", "!!str\n  &root\n  "},
	}
	styles := []struct{ name, text string }{
		{"plain", "root = this.name\n"},
		{"single-quoted", "'root = this.name'\n"},
		{"double-quoted", "\"root = this.name\"\n"},
		{"literal", "|-\n  root = this.name\n"},
		{"folded", ">-\n  root =\n  this.name\n"},
	}
	for _, prefix := range prefixes {
		for _, style := range styles {
			t.Run(prefix.name+"/"+style.name, func(t *testing.T) {
				const head = "label: ☕\nmapping: "
				text := head + prefix.text + style.text + "next: untouched\n"
				regions := extractRegions("file:///config.yaml", text)
				require.Len(t, regions, 1)
				r := regions[0]
				require.Equal(t, "root = this.name", r.text)
				require.Equal(t, len(head), r.start, "the region still includes YAML properties")
				require.Equal(t, len(head)+len(prefix.text), r.scalarStart)
				require.Len(t, r.offsets, len(r.text)+1)
				for _, needle := range []string{"root", "this.name"} {
					local := strings.Index(r.text, needle)
					host := r.scalarStart + strings.Index(text[r.scalarStart:], needle)
					for j := range len(needle) {
						require.Equal(t, host+j, r.offsets[local+j])
					}
					require.Equal(t, bytePosition(text, host), r.hostPosition(text, bytePosition(r.text, local)))
				}
				if style.name == "literal" || style.name == "folded" {
					require.Equal(t, strings.Index(text, "next:"), r.end)
				} else {
					require.Equal(t, len(head)+len(prefix.text)+len(style.text)-1, r.end)
				}
				require.Equal(t, strings.Index(text, "this.name")+len("this.name"), r.offsets[len(r.text)])
			})
		}
	}
}

func TestYAMLBlockScalarPropertiesAndIndentation(t *testing.T) {
	cases := []struct{ name, text, decoded string }{
		{"properties-on-separate-line", "mapping:\n  &root\n  !!str\n  |-\n  root = this.name\n\nnext: untouched\n", "root = this.name"},
		{"explicit-indent", "mapping: &root !!str |2-\n    root = this.name\n\nnext: untouched\n", "  root = this.name"},
		{"crlf", "mapping: &root\r\n  !!str\r\n  |-\r\n  root = this.name\r\n\r\nnext: untouched\r\n", "root = this.name"},
		{"keep-chomping", "mapping: &root !!str |+\n  root = this.name\n\nnext: untouched\n", "root = this.name\n\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			regions := extractRegions("file:///config.yaml", tt.text)
			require.Len(t, regions, 1)
			r := regions[0]
			require.Equal(t, tt.decoded, r.text)
			require.Equal(t, strings.Index(tt.text, "&root"), r.start)
			require.Equal(t, strings.Index(tt.text, "|"), r.scalarStart)
			require.Equal(t, strings.Index(tt.text, "next:"), r.end, "retain full scalar end, including trailing blank lines")
			require.Len(t, r.offsets, len(r.text)+1)
			require.Equal(t, strings.Index(tt.text, "root ="), r.offsets[strings.Index(r.text, "root =")])
			require.Equal(t, strings.Index(tt.text, "this.name"), r.offsets[strings.Index(r.text, "this.name")])
		})
	}
}

func TestYAMLScalarPropertiesPreserveQuotedEscapes(t *testing.T) {
	cases := []struct{ source, decoded, marker string }{
		{`!!str &root "root = \"😀\""`, `root = "😀"`, "😀"},
		{`&root !!str 'root = "it''s"'`, `root = "it's"`, "s"},
		{`&root !!str "root = \"\u00e9\""`, `root = "é"`, `\u00e9`},
	}
	for _, tt := range cases {
		t.Run(tt.source, func(t *testing.T) {
			text := "mapping: " + tt.source + "\n"
			regions := extractRegions("file:///config.yaml", text)
			require.Len(t, regions, 1)
			r := regions[0]
			require.Equal(t, tt.decoded, r.text)
			local := strings.Index(r.text, "é")
			if local < 0 {
				local = strings.LastIndex(r.text, tt.marker)
			}
			host := strings.LastIndex(text, tt.marker)
			require.Equal(t, host, r.offsets[local])
			require.Equal(t, bytePosition(text, host), r.hostPosition(text, bytePosition(r.text, local)))
		})
	}
}

func TestYAMLScalarPropertiesPublishSyntaxDiagnostics(t *testing.T) {
	for _, source := range []string{
		"&root root = (\n",
		"&root 'root = ('\n",
		"!!str \"root = (\"\n",
		"!!str &root |-\n  root = (\n",
		"&root\n  !!str # tag\n  >-\n  root = (\n",
	} {
		t.Run(source, func(t *testing.T) {
			h, uri := featureHandler(t)
			uri = protocol.DocumentURI(strings.TrimSuffix(string(uri), ".blobl") + ".yaml")
			client := servertest.New(t, h)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			text := "mapping: " + source + "next: untouched\n"
			require.NoError(t, client.DidOpen(uri, "yaml", text))
			diagnostics, err := client.WaitForDiagnostics(ctx, uri)
			require.NoError(t, err)
			var syntax []protocol.Diagnostic
			for _, d := range diagnostics {
				if d.Source == "bloblang" {
					syntax = append(syntax, d)
				}
			}
			require.Len(t, syntax, 1)
			require.Equal(t, protocol.SeverityError, *syntax[0].Severity)
			require.Equal(t, bytePosition(text, strings.Index(text, "(")+1), syntax[0].Range.Start)
		})
	}
}

func TestYAMLScalarPropertiesInterpolate(t *testing.T) {
	text := "message: &message !!str '${! this.name.uppercase() }'\n"
	regions := extractRegions("file:///config.yaml", text)
	require.Len(t, regions, 1)
	require.True(t, regions[0].interpolation)
	require.Equal(t, "root =  this.name.uppercase() ", regions[0].text)
	local := strings.Index(regions[0].text, "this.name")
	require.Equal(t, strings.Index(text, "this.name"), regions[0].offsets[local])
}
