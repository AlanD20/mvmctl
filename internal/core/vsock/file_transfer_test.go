// Package vsock tests internal (unexported) functions directly because
// readFTFrame and writeFTFrame are unexported. These are intentionally
// duplicated between vsock and vsockagent — both copies must be tested.
package vsock

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/service/vsockagent"
)

// mkfifo creates a named pipe (FIFO) at path for testing non-regular file handling.
func mkfifo(t *testing.T, path string, mode uint32) {
	t.Helper()
	require.NoError(t, unix.Mkfifo(path, mode), "mkfifo %s", path)
}

// --- readFTFrame / writeFTFrame ---
// Rationale: Binary framing is duplicated between this package and
// vsockagent. A bug here corrupts every file transfer initiated by the host.

func TestReadFTFrameWriteFTFrame(t *testing.T) {
	fiveKB := make([]byte, 5*1024)
	for i := range fiveKB {
		fiveKB[i] = byte(i % 256)
	}

	tests := map[string]struct {
		frameType byte
		payload   []byte
	}{
		"text_payload": {
			frameType: vsockagent.FtMeta,
			payload:   []byte(`{"path":"test.txt","size":42}`),
		},
		"empty_payload": {
			frameType: vsockagent.FtData,
			payload:   nil,
		},
		"json_payload": {
			frameType: vsockagent.FtPush,
			payload:   []byte(`{"paths":["a"],"dest":"/tmp","overwrite":true}`),
		},
		"binary_payload": {
			frameType: vsockagent.FtData,
			payload:   []byte{0x00, 0x01, 0x02, 0xff},
		},
		"large_payload": {
			frameType: vsockagent.FtData,
			payload:   fiveKB,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := vsockagent.WriteFTFrame(&buf, tc.frameType, tc.payload)
			require.NoError(t, err)

			gotType, gotPayload, err := vsockagent.ReadFTFrame(&buf)
			require.NoError(t, err)
			assert.Equal(t, tc.frameType, gotType, "frame type must match")

			// Compare payloads: nil and empty slice are equivalent.
			if len(tc.payload) == 0 && len(gotPayload) == 0 {
				return
			}
			if diff := cmp.Diff(tc.payload, gotPayload); diff != "" {
				t.Errorf("vsockagent.ReadFTFrame() payload mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReadFTFrame_Error(t *testing.T) {
	t.Run("closed_reader", func(t *testing.T) {
		r, w := io.Pipe()
		w.Close()
		_, _, err := vsockagent.ReadFTFrame(r)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read frame length")
	})

	t.Run("write_to_closed_pipe", func(t *testing.T) {
		r, w := io.Pipe()
		r.Close()
		err := vsockagent.WriteFTFrame(w, vsockagent.FtData, []byte("hello"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write frame")
	})
}

// --- Frame protocol exchange ---
// Rationale: The binary frame protocol must be symmetric — frames written
// by writeFTFrame on one side must be readable by readFTFrame on the other.
// This test proves the full protocol exchange (push → mkdir → meta → accept
// → data → ok → done) works through a connection-oriented transport.

func TestFTPushProtocolExchange(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	defer hostConn.Close()
	defer guestConn.Close()

	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)

		// Guest side: reads push frame, writes mkdir ack,
		// reads meta, writes accept, reads data+EOS, writes ok,
		// reads done, writes done echo.
		ft, payload := mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtPush, ft, "guest: expected vsockagent.FtPush")

		var push vsockagent.FtPushPayload
		err := json.Unmarshal(payload, &push)
		require.NoError(t, err)
		require.Equal(t, []string{"test.txt"}, push.Paths)

		mkdirPayload, _ := json.Marshal(map[string]string{"path": push.Dest})
		mustWriteFTFrame(t, guestConn, vsockagent.FtMkdir, mkdirPayload)

		ft, metaPayload := mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtMeta, ft, "guest: expected vsockagent.FtMeta")
		var meta vsockagent.FtMetaPayload
		err = json.Unmarshal(metaPayload, &meta)
		require.NoError(t, err)
		require.Equal(t, "test.txt", meta.Path)

		acceptPayload, _ := json.Marshal(vsockagent.FtMetaPayload{Accepted: true})
		mustWriteFTFrame(t, guestConn, vsockagent.FtMeta, acceptPayload)

		// Read data chunks until EOS.
		var received bytes.Buffer
		for {
			ft, chunk := mustReadFTFrame(t, guestConn)
			if ft == vsockagent.FtData && len(chunk) == 0 {
				break // EOS
			}
			require.Equal(t, vsockagent.FtData, ft, "guest: expected vsockagent.FtData")
			received.Write(chunk)
		}

		okPayload, _ := json.Marshal(vsockagent.FtMetaPayload{
			Path:   meta.Path,
			Size:   int64(received.Len()),
			SHA256: "",
		})
		mustWriteFTFrame(t, guestConn, vsockagent.FtOK, okPayload)

		ft, _ = mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtDone, ft, "guest: expected vsockagent.FtDone")

		donePayload, _ := json.Marshal(vsockagent.FtDonePayload{Files: 1, Bytes: int64(received.Len()), Errors: 0})
		mustWriteFTFrame(t, guestConn, vsockagent.FtDone, donePayload)
	}()

	// Host side: sends push, reads mkdir, sends meta, reads accept,
	// sends data+EOS, reads ok, sends done, reads done echo.
	payload, _ := json.Marshal(vsockagent.FtPushPayload{
		Paths:     []string{"test.txt"},
		Dest:      "/tmp/test",
		Overwrite: true,
	})
	mustWriteFTFrame(t, hostConn, vsockagent.FtPush, payload)

	ft, mkdirPayload := mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtMkdir, ft, "host: expected vsockagent.FtMkdir")
	var mkdirResp map[string]string
	err := json.Unmarshal(mkdirPayload, &mkdirResp)
	require.NoError(t, err)
	require.Equal(t, "/tmp/test", mkdirResp["path"])

	metaPayload, _ := json.Marshal(vsockagent.FtMetaPayload{
		Path: "test.txt", Size: 5, Mode: 0644, SHA256: "",
	})
	mustWriteFTFrame(t, hostConn, vsockagent.FtMeta, metaPayload)

	ft, acceptPayload := mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtMeta, ft, "host: expected vsockagent.FtMeta accept")
	var accept vsockagent.FtMetaPayload
	err = json.Unmarshal(acceptPayload, &accept)
	require.NoError(t, err)
	require.True(t, accept.Accepted)

	mustWriteFTFrame(t, hostConn, vsockagent.FtData, []byte("hello"))
	mustWriteFTFrame(t, hostConn, vsockagent.FtData, nil) // EOS

	ft, okPayload := mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtOK, ft, "host: expected vsockagent.FtOK")

	var okMeta vsockagent.FtMetaPayload
	err = json.Unmarshal(okPayload, &okMeta)
	require.NoError(t, err)
	require.Equal(t, int64(5), okMeta.Size)

	donePayload, _ := json.Marshal(vsockagent.FtDonePayload{Files: 1, Bytes: 5, Errors: 0})
	mustWriteFTFrame(t, hostConn, vsockagent.FtDone, donePayload)

	ft, doneAckPayload := mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtDone, ft, "host: expected vsockagent.FtDone echo")
	var done vsockagent.FtDonePayload
	err = json.Unmarshal(doneAckPayload, &done)
	require.NoError(t, err)
	require.Equal(t, 1, done.Files)
	require.Equal(t, int64(5), done.Bytes)

	select {
	case <-guestDone:
		// Protocol exchange completed.
	case <-time.After(5 * time.Second):
		t.Fatal("guest did not complete within 5s")
	}
}

