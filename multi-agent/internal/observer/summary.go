package observer

import (
	"encoding/json"
	"regexp"
	"strings"
)

var manifestBlock = regexp.MustCompile(`(?s)<USER_FILES_MANIFEST(?:\s[^>]*)?>.*?</USER_FILES_MANIFEST>`)
var whitespace = regexp.MustCompile(`\s+`)

func SummarizePrompt(prompt string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = 80
	}
	clean := manifestBlock.ReplaceAllString(prompt, "")
	clean = strings.TrimSpace(whitespace.ReplaceAllString(clean, " "))
	if fromJSON := summarizeJSON(clean); fromJSON != "" {
		clean = fromJSON
	}
	runes := []rune(clean)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return clean
}

func summarizeJSON(s string) string {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	for _, key := range []string{"description", "prompt", "name", "tool", "server"} {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(whitespace.ReplaceAllString(v, " "))
		}
	}
	return ""
}
