package console

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelayIOForwardsVMOutputToClient(t *testing.T) {
	vmR, vmW, err := os.Pipe()
	require.NoError(t, err)
	defer vmR.Close()
	defer vmW.Close()

	tmpDir := t.TempDir()
	// Unix socket paths are short; keep this path independent of the checkout and TMPDIR length.
	socketDir, err := os.MkdirTemp("/tmp", "mvm-console-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDir)) })
	sockPath := filepath.Join(socketDir, "console.sock")
	listener, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	unixListener := listener.(*net.UnixListener)
	defer unixListener.Close()

	logPath := filepath.Join(tmpDir, "console.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	defer logFile.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- runRelayIO(ctx, vmR, logFile, unixListener)
	}()

	time.Sleep(200 * time.Millisecond)

	select {
	case err := <-relayDone:
		t.Fatalf("Relay exited early: %v", err)
	default:
	}

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Send initial window size header (magic "MVM" + version 1 + 24 rows × 80 cols).
	var ws [wsHeaderSize]byte
	copy(ws[:3], wsMagic)
	ws[3] = wsVersion
	binary.LittleEndian.PutUint16(ws[4:6], uint16(24)) // rows
	binary.LittleEndian.PutUint16(ws[6:8], uint16(80)) // cols
	if _, err := client.Write(ws[:]); err != nil {
		t.Fatalf("Failed to send window size: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	// VM → Client
	_, err = vmW.Write([]byte("output\n"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, client.SetReadDeadline(time.Now().Add(500*time.Millisecond)))
	buf := make([]byte, 4096)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatalf("VM→Client: %v", err)
	}
	if string(buf[:n]) != "output\n" {
		t.Fatalf("got %q, want %q", string(buf[:n]), "output\n")
	}

	vmW.Close()
	vmR.Close()
	cancel()
	require.NoError(t, <-relayDone)
}
