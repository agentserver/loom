package jsonl

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLineSkipsOversizedLineAndContinues(t *testing.T) {
	rd := bufio.NewReaderSize(strings.NewReader("ok\n123456\nnext\n"), 2)

	line, err := ReadLine(rd, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != "ok\n" {
		t.Fatalf("line=%q want ok", line)
	}

	line, err = ReadLine(rd, 5)
	if err != nil {
		t.Fatal(err)
	}
	if line != nil {
		t.Fatalf("oversized line=%q want nil", line)
	}

	line, err = ReadLine(rd, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != "next\n" {
		t.Fatalf("line=%q want next", line)
	}
}

func TestReadLineSkipsOversizedLineAtEOF(t *testing.T) {
	rd := bufio.NewReaderSize(strings.NewReader("123456"), 2)

	line, err := ReadLine(rd, 5)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err=%v want EOF", err)
	}
	if line != nil {
		t.Fatalf("line=%q want nil", line)
	}
}
