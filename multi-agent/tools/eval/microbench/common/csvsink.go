package common

// CSV output with formula-injection defence (spec §6 (f)).
//
// Excel-family spreadsheets execute cell contents starting with one of
// {=, +, -, @, \t, \r} as formulas. Since these CSVs feed the paper
// author's Excel + pandas + R workflows via WT-2-metric-extract, cells
// that could form a formula MUST be prefixed with a single quote (').
//
// This implementation is inlined (not shared with metric-extract's D2
// helper) because those two worktrees may land in parallel; the
// integration-merge audit in spec §8 diffs the two goldens byte-for-byte.

import (
	"errors"
	"io"
	"strings"
)

// injectionTriggers is the exact set spec §6 (f) requires. Runtime cost
// is one byte-compare per cell.
var injectionTriggers = [...]byte{'=', '+', '-', '@', '\t', '\r'}

// escapeCell applies the injection-safe prefix and RFC 4180 quoting rules:
//   - If the first byte is a formula trigger, prefix with '.
//   - If the cell contains " or , or \n or \r, wrap in double quotes
//     and escape internal " as "".
//
// The order matters: the ' prefix goes on BEFORE the RFC 4180 wrap so
// downstream spreadsheets show `'=A1` (verbatim), not a literal formula.
func escapeCell(s string) string {
	if len(s) > 0 {
		for _, b := range injectionTriggers {
			if s[0] == b {
				s = "'" + s
				break
			}
		}
	}
	if strings.ContainsAny(s, "\",\n\r") {
		s = `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// WriteRow writes cells to w as one CSV record terminated by "\r\n"
// (RFC 4180). It applies escapeCell to every cell.
//
// Errors from w propagate to the caller. WriteRow does not flush; callers
// using bufio.Writer must flush themselves.
func WriteRow(w io.Writer, cells []string) error {
	if len(cells) == 0 {
		return errors.New("csvsink: WriteRow called with 0 cells")
	}
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(escapeCell(c))
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}
