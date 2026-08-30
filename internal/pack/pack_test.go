package pack

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "plugin.json"), `{"name":"x"}`)
	mustWrite(t, filepath.Join(src, "skills", "example", "SKILL.md"), "# hi\n")

	archive := filepath.Join(t.TempDir(), "out.tar.gz")
	checksum, err := Dir(src, archive)
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if checksum == "" {
		t.Fatal("expected non-empty checksum")
	}

	verify, err := Checksum(archive)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if verify != checksum {
		t.Fatalf("checksum mismatch: pack=%s verify=%s", checksum, verify)
	}

	dest := t.TempDir()
	if err := Unpack(archive, dest); err != nil {
		t.Fatalf("Unpack: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "skills", "example", "SKILL.md"))
	if err != nil {
		t.Fatalf("reading unpacked file: %v", err)
	}
	if string(got) != "# hi\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestUnpack_RejectsPathTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "../../etc/evil",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	dest := t.TempDir()
	if err := Unpack(archive, dest); err == nil {
		t.Fatal("expected Unpack to reject a path-traversal entry")
	}
}

func TestUnpack_RejectsDecompressionBomb(t *testing.T) {
	// Exercises unpackLimited directly with a tiny limit so the test
	// doesn't need to generate hundreds of megabytes of real data — the
	// limit-enforcement mechanism itself is what's under test, and it's
	// limit-agnostic.
	archive := filepath.Join(t.TempDir(), "bomb.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := make([]byte, 100) // legitimately 100 real bytes, no header lie involved
	if err := tw.WriteHeader(&tar.Header{
		Name:     "big-file",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	dest := t.TempDir()
	err = unpackLimited(archive, dest, 10) // limit far smaller than the 100-byte entry
	if err == nil {
		t.Fatal("expected unpackLimited to reject an archive exceeding the decompressed size limit")
	}

	if _, statErr := os.Stat(filepath.Join(dest, "big-file")); statErr == nil {
		t.Fatal("expected the oversized partial file to be removed, not left on disk")
	}
}

func TestUnpack_AllowsWithinLimit(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "small.txt"), "well within the limit")
	archive := filepath.Join(t.TempDir(), "ok.tar.gz")
	if _, err := Dir(src, archive); err != nil {
		t.Fatalf("Dir: %v", err)
	}

	dest := t.TempDir()
	if err := unpackLimited(archive, dest, 1<<20); err != nil {
		t.Fatalf("expected archive within limit to unpack cleanly, got: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
