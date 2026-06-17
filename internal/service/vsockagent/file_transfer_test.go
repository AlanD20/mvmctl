// Package vsockagent tests internal (unexported) functions directly because
// readFTFrame, writeFTFrame, handleFTPush, handleFTPull, and all ft*Payload
// types are unexported. Testing them directly is the only viable approach.
package vsockagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── readFTFrame / writeFTFrame ─────────────────────────────────────────────
// Rationale: Binary framing is the foundation of file transfer protocol.
// A bug here corrupts every file transfer. Must handle all payload types.

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
			frameType: FtMeta,
			payload:   []byte(`{"path":"test.txt","size":42}`),
		},
		"empty_payload": {
			frameType: FtData,
			payload:   nil,
		},
		"json_payload": {
			frameType: FtPush,
			payload:   []byte(`{"paths":["a","b"],"dest":"/tmp","overwrite":true}`),
		},
		"binary_payload": {
			frameType: FtData,
			payload:   []byte{0x00, 0x01, 0x02, 0xff, 0xfe},
		},
		"large_payload": {
			frameType: FtData,
			payload:   fiveKB,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteFTFrame(&buf, tc.frameType, tc.payload)
			require.NoError(t, err)

			gotType, gotPayload, err := ReadFTFrame(&buf)
			require.NoError(t, err)
			assert.Equal(t, tc.frameType, gotType, "frame type must match")

			// Compare payloads: nil and empty slice are equivalent.
			if len(tc.payload) == 0 && len(gotPayload) == 0 {
				return
			}
			if diff := cmp.Diff(tc.payload, gotPayload); diff != "" {
				t.Errorf("ReadFTFrame() payload mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReadFTFrame_Error(t *testing.T) {
	t.Run("closed_reader", func(t *testing.T) {
		r, w := io.Pipe()
		w.Close()
		_, _, err := ReadFTFrame(r)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read frame length")
	})

	t.Run("write_to_closed_pipe", func(t *testing.T) {
		r, w := io.Pipe()
		r.Close()
		err := WriteFTFrame(w, FtData, []byte("hello"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "write frame")
	})
}

// ─── JSON payload types ────────────────────────────────────────────────────
// Rationale: JSON field names must match the wire protocol spec. Mismatched
// names cause silent deserialisation failures — the payload arrives but fields
// are zero-valued. Every payload type is tested.

func TestFTPushPayloadJSON(t *testing.T) {
	tests := map[string]struct {
		input FtPushPayload
		want  string
	}{
		"full_fields": {
			input: FtPushPayload{
				Paths:     []string{"a.txt", "b.txt"},
				Dest:      "/tmp/vm",
				Overwrite: true,
			},
			want: `{"paths":["a.txt","b.txt"],"dest":"/tmp/vm","overwrite":true}`,
		},
		"empty_paths": {
			input: FtPushPayload{
				Paths:     nil,
				Dest:      "/tmp",
				Overwrite: false,
			},
			want: `{"paths":null,"dest":"/tmp","overwrite":false}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, string(data)); diff != "" {
				t.Errorf("Marshal FtPushPayload mismatch (-want +got):\n%s", diff)
			}

			var got FtPushPayload
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.input, got); diff != "" {
				t.Errorf("Unmarshal round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFTPullPayloadJSON(t *testing.T) {
	tests := map[string]struct {
		input FtPullPayload
		want  string
	}{
		"full_fields": {
			input: FtPullPayload{
				Path:      "/home/user/file.txt",
				Dest:      "/tmp/downloads",
				Overwrite: true,
			},
			want: `{"path":"/home/user/file.txt","dest":"/tmp/downloads","overwrite":true}`,
		},
		"zero_overwrite": {
			input: FtPullPayload{
				Path:      "/tmp/file.bin",
				Dest:      "/out",
				Overwrite: false,
			},
			want: `{"path":"/tmp/file.bin","dest":"/out","overwrite":false}`,
		},
		"minimal_fields": {
			input: FtPullPayload{
				Path: "/some/path",
			},
			want: `{"path":"/some/path","dest":"","overwrite":false}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, string(data)); diff != "" {
				t.Errorf("Marshal FtPullPayload mismatch (-want +got):\n%s", diff)
			}

			var got FtPullPayload
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.input, got); diff != "" {
				t.Errorf("Unmarshal round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFTMetaPayloadJSON(t *testing.T) {
	tests := map[string]struct {
		input FtMetaPayload
		want  string
	}{
		"full_fields": {
			input: FtMetaPayload{
				Path:     "file.txt",
				Size:     1024,
				Mode:     0644,
				SHA256:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
				Accepted: true,
			},
			want: `{"path":"file.txt","size":1024,"mode":420,"sha256":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890","accepted":true}`,
		},
		"empty_fields": {
			input: FtMetaPayload{},
			want:  `{}`,
		},
		"accepted_only": {
			input: FtMetaPayload{
				Accepted: true,
			},
			want: `{"accepted":true}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, string(data)); diff != "" {
				t.Errorf("Marshal FtMetaPayload mismatch (-want +got):\n%s", diff)
			}

			var got FtMetaPayload
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.input, got); diff != "" {
				t.Errorf("Unmarshal round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFTProgressPayloadJSON(t *testing.T) {
	tests := map[string]struct {
		input FtProgressPayload
		want  string
	}{
		"full_fields": {
			input: FtProgressPayload{
				Path:  "file.bin",
				Bytes: 500,
				Total: 1000,
			},
			want: `{"path":"file.bin","bytes":500,"total":1000}`,
		},
		"zero_values": {
			input: FtProgressPayload{},
			want:  `{"bytes":0,"total":0}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, string(data)); diff != "" {
				t.Errorf("Marshal FtProgressPayload mismatch (-want +got):\n%s", diff)
			}

			var got FtProgressPayload
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.input, got); diff != "" {
				t.Errorf("Unmarshal round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFTErrorPayloadJSON(t *testing.T) {
	tests := map[string]struct {
		input FtErrorPayload
		want  string
	}{
		"full_fields": {
			input: FtErrorPayload{
				Code:    "hash_mismatch",
				Message: "SHA-256 mismatch for file.txt",
			},
			want: `{"code":"hash_mismatch","message":"SHA-256 mismatch for file.txt"}`,
		},
		"empty_fields": {
			input: FtErrorPayload{},
			want:  `{"code":"","message":""}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, string(data)); diff != "" {
				t.Errorf("Marshal FtErrorPayload mismatch (-want +got):\n%s", diff)
			}

			var got FtErrorPayload
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.input, got); diff != "" {
				t.Errorf("Unmarshal round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFTDonePayloadJSON(t *testing.T) {
	tests := map[string]struct {
		input FtDonePayload
		want  string
	}{
		"full_fields": {
			input: FtDonePayload{
				Files:  3,
				Bytes:  4096,
				Errors: 0,
			},
			want: `{"files":3,"bytes":4096,"errors":0}`,
		},
		"with_errors": {
			input: FtDonePayload{
				Files:  5,
				Bytes:  0,
				Errors: 2,
			},
			want: `{"files":5,"bytes":0,"errors":2}`,
		},
		"zero_values": {
			input: FtDonePayload{},
			want:  `{"files":0,"bytes":0,"errors":0}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(tc.input)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, string(data)); diff != "" {
				t.Errorf("Marshal FtDonePayload mismatch (-want +got):\n%s", diff)
			}

			var got FtDonePayload
			err = json.Unmarshal(data, &got)
			require.NoError(t, err)
			if diff := cmp.Diff(tc.input, got); diff != "" {
				t.Errorf("Unmarshal round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── handleFTPush helpers ─────────────────────────────────────────────────
// Rationale: The push handler is the most complex code path in file transfer.
// The host-side simulator reads/writes binary frames and is reused across
// all push subtests.

// hostFrameHelper wraps a net.Conn with frame-level read/write operations
// for the host side of the file transfer protocol.
type hostFrameHelper struct {
	conn net.Conn
	t    *testing.T
}

func (h *hostFrameHelper) writeFrame(typ byte, payload []byte) {
	h.t.Helper()
	err := WriteFTFrame(h.conn, typ, payload)
	require.NoError(h.t, err)
}

func (h *hostFrameHelper) readFrame() (byte, []byte) {
	h.t.Helper()
	typ, payload, err := ReadFTFrame(h.conn)
	require.NoError(h.t, err)
	return typ, payload
}

// runPushAgent starts handleFTPush in a goroutine and calls hostFn with
// the host side of the pipe. The caller provides pushPayloadJSON as the
// first frame's payload that handleFTPush receives.
func runPushAgent(t *testing.T, ctx context.Context, pushPayloadJSON []byte, hostFn func(host *hostFrameHelper)) {
	t.Helper()

	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()

	done := make(chan struct{})
	go func() {
		handleFTPush(ctx, guest, pushPayloadJSON)
		close(done)
	}()

	helper := &hostFrameHelper{conn: host, t: t}
	hostFn(helper)

	select {
	case <-done:
		// handler completed
	case <-time.After(10 * time.Second):
		t.Fatal("handleFTPush did not return within 10s")
	}
}

// pushFile completes one file in the push protocol from the host side.
// It sends the metadata, reads acceptance, streams data chunks + EOS,
// and reads the OK frame.
func pushFile(t *testing.T, host *hostFrameHelper, meta FtMetaPayload, data []byte) {
	t.Helper()

	metaPayload, err := json.Marshal(meta)
	require.NoError(t, err)

	// Send metadata.
	host.writeFrame(FtMeta, metaPayload)

	// Read acceptance.
	ft, acceptPayload := host.readFrame()
	assert.Equal(t, FtMeta, ft, "expected FtMeta accept frame")
	var accept FtMetaPayload
	err = json.Unmarshal(acceptPayload, &accept)
	require.NoError(t, err)
	if !accept.Accepted {
		t.Fatalf("agent did not accept file: %s", meta.Path)
	}

	// Stream data in chunks.
	chunkSize := ftBufferSize
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		host.writeFrame(FtData, data[offset:end])
	}

	// Send EOS (empty FtData).
	host.writeFrame(FtData, nil)

	// Read OK.
	ft, okPayload := host.readFrame()
	assert.Equal(t, FtOK, ft, "expected FtOK frame")
	var okMeta FtMetaPayload
	err = json.Unmarshal(okPayload, &okMeta)
	require.NoError(t, err)
	_ = okMeta
}

// ─── handleFTPush ──────────────────────────────────────────────────────────
// Rationale: handleFTPush is the core file upload path. A bug here means
// files are silently corrupted or not written at all. Each subtest targets
// a specific failure mode.

func TestHandleFTPush_SingleFile(t *testing.T) {
	destDir := t.TempDir()
	content := []byte("hello world, this is a test file\n")
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"test.txt"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		// Read MKDIR acknowledgement.
		ft, mkdirPayload := host.readFrame()
		assert.Equal(t, FtMkdir, ft, "expected FtMkdir frame")
		var mkdirResp map[string]string
		err := json.Unmarshal(mkdirPayload, &mkdirResp)
		require.NoError(t, err)
		assert.Equal(t, destDir, mkdirResp["path"])

		// Push one file.
		pushFile(t, host, FtMetaPayload{
			Path:   "test.txt",
			Size:   int64(len(content)),
			Mode:   0644,
			SHA256: hashHex,
		}, content)

		// Send DONE and read summary.
		host.writeFrame(FtDone, nil)
		ft, donePayload := host.readFrame()
		assert.Equal(t, FtDone, ft, "expected FtDone frame")
		var done FtDonePayload
		err = json.Unmarshal(donePayload, &done)
		require.NoError(t, err)
		assert.Equal(t, 1, done.Files)
		assert.Equal(t, int64(len(content)), done.Bytes)
		assert.Equal(t, 0, done.Errors)
	})

	// Verify the file was written correctly.
	written, err := os.ReadFile(filepath.Join(destDir, "test.txt"))
	require.NoError(t, err, "file must exist at destination")
	if diff := cmp.Diff(string(content), string(written)); diff != "" {
		t.Errorf("file content mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_EmptyFile(t *testing.T) {
	destDir := t.TempDir()
	content := []byte{}

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"empty.txt"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		// Read MKDIR.
		_, _ = host.readFrame()

		pushFile(t, host, FtMetaPayload{
			Path:   "empty.txt",
			Size:   0,
			Mode:   0644,
			SHA256: "",
		}, content)

		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	written, err := os.ReadFile(filepath.Join(destDir, "empty.txt"))
	require.NoError(t, err, "empty file must exist")
	assert.Len(t, written, 0, "empty file must be 0 bytes")
}

func TestHandleFTPush_HashMismatch(t *testing.T) {
	destDir := t.TempDir()
	content := []byte("actual content")
	fakeHash := "0000000000000000000000000000000000000000000000000000000000000000"

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"mismatch.bin"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		_, _ = host.readFrame() // MKDIR

		metaPayload, _ := json.Marshal(FtMetaPayload{
			Path:   "mismatch.bin",
			Size:   int64(len(content)),
			Mode:   0644,
			SHA256: fakeHash,
		})
		host.writeFrame(FtMeta, metaPayload)

		// Read acceptance.
		ft, acceptPayload := host.readFrame()
		require.Equal(t, FtMeta, ft)
		var accept FtMetaPayload
		json.Unmarshal(acceptPayload, &accept)
		require.True(t, accept.Accepted)

		// Send data.
		host.writeFrame(FtData, content)
		host.writeFrame(FtData, nil) // EOS

		// Read error (hash mismatch).
		ft, errPayload := host.readFrame()
		require.Equal(t, FtError, ft, "expected FtError frame on hash mismatch")
		var errResp FtErrorPayload
		json.Unmarshal(errPayload, &errResp)
		assert.Equal(t, "hash_mismatch", errResp.Code)

		// Send DONE to complete.
		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	// Verify the file was removed after hash mismatch.
	_, err = os.Stat(filepath.Join(destDir, "mismatch.bin"))
	assert.True(t, os.IsNotExist(err), "corrupted file must be removed after hash mismatch")
}

func TestHandleFTPush_FileExists(t *testing.T) {
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "existing.txt")
	err := os.WriteFile(destPath, []byte("original content"), 0644)
	require.NoError(t, err)

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"existing.txt"},
		Dest:      destDir,
		Overwrite: false,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		_, _ = host.readFrame() // MKDIR

		host.writeFrame(FtMeta, []byte(`{"path":"existing.txt","size":25,"mode":420}`))

		ft, errPayload := host.readFrame()
		require.Equal(t, FtError, ft, "expected FtError for existing file")
		var errResp FtErrorPayload
		json.Unmarshal(errPayload, &errResp)
		assert.Equal(t, "exists", errResp.Code)

		// Complete the transfer.
		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	// Verify original content preserved.
	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "original content", string(got), "original file must not be overwritten")
}

func TestHandleFTPush_OverwriteTrue(t *testing.T) {
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "overwrite.txt")
	err := os.WriteFile(destPath, []byte("old content"), 0644)
	require.NoError(t, err)

	newContent := []byte("new content that should overwrite")
	hash := sha256.Sum256(newContent)
	hashHex := hex.EncodeToString(hash[:])

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"overwrite.txt"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		_, _ = host.readFrame() // MKDIR

		pushFile(t, host, FtMetaPayload{
			Path:   "overwrite.txt",
			Size:   int64(len(newContent)),
			Mode:   0644,
			SHA256: hashHex,
		}, newContent)

		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	if diff := cmp.Diff(string(newContent), string(got)); diff != "" {
		t.Errorf("file content after overwrite mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_MultipleFiles(t *testing.T) {
	destDir := t.TempDir()

	fileA := []byte("content of file A")
	hashA := sha256.Sum256(fileA)
	hashHexA := hex.EncodeToString(hashA[:])

	fileB := []byte("content of file B is different")
	hashB := sha256.Sum256(fileB)
	hashHexB := hex.EncodeToString(hashB[:])

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"a.txt", "b.txt"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		_, _ = host.readFrame() // MKDIR

		// Push file A.
		pushFile(t, host, FtMetaPayload{
			Path:   "a.txt",
			Size:   int64(len(fileA)),
			Mode:   0644,
			SHA256: hashHexA,
		}, fileA)

		// Push file B.
		pushFile(t, host, FtMetaPayload{
			Path:   "b.txt",
			Size:   int64(len(fileB)),
			Mode:   0644,
			SHA256: hashHexB,
		}, fileB)

		host.writeFrame(FtDone, nil)
		ft, donePayload := host.readFrame()
		assert.Equal(t, FtDone, ft)
		var done FtDonePayload
		json.Unmarshal(donePayload, &done)
		assert.Equal(t, 2, done.Files)
		assert.Equal(t, 0, done.Errors)
	})

	gotA, err := os.ReadFile(filepath.Join(destDir, "a.txt"))
	require.NoError(t, err)
	if diff := cmp.Diff(string(fileA), string(gotA)); diff != "" {
		t.Errorf("file a.txt mismatch (-want +got):\n%s", diff)
	}

	gotB, err := os.ReadFile(filepath.Join(destDir, "b.txt"))
	require.NoError(t, err)
	if diff := cmp.Diff(string(fileB), string(gotB)); diff != "" {
		t.Errorf("file b.txt mismatch (-want +got):\n%s", diff)
	}
}

// TestHandleFTPush_DirSourceMultipleFiles catches the exact bug that caused
// "expected meta/accept frame, got 0x60": push.Paths has 1 element (like
// ["somedir"] from a directory source) but the host sends N META frames
// (one per expanded file). The agent loop must NOT be bounded by push.Paths.
func TestHandleFTPush_DirSourceMultipleFiles(t *testing.T) {
	destDir := t.TempDir()

	fileA := []byte("content of file A")
	hashA := sha256.Sum256(fileA)
	hashHexA := hex.EncodeToString(hashA[:])

	fileB := []byte("content of file B is different")
	hashB := sha256.Sum256(fileB)
	hashHexB := hex.EncodeToString(hashB[:])

	// push.Paths has 1 element ("somedir"), but we send 2 META frames.
	// This simulates what happens when the host walks a directory:
	// push.Paths=["somedir"] but expandSources produces 2 entries.
	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"somedir"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		_, _ = host.readFrame() // MKDIR

		// Push file A (path includes dir prefix "somedir/").
		pushFile(t, host, FtMetaPayload{
			Path:   "somedir/a.txt",
			Size:   int64(len(fileA)),
			Mode:   0644,
			SHA256: hashHexA,
		}, fileA)

		// Push file B (path includes dir prefix "somedir/").
		pushFile(t, host, FtMetaPayload{
			Path:   "somedir/b.txt",
			Size:   int64(len(fileB)),
			Mode:   0644,
			SHA256: hashHexB,
		}, fileB)

		host.writeFrame(FtDone, nil)
		ft, donePayload := host.readFrame()
		assert.Equal(t, FtDone, ft)
		var done FtDonePayload
		json.Unmarshal(donePayload, &done)
		assert.Equal(t, 2, done.Files)
		assert.Equal(t, 0, done.Errors)
	})

	// Verify both files were written at destDir/somedir/* (agent joins dest + meta.Path).
	gotA, err := os.ReadFile(filepath.Join(destDir, "somedir", "a.txt"))
	require.NoError(t, err)
	if diff := cmp.Diff(string(fileA), string(gotA)); diff != "" {
		t.Errorf("file somedir/a.txt mismatch (-want +got):\n%s", diff)
	}

	gotB, err := os.ReadFile(filepath.Join(destDir, "somedir", "b.txt"))
	require.NoError(t, err)
	if diff := cmp.Diff(string(fileB), string(gotB)); diff != "" {
		t.Errorf("file somedir/b.txt mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_AbortOnFtError(t *testing.T) {
	destDir := t.TempDir()

	fileB := []byte("content of file B")
	hashB := sha256.Sum256(fileB)
	hashHexB := hex.EncodeToString(hashB[:])

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"skip.txt", "b.txt"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		_, _ = host.readFrame() // MKDIR

		// Send FtError instead of FtMeta for the first file — agent should skip.
		host.writeFrame(FtError, []byte(`{"code":"skip","message":"skip this file"}`))

		// Push file B normally.
		pushFile(t, host, FtMetaPayload{
			Path:   "b.txt",
			Size:   int64(len(fileB)),
			Mode:   0644,
			SHA256: hashHexB,
		}, fileB)

		host.writeFrame(FtDone, nil)
		ft, donePayload := host.readFrame()
		assert.Equal(t, FtDone, ft)
		var done FtDonePayload
		json.Unmarshal(donePayload, &done)
		assert.Equal(t, 1, done.Files, "only file B should succeed")
		assert.Equal(t, 1, done.Errors, "file A was aborted via FtError")
	})

	// Verify only file B was written.
	_, err = os.Stat(filepath.Join(destDir, "skip.txt"))
	assert.True(t, os.IsNotExist(err), "skipped file must not exist")

	_, err = os.Stat(filepath.Join(destDir, "b.txt"))
	assert.NoError(t, err, "file B must exist")
}

func TestHandleFTPush_ContextCancelled(t *testing.T) {
	destDir := t.TempDir()

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"test.txt"},
		Dest:      destDir,
		Overwrite: true,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()

	done := make(chan struct{})
	go func() {
		handleFTPush(ctx, guest, pushPayload)
		close(done)
	}()

	// Cancel context to signal handler to abort.
	cancel()

	// Close the host connection to unblock any pending read on guest side.
	host.Close()

	select {
	case <-done:
		// handler exited cleanly after cancellation
	case <-time.After(5 * time.Second):
		t.Fatal("handleFTPush did not return within 5s after context cancellation")
	}
}

// ─── handleFTPush — mode detection (stat-based) ───────────────────────────
// Rationale: The agent now decides file vs directory mode via os.Stat on the
// dest path (or trailing /). These tests verify all five code paths.

func TestHandleFTPush_DirModeTrailingSlash(t *testing.T) {
	// Dest = /tmp/dir/ — trailing / triggers dir mode even if dir doesn't exist.
	content := []byte("file in trailing-slash dir\n")
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])

	dest := filepath.Join(t.TempDir(), "subdir") // does not exist yet
	destWithSlash := dest + "/"

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"test.txt"},
		Dest:      destWithSlash,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		// Read MKDIR acknowledgement.
		ft, mkdirPayload := host.readFrame()
		assert.Equal(t, FtMkdir, ft, "expected FtMkdir frame")
		var mkdirResp map[string]string
		err := json.Unmarshal(mkdirPayload, &mkdirResp)
		require.NoError(t, err)
		assert.Equal(t, dest, mkdirResp["path"])

		pushFile(t, host, FtMetaPayload{
			Path:   "test.txt",
			Size:   int64(len(content)),
			Mode:   0644,
			SHA256: hashHex,
		}, content)

		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	// Verify file was written inside the created directory.
	written, err := os.ReadFile(filepath.Join(dest, "test.txt"))
	require.NoError(t, err, "file must exist inside dir created from trailing-slash dest")
	if diff := cmp.Diff(string(content), string(written)); diff != "" {
		t.Errorf("file content mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_DirModeExistingDir(t *testing.T) {
	// Dest = /tmp/dir (no trailing /), but /tmp/dir exists as a directory.
	// Agent should stat, find it's a dir, and write file inside.
	content := []byte("file in existing dir\n")
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])

	dest := t.TempDir() // already exists as directory, no trailing /

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"test.txt"},
		Dest:      dest,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		// Read MKDIR acknowledgement.
		ft, mkdirPayload := host.readFrame()
		assert.Equal(t, FtMkdir, ft, "expected FtMkdir frame")
		var mkdirResp map[string]string
		err := json.Unmarshal(mkdirPayload, &mkdirResp)
		require.NoError(t, err)
		assert.Equal(t, dest, mkdirResp["path"])

		pushFile(t, host, FtMetaPayload{
			Path:   "test.txt",
			Size:   int64(len(content)),
			Mode:   0644,
			SHA256: hashHex,
		}, content)

		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	// Verify file was written inside the existing directory.
	written, err := os.ReadFile(filepath.Join(dest, "test.txt"))
	require.NoError(t, err, "file must exist inside existing dir")
	if diff := cmp.Diff(string(content), string(written)); diff != "" {
		t.Errorf("file content mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_FileModeNewPath(t *testing.T) {
	// Dest = /tmp/newfile.txt — doesn't exist. Agent should use file mode,
	// write to exact path, and create parent dirs.
	content := []byte("new file content\n")
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])

	baseDir := t.TempDir()
	destPath := filepath.Join(baseDir, "sub", "newfile.txt") // doesn't exist, has parent to create

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"ignored_name.txt"},
		Dest:      destPath,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		// Read MKDIR acknowledgement (file mode: creates parent dir).
		ft, mkdirPayload := host.readFrame()
		assert.Equal(t, FtMkdir, ft, "expected FtMkdir frame")
		var mkdirResp map[string]string
		err := json.Unmarshal(mkdirPayload, &mkdirResp)
		require.NoError(t, err)
		assert.Equal(t, filepath.Dir(destPath), mkdirResp["path"])

		pushFile(t, host, FtMetaPayload{
			Path:   "ignored_name.txt",
			Size:   int64(len(content)),
			Mode:   0644,
			SHA256: hashHex,
		}, content)

		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	// Verify file was written at exact destPath (not inside a subdirectory).
	written, err := os.ReadFile(destPath)
	require.NoError(t, err, "file must exist at exact dest path")
	if diff := cmp.Diff(string(content), string(written)); diff != "" {
		t.Errorf("file content mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_FileModeExistingFile(t *testing.T) {
	// Dest = /tmp/existing.txt exists as a file (not dir).
	// Agent should stat, find it's a file, and treat as file mode.
	content := []byte("overwritten content\n")
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])

	baseDir := t.TempDir()
	destPath := filepath.Join(baseDir, "existing.txt")
	err := os.WriteFile(destPath, []byte("original"), 0644)
	require.NoError(t, err)

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"should_be_ignored.txt"},
		Dest:      destPath,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		// Read MKDIR acknowledgement (file mode: creates parent dir).
		ft, mkdirPayload := host.readFrame()
		assert.Equal(t, FtMkdir, ft, "expected FtMkdir frame")
		var mkdirResp map[string]string
		err := json.Unmarshal(mkdirPayload, &mkdirResp)
		require.NoError(t, err)
		assert.Equal(t, filepath.Dir(destPath), mkdirResp["path"])

		pushFile(t, host, FtMetaPayload{
			Path:   "should_be_ignored.txt",
			Size:   int64(len(content)),
			Mode:   0644,
			SHA256: hashHex,
		}, content)

		host.writeFrame(FtDone, nil)
		_, _ = host.readFrame()
	})

	// Verify file was written at exact destPath (overwritten).
	written, err := os.ReadFile(destPath)
	require.NoError(t, err)
	if diff := cmp.Diff(string(content), string(written)); diff != "" {
		t.Errorf("file content mismatch (-want +got):\n%s", diff)
	}
}

