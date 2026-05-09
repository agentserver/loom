package imageops

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestSynthPNG_DecodableAndDeterministic(t *testing.T) {
	a, err := SynthPNG(64, 64, 42)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SynthPNG(64, 64, 42)
	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different bytes; not deterministic")
	}
	img, _, err := image.Decode(bytes.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Fatalf("unexpected size: %v", img.Bounds())
	}
}

func TestSynthPNG_DifferentSeed_DifferentBytes(t *testing.T) {
	a, _ := SynthPNG(32, 32, 1)
	b, _ := SynthPNG(32, 32, 2)
	if bytes.Equal(a, b) {
		t.Fatal("different seeds produced identical bytes")
	}
}

func TestEncodeJPEG_ShrinksAndRoundTrips(t *testing.T) {
	pngBytes, _ := SynthPNG(256, 256, 42)
	img, _ := png.Decode(bytes.NewReader(pngBytes))
	jpegBytes, err := EncodeJPEG(img, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpegBytes) >= len(pngBytes) {
		t.Fatalf("compression did not shrink: png=%d jpeg=%d", len(pngBytes), len(jpegBytes))
	}
	if _, _, err := image.Decode(bytes.NewReader(jpegBytes)); err != nil {
		t.Fatalf("jpeg not decodable: %v", err)
	}
}
