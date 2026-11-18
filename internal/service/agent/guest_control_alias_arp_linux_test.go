package agent

import (
	"context"
	"encoding/hex"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

func announcementFixture() (guestControlAliasLink, guestControlAliasAddress) {
	return guestControlAliasLink{
			Index: 2,
			Name:  "eth0",
			MAC:   [6]byte{2, 0xfc, 0x33, 0x8a, 0xda, 0xad},
			Flags: unix.IFF_UP,
		},
		guestControlAliasAddress{
			Index:   2,
			IP:      netip.MustParseAddr("10.143.16.100"),
			Prefix:  32,
			Scope:   unix.RT_SCOPE_UNIVERSE,
			Flags:   unix.IFA_F_PERMANENT | unix.IFA_F_NOPREFIXROUTE,
			Label:   "eth0:12345678",
			Created: 7,
		}
}
func TestGuestControlAliasAnnouncementEncodesIndependentEthernetARPFixture(t *testing.T) {
	link, address := announcementFixture()
	frame, err := marshalGuestControlAliasAnnouncement(link, address)
	require.NoError(t, err)
	// RFC 826 Ethernet/IPv4 request: broadcast Ethernet; sender and target protocol addresses are the VIP.
	expected, err := hex.DecodeString(
		"ffffffffffff02fc338adaad0806000108000604000102fc338adaad0a8f10640000000000000a8f1064",
	)
	require.NoError(t, err)
	require.Equal(t, expected, frame)
}
func TestGuestControlAliasAnnouncementRefusesUnownedOrChangedNICAndAddress(t *testing.T) {
	for name, mutate := range map[string]func(*guestControlAliasLink, *guestControlAliasAddress){
		"down":           func(link *guestControlAliasLink, _ *guestControlAliasAddress) { link.Flags = 0 },
		"loopback":       func(link *guestControlAliasLink, _ *guestControlAliasAddress) { link.Flags |= unix.IFF_LOOPBACK },
		"multicast MAC":  func(link *guestControlAliasLink, _ *guestControlAliasAddress) { link.MAC[0] = 1 },
		"zero MAC":       func(link *guestControlAliasLink, _ *guestControlAliasAddress) { link.MAC = [6]byte{} },
		"wrong NIC":      func(_ *guestControlAliasLink, address *guestControlAliasAddress) { address.Index++ },
		"primary prefix": func(_ *guestControlAliasLink, address *guestControlAliasAddress) { address.Prefix = 24 },
		"public IPv4": func(_ *guestControlAliasLink, address *guestControlAliasAddress) {
			address.IP = netip.MustParseAddr("8.8.8.8")
		},
		"IPv6": func(_ *guestControlAliasLink, address *guestControlAliasAddress) {
			address.IP = netip.MustParseAddr("fd00::1")
		},
		"unowned label": func(_ *guestControlAliasLink, address *guestControlAliasAddress) { address.Label = "eth0" },
		"route-producing alias": func(_ *guestControlAliasLink, address *guestControlAliasAddress) {
			address.Flags &^= unix.IFA_F_NOPREFIXROUTE
		},
	} {
		t.Run(name, func(t *testing.T) {
			link, address := announcementFixture()
			mutate(&link, &address)
			_, err := marshalGuestControlAliasAnnouncement(link, address)
			require.Error(t, err)
		})
	}
	t.Run("nil context", func(t *testing.T) {
		link, address := announcementFixture()
		var kernel guestControlAliasNetlink
		require.Error(t, kernel.announceIPv4(nil, link, address))
	})
	t.Run("cancelled before effects", func(t *testing.T) {
		link, address := announcementFixture()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var kernel guestControlAliasNetlink
		require.ErrorIs(t, kernel.announceIPv4(ctx, link, address), context.Canceled)
	})
}
func TestGuestControlAliasFiniteAnnouncementFailureRetainsReceiptAndFreshRenewRetries(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot(), failAnnounce: true}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 10000)
	started := aliasNow(t)
	require.ErrorContains(
		t,
		aliases.reconcile(t.Context(), session, started, request, response),
		"announcement failure",
	)
	record := aliasRecord(aliases, request.Input.ResourceID)
	require.True(t, record.Installed)
	require.Equal(t, started+uint64(10*time.Second), record.Deadline)
	require.Equal(t, 1, kernel.announceAttempts)
	require.Empty(t, aliases.announced)
	// A receipt alone and inspection cannot retry an announcement or extend the finite lease.
	inspect := controlTestRequest(guestcontrolwire.Inspect)
	observed := controlTestResponse(inspect)
	observed.Result.Lifetime = guestcontrolwire.Finite
	observed.Result.Ownership = guestcontrolwire.OwnedBySelf
	observed.Result.IPv4 = record.Address.IP.String()
	observed.Result.Generation = record.Generation
	observed.Result.LeaseRemainingMillis = 10000
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), inspect, observed))
	require.Equal(t, 1, kernel.announceAttempts)
	require.Equal(t, record, aliasRecord(aliases, record.Resource))
	kernel.failAnnounce = false
	renew, renewed := aliasSelfRequest(guestcontrolwire.Renew, record.Generation, 10000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), renew, renewed))
	require.Equal(t, 2, kernel.announceAttempts)
	require.Len(t, aliases.announced, 1)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), renew, renewed))
	require.Equal(t, 2, kernel.announceAttempts, "successful exact identity must not broadcast for ordinary renewals")
	current := aliasRecord(aliases, record.Resource)
	require.NoError(t, kernel.remove(t.Context(), current.Address))
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), renew, renewed))
	require.Equal(t, 3, kernel.announceAttempts, "a reinstalled alias has a new address identity and must announce")
}
func TestGuestControlAliasAnnouncementCannotAcknowledgeExpiredFiniteLease(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot(), announceDelay: 250 * time.Millisecond}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 200)
	require.ErrorContains(
		t,
		aliases.reconcile(t.Context(), session, aliasNow(t), request, response),
		"conservative lease deadline",
	)
	require.Equal(t, 1, kernel.announceAttempts)
	require.Empty(t, aliases.announced)
	require.Zero(t, aliasRecordCount(aliases), "expiry must remove only the exact owned alias")
	observed, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, aliasPrimarySnapshot(), observed)
}
func TestOperatorAliasAnnouncementRetryDedupAndNewSession(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot(), failAnnounce: true}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	snapshot := operatorAliasSnapshot()
	require.ErrorContains(t, aliases.syncOperatorAliases(t.Context(), session, snapshot), "announcement failure")
	record := aliasRecord(aliases, snapshot.Aliases[0].ResourceID)
	require.True(t, record.Installed)
	require.Zero(t, record.Deadline)
	require.Equal(t, 1, kernel.announceAttempts)
	require.Empty(t, aliases.announced)
	kernel.failAnnounce = false
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), session, snapshot))
	require.Equal(t, 2, kernel.announceAttempts)
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), session, snapshot))
	require.Equal(t, 2, kernel.announceAttempts)
	aliases.sessionLost(session)
	next, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), next, snapshot))
	require.Equal(t, 3, kernel.announceAttempts)
	empty := snapshot
	empty.Aliases = []guestcontrolwire.FloatingIPAlias{}
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), next, empty))
	require.Empty(t, aliases.announced)
	snapshot.Aliases[0].Generation++
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), next, snapshot))
	require.Equal(t, 4, kernel.announceAttempts)
}

