package agent

import (
	"encoding/binary"
	"io"
	"net/netip"
	"os"
	"runtime"
	"testing"

	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

func aliasNamespaceKernel(t *testing.T) *guestControlAliasNetlink {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("isolated network namespace needs approved root QA")
	}
	type result struct {
		kernel *guestControlAliasNetlink
		err    error
	}
	ready := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		original, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			ready <- result{err: err}
			runtime.UnlockOSThread()
			return
		}
		defer unix.Close(original)
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			ready <- result{err: err}
			runtime.UnlockOSThread()
			return
		}
		kernel, openErr := newGuestControlAliasNetlink()
		if err := unix.Setns(original, unix.CLONE_NEWNET); err != nil {
			if kernel != nil {
				_ = kernel.close()
			}
			ready <- result{err: err}
			return // Locked thread is retired, never returned contaminated.
		}
		runtime.UnlockOSThread()
		ready <- result{kernel: kernel, err: openErr}
	}()
	received := <-ready
	require.NoError(t, received.err)
	t.Cleanup(func() { require.NoError(t, received.kernel.close()) })
	return received.kernel
}
func aliasNamespaceAttrs(t *testing.T, attributes ...netlink.Attribute) []byte {
	t.Helper()
	encoded, err := netlink.MarshalAttributes(attributes)
	require.NoError(t, err)
	return encoded
}
func aliasNamespaceSetup(t *testing.T, kernel *guestControlAliasNetlink) guestControlAliasSnapshot {
	t.Helper()
	peerName := aliasNamespaceAttrs(t, netlink.Attribute{Type: unix.IFLA_IFNAME, Data: []byte("gc1\x00")})
	peer := append(make([]byte, 16), peerName...)
	data := aliasNamespaceAttrs(t, netlink.Attribute{Type: 1 | unix.NLA_F_NESTED, Data: peer})
	info := aliasNamespaceAttrs(t, netlink.Attribute{Type: unix.IFLA_INFO_KIND, Data: []byte("veth\x00")},
		netlink.Attribute{Type: unix.IFLA_INFO_DATA | unix.NLA_F_NESTED, Data: data})
	attributes := aliasNamespaceAttrs(t, netlink.Attribute{Type: unix.IFLA_IFNAME, Data: []byte("gc0\x00")},
		netlink.Attribute{Type: unix.IFLA_LINKINFO | unix.NLA_F_NESTED, Data: info})
	_, err := kernel.execute(t.Context(), netlink.Message{Header: netlink.Header{Type: unix.RTM_NEWLINK,
		Flags: netlink.Request | netlink.Acknowledge | netlink.Create | netlink.Excl},
		Data: append(make([]byte, 16), attributes...),
	})
	require.NoError(t, err)
	snapshot, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	index := 0
	for _, link := range snapshot.Links {
		if link.Name != "gc0" && link.Name != "gc1" {
			continue
		}
		if link.Name == "gc0" {
			index = link.Index
		}
		header := make([]byte, 16)
		binary.NativeEndian.PutUint32(header[4:8], uint32(link.Index))
		binary.NativeEndian.PutUint32(header[8:12], unix.IFF_UP)
		binary.NativeEndian.PutUint32(header[12:16], unix.IFF_UP)
		_, err := kernel.execute(t.Context(), netlink.Message{Header: netlink.Header{Type: unix.RTM_NEWLINK,
			Flags: netlink.Request | netlink.Acknowledge}, Data: header})
		require.NoError(t, err)
	}
	require.Positive(t, index)
	require.NoError(t, kernel.add(t.Context(), guestControlAliasAddress{Index: index, IP: netip.MustParseAddr("10.60.0.2"),
		Prefix: 24, Scope: unix.RT_SCOPE_UNIVERSE, Flags: unix.IFA_F_PERMANENT, Label: "gc0"}))
	require.NoError(t, kernel.add(t.Context(), guestControlAliasAddress{
		Index: index, IP: netip.MustParseAddr("10.60.0.90"),
		Prefix: 32, Scope: unix.RT_SCOPE_UNIVERSE, Flags: unix.IFA_F_PERMANENT | unix.IFA_F_NOPREFIXROUTE,
		Label: "gc0:other",
	}))
	snapshot, err = kernel.snapshot(t.Context())
	require.NoError(t, err)
	return snapshot
}
func TestGuestControlAliasNamespaceExactAddRemovePreservesPrimaryAndRoutes(t *testing.T) {
	kernel := aliasNamespaceKernel(t)
	before := aliasNamespaceSetup(t, kernel)
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 5000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	installed, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, installed.Addresses, len(before.Addresses)+1)
	require.Equal(t, before.Routes, installed.Routes, "/32 alias must not replace connected routes")
	record := aliasRecord(aliases, request.Input.ResourceID)
	actual, err := ownedGuestControlAlias(installed, record)
	require.NoError(t, err)
	require.NotNil(t, actual)
	aliases.sessionLost(session)
	after, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, before.Addresses, after.Addresses)
	require.Equal(t, before.Routes, after.Routes)
}
func TestGuestControlAliasNamespaceReplacementLabelIsNeverDeleted(t *testing.T) {
	kernel := aliasNamespaceKernel(t)
	before := aliasNamespaceSetup(t, kernel)
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 5000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	record := aliasRecord(aliases, request.Input.ResourceID)
	require.NoError(t, kernel.remove(t.Context(), record.Address))
	foreign := record.Address
	foreign.Label = "gc0:manual"
	require.NoError(t, kernel.add(t.Context(), foreign))
	require.Error(t, kernel.remove(t.Context(), record.Address),
		"kernel deletion must honor exact label, not IP/index alone")
	aliases.sessionLost(session)
	snapshot, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Addresses, len(before.Addresses)+1)
	found := false
	for _, address := range snapshot.Addresses {
		if address.IP == foreign.IP && address.Label == foreign.Label {
			found = true
		}
	}
	require.True(t, found, "unrelated replacement must survive cleanup")
	require.Equal(t, 1, aliasRecordCount(aliases))
	require.Equal(t, before.Routes, snapshot.Routes)
}

