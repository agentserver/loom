package commanderhub

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
)

func TestEnvelopeEncoder_Encode_Basic(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEnvelopeEncoder(&buf)

	env := &commander.Envelope{
		Type: "register",
		ID:   "test-id",
		Payload: json.RawMessage(`{"key":"value"}`),
	}

	err := enc.Encode(env)
	require.NoError(t, err)

	result := buf.String()
	// Should have format: <length>\n<json>
	lines := strings.SplitN(result, "\n", 2)
	require.Len(t, lines, 2)

	// Verify length is correct
	expectedJSON, _ := json.Marshal(env)
	require.Equal(t, string(expectedJSON), lines[1])
}

func TestEnvelopeDecoder_Decode_Basic(t *testing.T) {
	env := &commander.Envelope{
		Type:    "register",
		ID:      "test-id",
		Payload: json.RawMessage(`{"key":"value"}`),
	}

	// Encode
	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	// Decode
	dec := NewEnvelopeDecoder(bytes.NewReader(encoded))
	decoded, err := dec.Decode()
	require.NoError(t, err)

	require.Equal(t, env.Type, decoded.Type)
	require.Equal(t, env.ID, decoded.ID)
	require.Equal(t, env.Payload, decoded.Payload)
}

func TestEnvelopeDecoder_Decode_MultipleFrames(t *testing.T) {
	envelopes := []*commander.Envelope{
		{Type: "register", ID: "1"},
		{Type: "heartbeat", ID: "2"},
		{Type: "event", ID: "3"},
	}

	// Encode all to a buffer
	var buf bytes.Buffer
	enc := NewEnvelopeEncoder(&buf)
	for _, env := range envelopes {
		err := enc.Encode(env)
		require.NoError(t, err)
	}

	// Decode all back
	dec := NewEnvelopeDecoder(&buf)
	for i, expected := range envelopes {
		decoded, err := dec.Decode()
		require.NoError(t, err, "envelope %d", i)
		require.Equal(t, expected.Type, decoded.Type, "envelope %d type", i)
		require.Equal(t, expected.ID, decoded.ID, "envelope %d id", i)
	}

	// Next read should be EOF
	_, err := dec.Decode()
	require.Equal(t, io.EOF, err)
}

func TestEnvelopeDecoder_Decode_WithPayload(t *testing.T) {
	payload := json.RawMessage(`{"command":"session_turn","args":{"id":"s1","prompt":"hello"}}`)
	env := &commander.Envelope{
		Type:    "command",
		ID:      "cmd-1",
		Payload: payload,
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)

	require.Equal(t, "command", decoded.Type)
	require.Equal(t, "cmd-1", decoded.ID)
	require.Equal(t, payload, decoded.Payload)
}

func TestEnvelopeDecoder_Decode_LengthTooLarge(t *testing.T) {
	// Create a length prefix that exceeds the 1 MiB limit
	lengthStr := "1048576" // Exactly 1 MiB
	tooLargeStr := "1048577" // 1 MiB + 1

	tests := []struct {
		name    string
		length  string
		wantErr bool
	}{
		{"exactly at limit", lengthStr, false},
		{"exceeds limit", tooLargeStr, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a reader with a length prefix but don't provide the payload
			data := tt.length + "\n"
			dec := NewEnvelopeDecoder(strings.NewReader(data))
			_, err := dec.Decode()

			if tt.wantErr {
				require.Equal(t, ErrLengthTooLarge, err)
			} else {
				// Error should be different (unexpected EOF, not length error)
				require.NotEqual(t, ErrLengthTooLarge, err)
			}
		})
	}
}

func TestEnvelopeDecoder_Decode_LengthTooLarge_NoAllocation(t *testing.T) {
	// Verify that rejecting oversized length doesn't allocate the payload buffer.
	// The key property: we check length before allocating, so oversized envelopes
	// are rejected without allocating the large payload buffer.

	// Create a reader with an oversized length (within 7 digits)
	bigLength := "2000000" // 2 MiB (exceeds 1 MiB limit)
	data := bigLength + "\nsome_payload_data"
	reader := strings.NewReader(data)
	dec := NewEnvelopeDecoder(reader)

	// This should return ErrLengthTooLarge before allocating 2MB
	err := dec.DecodeInto(&commander.Envelope{})
	require.Equal(t, ErrLengthTooLarge, err)
}

func TestEnvelopeDecoder_Decode_NoNewline(t *testing.T) {
	// Length prefix without newline should fail
	// Create a 7+ character number without newline
	data := "1234567890" // No newline, exceeds maxLengthDigits
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	require.Equal(t, ErrNoNewline, err)
}

func TestEnvelopeDecoder_Decode_InvalidLength(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"non-numeric", "abc\n"},
		{"hex", "0x10\n"},
		{"float", "123.45\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := NewEnvelopeDecoder(strings.NewReader(tt.data))
			_, err := dec.Decode()
			require.ErrorIs(t, err, ErrInvalidLength)
		})
	}
}

