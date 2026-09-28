package jailer

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// observeEndpoint admits only the record-derived existing runtime directory and
// proves the connected socket belongs to the exact relay. It publishes no files.
func (server *ConsoleServer) observeEndpoint(ctx context.Context, record instanceRecord) (returnErr error) {
	runtime, err := reopenManagedVMRuntime(ctx, server.runtime,
		managedVMRuntimePolicyFromInstanceAuthority(server.authority.policy),
		instanceCaller{uid: record.ownerUID, gid: record.launch.ownerGID}, record.vmID)
	if err != nil {
		return err
	}
	if runtime == nil {
		return consoleAuthorityError(record.vmID, "console runtime directory is absent", nil)
	}
	defer func() {
		// The existing launch detach seam closes descriptors only; observation must
		// never call runtime removal or fixed-leaf cleanup.
		returnErr = appendInstanceOperationError(returnErr, "close observed console runtime",
			runtime.detachCommittedLaunch(context.WithoutCancel(ctx), record))
	}()
	directory := runtime.state.vmDirectoryFD
	var socket unix.Stat_t
	if err := unix.Fstatat(directory, infra.VMConsoleSocketFilename, &socket, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return consoleAuthorityError(record.vmID, "inspect console socket", err)
	}
	if socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Uid != record.ownerUID ||
		socket.Gid != record.launch.ownerGID || socket.Mode&0077 != 0 || socket.Mode&0200 == 0 {
		return consoleAuthorityError(record.vmID, "console socket identity is unsafe", nil)
	}
	path := fmt.Sprintf("/proc/self/fd/%d/%s", directory, infra.VMConsoleSocketFilename)
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return consoleAuthorityError(record.vmID, "connect console endpoint", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close observed console connection", connection.Close())
	}()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return consoleAuthorityError(record.vmID, "console endpoint is not AF_UNIX", nil)
	}
	raw, err := unixConnection.SyscallConn()
	if err != nil {
		return consoleAuthorityError(record.vmID, "inspect console connection", err)
	}
	var peerErr error
	if err := raw.Control(func(fd uintptr) { peerErr = server.observeConsolePeer(ctx, record, int(fd)) }); err != nil {
		return consoleAuthorityError(record.vmID, "observe console peer", err)
	}
	return peerErr
}

func (server *ConsoleServer) observeConsolePeer(ctx context.Context, record instanceRecord, connectionFD int) (
	returnErr error,
) {
	credentials, err := unix.GetsockoptUcred(connectionFD, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return consoleAuthorityError(record.vmID, "read console peer credentials", err)
	}
	if credentials == nil || int(credentials.Pid) != record.launch.relay.locator.pid ||
		credentials.Uid != record.ownerUID || credentials.Gid != record.launch.ownerGID {
		return consoleAuthorityError(record.vmID, "console endpoint peer differs from relay authority", nil)
	}
	fd, err := unix.GetsockoptInt(connectionFD, unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return consoleAuthorityError(record.vmID, "acquire console peer pidfd", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close console peer pidfd", unix.Close(fd))
	}()
	flags, err := server.recovery.descriptorFlags(ctx, fd)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return consoleAuthorityError(record.vmID, "console peer pidfd is not close-on-exec", err)
	}
	relay := recordedConsoleRelay{deps: server.recovery, pidfd: fd}
	return relay.verify(ctx, server.identity, record)
}
