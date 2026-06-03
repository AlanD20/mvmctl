// Package archive provides archive extraction, validation, and format detection.
//
// Supported formats:
//   - .tar — plain tar via Go stdlib archive/tar
//   - .tar.gz, .tgz — gzip-compressed tar via Go stdlib compress/gzip + archive/tar
//   - .tar.xz — xz-compressed tar via subprocess xz -d --stdout + archive/tar
//
// All extraction includes path traversal protection. Symlinks and device files
// are skipped (matching Python's tarfile filter="data"). File permissions are
// NOT restored from the archive — extracted files always get 0644/0755.
package archive

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Format represents a detected archive format.
type Format int

const (
	FormatUnknown Format = iota
	FormatTar
	FormatTarGzip
	FormatTarXz
)

// Magic bytes for format detection.
var (
	gzipMagic = []byte{0x1f, 0x8b}
	xzMagic   = []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}
)

// maxMagicLen is the longest magic byte sequence we check (6 for xz).
const maxMagicLen = 6

// ── Format detection ────────────────────────────────────────────────────────

// DetectFormat detects the archive format from magic bytes, falling back to
// file extension. Opens the file briefly to read header bytes.
func DetectFormat(path string) Format {
	f, err := os.Open(path)
	if err != nil {
		return formatFromExtension(path)
	}
	defer f.Close()

	header := make([]byte, maxMagicLen)
	n, _ := io.ReadFull(f, header)
	if n < 2 {
		return FormatUnknown
	}

	if header[0] == gzipMagic[0] && header[1] == gzipMagic[1] {
		return FormatTarGzip
	}

	if n >= 6 {
		match := true
		for i := range 6 {
			if header[i] != xzMagic[i] {
				match = false
				break
			}
		}
		if match {
			return FormatTarXz
		}
	}

	return formatFromExtension(path)
}

func formatFromExtension(path string) Format {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGzip
	case strings.HasSuffix(lower, ".tar.xz"):
		return FormatTarXz
	case strings.HasSuffix(lower, ".tar"):
		return FormatTar
	}
	return FormatUnknown
}

// IsArchive returns true if the file at path is a supported archive format.
func IsArchive(path string) bool {
	return DetectFormat(path) != FormatUnknown
}

// ── Reader setup ────────────────────────────────────────────────────────────

// archiveReader holds the io.Reader and a cleanup function for the archive.
// The cleanup function may return an error from the decompressor (e.g., xz exit status).
type archiveReader struct {
	reader  io.Reader
	cleanup func() error
}

func openArchive(ctx context.Context, path string) (*archiveReader, error) {
	format := DetectFormat(path)
	if format == FormatUnknown {
		return nil, fmt.Errorf("unsupported archive format: %s", path)
	}

	switch format {
	case FormatTar:
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open archive: %w", err)
		}
		return &archiveReader{
			reader:  f,
			cleanup: func() error { return f.Close() },
		}, nil

	case FormatTarGzip:
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open archive: %w", err)
		}
		gr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("gzip decompress: %w", err)
		}
		return &archiveReader{
			reader: gr,
			cleanup: func() error {
				grErr := gr.Close()
				fErr := f.Close()
				if grErr != nil {
					return grErr
				}
				return fErr
			},
		}, nil

	case FormatTarXz:
		if _, err := exec.LookPath("xz"); err != nil {
			return nil, fmt.Errorf("xz not found: install xz-utils (e.g., apt install xz-utils)")
		}
		cmd := exec.CommandContext(ctx, "xz", "-d", "--stdout", path)
		cmd.Stderr = os.Stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("xz pipe: %w", err)
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("xz start: %w", err)
		}
		return &archiveReader{
			reader: stdout,
			cleanup: func() error {
				// Close the pipe first to unblock xz if the reader stopped
				// consuming data (e.g., early exit due to error or path
				// traversal). Without this, cmd.Wait() deadlocks: xz can't
				// exit because it's blocked writing to a full pipe buffer.
				pipeErr := stdout.Close()
				waitErr := cmd.Wait()
				if pipeErr != nil {
					return pipeErr
				}
				return waitErr
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported archive format")
	}
}

func (ar *archiveReader) close() error {
	if ar.cleanup != nil {
		return ar.cleanup()
	}
	return nil
}

// ── Streaming (in-memory tar pipe) ──────────────────────────────────────────

