package lsp

import (
	"fmt"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/teyfix/bloblang/internal/benthos"
	"path/filepath"
	"strings"
)

func (h *Handler) updateRegionSample(host protocol.DocumentURI, r embeddedRegion) {
	stem := ""
	if p, e := uriToPath(host); e == nil && !r.interpolation {
		stem = fmt.Sprintf("%s.sample-%03d", strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)), r.sampleNumber)
	}
	sample, err := benthos.ExtractSampleWithSource(h.parser, string(host), r.text, h.baseDirForURI(host), r.samplePath, stem)
	h.storeSample(r.uri, r.text, sample, err)
}
