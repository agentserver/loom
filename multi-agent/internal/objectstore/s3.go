package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	UseSSL    bool
	AccessKey string
	SecretKey string
}

type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("objectstore/s3: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("objectstore/s3: bucket is required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3Store) PutPresignedURL(ctx context.Context, key, mime string, expires time.Duration) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, expires)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *S3Store) GetPresignedURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, expires, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *S3Store) Put(ctx context.Context, key, mime string, body io.Reader) (ObjectInfo, error) {
	hash := sha256.New()
	upload, err := s.client.PutObject(ctx, s.bucket, key, io.TeeReader(body, hash), -1, minio.PutObjectOptions{
		ContentType: mime,
	})
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Bytes: upload.Size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