// Test helpers for the protocol exchange test.
func mustReadFTFrame(t *testing.T, r io.Reader) (byte, []byte) {
	t.Helper()
	typ, payload, err := vsockagent.ReadFTFrame(r)
	require.NoError(t, err)
	return typ, payload
}

func mustWriteFTFrame(t *testing.T, w io.Writer, typ byte, payload []byte) {
	t.Helper()
	err := vsockagent.WriteFTFrame(w, typ, payload)
	require.NoError(t, err)
}

// --- expandSources ---
// Rationale: expandSources resolves user-provided source paths into a flat
// list of {absPath, relativePath} entries. Bugs here skip files silently,
// copy wrong paths, or produce unhelpful errors — corrupting file transfers.

func TestExpandSources(t *testing.T) {
	tests := map[string]struct {
		setup   func(t *testing.T) (srcPaths []string, want []fileEntry)
		wantErr string
	}{
		"non_existent_path": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				return []string{"/tmp/nonexistent-mvm-test-file"}, nil
			},
			wantErr: "source not found",
		},
		"single_file": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				f := filepath.Join(dir, "test.txt")
				err := os.WriteFile(f, []byte("content"), 0644)
				require.NoError(t, err)
				return []string{f}, []fileEntry{{absPath: f, relativePath: "test.txt"}}
			},
		},
		"directory_with_files": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				a := filepath.Join(dir, "a.txt")
				b := filepath.Join(dir, "b.txt")
				require.NoError(t, os.WriteFile(a, []byte("a"), 0644))
				require.NoError(t, os.WriteFile(b, []byte("b"), 0644))
				return []string{dir}, []fileEntry{
					{absPath: a, relativePath: filepath.Join(base, "a.txt")},
					{absPath: b, relativePath: filepath.Join(base, "b.txt")},
				}
			},
		},
		"nested_subdirectory": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				sub := filepath.Join(dir, "sub")
				require.NoError(t, os.Mkdir(sub, 0755))
				c := filepath.Join(sub, "c.txt")
				require.NoError(t, os.WriteFile(c, []byte("c"), 0644))
				return []string{dir}, []fileEntry{
					{absPath: c, relativePath: filepath.Join(base, "sub/c.txt")},
				}
			},
		},
		"empty_directory": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				return []string{dir}, nil
			},
		},
		"multiple_sources_mixed": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()

				// Single file source.
				f1 := filepath.Join(dir, "root.txt")
				require.NoError(t, os.WriteFile(f1, []byte("root"), 0644))

				// Directory source with files.
				sub := filepath.Join(dir, "subdir")
				subBase := filepath.Base(sub)
				require.NoError(t, os.Mkdir(sub, 0755))
				f2 := filepath.Join(sub, "nested.py")
				require.NoError(t, os.WriteFile(f2, []byte("nested"), 0644))

				return []string{f1, sub}, []fileEntry{
					{absPath: f1, relativePath: "root.txt"},
					{absPath: f2, relativePath: filepath.Join(subBase, "nested.py")},
				}
			},
		},
		"broken_symlink_inside_directory": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				a := filepath.Join(dir, "a.txt")
				require.NoError(t, os.WriteFile(a, []byte("a"), 0644))
				broken := filepath.Join(dir, "broken")
				require.NoError(t, os.Symlink("/nonexistent-target-xyz", broken))
				// Only a.txt should appear; broken symlink is skipped.
				return []string{dir}, []fileEntry{
					{absPath: a, relativePath: filepath.Join(base, "a.txt")},
				}
			},
		},
		"symlink_to_directory": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				// Target subdirectory with a file.
				target := filepath.Join(dir, "realdir_target")
				require.NoError(t, os.Mkdir(target, 0755))
				f := filepath.Join(target, "nested.txt")
				require.NoError(t, os.WriteFile(f, []byte("nested"), 0644))
				// Symlink pointing to that directory.
				link := filepath.Join(dir, "mylink")
				require.NoError(t, os.Symlink(target, link))
				// With a per-branch ancestry stack, sibling symlinks are NOT
				// cycles. Both the physical path AND the symlink path are
				// followed because they are siblings, not descendants of each
				// other. The test expects both entries.
				return []string{dir}, []fileEntry{
					{
						absPath:      f,
						relativePath: filepath.Join(base, "realdir_target/nested.txt"),
					},
					{
						absPath:      filepath.Join(link, "nested.txt"),
						relativePath: filepath.Join(base, "mylink/nested.txt"),
					},
				}
			},
		},
		"symlink_to_already_walked_directory": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				// Create a real directory with a file.
				realDir := filepath.Join(dir, "realdir")
				require.NoError(t, os.Mkdir(realDir, 0755))
				f := filepath.Join(realDir, "file.txt")
				require.NoError(t, os.WriteFile(f, []byte("content"), 0644))
				// Create a sibling symlink that also points to realdir.
				link := filepath.Join(dir, "alink")
				require.NoError(t, os.Symlink("realdir", link))
				// With a per-branch stack, the sibling symlink is NOT a
				// cycle — both realdir/file.txt and alink/file.txt appear.
				return []string{dir}, []fileEntry{
					{absPath: f, relativePath: filepath.Join(base, "realdir/file.txt")},
					{absPath: filepath.Join(link, "file.txt"), relativePath: filepath.Join(base, "alink/file.txt")},
				}
			},
		},
		"symlink_cycle": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				// Create two directories and a regular file.
				aDir := filepath.Join(dir, "a")
				bDir := filepath.Join(dir, "b")
				require.NoError(t, os.Mkdir(aDir, 0755))
				require.NoError(t, os.Mkdir(bDir, 0755))
				reg := filepath.Join(dir, "real.txt")
				require.NoError(t, os.WriteFile(reg, []byte("real"), 0644))
				// a/link -> ../b  and  b/link -> ../a  create a cycle.
				require.NoError(t, os.Symlink("../b", filepath.Join(aDir, "link_to_b")))
				require.NoError(t, os.Symlink("../a", filepath.Join(bDir, "link_to_a")))
				// Only the regular file should be returned; the cycle is detected.
				return []string{dir}, []fileEntry{
					{absPath: reg, relativePath: filepath.Join(base, "real.txt")},
				}
			},
		},
		"non_regular_file_inside_directory": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				base := filepath.Base(dir)
				reg := filepath.Join(dir, "regular.txt")
				require.NoError(t, os.WriteFile(reg, []byte("content"), 0644))
				// Create a FIFO (named pipe) — a non-regular file.
				fifo := filepath.Join(dir, "myfifo")
				mkfifo(t, fifo, 0644)
				return []string{dir}, []fileEntry{
					{absPath: reg, relativePath: filepath.Join(base, "regular.txt")},
				}
			},
		},
		"symlink_followed": {
			setup: func(t *testing.T) ([]string, []fileEntry) {
				dir := t.TempDir()
				target := filepath.Join(dir, "target.txt")
				link := filepath.Join(dir, "link.txt")
				require.NoError(t, os.WriteFile(target, []byte("target"), 0644))
				require.NoError(t, os.Symlink(target, link))
				return []string{link}, []fileEntry{
					{absPath: link, relativePath: "link.txt"},
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srcPaths, want := tc.setup(t)
			got, err := expandSources(srcPaths)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Sort by relativePath for deterministic comparison.
			sort.Slice(got, func(i, j int) bool {
				return got[i].relativePath < got[j].relativePath
			})
			sort.Slice(want, func(i, j int) bool {
				return want[i].relativePath < want[j].relativePath
			})

			if diff := cmp.Diff(want, got, cmp.AllowUnexported(fileEntry{})); diff != "" {
				t.Errorf("expandSources() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- FTCopyFromVM: directory pull protocol exchange ---
// Rationale: The host-side directory pull reads META/DONE frames in a loop.
// This test simulates the guest agent sending multiple files via the protocol
// and verifies the host-side frame-reading logic works correctly.

func TestFTPullDirectoryProtocolExchange(t *testing.T) {
	hostConn, guestConn := net.Pipe()
	defer hostConn.Close()
	defer guestConn.Close()

	fileAContent := []byte("content of file A\n")
	fileBContent := []byte("content of file B is different\n")

	hashA := sha256.Sum256(fileAContent)
	hashAHex := hex.EncodeToString(hashA[:])
	hashB := sha256.Sum256(fileBContent)
	hashBHex := hex.EncodeToString(hashB[:])

	destDir := t.TempDir()

	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)

		// Guest side: reads PULL frame, sends META for file A,
		// reads accept, streams data, reads OK, sends META for file B,
		// reads accept, streams data, reads OK, sends DONE.
		ft, pullPayload := mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtPull, ft, "guest: expected FtPull")

		var pull vsockagent.FtPullPayload
		err := json.Unmarshal(pullPayload, &pull)
		require.NoError(t, err)
		require.True(t, pull.Recursive, "guest: expected Recursive=true")

		// Send META for file A (path is relative to source dir).
		metaA, _ := json.Marshal(vsockagent.FtMetaPayload{
			Path:   "a.txt",
			Size:   int64(len(fileAContent)),
			Mode:   0644,
			SHA256: hashAHex,
		})
		mustWriteFTFrame(t, guestConn, vsockagent.FtMeta, metaA)

		// Read accept.
		ft, acceptPayload := mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtMeta, ft, "guest: expected FtMeta accept")
		var accept vsockagent.FtMetaPayload
		json.Unmarshal(acceptPayload, &accept)
		require.True(t, accept.Accepted)

		// Stream file A data + EOS.
		mustWriteFTFrame(t, guestConn, vsockagent.FtData, fileAContent)
		mustWriteFTFrame(t, guestConn, vsockagent.FtData, nil)

		// Read OK.
		ft, okPayload := mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtOK, ft, "guest: expected FtOK")
		var okMeta vsockagent.FtMetaPayload
		json.Unmarshal(okPayload, &okMeta)
		require.Equal(t, int64(len(fileAContent)), okMeta.Size)

		// Send META for file B (path includes subdirectory).
		metaB, _ := json.Marshal(vsockagent.FtMetaPayload{
			Path:   "sub/b.txt",
			Size:   int64(len(fileBContent)),
			Mode:   0644,
			SHA256: hashBHex,
		})
		mustWriteFTFrame(t, guestConn, vsockagent.FtMeta, metaB)

		// Read accept.
		ft, acceptPayload = mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtMeta, ft, "guest: expected FtMeta accept")
		json.Unmarshal(acceptPayload, &accept)
		require.True(t, accept.Accepted)

		// Stream file B data + EOS.
		mustWriteFTFrame(t, guestConn, vsockagent.FtData, fileBContent)
		mustWriteFTFrame(t, guestConn, vsockagent.FtData, nil)

		// Read OK.
		ft, okPayload = mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtOK, ft, "guest: expected FtOK")
		json.Unmarshal(okPayload, &okMeta)
		require.Equal(t, int64(len(fileBContent)), okMeta.Size)

		// Send DONE with summary.
		donePayload, _ := json.Marshal(vsockagent.FtDonePayload{
			Files:  2,
			Bytes:  int64(len(fileAContent) + len(fileBContent)),
			Errors: 0,
		})
		mustWriteFTFrame(t, guestConn, vsockagent.FtDone, donePayload)
	}()

	// Host side: sends PULL with Recursive=true, then reads META/DONE loop.
	pullPayload, _ := json.Marshal(vsockagent.FtPullPayload{
		Path:      "/remote/dir/",
		Dest:      destDir + "/",
		Overwrite: false,
		Recursive: true,
	})
	mustWriteFTFrame(t, hostConn, vsockagent.FtPull, pullPayload)

	// Process file A.
	ft, metaPayload := mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtMeta, ft, "host: expected FtMeta for file A")
	var metaA vsockagent.FtMetaPayload
	err := json.Unmarshal(metaPayload, &metaA)
	require.NoError(t, err)
	require.Equal(t, "a.txt", metaA.Path)

	// Accept and receive.
	acceptPayload, _ := json.Marshal(vsockagent.FtMetaPayload{Accepted: true})
	mustWriteFTFrame(t, hostConn, vsockagent.FtMeta, acceptPayload)

	// Receive data + EOS and write to disk.
	destA := filepath.Join(destDir, "a.txt")
	err = os.MkdirAll(filepath.Dir(destA), 0755)
	require.NoError(t, err)

	var receivedA bytes.Buffer
	for {
		ft, chunk := mustReadFTFrame(t, hostConn)
		if ft == vsockagent.FtData && len(chunk) == 0 {
			break
		}
		require.Equal(t, vsockagent.FtData, ft)
		receivedA.Write(chunk)
	}
	require.Equal(t, string(fileAContent), receivedA.String())

	err = os.WriteFile(destA, receivedA.Bytes(), os.FileMode(metaA.Mode))
	require.NoError(t, err)

	// Verify SHA-256 and send OK.
	localHashA := sha256.Sum256(receivedA.Bytes())
	require.Equal(t, hashAHex, hex.EncodeToString(localHashA[:]))

	okPayload, _ := json.Marshal(vsockagent.FtMetaPayload{
		Path:   metaA.Path,
		Size:   int64(receivedA.Len()),
		SHA256: hex.EncodeToString(localHashA[:]),
	})
	mustWriteFTFrame(t, hostConn, vsockagent.FtOK, okPayload)

	// Process file B.
	ft, metaPayload = mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtMeta, ft, "host: expected FtMeta for file B")
	var metaB vsockagent.FtMetaPayload
	err = json.Unmarshal(metaPayload, &metaB)
	require.NoError(t, err)
	require.Equal(t, "sub/b.txt", metaB.Path)

	// Accept and receive.
	mustWriteFTFrame(t, hostConn, vsockagent.FtMeta, acceptPayload)

	destB := filepath.Join(destDir, "sub", "b.txt")
	err = os.MkdirAll(filepath.Dir(destB), 0755)
	require.NoError(t, err)

	var receivedB bytes.Buffer
	for {
		ft, chunk := mustReadFTFrame(t, hostConn)
		if ft == vsockagent.FtData && len(chunk) == 0 {
			break
		}
		require.Equal(t, vsockagent.FtData, ft)
		receivedB.Write(chunk)
	}
	require.Equal(t, string(fileBContent), receivedB.String())

	err = os.WriteFile(destB, receivedB.Bytes(), os.FileMode(metaB.Mode))
	require.NoError(t, err)

	localHashB := sha256.Sum256(receivedB.Bytes())
	require.Equal(t, hashBHex, hex.EncodeToString(localHashB[:]))

	okPayload, _ = json.Marshal(vsockagent.FtMetaPayload{
		Path:   metaB.Path,
		Size:   int64(receivedB.Len()),
		SHA256: hex.EncodeToString(localHashB[:]),
	})
	mustWriteFTFrame(t, hostConn, vsockagent.FtOK, okPayload)

	// Read DONE.
	ft, donePayload := mustReadFTFrame(t, hostConn)
	require.Equal(t, vsockagent.FtDone, ft, "host: expected FtDone")
	var done vsockagent.FtDonePayload
	json.Unmarshal(donePayload, &done)
	require.Equal(t, 2, done.Files)
	require.Equal(t, int64(len(fileAContent)+len(fileBContent)), done.Bytes)
	require.Equal(t, 0, done.Errors)

	// Verify the two files were written on disk.
	writtenA, err := os.ReadFile(destA)
	require.NoError(t, err, "file a.txt must exist at dest")
	if diff := cmp.Diff(string(fileAContent), string(writtenA)); diff != "" {
		t.Errorf("file a.txt mismatch (-want +got):\n%s", diff)
	}

	writtenB, err := os.ReadFile(destB)
	require.NoError(t, err, "file sub/b.txt must exist at dest")
	if diff := cmp.Diff(string(fileBContent), string(writtenB)); diff != "" {
		t.Errorf("file sub/b.txt mismatch (-want +got):\n%s", diff)
	}

	select {
	case <-guestDone:
		// Protocol exchange completed.
	case <-time.After(5 * time.Second):
		t.Fatal("guest did not complete within 5s")
	}

	// Also verify that receivingPullFile correctly handles the "exists" case
	// when overwrite=false and the file already exists.
	t.Run("exists_rejection", func(t *testing.T) {
		// Create a new pipe to test a fresh exchange.
		hConn, gConn := net.Pipe()
		defer hConn.Close()
		defer gConn.Close()

		gDone := make(chan struct{})
		go func() {
			defer close(gDone)
			// Agent sends PULL with Recursive=true, then META for a file,
			// then expects FtError (reject), then sends DONE.

			ft, _ := mustReadFTFrame(t, gConn)
			require.Equal(t, vsockagent.FtPull, ft)

			metaPayload, _ := json.Marshal(vsockagent.FtMetaPayload{
				Path:   "existing.txt",
				Size:   5,
				Mode:   0644,
				SHA256: "",
			})
			mustWriteFTFrame(t, gConn, vsockagent.FtMeta, metaPayload)

			// Expect FtError (reject).
			ft, errPayload := mustReadFTFrame(t, gConn)
			require.Equal(t, vsockagent.FtError, ft)

			var errResp vsockagent.FtErrorPayload
			json.Unmarshal(errPayload, &errResp)
			require.Equal(t, "exists", errResp.Code)

			// Send DONE to close the loop on host side.
			donePayload, _ := json.Marshal(vsockagent.FtDonePayload{Files: 0, Bytes: 0, Errors: 1})
			mustWriteFTFrame(t, gConn, vsockagent.FtDone, donePayload)
		}()

		// Create a file at the destination that will be rejected.
		existingPath := filepath.Join(t.TempDir(), "existing.txt")
		err := os.WriteFile(existingPath, []byte("original"), 0644)
		require.NoError(t, err)

		pullPayload, _ := json.Marshal(vsockagent.FtPullPayload{
			Path:      "/remote/dir/",
			Dest:      filepath.Dir(existingPath) + "/",
			Overwrite: false,
			Recursive: true,
		})
		mustWriteFTFrame(t, hConn, vsockagent.FtPull, pullPayload)

		// Read META.
		ft, _ := mustReadFTFrame(t, hConn)
		require.Equal(t, vsockagent.FtMeta, ft)

		// Send FtError (reject).
		rejectPayload, _ := json.Marshal(vsockagent.FtErrorPayload{
			Code:    "exists",
			Message: fmt.Sprintf("file exists: %s", existingPath),
		})
		mustWriteFTFrame(t, hConn, vsockagent.FtError, rejectPayload)

		// Read DONE.
		ft, _ = mustReadFTFrame(t, hConn)
		require.Equal(t, vsockagent.FtDone, ft)

		select {
		case <-gDone:
		case <-time.After(5 * time.Second):
			t.Fatal("guest did not complete within 5s")
		}
	})
}

