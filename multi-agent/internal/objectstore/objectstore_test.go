package objectstore

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryPutOpenAndSHA256(t *testing.T) {
	store := NewMemory()
	key := ArtifactKey("ws1", "art1")

	info, err := store.Put(context.Background(), key, "text/plain", strings.NewReader("hello"))
	require.NoError(t, err)
	require.Equal(t, int64(5), info.Bytes)
	require.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", info.SHA256)

	body, err := store.Open(context.Background(), key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, body.Close()) })

	got, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))
}

func TestMemoryOpenMissingKeyReturnsClearError(t *testing.T) {
	store := NewMemory()

	_, err := store.Open(context.Background(), ArtifactKey("ws1", "missing"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "object not found")
}

func TestMemoryPresignedURLEscapesKeys(t *testing.T) {
	store := NewMemory()
	key := WriteKey("ws/one", "wr 1?#")

	putURL, err := store.PutPresignedURL(context.Background(), key, "text/plain", time.Minute)
	require.NoError(t, err)
	require.Equal(t, "memory://put/"+url.PathEscape(key), putURL)

	getURL, err := store.GetPresignedURL(context.Background(), key, time.Minute)
	require.NoError(t, err)
	require.Equal(t, "memory://get/"+url.PathEscape(key), getURL)
}