func TestHandleFTPush_MultiSourceToFile(t *testing.T) {
	// 2 paths + dest = /tmp/singlefile (not a dir). Agent should return error.
	dest := filepath.Join(t.TempDir(), "singlefile") // doesn't exist, and there are 2 paths

	pushPayload, err := json.Marshal(FtPushPayload{
		Paths:     []string{"a.txt", "b.txt"},
		Dest:      dest,
		Overwrite: true,
	})
	require.NoError(t, err)

	runPushAgent(t, context.Background(), pushPayload, func(host *hostFrameHelper) {
		ft, errPayload := host.readFrame()
		assert.Equal(t, FtError, ft, "expected FtError for multi-source to non-dir")
		var errResp FtErrorPayload
		json.Unmarshal(errPayload, &errResp)
		assert.Equal(t, "not_a_directory", errResp.Code)
	})

	// Verify no files were written.
	_, err = os.Stat(dest)
	assert.True(t, os.IsNotExist(err), "no files should be written after error")
}

// ─── handleFTPull ──────────────────────────────────────────────────────────
// Rationale: handleFTPull reads a file from the VM filesystem and streams it
// to the host. A bug here leaks file contents or blocks the connection.

// runPullAgent starts handleFTPull in a goroutine and calls hostFn with
// the host side of the pipe.
func runPullAgent(t *testing.T, ctx context.Context, pullPayloadJSON []byte, hostFn func(host *hostFrameHelper)) {
	t.Helper()

	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()

	done := make(chan struct{})
	go func() {
		handleFTPull(ctx, guest, pullPayloadJSON)
		close(done)
	}()

	helper := &hostFrameHelper{conn: host, t: t}
	hostFn(helper)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handleFTPull did not return within 10s")
	}
}

