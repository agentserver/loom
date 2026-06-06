package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"
)

type memoryObject struct {
	mime   string
	body   []byte
	sha256 string
}

type Memory struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

func NewMemory() *Memory {
	return &Memory{objects: make(map[string]memoryObject)}
}

func (m *Memory) PutPresignedURL(ctx context.Context, key, mime string, expires time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "memory://put/" + url.PathEscape(key), nil
}

func (m *Memory) GetPresignedURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "memory://get/" + url.PathEscape(key), nil
}

func (m *Memory) Put(ctx context.Context, key, mime string, body io.Reader) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return ObjectInfo{}, err
	}
	sum := sha256.Sum256(data)
	info := ObjectInfo{Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}

	m.mu.Lock()
	defer m.mu.Unlock()
	copied := append([]byte(nil), data...)
	m.objects[key] = memoryObject{mime: mime, body: copied, sha256: info.SHA256}
	return info, nil
}

func (m *Memory) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	obj, ok := m.objects[key]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("objectstore: object not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), obj.body...))), nil
}

func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
