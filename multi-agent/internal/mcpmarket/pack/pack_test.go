package pack

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteTarball_Deterministic(t *testing.T) {
	files := []File{
		{Path: "b.txt", Content: []byte("two")},
		{Path: "a.txt", Content: []byte("one")},
		{Path: "sub/c.txt", Content: []byte("three")},
	}
	var firstBytes []byte
	var firstHash string
	for i := 0; i < 10; i++ {
		out, hash, err := WriteTarball("pkg-foo-1.0.0", files)
		require.NoError(t, err)
		if i == 0 {
			firstBytes = out
			firstHash = hash
		} else {
			require.True(t, bytes.Equal(out, firstBytes), "run %d differs from run 0", i)
			require.Equal(t, firstHash, hash)
		}
	}
}

func TestWriteThenRead_RoundTrip(t *testing.T) {
	in := []File{
		{Path: "manifest.json", Content: []byte(`{"x":1}`)},
		{Path: "src/server.py", Content: []byte("print('hi')")},
	}
	tgz, _, err := WriteTarball("pkg-foo-1.0.0", in)
	require.NoError(t, err)
	prefix, out, err := ReadTarball(tgz)
	require.NoError(t, err)
	require.Equal(t, "pkg-foo-1.0.0", prefix)
	require.Len(t, out, 2)
	require.Equal(t, "manifest.json", out[0].Path)
	require.Equal(t, "src/server.py", out[1].Path)
	require.Equal(t, in[0].Content, out[0].Content)
}

func TestWriteTarball_RejectsZipSlipPath(t *testing.T) {
	files := []File{{Path: "../escape.txt", Content: []byte("nope")}}
	_, _, err := WriteTarball("pkg", files)
	require.Error(t, err)
	require.Contains(t, err.Error(), "..")
}

func TestWriteTarball_RejectsAbsolutePath(t *testing.T) {
	files := []File{{Path: "/etc/passwd", Content: []byte("x")}}
	_, _, err := WriteTarball("pkg", files)
	require.Error(t, err)
}

func TestWriteTarball_RejectsOversizedFile(t *testing.T) {
	big := make([]byte, MaxFileBytes+1)
	_, _, err := WriteTarball("pkg", []File{{Path: "big.bin", Content: big}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")
}

func TestWriteTarball_PrefixDirRequired(t *testing.T) {
	_, _, err := WriteTarball("", []File{{Path: "x.txt", Content: []byte("y")}})
	require.Error(t, err)
}

func TestWriteTarball_RejectsDuplicate(t *testing.T) {
	_, _, err := WriteTarball("pkg", []File{
		{Path: "x.txt", Content: []byte("a")},
		{Path: "x.txt", Content: []byte("b")},
	})
	require.Error(t, err)
}