// Pack creates a tar archive of the given path and writes it to w.
// Uses the same convention as tar cf - -C <parent> <base>:
//   - If path is a directory, its CONTENTS are archived (not the dir itself).
//   - If path is a file, just that file is archived.
//   - base is the name to use for the entry in the archive. If empty,
//     filepath.Base(path) is used. For directories an empty base means
//     "archive contents only" (like tar cf - -C path .).
func Pack(ctx context.Context, path, base string, w io.Writer) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("pack: %w", err)
	}

	// Buffered writer to coalesce small tar blocks into larger writes
	// (important for SSH streaming — avoids many small TCP packets).
	buf := bufio.NewWriterSize(w, 256*1024)
	tw := tar.NewWriter(buf)

	// Close must flush both the tar EOF blocks and the buffered writer.
	closeFn := func() error {
		if err := tw.Close(); err != nil {
			return err
		}
		return buf.Flush()
	}

	if info.IsDir() {
		if base == "" {
			err = packDir(ctx, tw, path, "", info)
			return firstErr(err, closeFn())
		}
		if err := writeDirEntry(tw, base, info); err != nil {
			closeFn()
			return err
		}
		err = packDir(ctx, tw, path, base, info)
		return firstErr(err, closeFn())
	}

	// Single file
	entryName := base
	if entryName == "" {
		entryName = filepath.Base(path)
	}
	if err := writeFileEntry(tw, path, entryName, info); err != nil {
		closeFn()
		return err
	}
	return closeFn()
}

// firstErr returns a if non-nil, otherwise b.
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func packDir(ctx context.Context, tw *tar.Writer, dir, prefix string, info os.FileInfo) error {
	return filepath.Walk(dir, func(fpath string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Check context cancellation so Ctrl+C/interrupt stops the walk.
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, fpath)
		if rel == "." {
			return nil
		}
		entryName := filepath.Join(prefix, rel)

		switch {
		case fi.IsDir():
			return writeDirEntry(tw, entryName, fi)
		case fi.Mode().IsRegular():
			return writeFileEntry(tw, fpath, entryName, fi)
		case fi.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(fpath)
			if err != nil {
				return err
			}
			return tw.WriteHeader(&tar.Header{
				Name:     entryName,
				Linkname: link,
				Size:     0,
				Mode:     int64(fi.Mode().Perm()),
				Typeflag: tar.TypeSymlink,
				ModTime:  fi.ModTime(),
			})
		}
		return nil
	})
}

func writeDirEntry(tw *tar.Writer, name string, fi os.FileInfo) error {
	return tw.WriteHeader(&tar.Header{
		Name:     name + "/",
		Mode:     int64(fi.Mode().Perm()),
		Typeflag: tar.TypeDir,
		ModTime:  fi.ModTime(),
	})
}

