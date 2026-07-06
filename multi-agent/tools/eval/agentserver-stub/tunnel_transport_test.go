package main

import (
	"bytes"
	"testing"
)

func TestWriteReadStreamHeader_RoundTrip(t *testing.T) {
	meta := []byte(`{"method":"GET","path":"/x","headers":{"a":"b"},"body_len":0}`)
	var buf bytes.Buffer
	if err := writeStreamHeader(&buf, streamTypeHTTP, meta); err != nil {
		t.Fatalf("write: %v", err)
	}
	typ, got, err := readStreamHeader(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != streamTypeHTTP {
		t.Fatalf("streamType: want %d got %d", streamTypeHTTP, typ)
	}
	if !bytes.Equal(got, meta) {
		t.Fatalf("metadata: want %q got %q", meta, got)
	}
}

func TestReadStreamHeader_MetaTooLarge_Errors(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{streamTypeHTTP})
	buf.Write([]byte{0x00, 0x20, 0x00, 0x00}) // 2MB
	if _, _, err := readStreamHeader(&buf); err == nil {
		t.Fatal("want error for 2MB metadata; got nil")
	}
}