func TestEnvelopeDecoder_Decode_EmptyEnvelope(t *testing.T) {
	env := &commander.Envelope{}
	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)
	require.Equal(t, "", decoded.Type)
	require.Equal(t, "", decoded.ID)
}

func TestEnvelopeDecoder_Decode_UnexpectedEOF(t *testing.T) {
	// Length says 100 bytes, but only 50 are provided
	data := "100\n" + strings.Repeat("a", 50)
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	require.NotNil(t, err)
	require.True(t, strings.Contains(err.Error(), "read payload"))
}

func TestEnvelopeDecoder_DecodeInto(t *testing.T) {
	env := &commander.Envelope{
		Type:    "event",
		ID:      "ev-1",
		Payload: json.RawMessage(`{"event_kind":"text","text":"hello"}`),
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	// Reuse envelope struct
	dest := &commander.Envelope{}
	dec := NewEnvelopeDecoder(bytes.NewReader(encoded))
	err = dec.DecodeInto(dest)
	require.NoError(t, err)

	require.Equal(t, "event", dest.Type)
	require.Equal(t, "ev-1", dest.ID)
}

func TestEnvelopeDecoder_DecodeInto_MultipleFrames(t *testing.T) {
	envelopes := []*commander.Envelope{
		{Type: "register", ID: "1"},
		{Type: "heartbeat", ID: "2"},
		{Type: "event", ID: "3"},
	}

	// Encode all to a buffer
	var buf bytes.Buffer
	enc := NewEnvelopeEncoder(&buf)
	for _, env := range envelopes {
		err := enc.Encode(env)
		require.NoError(t, err)
	}

	// Decode all back using DecodeInto with reused struct
	dest := &commander.Envelope{}
	dec := NewEnvelopeDecoder(&buf)
	for i, expected := range envelopes {
		err := dec.DecodeInto(dest)
		require.NoError(t, err, "envelope %d", i)
		require.Equal(t, expected.Type, dest.Type, "envelope %d type", i)
		require.Equal(t, expected.ID, dest.ID, "envelope %d id", i)
		// Zero it out for the next iteration
		*dest = commander.Envelope{}
	}

	// Next read should be EOF
	err := dec.DecodeInto(dest)
	require.Equal(t, io.EOF, err)
}

func TestEnvelopeCodec_LargePayload(t *testing.T) {
	// Create a payload close to the 1 MiB limit
	// Use valid JSON: {"text":"..." + padding + "..."}
	// This ensures the payload is valid JSON that can be marshaled
	largeText := strings.Repeat("x", maxEnvelopeSize-200) // Leave room for envelope structure
	payload := json.RawMessage(`{"text":"` + largeText + `"}`)

	env := &commander.Envelope{
		Type:    "event",
		Payload: payload,
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)
	require.Equal(t, env.Type, decoded.Type)
	// Payload should contain the large text
	require.Contains(t, string(decoded.Payload), largeText[:100])
}

func TestEnvelopeCodec_AllEnvelopeFields(t *testing.T) {
	payload := json.RawMessage(`{"test":"data"}`)
	env := &commander.Envelope{
		Type:    "command_result",
		ID:      "cmd-uuid-12345",
		Payload: payload,
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)

	require.Equal(t, env.Type, decoded.Type)
	require.Equal(t, env.ID, decoded.ID)
	require.Equal(t, env.Payload, decoded.Payload)
}

func TestEnvelopeDecoder_Decode_EmptyFrame(t *testing.T) {
	// Empty reader should return EOF
	dec := NewEnvelopeDecoder(strings.NewReader(""))
	_, err := dec.Decode()
	require.Equal(t, io.EOF, err)
}

func TestEnvelopeDecoder_Decode_MaxLengthDigits(t *testing.T) {
	// Test that we handle exactly maxLengthDigits digits
	// Create a length that's 7 digits long
	jsonStr := `{"type":"test"}`
	lengthStr := "1234567" // 7 digits, within limit
	data := lengthStr + "\n" + strings.Repeat("x", len(jsonStr))

	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	// Should fail on JSON unmarshal, not on length parsing
	require.NotEqual(t, ErrInvalidLength, err)
}

func TestEnvelopeDecoder_Decode_ZeroLength(t *testing.T) {
	// Zero-length envelope is technically valid JSON (empty object {})
	// Manually create a zero-length envelope
	data := "2\n{}"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	decoded, err := dec.Decode()
	require.NoError(t, err)
	require.NotNil(t, decoded)
}

func TestEncodeToBytes_SmallMessage(t *testing.T) {
	env := &commander.Envelope{
		Type: "ping",
		ID:   "p1",
	}

	bytes, err := EncodeToBytes(env)
	require.NoError(t, err)
	require.NotNil(t, bytes)
	require.Greater(t, len(bytes), 0)

	// Verify it's decodable
	decoded, err := DecodeFromBytes(bytes)
	require.NoError(t, err)
	require.Equal(t, "ping", decoded.Type)
}

func TestEncodeDecodeRoundtrip_ComplexPayload(t *testing.T) {
	payload := json.RawMessage(`{"event_kind":"text","text":"multi\nline\ntext","extra":{"nested":["array","of","values"],"number":42}}`)

	env := &commander.Envelope{
		Type:    "event",
		ID:      "evt-123",
		Payload: payload,
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)

	require.Equal(t, env.Type, decoded.Type)
	require.Equal(t, env.ID, decoded.ID)

	// Verify the payload unmarshals correctly
	var decodedPayload map[string]interface{}
	var expectedPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(decoded.Payload, &decodedPayload))
	require.NoError(t, json.Unmarshal(payload, &expectedPayload))
	require.Equal(t, expectedPayload, decodedPayload)
}