func writeFileEntry(tw *tar.Writer, fpath, name string, fi os.FileInfo) error {
	f, err := os.Open(fpath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Size:    fi.Size(),
		Mode:    int64(fi.Mode().Perm()),
		ModTime: fi.ModTime(),
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Unpack extracts a tar archive from r into destDir using Go's archive/tar.
// Same path traversal protection as Extract. Symlinks are skipped.
func Unpack(ctx context.Context, r io.Reader, destDir string) error {
	return extractTarReader(r, destDir, nil)
}

// ── Extraction ──────────────────────────────────────────────────────────────
func Extract(ctx context.Context, path, destDir string) error {
	return extractFiltered(ctx, path, destDir, nil)
}

// ExtractMembers extracts only the named members from the archive.
// Members not in the list are skipped.
func ExtractMembers(ctx context.Context, path, destDir string, members []string) error {
	memberSet := make(map[string]bool, len(members))
	for _, m := range members {
		memberSet[m] = true
	}
	return extractFiltered(ctx, path, destDir, func(name string) bool {
		return memberSet[name]
	})
}

// RenameEntry maps an archive member (matched by basename) to an output path.
type RenameEntry struct {
	// ArchiveName is the entry name to match. Members whose filepath.Base()
	// equals this are extracted. If ArchiveName contains a "/", the match is
	// against the full member path instead.
	ArchiveName string

	// OutputPath is the absolute filesystem path to write the extracted data.
	OutputPath string

	// Mode sets the file permissions. 0 means default (0644).
	Mode os.FileMode
}

// ExtractRenamed extracts specific entries from the archive, writing each to
// its configured OutputPath with the configured Mode. Members are matched by
// basename unless ArchiveName contains "/" (full path match).
// Returns an error if any entry's ArchiveName is not found in the archive.
func ExtractRenamed(ctx context.Context, path string, entries []RenameEntry) (retErr error) {
	// Build lookup: archive name → entry index
	needed := make(map[string]int, len(entries))
	for i, e := range entries {
		needed[e.ArchiveName] = i
	}

	ar, err := openArchive(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := ar.close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	tr := tar.NewReader(ar.reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		// Match by basename. If ArchiveName contains "/", also try full path.
		matchKey := filepath.Base(header.Name)
		idx, ok := needed[matchKey]
		if !ok && strings.Contains(header.Name, "/") {
			// Also try full path match
			idx, ok = needed[header.Name]
		}
		if !ok {
			continue
		}

		entry := entries[idx]
		if err := os.MkdirAll(filepath.Dir(entry.OutputPath), 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(entry.OutputPath), err)
		}
		mode := entry.Mode
		if mode == 0 {
			mode = 0644
		}
		outFile, err := os.OpenFile(entry.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("create %s: %w", entry.OutputPath, err)
		}
		if _, err := io.Copy(outFile, tr); err != nil {
			outFile.Close()
			return fmt.Errorf("write %s: %w", entry.OutputPath, err)
		}
		outFile.Close()
		delete(needed, entry.ArchiveName)
	}

	if len(needed) > 0 {
		var missing []string
		for name := range needed {
			missing = append(missing, name)
		}
		return fmt.Errorf("archive missing entries: %s", strings.Join(missing, ", "))
	}
	return nil
}

func extractFiltered(ctx context.Context, path, destDir string, filter func(string) bool) (retErr error) {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create extract dir: %w", err)
	}

	ar, err := openArchive(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := ar.close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	return extractTarReader(ar.reader, destDir, filter)
}

// ── Shared tar extraction loop ──────────────────────────────────────────────

func extractTarReader(r io.Reader, destDir string, filter func(string) bool) error {
	tr := tar.NewReader(r)
	destDir = filepath.Clean(destDir)
	prefix := destDir + string(os.PathSeparator)

	// Hardlinks may appear before their target file in the archive. Collect
	// them and process in a second pass after all regular files are written.
	type pendingLink struct {
		target string
		link   string
	}
	var hardlinks []pendingLink

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		// Path traversal protection: resolve header.Name against destDir and
		// verify the result stays within bounds. filepath.Join handles "..",
		// absolute-looking paths (stripped of leading "/"), etc.
		target := filepath.Join(destDir, header.Name)
		if target != destDir && !strings.HasPrefix(target, prefix) {
			return fmt.Errorf("path traversal detected: %s", header.Name)
		}

		// Filter: skip if not in member list
		if filter != nil && !filter(header.Name) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}

		case tar.TypeReg, tar.TypeRegA, tar.TypeGNUSparse:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(target), err)
			}
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				return fmt.Errorf("create %s: %w", target, err)
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("write %s: %w", target, err)
			}
			outFile.Close()

		case tar.TypeLink:
			// Defer hardlink creation — the target might not exist yet.
			linkTarget := filepath.Join(destDir, header.Linkname)
			if linkTarget != destDir && !strings.HasPrefix(linkTarget, prefix) {
				return fmt.Errorf("path traversal detected in hardlink: %s", header.Linkname)
			}
			hardlinks = append(hardlinks, pendingLink{target: target, link: linkTarget})

		case tar.TypeSymlink:
			continue

		default:
			continue
		}
	}

	// Second pass: create hardlinks now that all regular files exist.
	for _, hl := range hardlinks {
		if err := os.MkdirAll(filepath.Dir(hl.target), 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(hl.target), err)
		}
		if err := os.Link(hl.link, hl.target); err != nil {
			return fmt.Errorf("link %s -> %s: %w", hl.target, hl.link, err)
		}
	}

	return nil
}

// ── Validation ──────────────────────────────────────────────────────────────

// Validate checks that the archive is fully readable by iterating all members.
func Validate(ctx context.Context, path string) (retErr error) {
	ar, err := openArchive(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := ar.close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	tr := tar.NewReader(ar.reader)
	for {
		if _, err := tr.Next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("archive validation failed: %w", err)
		}
	}
	return nil
}

// ── Listing ─────────────────────────────────────────────────────────────────

// List returns the names of all entries in the archive.
func List(ctx context.Context, path string) (names []string, retErr error) {
	ar, err := openArchive(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := ar.close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	tr := tar.NewReader(ar.reader)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list archive: %w", err)
		}
		names = append(names, h.Name)
	}
	return names, nil
}
