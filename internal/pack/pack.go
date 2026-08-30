// Package pack tars/gzips a plugin directory into a distributable archive
// and unpacks it back out, verifying containment so no entry can escape the
// target directory (path traversal via "../" or absolute paths in the
// archive) and capping total decompressed output (decompression-bomb
// protection).
package pack

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Dir tars+gzips the contents of srcDir into a new file at destTarGz and
// returns the archive's sha256 checksum (hex-encoded).
func Dir(srcDir, destTarGz string) (checksum string, err error) {
	out, err := os.Create(destTarGz)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", destTarGz, err)
	}
	defer out.Close()

	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, h))
	tw := tar.NewWriter(gz)

	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return err
		}

		if d.IsDir() {
			hdr := &tar.Header{Name: rel + "/", Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()}
			return tw.WriteHeader(hdr)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to pack non-regular file %s", rel)
		}

		hdr := &tar.Header{Name: rel, Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()), Size: info.Size(), ModTime: info.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if walkErr != nil {
		return "", walkErr
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Checksum computes the sha256 checksum (hex-encoded) of a file.
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MaxDecompressedBytes caps the total bytes Unpack will write across every
// entry in an archive. Without this, a small, highly-compressible upload
// (well under the transport-level upload size cap, since that cap only
// bounds the *compressed* bytes) could decompress to gigabytes and exhaust
// disk — a classic decompression-bomb DoS. 256MiB is generous for a plugin
// bundle (skills/docs/small scripts).
const MaxDecompressedBytes int64 = 256 << 20

// Unpack extracts a tar.gz archive into destDir, which must already exist.
// Every entry is required to resolve inside destDir; anything else
// (absolute paths, "../" traversal, symlinks pointing outside) is rejected.
// Total decompressed output is capped at MaxDecompressedBytes. This is the
// strict mode used for our own archives (server-side publish validation,
// client install) — any entry type other than a file or directory aborts
// the whole extraction.
func Unpack(tarGzPath, destDir string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return ExtractReader(f, destDir, ExtractOptions{})
}

// ExtractOptions configures ExtractReader. The zero value matches Unpack's
// behavior: MaxDecompressedBytes cap, no path stripping, unsupported entry
// types are an error.
type ExtractOptions struct {
	// MaxBytes caps total decompressed bytes across all entries. Zero
	// means MaxDecompressedBytes.
	MaxBytes int64
	// StripComponents drops this many leading path segments from every
	// entry name before joining onto destDir — e.g. 1 to unwrap a
	// GitHub codeload archive's "{owner}-{repo}-{sha}/" root directory.
	StripComponents int
	// SkipUnsupportedTypes, when true, silently skips tar entries that
	// are neither a regular file nor a directory (symlinks, etc.)
	// instead of aborting extraction. Real-world third-party archives
	// (e.g. from the crawler) legitimately contain these; our own
	// archives never should, so Unpack leaves this false.
	SkipUnsupportedTypes bool
}

// ExtractReader is the shared extraction core behind Unpack, generalized
// to work from any gzip-compressed tar stream (not just a local file) so
// it can also power extracting untrusted third-party archives (see
// internal/crawler) — one well-tested implementation of the
// path-traversal and decompression-bomb protections, not two.
func ExtractReader(r io.Reader, destDir string, opts ExtractOptions) error {
	limit := opts.MaxBytes
	if limit <= 0 {
		limit = MaxDecompressedBytes
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var totalWritten int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		name, ok := stripComponents(hdr.Name, opts.StripComponents)
		if !ok {
			continue // exactly the stripped prefix itself (e.g. the archive's root dir entry)
		}

		target, err := safeJoin(destDir, name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileMode(hdr.Mode))
			if err != nil {
				return err
			}

			remaining := limit - totalWritten
			if remaining <= 0 {
				out.Close()
				os.Remove(target)
				return fmt.Errorf("archive exceeds %d byte decompressed size limit", limit)
			}
			n, err := io.CopyN(out, tr, remaining+1)
			totalWritten += n
			if err != nil && err != io.EOF {
				out.Close()
				return err
			}
			if n > remaining {
				out.Close()
				os.Remove(target)
				return fmt.Errorf("archive exceeds %d byte decompressed size limit", limit)
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			if opts.SkipUnsupportedTypes {
				continue
			}
			return fmt.Errorf("unsupported tar entry type %d for %s", hdr.Typeflag, name)
		}
	}
}

// stripComponents drops the first n "/"-separated segments from a tar
// entry name (tar names always use "/" regardless of host OS). Returns
// ok=false if the name has n or fewer segments — i.e. it names exactly
// the prefix being stripped, or is otherwise too shallow to keep.
func stripComponents(name string, n int) (string, bool) {
	name = path.Clean(name)
	if name == "." {
		return "", false
	}
	segs := strings.Split(name, "/")
	if len(segs) <= n {
		return "", false
	}
	return strings.Join(segs[n:], "/"), true
}

func fileMode(m int64) os.FileMode {
	mode := os.FileMode(m).Perm()
	if mode == 0 {
		return 0o644
	}
	return mode
}

// safeJoin joins name onto base, rejecting absolute paths and any result
// that would escape base.
func safeJoin(base, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(filepath.ToSlash(name), "/") {
		return "", fmt.Errorf("archive entry has absolute path: %s", name)
	}
	cleaned := filepath.Clean(filepath.Join(base, name))
	baseClean := filepath.Clean(base)
	if cleaned != baseClean && !strings.HasPrefix(cleaned, baseClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes target directory: %s", name)
	}
	return cleaned, nil
}
