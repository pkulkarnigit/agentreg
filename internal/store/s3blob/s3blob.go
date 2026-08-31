// Package s3blob implements store.BlobStore on S3 or any S3-compatible
// object store (MinIO, Cloudflare R2, DigitalOcean Spaces, ...) — the swap
// that actually unblocks horizontal scaling: fsblob ties krate-server to
// one machine's disk, so a second replica can't see what the first one
// wrote. Once blobs live here, the server is fully stateless (metadata is
// already on Postgres) and safe to run as N replicas behind a load
// balancer.
package s3blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/pkulkarni/apreg/internal/store"
)

type Store struct {
	client *s3.Client
	bucket string
}

// Config holds the connection details. Endpoint and ForcePathStyle are
// only needed for S3-compatible providers other than real AWS S3 (MinIO,
// R2, Spaces, ...) — leave both zero-valued for real S3, where path-style
// addressing has been deprecated for years and the default endpoint is
// resolved from Region.
type Config struct {
	Bucket         string
	Region         string
	Endpoint       string // e.g. http://localhost:9000 for a local MinIO
	ForcePathStyle bool   // required by MinIO and most non-AWS S3-compatible stores
}

// Open builds a client using the AWS SDK's standard credential chain (env
// vars, ~/.aws/credentials, an EC2/ECS instance role, ...) — deliberately
// not reinventing credential handling here.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3blob: Bucket is required")
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("s3blob: load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})

	return &Store{client: client, bucket: cfg.Bucket}, nil
}

func objectKey(scope, name, version string) string {
	return scope + "/" + name + "/" + version + ".tar.gz"
}

func (s *Store) Put(ctx context.Context, scope, name, version string, r io.Reader) (string, int64, error) {
	key := objectKey(scope, name, version)

	// Stage to a local temp file first, hashing as we go — the same shape
	// as fsblob.Put. This lets us know the exact size and checksum before
	// talking to S3 at all, and gives PutObject a ReadSeeker (an *os.File)
	// rather than an unsized stream.
	tmp, err := os.CreateTemp("", "krate-s3blob-*")
	if err != nil {
		return "", 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}

	// Hard backstop against overwriting an existing version, same
	// rationale as fsblob's os.Link switch: the registry layer already
	// checks for an existing version before ever calling Put, but this
	// is the defense-in-depth layer in case that check is ever bypassed
	// or racing. S3 has no universally-supported atomic "create if
	// absent" across every S3-compatible provider, so this is a
	// check-then-write with a narrow race window — the registry-level
	// check is what actually closes that window in practice.
	var notFound *types.NotFound
	_, headErr := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	switch {
	case headErr == nil:
		return "", 0, store.ErrConflict
	case errors.As(headErr, &notFound):
		// doesn't exist yet, proceed
	default:
		return "", 0, fmt.Errorf("s3blob: check existing object: %w", headErr)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   f,
	}); err != nil {
		return "", 0, fmt.Errorf("s3blob: put object: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func (s *Store) Open(ctx context.Context, scope, name, version string) (io.ReadCloser, error) {
	key := objectKey(scope, name, version)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("s3blob: get object: %w", err)
	}
	return out.Body, nil
}

var _ store.BlobStore = (*Store)(nil)