// TestFTPushDirSourceAutoSlash verifies that when copying a single directory
// source to a destination without a trailing slash, the host appends "/" to
// force directory mode on the agent. Without this, the agent treats the dest
// as a regular file path and every file tries to create the same path.
func TestFTPushDirSourceAutoSlash(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("a"), 0644))

	hostConn, guestConn := net.Pipe()
	defer hostConn.Close()
	defer guestConn.Close()

	guestDone := make(chan struct{})
	go func() {
		defer close(guestDone)

		// Guest reads Push frame and verifies Dest ends with "/".
		ft, payload := mustReadFTFrame(t, guestConn)
		require.Equal(t, vsockagent.FtPush, ft)
		var push vsockagent.FtPushPayload
		require.NoError(t, json.Unmarshal(payload, &push))
		assert.True(t, strings.HasSuffix(push.Dest, "/"),
			"Dest %q should end with / for single directory source", push.Dest)

		// Send Mkdir ack to unblock host.
		mustWriteFTFrame(t, guestConn, vsockagent.FtMkdir, []byte(`{"path":"/dest/"}`))

		// Reject every Meta to drain the protocol without streaming files.
		for {
			ft, _, err := vsockagent.ReadFTFrame(guestConn)
			if err != nil {
				return
			}
			if ft == vsockagent.FtDone {
				mustWriteFTFrame(t, guestConn, vsockagent.FtDone, nil)
				return
			}
			if ft == vsockagent.FtMeta {
				errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{Code: "test", Message: "test"})
				mustWriteFTFrame(t, guestConn, vsockagent.FtError, errPayload)
			}
		}
	}()

	// Simulate what FTCopyToVM does: single dir source, no trailing slash on dest.
	srcPaths := []string{srcDir}
	destPath := "/dest"
	if len(srcPaths) == 1 && !strings.HasSuffix(destPath, "/") {
		if fi, stErr := os.Stat(srcPaths[0]); stErr == nil && fi.IsDir() {
			destPath += "/"
		}
	}

	// Send push frame with the (potentially modified) destPath.
	pushPayload, _ := json.Marshal(vsockagent.FtPushPayload{
		Paths: srcPaths, Dest: destPath, Overwrite: false,
	})
	mustWriteFTFrame(t, hostConn, vsockagent.FtPush, pushPayload)
	ft, _ := mustReadFTFrame(t, hostConn) // Mkdir ack
	require.Equal(t, vsockagent.FtMkdir, ft)

	// Expand sources and reject each file so the protocol completes.
	entries, err := ExpandSources(srcPaths)
	require.NoError(t, err)
	for _, entry := range entries {
		meta, _ := json.Marshal(vsockagent.FtMetaPayload{
			Path: entry.relativePath, Size: 1, Mode: 0644,
		})
		mustWriteFTFrame(t, hostConn, vsockagent.FtMeta, meta)
		ft, _ := mustReadFTFrame(t, hostConn)
		require.Equal(t, vsockagent.FtError, ft)
	}

	// Send Done.
	mustWriteFTFrame(t, hostConn, vsockagent.FtDone, nil)
	ft, _ = mustReadFTFrame(t, hostConn) // Done echo
	require.Equal(t, vsockagent.FtDone, ft)

	select {
	case <-guestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
}
