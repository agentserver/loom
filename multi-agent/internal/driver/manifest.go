package driver

import (
	"bytes"
	"encoding/json"
)

// Manifest is the USER_FILES_MANIFEST payload prepended to every delegated prompt.
// "Files" entries are read-handles (file or dir); "Writes" entries are PUT-handles.
type Manifest struct {
	Files  []FileEntry         `json:"files"`
	Writes []WriteRequestEntry `json:"writes"`
}

type FileEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`               // "file" | "dir"
	MIME    string `json:"mime,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	URL     string `json:"url,omitempty"`      // file kind only
	ListURL string `json:"list_url,omitempty"` // dir kind only
	BlobURL string `json:"blob_url,omitempty"` // dir kind only
}

type WriteRequestEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`      // "file"
	Overwrite bool   `json:"overwrite"`
	PutURL    string `json:"put_url"`
}

// Encode renders the block as: opening fence, single-line JSON, closing fence.
// Always emits both arrays even when empty so downstream prompt instructions
// can be unconditional.
func (m Manifest) Encode() string {
	if m.Files == nil {
		m.Files = []FileEntry{}
	}
	if m.Writes == nil {
		m.Writes = []WriteRequestEntry{}
	}
	var buf bytes.Buffer
	buf.WriteString("<USER_FILES_MANIFEST version=1>\n")
	var inner bytes.Buffer
	innerEnc := json.NewEncoder(&inner)
	// HTML escaping is intentionally left ON (default) so that any embedded
	// "<USER_FILES_MANIFEST" or "</USER_FILES_MANIFEST>" sequences in field
	// values are rendered as < … >, preventing fence injection.
	_ = innerEnc.Encode(m)
	jsonLine := bytes.TrimRight(inner.Bytes(), "\n")
	buf.Write(jsonLine)
	buf.WriteString("\n</USER_FILES_MANIFEST>")
	return buf.String()
}
