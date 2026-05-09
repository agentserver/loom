package transport

import (
	"reflect"
	"testing"
)

func TestHandle_MarshalRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		h    Handle
	}{
		{"minimal", Handle{Type: "image_url", URL: "http://x/y"}},
		{"with_bytes_mime", Handle{Type: "image_url", URL: "http://x/y", Bytes: 123, MIME: "image/png"}},
		{"with_meta", Handle{Type: "image_url", URL: "http://x/y", Bytes: 99, MIME: "image/jpeg", Meta: map[string]string{"original_bytes": "200", "ratio": "0.49"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.h.Marshal()
			got, ok := ParseHandle(s)
			if !ok {
				t.Fatalf("ParseHandle failed for %q", s)
			}
			if !reflect.DeepEqual(got, c.h) {
				t.Fatalf("round-trip mismatch:\n got:  %+v\n want: %+v", got, c.h)
			}
		})
	}
}

func TestParseHandle_FallbackCases(t *testing.T) {
	cases := []string{
		"",
		"not json at all",
		`{"foo":"bar"}`,                        // missing type and url
		`{"type":"image_url"}`,                 // missing url
		`{"url":"http://x/y"}`,                 // missing type
		`{"type":"","url":"http://x/y"}`,       // empty type
		`{"type":"image_url","url":""}`,        // empty url
	}
	for _, c := range cases {
		if _, ok := ParseHandle(c); ok {
			t.Errorf("ParseHandle(%q) returned ok=true; want false", c)
		}
	}
}
