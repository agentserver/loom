// Package transport provides a small library for moving sub-task artifacts
// out of the {{nX.output}} prompt-template channel and onto a side channel.
//
// A producer Puts bytes via a Transport and gets back a Handle (a small JSON
// document carrying a URL/path). It returns Handle.Marshal() as its task
// output. The framework substitutes that string into the next node's prompt
// as usual; the consumer ParseHandles the substituted text and Gets the bytes
// back.
//
// This package is NOT imported by multi-agent/internal/* — the framework is
// transport-agnostic on purpose.
package transport

import (
	"context"
	"encoding/json"
	"io"
)

// Handle is the small JSON-serializable descriptor that travels through the
// {{nX.output}} template path. Bytes themselves move via the side channel
// referenced by URL.
type Handle struct {
	Type  string            `json:"type"`            // caller-defined: image_url, blob_url, ...
	URL   string            `json:"url"`             // dereferencing locator
	Bytes int64             `json:"bytes,omitempty"` // size hint
	MIME  string            `json:"mime,omitempty"`  // e.g. image/png
	Meta  map[string]string `json:"meta,omitempty"`  // free-form
}

// Marshal returns the canonical one-line JSON form. Always succeeds.
func (h Handle) Marshal() string {
	b, _ := json.Marshal(h)
	return string(b)
}

// ParseHandle attempts to interpret s as a Handle JSON document. Returns
// (zero, false) if s is not JSON or lacks the required Type/URL fields, so
// callers can transparently fall back to treating s as plain text.
func ParseHandle(s string) (Handle, bool) {
	var h Handle
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return Handle{}, false
	}
	if h.Type == "" || h.URL == "" {
		return Handle{}, false
	}
	return h, true
}

// Transport stores and retrieves opaque byte payloads. Producers Put bytes
// and receive a Handle (with empty Type — caller fills it in); consumers Get
// bytes from a Handle.
type Transport interface {
	Put(ctx context.Context, mime string, data io.Reader) (Handle, error)
	Get(ctx context.Context, h Handle) (io.ReadCloser, error)
	io.Closer
}
