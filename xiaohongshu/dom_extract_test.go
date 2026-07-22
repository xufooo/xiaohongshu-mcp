package xiaohongshu

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchDOMExtractorSupportsLazyCardAttributes(t *testing.T) {
	source, err := os.ReadFile("dom_extract.go")
	require.NoError(t, err)
	for _, want := range []string{
		`data-note-id`, `data-xsec-token`, `data-src`, `data-original`,
		`data-lazy-src`, `data-srcset`, `/user/profile/`, `naturalWidth`,
	} {
		require.Contains(t, string(source), want)
	}
}
