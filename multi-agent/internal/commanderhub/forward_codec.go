package commanderhub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/yourorg/multi-agent/internal/commander"
)

const (
	// maxEnvelopeSize is the cap on decoded envelope size (1 MiB).
	maxEnvelopeSize = 1 << 20 // 1 MiB
	// maxLengthDigits is the maximum number of decimal ASCII digits for the length prefix.
	maxLengthDigits = 7 // supports up to 9999999 bytes
)

var (
	// ErrLengthTooLarge is returned when the length prefix exceeds maxEnvelopeSize.
	ErrLengthTooLarge = errors.New("envelope length exceeds 1 MiB limit")
	// ErrNoNewline is returned when no newline is found before maxLengthDigits digits.
	ErrNoNewline = errors.New("no newline found in length prefix")
	// ErrInvalidLength is returned when the length prefix is not valid decimal ASCII.
	ErrInvalidLength = errors.New("invalid length prefix (not decimal)")
	// ErrEnvelopeTooLarge is returned by Encode when the marshaled envelope exceeds maxEnvelopeSize.
	ErrEnvelopeTooLarge = errors.New("envelope too large to encode (exceeds 1 MiB limit)")
)

// isAllDigits reports true only when b is non-empty and every byte is an ASCII
// decimal digit ('0'–'9'). This rejects negative ("-1") and positive-signed
// ("+1") prefixes that strconv.Atoi would otherwise accept, preventing a
// negative make([]byte, n) panic in the decode path.
func isAllDigits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// EnvelopeEncoder writes length-prefixed JSON envelopes to a writer.
type EnvelopeEncoder struct {
	w io.Writer
}

// NewEnvelopeEncoder creates a new encoder writing to w.
func NewEnvelopeEncoder(w io.Writer) *EnvelopeEncoder {
	return &EnvelopeEncoder{w: w}
}

// Encode writes an Envelope as a length-prefixed JSON line.
// Format: <decimal-length>\n<json-bytes>
// Returns ErrEnvelopeTooLarge without writing if the marshaled size exceeds maxEnvelopeSize.
func (e *EnvelopeEncoder) Encode(env *commander.Envelope) error {
	// Marshal envelope to JSON
	jsonBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	// Enforce size cap before writing anything.
	if len(jsonBytes) > maxEnvelopeSize {
		return ErrEnvelopeTooLarge
	}

	// Write length as decimal ASCII, then newline, then JSON
	lengthStr := strconv.Itoa(len(jsonBytes))
	if _, err := io.WriteString(e.w, lengthStr); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := io.WriteString(e.w, "\n"); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	if _, err := e.w.Write(jsonBytes); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// EnvelopeDecoder reads length-prefixed JSON envelopes from a reader.
type EnvelopeDecoder struct {
	r *bufio.Reader
}

// NewEnvelopeDecoder creates a new decoder reading from r.
func NewEnvelopeDecoder(r io.Reader) *EnvelopeDecoder {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &EnvelopeDecoder{r: br}
}

// Decode reads one length-prefixed JSON envelope.
// Returns ErrLengthTooLarge without allocating if length exceeds maxEnvelopeSize.
func (d *EnvelopeDecoder) Decode() (*commander.Envelope, error) {
	// Read length prefix (decimal ASCII digits followed by \n).
	// We limit to maxLengthDigits to prevent unbounded scanning.
	lengthBytes := make([]byte, 0, maxLengthDigits+1) // +1 for \n
	foundNewline := false
	for len(lengthBytes) < maxLengthDigits+1 {
		b, err := d.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(lengthBytes) == 0 {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("read length byte: %w", err)
		}
		lengthBytes = append(lengthBytes, b)
		if b == '\n' {
			foundNewline = true
			break
		}
	}

	// Check that we found a newline
	if !foundNewline {
		return nil, ErrNoNewline
	}

	// Validate: prefix must be all ASCII decimal digits (rejects "-1", "+1", etc.)
	// This check prevents make([]byte, negative) panics from strconv.Atoi accepting
	// signed integers.
	prefixBytes := lengthBytes[:len(lengthBytes)-1]
	if !isAllDigits(prefixBytes) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidLength, prefixBytes)
	}

	// Parse length (strip trailing \n)
	lengthStr := string(prefixBytes)
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLength, err)
	}

	// Check that length is within bounds (without allocating the buffer yet).
	if length > maxEnvelopeSize {
		return nil, ErrLengthTooLarge
	}

	// Read envelope payload
	payload := make([]byte, length)
	_, err = io.ReadFull(d.r, payload)
	if err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	// Unmarshal JSON
	var env commander.Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	return &env, nil
}

// DecodeInto reads one length-prefixed JSON envelope and unmarshals into dest.
// Reuses buffers where possible to reduce allocations.
func (d *EnvelopeDecoder) DecodeInto(dest *commander.Envelope) error {
	// Read length prefix (decimal ASCII digits followed by \n).
	// We limit to maxLengthDigits to prevent unbounded scanning.
	var lengthBytes [maxLengthDigits + 1]byte
	lengthLen := 0
	foundNewline := false
	for lengthLen < len(lengthBytes) {
		b, err := d.r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && lengthLen == 0 {
				return io.EOF
			}
			return fmt.Errorf("read length byte: %w", err)
		}
		lengthBytes[lengthLen] = b
		lengthLen++
		if b == '\n' {
			foundNewline = true
			break
		}
	}

	// Check that we found a newline
	if !foundNewline {
		return ErrNoNewline
	}

	// Validate: prefix must be all ASCII decimal digits (rejects "-1", "+1", etc.)
	// This check prevents make([]byte, negative) panics from strconv.Atoi accepting
	// signed integers.
	prefixBytes := lengthBytes[:lengthLen-1]
	if !isAllDigits(prefixBytes) {
		return fmt.Errorf("%w: %q", ErrInvalidLength, prefixBytes)
	}

	// Parse length (strip trailing \n)
	lengthStr := string(prefixBytes)
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLength, err)
	}

	// Check that length is within bounds (without allocating the buffer yet).
	if length > maxEnvelopeSize {
		return ErrLengthTooLarge
	}

	// Read envelope payload
	payload := make([]byte, length)
	_, err = io.ReadFull(d.r, payload)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}

	// Unmarshal JSON
	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("unmarshal envelope: %w", err)
	}

	return nil
}

// EncodeToBytes encodes an Envelope to a byte slice.
// Useful for testing and small messages.
func EncodeToBytes(env *commander.Envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := NewEnvelopeEncoder(&buf)
	if err := enc.Encode(env); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeFromBytes decodes an Envelope from a byte slice.
// Useful for testing.
func DecodeFromBytes(data []byte) (*commander.Envelope, error) {
	dec := NewEnvelopeDecoder(bytes.NewReader(data))
	return dec.Decode()
}