func TestHandleFTPull_SingleFile(t *testing.T) {
	srcDir := t.TempDir()
	content := []byte("pull test file content\nwith multiple lines\n")
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])

	srcPath := filepath.Join(srcDir, "pull_source.txt")
	err := os.WriteFile(srcPath, content, 0644)
	require.NoError(t, err)

	pullPayload, err := json.Marshal(FtPullPayload{
		Path:      srcPath,
		Dest:      "/tmp",
		Overwrite: false,
	})
	require.NoError(t, err)

	runPullAgent(t, context.Background(), pullPayload, func(host *hostFrameHelper) {
		// Read META frame with file info.
		ft, metaPayload := host.readFrame()
		assert.Equal(t, FtMeta, ft, "expected FtMeta with file info")
		var meta FtMetaPayload
		err := json.Unmarshal(metaPayload, &meta)
		require.NoError(t, err)
		assert.Equal(t, "pull_source.txt", meta.Path)
		assert.Equal(t, int64(len(content)), meta.Size)
		assert.Equal(t, hashHex, meta.SHA256)
		assert.NotZero(t, meta.Mode)

		// Send acceptance.
		acceptPayload, _ := json.Marshal(FtMetaPayload{Accepted: true})
		host.writeFrame(FtMeta, acceptPayload)

		// Read data frames + progress until EOS.
		var received bytes.Buffer
		for {
			ft, chunk := host.readFrame()
			switch ft {
			case FtData:
				if len(chunk) == 0 {
					// EOS — done reading.
					goto readEOS
				}
				received.Write(chunk)
			case FtProgress:
				// Progress frames are informational.
			default:
				t.Fatalf("unexpected frame type in data stream: 0x%02x", ft)
			}
		}
	readEOS:

		if diff := cmp.Diff(string(content), received.String()); diff != "" {
			t.Errorf("received content mismatch (-want +got):\n%s", diff)
		}

		// Send OK with correct SHA-256 of received content.
		h := sha256.New()
		h.Write(received.Bytes())
		receivedHash := hex.EncodeToString(h.Sum(nil))
		okPayload, _ := json.Marshal(FtMetaPayload{
			Path:   meta.Path,
			Size:   int64(received.Len()),
			SHA256: receivedHash,
		})
		host.writeFrame(FtOK, okPayload)

		// Read DONE.
		ft, donePayload := host.readFrame()
		assert.Equal(t, FtDone, ft, "expected FtDone frame")
		var done FtDonePayload
		json.Unmarshal(donePayload, &done)
		assert.Equal(t, 1, done.Files)
		assert.Equal(t, int64(len(content)), done.Bytes)
		assert.Equal(t, 0, done.Errors)
	})
}

