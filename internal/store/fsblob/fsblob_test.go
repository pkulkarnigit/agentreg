package fsblob

import (
	"testing"

	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/blobtest"
)

func TestConformance(t *testing.T) {
	blobtest.RunConformanceSuite(t, func(t *testing.T) store.BlobStore {
		s, err := Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s
	})
}