func TestGuestControlAliasAnnouncementNamespaceFrameUsesAdmittedNIC(t *testing.T) {
	kernel := aliasNamespaceKernel(t)
	original, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(original)
	before := aliasNamespaceSetup(t, kernel)
	var link, peer guestControlAliasLink
	for _, candidate := range before.Links {
		if candidate.Name == "gc0" {
			link = candidate
		}
		if candidate.Name == "gc1" {
			peer = candidate
		}
	}
	capture, err := openGuestControlAliasARPSocket(t.Context(), kernel.namespace, peer)
	require.NoError(t, err)
	defer unix.Close(capture)
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), session, operatorAliasSnapshot()))
	poll := []unix.PollFd{{Fd: int32(capture), Events: unix.POLLIN}}
	count, err := unix.Poll(poll, 2000)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the exact veth peer must receive the announcement")
	buffer := make([]byte, 2048)
	received, from, err := unix.Recvfrom(capture, buffer, unix.MSG_DONTWAIT)
	require.NoError(t, err)
	expected, err := hex.DecodeString(
		"ffffffffffff" + hex.EncodeToString(
			link.MAC[:],
		) + "08060001080006040001" + hex.EncodeToString(
			link.MAC[:],
		) + "0a3c0064" + "000000000000" + "0a3c0064",
	)
	require.NoError(t, err)
	require.GreaterOrEqual(t, received, len(expected))
	require.Equal(t, expected, buffer[:len(expected)])
	origin, ok := from.(*unix.SockaddrLinklayer)
	require.True(t, ok)
	require.Equal(t, peer.Index, origin.Ifindex)
	record := aliasRecord(aliases, operatorAliasSnapshot().Aliases[0].ResourceID)
	changed := record.Address
	changed.Created++
	require.ErrorContains(t, kernel.announceIPv4(t.Context(), link, changed), "identity changed")
	_, _, err = unix.Recvfrom(capture, buffer, unix.MSG_DONTWAIT)
	require.ErrorIs(t, err, unix.EAGAIN)
	require.NoError(
		t,
		verifyGuestControlAliasNamespace(original),
		"opening/sending in the pinned namespace must restore the caller",
	)
}