func TestHandleFTPull_FileNotFound(t *testing.T) {
	pullPayload, err := json.Marshal(FtPullPayload{
		Path:      "/nonexistent/path/file.txt",
		Dest:      "/tmp",
		Overwrite: false,
	})
	require.NoError(t, err)

	runPullAgent(t, context.Background(), pullPayload, func(host *hostFrameHelper) {
		ft, errPayload := host.readFrame()
		assert.Equal(t, FtError, ft, "expected FtError for non-existent file")
		var errResp FtErrorPayload
		json.Unmarshal(errPayload, &errResp)
		assert.Equal(t, "not_found", errResp.Code)
	})
}

func TestHandleFTPull_HostRejects(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "rejected.txt")
	err := os.WriteFile(srcPath, []byte("content"), 0644)
	require.NoError(t, err)

	pullPayload, err := json.Marshal(FtPullPayload{
		Path:      srcPath,
		Dest:      "/tmp",
		Overwrite: false,
	})
	require.NoError(t, err)

	runPullAgent(t, context.Background(), pullPayload, func(host *hostFrameHelper) {
		// Read META.
		ft, _ := host.readFrame()
		require.Equal(t, FtMeta, ft)

		// Reject by sending FtError instead of accept.
		host.writeFrame(FtError, []byte(`{"code":"rejected","message":"host rejected file"}`))

		// Agent should log the rejection and return cleanly — no more frames.
		// We read from the conn after a short delay to verify the connection is idle.
		_ = host.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1)
		n, readErr := host.conn.Read(buf)
		if readErr == nil {
			t.Fatalf("expected no data after host rejection, got %d bytes", n)
		}
		// Restore deadline.
		_ = host.conn.SetReadDeadline(time.Time{})
	})
}

func TestHandleFTPull_ContextCancelled(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "cancel_me.txt")
	err := os.WriteFile(srcPath, []byte("content to pull"), 0644)
	require.NoError(t, err)

	pullPayload, err := json.Marshal(FtPullPayload{
		Path:      srcPath,
		Dest:      "/tmp",
		Overwrite: false,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	host, guest := net.Pipe()
	defer host.Close()
	defer guest.Close()

	done := make(chan struct{})
	go func() {
		handleFTPull(ctx, guest, pullPayload)
		close(done)
	}()

	// Cancel context to signal handler to abort.
	cancel()

	// Close the host connection to unblock any pending read on guest side.
	host.Close()

	select {
	case <-done:
		// handler exited cleanly after cancellation
	case <-time.After(5 * time.Second):
		t.Fatal("handleFTPull did not return within 5s after context cancellation")
	}
}
