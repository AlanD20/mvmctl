// Package vsock tests internal (unexported) functions directly because
// readFTFrame and writeFTFrame are unexported. These are intentionally
// duplicated between vsock and vsockagent — both copies must be tested.
package vsock

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/service/vsockagent"
)

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
