package objectstore

import (
	"context"
	"io"
	"time"
)

type ObjectInfo struct {
	Bytes  int64
	SHA256 string
}

type Store interface {
	PutPresignedURL(ctx context.Context, key, mime string, expires time.Duration) (string, error)
	GetPresignedURL(ctx context.Context, key string, expires time.Duration) (string, error)
	Put(ctx context.Context, key, mime string, body io.Reader) (ObjectInfo, error)
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

func ArtifactKey(workspaceID, artifactID string) string {
	return "workspaces/" + workspaceID + "/artifacts/" + artifactID
}

func WriteKey(workspaceID, writeID string) string {
	return "workspaces/" + workspaceID + "/writes/" + writeID
}
