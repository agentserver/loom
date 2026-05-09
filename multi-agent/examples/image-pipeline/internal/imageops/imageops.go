// Package imageops contains the deterministic image generation and
// JPEG-encoding primitives used by the image-pipeline example agents.
// Kept separate so they're testable without an agent process.
package imageops

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
)

// SynthPNG generates a width x height PNG of seeded pseudo-random RGBA
// pixels. Same (width, height, seed) always produces identical bytes.
func SynthPNG(width, height int, seed int64) ([]byte, error) {
	if width < 1 || width > 4096 || height < 1 || height > 4096 {
		return nil, fmt.Errorf("dimensions out of range: %dx%d", width, height)
	}
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("png encode: %w", err)
	}
	return buf.Bytes(), nil
}

// EncodeJPEG writes img as a JPEG at the given quality (1..100).
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality < 1 || quality > 100 {
		return nil, fmt.Errorf("quality out of range: %d", quality)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}
