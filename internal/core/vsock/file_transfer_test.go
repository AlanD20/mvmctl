// Package vsock tests internal (unexported) functions directly because
// readFTFrame and writeFTFrame are unexported. These are intentionally
// duplicated between vsock and vsockagent — both copies must be tested.
package vsock

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/service/vsockagent"
)

// ─── readFTFrame / writeFTFrame ─────────────────────────────────────────────
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

// ─── Frame protocol exchange ───────────────────────────────────────────────
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
