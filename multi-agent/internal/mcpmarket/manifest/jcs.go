package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Canonicalize produces RFC 8785 JCS form: keys in codepoint-sorted order,
// no whitespace, ECMA-262 number toString, RFC 8259 string escapes.
// Input must be valid JSON; returns canonical bytes.
func Canonicalize(in []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("jcs: decode: %w", err)
	}
	var buf bytes.Buffer
	if err := writeJCS(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJCS(w *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		w.WriteString("null")
	case bool:
		if x {
			w.WriteString("true")
		} else {
			w.WriteString("false")
		}
	case string:
		writeJCSString(w, x)
	case json.Number:
		return writeJCSNumber(w, string(x))
	case []any:
		w.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				w.WriteByte(',')
			}
			if err := writeJCS(w, e); err != nil {
				return err
			}
		}
		w.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // codepoint order = lex order for UTF-8
		w.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				w.WriteByte(',')
			}
			writeJCSString(w, k)
			w.WriteByte(':')
			if err := writeJCS(w, x[k]); err != nil {
				return err
			}
		}
		w.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported type %T", v)
	}
	return nil
}

func writeJCSString(w *bytes.Buffer, s string) {
	w.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			w.WriteString(`\"`)
		case '\\':
			w.WriteString(`\\`)
		case '\b':
			w.WriteString(`\b`)
		case '\f':
			w.WriteString(`\f`)
		case '\n':
			w.WriteString(`\n`)
		case '\r':
			w.WriteString(`\r`)
		case '\t':
			w.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(w, `\u%04x`, r)
			} else {
				w.WriteRune(r)
			}
		}
	}
	w.WriteByte('"')
}

func writeJCSNumber(w *bytes.Buffer, raw string) error {
	if i, err := strconv.ParseInt(raw, 10, 64); err == nil {
		w.WriteString(strconv.FormatInt(i, 10))
		return nil
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("jcs: bad number %q: %w", raw, err)
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	s = strings.ReplaceAll(s, "e+0", "e")
	s = strings.ReplaceAll(s, "e+", "e")
	s = strings.ReplaceAll(s, "e-0", "e-")
	w.WriteString(s)
	return nil
}
