package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/guestcontrolauth"
	"mvmctl/internal/lib/guestcontrolwire"
)

func TestGuestControlUnprivilegedPeerProcess(t *testing.T) {
	path := os.Getenv("MVM_GUEST_CONTROL_PEER_TEST")
	if path == "" {
		return
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	require.NoError(t, err)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	require.NoError(t, conn.SetDeadline(deadline))
	request := controlTestRequest(guestcontrolwire.Inspect)
	if err := guestcontrolwire.EncodeRequest(ctx, conn, request); err != nil {
		require.True(t, errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET),
			"root gate must reject a valid frame")
		return
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		require.True(t, errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EPIPE))
		return
	}
	_, err = guestcontrolwire.DecodeResponse(ctx, conn, request)
	require.True(t, errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET),
		"UID65534 must not receive a response to its valid delegated operation")

}

func TestGuestControlRootPeerGateRejectsUnprivilegedUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root credential switching requires approved root QA")
	}
	hostMaterial, material, err := guestcontrolauth.Generate(t.Context())
	require.NoError(t, err)
	host, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// Abstract Unix sockets deliberately provide no filesystem gate, isolating the mandatory SO_PEERCRED check.
	path := "@mvm-agent-peer-" + uuid.NewString()
	local, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	gateway, err := NewGuestControlGateway(host, local, material)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- gateway.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	raw, err := net.Dial("tcp", host.Addr().String())
	require.NoError(t, err)
	secured, err := guestcontrolauth.AuthenticateAgent(ctx, raw, hostMaterial)
	require.NoError(t, err)
	defer secured.Close()
	require.Eventually(t, func() bool {
		gateway.mu.Lock()
		defer gateway.mu.Unlock()
		return gateway.session != nil
	}, time.Second, time.Millisecond)
	forwarded := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		request, err := guestcontrolwire.DecodeRequest(ctx, secured)
		if err == nil {
			forwarded <- struct{}{}
			_ = guestcontrolwire.EncodeResponse(ctx, secured, request, controlTestResponse(request))
		}
	}()
	defer func() { cancel(); _ = secured.Close(); <-workerDone }()
	command := exec.CommandContext(ctx, "/proc/self/exe", "-test.run=^TestGuestControlUnprivilegedPeerProcess$")
	command.Env = append(os.Environ(), "MVM_GUEST_CONTROL_PEER_TEST="+path)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	output, err := command.CombinedOutput()
	require.NoError(t, err, "unprivileged peer process: %s", output)
	select {
	case <-forwarded:
		t.Fatal("UID65534 operation crossed the local root gate")
	default:
	}
}
