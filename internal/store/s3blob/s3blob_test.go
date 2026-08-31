package s3blob

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/blobtest"
)

// TestConformance runs the exact same behavioral suite internal/store/fsblob
// runs, against a real S3-compatible endpoint (a local MinIO in
// development), proving both blob backends satisfy store.BlobStore
// identically. Requires KRATE_TEST_S3_ENDPOINT plus the standard AWS
// credential env vars (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) —
// config.LoadDefaultConfig picks those up on its own, no extra plumbing
// needed. Skips cleanly without them so the rest of the suite still runs
// with no S3-compatible store reachable.
func TestConformance(t *testing.T) {
	endpoint := os.Getenv("KRATE_TEST_S3_ENDPOINT")
	if endpoint == "" || os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		t.Skip("KRATE_TEST_S3_ENDPOINT/AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY not set; skipping S3 conformance suite")
	}

	n := 0
	blobtest.RunConformanceSuite(t, func(t *testing.T) store.BlobStore {
		n++
		bucket := fmt.Sprintf("krate-test-%d", n)

		ctx := context.Background()
		s, err := Open(ctx, Config{
			Bucket: bucket, Region: "us-east-1",
			Endpoint: endpoint, ForcePathStyle: true,
		})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket}); err != nil {
			t.Fatalf("create test bucket: %v", err)
		}
		t.Cleanup(func() { emptyAndDeleteBucket(t, ctx, s.client, bucket) })

		return s
	})
}

// emptyAndDeleteBucket removes every object (S3 refuses to delete a
// non-empty bucket) then the bucket itself, so repeated test runs don't
// accumulate garbage buckets on the test MinIO instance.
func emptyAndDeleteBucket(t *testing.T, ctx context.Context, client *s3.Client, bucket string) {
	t.Helper()
	out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &bucket})
	if err != nil {
		t.Logf("cleanup: list objects in %s: %v", bucket, err)
		return
	}
	for _, obj := range out.Contents {
		if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: obj.Key}); err != nil {
			t.Logf("cleanup: delete %s/%s: %v", bucket, *obj.Key, err)
		}
	}
	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &bucket}); err != nil {
		t.Logf("cleanup: delete bucket %s: %v", bucket, err)
	}
}
