package jsonl

import (
	"bufio"
	"errors"
	"io"
)

const MaxLineBytes = 4 * 1024 * 1024

// ReadLine reads one newline-delimited record with a hard per-line cap. If the
// record is larger than maxBytes, it discards that record and returns nil, nil
// so callers can continue with the next JSONL line.
func ReadLine(r *bufio.Reader, maxBytes int) ([]byte, error) {
	if maxBytes < 1 {
		maxBytes = 1
	}
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(frag) > 0 {
			if len(line)+len(frag) > maxBytes {
				if discardErr := discardRestOfLine(r, err); discardErr != nil {
					return nil, discardErr
				}
				return nil, nil
			}
			line = append(line, frag...)
		}
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) > 0 {
				return line, nil
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

func discardRestOfLine(r *bufio.Reader, err error) error {
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = r.ReadSlice('\n')
	}
	if errors.Is(err, io.EOF) {
		return io.EOF
	}
	return err
}
