package probes

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const humanCountFile = ".probes/humanloop.count"
const humanCountMaxBytes = 4 * 1024

// EmitHumanCount reads ${wsRoot}/.probes/humanloop.count (integer only)
// and emits MetricHumanContextSelectionCount. NEVER reads any other
// file in .probes/ — §7(d) discipline. Absent file is legitimate
// ("no humanloop instrumentation landed yet"), value=0, source=absent.
func EmitHumanCount(ctx context.Context, e *Emitter, wsRoot string, stderr io.Writer) {
	path := filepath.Join(wsRoot, humanCountFile)
	n, src := countFile(path, stderr)
	_ = e.Emit(ctx, MetricHumanContextSelectionCount, n, map[string]string{"source": src})
}

// countFile parses the file at path. Contract per spec §3.4.
func countFile(path string, stderr io.Writer) (int, string) {
	if stderr == nil {
		stderr = io.Discard
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "absent"
		}
		fmt.Fprintf(stderr, "probes: humanloop count file open: %v\n", err)
		return 0, "malformed"
	}
	defer f.Close()

	// Hard-cap the read at humanCountMaxBytes+1 so we can detect
	// "file larger than the cap" without slurping GBs.
	buf := make([]byte, humanCountMaxBytes+1)
	read, err := io.ReadFull(f, buf)
	switch err {
	case nil:
		// File is at least humanCountMaxBytes+1 → over the cap.
		fmt.Fprintf(stderr, "probes: humanloop count file exceeds %d bytes\n", humanCountMaxBytes)
		return 0, "malformed"
	case io.ErrUnexpectedEOF, io.EOF:
		buf = buf[:read]
	default:
		fmt.Fprintf(stderr, "probes: humanloop count file read: %v\n", err)
		return 0, "malformed"
	}
	// First line only; trim trailing whitespace.
	line := string(buf)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	n, perr := strconv.Atoi(line)
	if perr != nil || n < 0 {
		fmt.Fprintf(stderr, "probes: humanloop count file malformed (%q)\n", line)
		return 0, "malformed"
	}
	return n, "humanloop_counter_file"
}
