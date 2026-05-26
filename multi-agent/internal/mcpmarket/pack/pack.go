package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits — enforced by both pack and unpack.
const (
	MaxCompressedBytes   = 10 * 1024 * 1024 // 10 MiB
	MaxUncompressedBytes = 50 * 1024 * 1024 // 50 MiB
	MaxFileBytes         = 5 * 1024 * 1024  // 5 MiB
	MaxFileCount         = 1024
)

// File is one entry to pack. Path is the relative path inside the
// tarball-prefix directory; Content is its bytes.
type File struct {
	Path    string
	Content []byte
	Mode    os.FileMode // 0644 for files, 0755 for dirs (auto-coerced)
}

// WriteTarball serializes files into a deterministic .tar.gz.
// All entries are placed under "<prefix>/" inside the archive.
// Returns the compressed bytes and their sha256 hex digest.
func WriteTarball(prefix string, files []File) ([]byte, string, error) {
	if prefix == "" {
		return nil, "", errors.New("pack: prefix required")
	}
	if len(files) > MaxFileCount {
		return nil, "", fmt.Errorf("pack: too many files (%d > %d)", len(files), MaxFileCount)
	}

	// Sort by path (codepoint) for determinism.
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	// Validate paths and accumulate sizes.
	seen := map[string]bool{}
	var totalUncompressed int64
	for _, f := range sorted {
		if err := checkRelPath(f.Path); err != nil {
			return nil, "", err
		}
		if len(f.Content) > MaxFileBytes {
			return nil, "", fmt.Errorf("pack: file %q exceeds %d bytes", f.Path, MaxFileBytes)
		}
		if seen[f.Path] {
			return nil, "", fmt.Errorf("pack: duplicate entry %q", f.Path)
		}
		seen[f.Path] = true
		totalUncompressed += int64(len(f.Content))
	}
	if totalUncompressed > MaxUncompressedBytes {
		return nil, "", fmt.Errorf("pack: total uncompressed %d exceeds %d", totalUncompressed, MaxUncompressedBytes)
	}

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, "", err
	}
	gz.Header.OS = 255 // deterministic
	gz.Header.ModTime = epoch()
	tw := tar.NewWriter(gz)

	// No explicit directory entries.
	for _, f := range sorted {
		entryName := prefix + "/" + f.Path
		if len(entryName) > 100 {
			return nil, "", fmt.Errorf("pack: path too long for USTAR: %q", entryName)
		}
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     entryName,
			Size:     int64(len(f.Content)),
			Mode:     0o644,
			ModTime:  epoch(),
			Uid:      0,
			Gid:      0,
			Uname:    "",
			Gname:    "",
			Format:   tar.FormatUSTAR,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", err
		}
		if _, err := tw.Write(f.Content); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", err
	}

	out := buf.Bytes()
	if len(out) > MaxCompressedBytes {
		return nil, "", fmt.Errorf("pack: compressed %d exceeds %d", len(out), MaxCompressedBytes)
	}

	h := sha256.Sum256(out)
	return out, hex.EncodeToString(h[:]), nil
}

// ReadTarball reverses WriteTarball with zip-slip / symlink / size protection.
// Returns the prefix directory name and the file list.
func ReadTarball(data []byte) (prefix string, files []File, err error) {
	if len(data) > MaxCompressedBytes {
		return "", nil, fmt.Errorf("unpack: compressed %d exceeds %d", len(data), MaxCompressedBytes)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", nil, fmt.Errorf("unpack: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, fmt.Errorf("unpack: tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			return "", nil, fmt.Errorf("unpack: entry %q has unsupported type %c", hdr.Name, hdr.Typeflag)
		}
		name := path.Clean(hdr.Name)
		if name != hdr.Name {
			return "", nil, fmt.Errorf("unpack: non-canonical path %q", hdr.Name)
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return "", nil, fmt.Errorf("unpack: unsafe path %q", hdr.Name)
		}
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			return "", nil, fmt.Errorf("unpack: entry %q missing prefix dir", hdr.Name)
		}
		if prefix == "" {
			prefix = parts[0]
		} else if prefix != parts[0] {
			return "", nil, fmt.Errorf("unpack: multiple prefixes (%q and %q)", prefix, parts[0])
		}
		if hdr.Size > MaxFileBytes {
			return "", nil, fmt.Errorf("unpack: file %q size %d exceeds %d", hdr.Name, hdr.Size, MaxFileBytes)
		}
		total += hdr.Size
		if total > MaxUncompressedBytes {
			return "", nil, fmt.Errorf("unpack: total uncompressed exceeds %d", MaxUncompressedBytes)
		}
		if len(files) >= MaxFileCount {
			return "", nil, fmt.Errorf("unpack: too many files (>%d)", MaxFileCount)
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxFileBytes+1))
		if err != nil {
			return "", nil, fmt.Errorf("unpack: read %q: %w", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			return "", nil, fmt.Errorf("unpack: short read on %q", hdr.Name)
		}
		files = append(files, File{Path: parts[1], Content: body, Mode: 0o644})
	}
	if prefix == "" {
		return "", nil, errors.New("unpack: empty archive")
	}
	return prefix, files, nil
}

// FilesFromDir reads a directory tree as []File ready for WriteTarball.
// Skips entries whose names start with ".".
func FilesFromDir(root string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, File{Path: rel, Content: body, Mode: 0o644})
		return nil
	})
	return out, err
}

func checkRelPath(p string) error {
	if p == "" {
		return errors.New("pack: empty path")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("pack: absolute path %q", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("pack: %q contains ..", p)
	}
	clean := path.Clean(p)
	if clean != p {
		return fmt.Errorf("pack: non-canonical path %q (expected %q)", p, clean)
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("pack: path %q contains control char", p)
		}
	}
	return nil
}

// epoch returns a frozen UTC zero time so identical inputs always produce
// identical archive bytes.
func epoch() time.Time { return time.Unix(0, 0).UTC() }