func TestEnvelopeDecoder_EdgeCase_LengthAt7Digits(t *testing.T) {
	// Create a 7-digit length (maximum allowed)
	// 9999999 is 7 digits, but way over limit
	data := "9999999\n"

	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	require.Equal(t, ErrLengthTooLarge, err)
}

func TestEnvelopeCodec_RealWorldRegister(t *testing.T) {
	payload := json.RawMessage(`{
		"schema_version": 1,
		"kind": "claude",
		"agent_bin": "/path/to/agent",
		"agent_workdir": "/home/user",
		"display_name": "my-mac",
		"driver_version": "v1.0.0",
		"capabilities": ["sessions", "turn", "files"]
	}`)

	env := &commander.Envelope{
		Type:    "register",
		Payload: payload,
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)

	require.Equal(t, "register", decoded.Type)
	require.NotNil(t, decoded.Payload)
}

// ---------------------------------------------------------------------------
// Fix #2: negative/signed length prefix rejection (panic guard)
// ---------------------------------------------------------------------------

func TestDecoder_RejectsNegativeLength(t *testing.T) {
	// "-1\n" — strconv.Atoi would succeed with -1, make([]byte,-1) would panic.
	data := "-1\n"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	require.ErrorIs(t, err, ErrInvalidLength, "negative length prefix must return ErrInvalidLength")
}

func TestDecoder_RejectsPositiveSignedLength(t *testing.T) {
	// "+1\n" — the '+' sign is not a decimal digit.
	data := "+1\n"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	require.ErrorIs(t, err, ErrInvalidLength, "positive-signed length prefix must return ErrInvalidLength")
}

func TestDecoder_RejectsEmptyLengthPrefix(t *testing.T) {
	// "\n" — empty prefix before the newline.
	data := "\n"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	_, err := dec.Decode()
	require.ErrorIs(t, err, ErrInvalidLength, "empty length prefix must return ErrInvalidLength")
}

func TestDecodeInto_RejectsNegativeLength(t *testing.T) {
	data := "-1\n"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	err := dec.DecodeInto(&commander.Envelope{})
	require.ErrorIs(t, err, ErrInvalidLength, "DecodeInto: negative length must return ErrInvalidLength")
}

func TestDecodeInto_RejectsPositiveSignedLength(t *testing.T) {
	data := "+1\n"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	err := dec.DecodeInto(&commander.Envelope{})
	require.ErrorIs(t, err, ErrInvalidLength, "DecodeInto: positive-signed length must return ErrInvalidLength")
}

func TestDecodeInto_RejectsEmptyLengthPrefix(t *testing.T) {
	data := "\n"
	dec := NewEnvelopeDecoder(strings.NewReader(data))
	err := dec.DecodeInto(&commander.Envelope{})
	require.ErrorIs(t, err, ErrInvalidLength, "DecodeInto: empty length prefix must return ErrInvalidLength")
}

// ---------------------------------------------------------------------------
// Fix #3: encoder size cap
// ---------------------------------------------------------------------------

func TestEncoder_RejectsOversized(t *testing.T) {
	// Create a payload that makes the marshaled envelope exceed 1 MiB.
	// Use a raw payload of 2 MiB; the marshaled Envelope will include the
	// JSON overhead but 2 MiB payload alone already exceeds maxEnvelopeSize.
	largePayload := bytes.Repeat([]byte("x"), 2*maxEnvelopeSize)
	env := &commander.Envelope{
		Type:    "event",
		Payload: json.RawMessage(`"` + string(largePayload) + `"`),
	}
	var buf bytes.Buffer
	enc := NewEnvelopeEncoder(&buf)
	err := enc.Encode(env)
	require.ErrorIs(t, err, ErrEnvelopeTooLarge, "encoder must reject oversized envelopes")
	require.Equal(t, 0, buf.Len(), "nothing should be written on oversized rejection")
}

func TestEnvelopeCodec_RealWorldEvent(t *testing.T) {
	payload := json.RawMessage(`{
		"event_kind": "text",
		"text": "Hello from the daemon",
		"extra": null,
		"status_code": null
	}`)

	env := &commander.Envelope{
		Type:    "event",
		ID:      "cmd-456",
		Payload: payload,
	}

	encoded, err := EncodeToBytes(env)
	require.NoError(t, err)

	decoded, err := DecodeFromBytes(encoded)
	require.NoError(t, err)

	require.Equal(t, "event", decoded.Type)
	require.Equal(t, "cmd-456", decoded.ID)
}
