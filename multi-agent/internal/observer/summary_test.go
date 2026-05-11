package observer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizePrompt_RemovesManifestAndCollapsesWhitespace(t *testing.T) {
	in := "<USER_FILES_MANIFEST>{\"files\":[{\"path\":\"/secret\"}]}</USER_FILES_MANIFEST>\n\n  analyze   this\nfile "
	require.Equal(t, "analyze this file", SummarizePrompt(in, 80))
}

func TestSummarizePrompt_RemovesVersionedManifest(t *testing.T) {
	in := "<USER_FILES_MANIFEST version=1>{\"files\":[{\"path\":\"/secret\"}]}</USER_FILES_MANIFEST>\n\n  analyze this"
	require.Equal(t, "analyze this", SummarizePrompt(in, 80))
}

func TestSummarizePrompt_PrefersJSONDescription(t *testing.T) {
	in := `{"name":"calc","description":"create calculator MCP","tools":[{"name":"add"}]}`
	require.Equal(t, "create calculator MCP", SummarizePrompt(in, 80))
}

func TestSummarizePrompt_TruncatesRunes(t *testing.T) {
	in := strings.Repeat("界", 90)
	got := SummarizePrompt(in, 80)
	require.Len(t, []rune(got), 80)
}