func TestGuestControlAliasNamespaceRestartIgnoresUnpublishedCandidates(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "complete"}[complete], func(t *testing.T) {
			kernel := aliasNamespaceKernel(t)
			before := aliasNamespaceSetup(t, kernel)
			store := aliasTestStore(t)
			aliases := aliasTestManager(t, kernel, store)
			session, err := aliases.beginSession(t.Context())
			require.NoError(t, err)
			request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 60000)
			require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
			record := aliasRecord(aliases, request.Input.ResourceID)
			// Crash during renewal: the published receipt still owns the installed alias.
			candidate := record
			candidate.Revision++
			candidate.Deadline++
			raw, err := encodeGuestControlAliasReceipt(candidate)
			require.NoError(t, err)
			if !complete {
				raw = raw[:len(raw)/2]
			}
			name := ".b524a876-14b5-4452-bff7-1adb35d65702.tmp"
			fd, err := unix.Openat(store.directory, name,
				unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
			require.NoError(t, err)
			file := os.NewFile(uintptr(fd), "crashed-renewal-candidate")
			_, err = file.Write(raw)
			require.NoError(t, err)
			require.NoError(t, file.Close())
			aliases.mu.Lock()
			for _, timer := range aliases.timers {
				timer.Stop()
			}
			aliases.closed = true // Process loss without its normal cleanup.
			aliases.mu.Unlock()
			restarted := aliasTestManager(t, kernel, store)
			require.NoError(t, restarted.recover(t.Context()))
			after, err := kernel.snapshot(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after, "restart removes only the published owned alias")
			_, _, found, err := store.read(t.Context(), record.Resource)
			require.NoError(t, err)
			require.False(t, found)
			fd, err = unix.Openat(store.directory, name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
			require.NoError(t, err)
			file = os.NewFile(uintptr(fd), "preserved-candidate")
			got, err := io.ReadAll(file)
			require.NoError(t, err)
			require.NoError(t, file.Close())
			require.Equal(t, raw, got, "unpublished candidate is preserved, never replayed")
			require.NoError(t, restarted.recover(t.Context()))
			after, err = kernel.snapshot(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after, "candidate cannot recreate an alias")
		})
	}
}
