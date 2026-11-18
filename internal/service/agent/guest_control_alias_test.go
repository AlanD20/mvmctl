package agent

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

type aliasTestKernel struct {
	mu               sync.Mutex
	state            guestControlAliasSnapshot
	failRemove       bool
	failAnnounce     bool
	announceAttempts int
	announceDelay    time.Duration
	announcements    []guestControlAliasAddress
	created          uint32
}

func (kernel *aliasTestKernel) announceIPv4(
	ctx context.Context,
	link guestControlAliasLink,
	address guestControlAliasAddress,
) error {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	kernel.announceAttempts++
	if kernel.announceDelay > 0 {
		timer := time.NewTimer(kernel.announceDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if kernel.failAnnounce {
		return errors.New("injected alias announcement failure")
	}
	kernel.announcements = append(kernel.announcements, address)
	return nil
}
func (kernel *aliasTestKernel) snapshot(ctx context.Context) (guestControlAliasSnapshot, error) {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return guestControlAliasSnapshot{}, err
	}
	return guestControlAliasSnapshot{Links: append([]guestControlAliasLink{}, kernel.state.Links...),
		Addresses: append([]guestControlAliasAddress{}, kernel.state.Addresses...),
		Routes:    append([]guestControlAliasRoute{}, kernel.state.Routes...),
	}, nil
}
func (kernel *aliasTestKernel) add(ctx context.Context, address guestControlAliasAddress) error {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, existing := range kernel.state.Addresses {
		if existing.Index == address.Index && existing.IP == address.IP {
			return unix.EEXIST
		}
	}
	kernel.created++
	address.Created = kernel.created
	kernel.state.Addresses = append(kernel.state.Addresses, address)
	return nil
}
func (kernel *aliasTestKernel) remove(ctx context.Context, address guestControlAliasAddress) error {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if kernel.failRemove {
		return errors.New("injected exact alias removal failure")
	}
	for index, existing := range kernel.state.Addresses {
		if existing.Index == address.Index && existing.IP == address.IP && existing.Label == address.Label {
			kernel.state.Addresses = append(kernel.state.Addresses[:index], kernel.state.Addresses[index+1:]...)
			return nil
		}
	}
	return unix.ENOENT
}
func aliasPrimarySnapshot() guestControlAliasSnapshot {
	primary := guestControlAliasAddress{Index: 2, IP: netip.MustParseAddr("10.60.0.2"), Prefix: 24,
		Scope: unix.RT_SCOPE_UNIVERSE, Flags: unix.IFA_F_PERMANENT, Label: "gc0", Created: 1}
	return guestControlAliasSnapshot{
		Links: []guestControlAliasLink{
			{Index: 2, Name: "gc0", MAC: [6]byte{2, 0, 0, 0, 0, 2}, Flags: unix.IFF_UP},
		},
		Addresses: []guestControlAliasAddress{primary},
		Routes: []guestControlAliasRoute{
			{Index: 2, Prefix: netip.MustParsePrefix("10.60.0.0/24"), Source: primary.IP},
		},
	}
}
func aliasTestStore(t *testing.T) *guestControlAliasStore {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root-private receipt tests require approved root QA")
	}
	path := t.TempDir()
	require.NoError(t, os.Chmod(path, 0700))
	require.NoError(t, os.Chown(path, 0, 0))
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(fd)
	store, err := openGuestControlAliasStoreAt(fd)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.close()) })
	return store
}
func aliasTestManager(
	t *testing.T, kernel guestControlAliasKernel, store *guestControlAliasStore,
) *guestControlAliases {
	t.Helper()
	aliases, err := newGuestControlAliases(kernel, store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = aliases.close() })
	return aliases
}
func aliasSelfRequest(
	operation guestcontrolwire.Operation, generation uint64, millis uint64,
) (guestcontrolwire.Request, guestcontrolwire.Response) {
	request := controlTestRequest(operation)
	request.Input.ExpectedGeneration = generation
	response := controlTestResponse(request)
	response.Result.IPv4 = "10.60.0.100"
	response.Result.LeaseRemainingMillis = millis
	return request, response
}
func aliasNow(t *testing.T) uint64 {
	t.Helper()
	now, err := guestControlAliasNow()
	require.NoError(t, err)
	return now
}
func aliasRecord(aliases *guestControlAliases, resource string) guestControlAliasReceipt {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	return aliases.records[resource]
}
func aliasRecordCount(aliases *guestControlAliases) int {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	return len(aliases.records)
}
func TestGuestControlAliasOwnershipGenerationAndPrimaryPreservation(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	store := aliasTestStore(t)
	aliases := aliasTestManager(t, kernel, store)
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 1000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	snapshot, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Addresses, 2)
	require.Equal(t, aliasPrimarySnapshot().Addresses[0], snapshot.Addresses[0])
	require.Equal(t, aliasPrimarySnapshot().Routes, snapshot.Routes)
	record := aliasRecord(aliases, request.Input.ResourceID)
	stale := controlTestRequest(guestcontrolwire.Inspect)
	old := controlTestResponse(stale)
	old.Result.Generation = 8
	old.Result.IPv4 = "10.60.0.100"
	require.Error(t, aliases.reconcile(t.Context(), session, aliasNow(t), stale, old))
	require.Equal(t, record, aliasRecord(aliases, record.Resource))
	release := controlTestRequest(guestcontrolwire.Release)
	release.Input.ExpectedGeneration = 9
	released := controlTestResponse(release)
	released.Result.IPv4 = "10.60.0.100"
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), release, released))
	snapshot, err = kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, aliasPrimarySnapshot(), snapshot)
	receipts, err := store.list(t.Context())
	require.NoError(t, err)
	require.Empty(t, receipts)
}
func TestGuestControlAliasInspectCannotCreateOrExtendAndDelayedSelfRefuses(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	inspect := controlTestRequest(guestcontrolwire.Inspect)
	observed := controlTestResponse(inspect)
	observed.Result.Lifetime = guestcontrolwire.Finite
	observed.Result.Ownership = guestcontrolwire.OwnedBySelf
	observed.Result.LeaseRemainingMillis = 1000
	observed.Result.IPv4 = "10.60.0.100"
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), inspect, observed))
	require.Zero(t, aliasRecordCount(aliases))
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 1000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	before := aliasRecord(aliases, request.Input.ResourceID)
	observed.Result.Generation = 9
	observed.Result.LeaseRemainingMillis = 5000
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), inspect, observed))
	require.Equal(t, before.Deadline, aliasRecord(aliases, before.Resource).Deadline)
	delayed := request
	delayed.Input.ResourceID = strings.Repeat("b", 32)
	late := controlTestResponse(delayed)
	late.Result.IPv4 = "10.60.0.101"
	late.Result.LeaseRemainingMillis = 1
	require.Error(t, aliases.reconcile(t.Context(), session, aliasNow(t)-uint64(2*time.Millisecond), delayed, late))
	require.Equal(t, 1, aliasRecordCount(aliases))
}
func TestGuestControlAliasOldSessionAndTimerCannotDeleteNewOwner(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	old, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 1000)
	require.NoError(t, aliases.reconcile(t.Context(), old, aliasNow(t), request, response))
	previous := aliasRecord(aliases, request.Input.ResourceID)
	fresh, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, old, fresh)
	renew, renewed := aliasSelfRequest(guestcontrolwire.Renew, 9, 1000)
	require.NoError(t, aliases.reconcile(t.Context(), fresh, aliasNow(t), renew, renewed))
	current := aliasRecord(aliases, request.Input.ResourceID)
	require.NotEqual(t, previous.Address.Label, current.Address.Label)
	aliases.sessionLost(old)
	aliases.expire(previous)
	require.Equal(t, current, aliasRecord(aliases, current.Resource))
	require.Error(t, aliases.reconcile(t.Context(), old, aliasNow(t), request, response))
	require.NoError(t, aliases.reconcile(t.Context(), fresh, aliasNow(t), renew, renewed))
	renewedReceipt := aliasRecord(aliases, current.Resource)
	require.Greater(t, renewedReceipt.Revision, current.Revision)
	require.Equal(t, current.Address.Label, renewedReceipt.Address.Label)
	aliases.expire(current)
	require.Equal(t, renewedReceipt, aliasRecord(aliases, current.Resource))
}
func TestGuestControlAliasCleanupFailureRetryAndChangedAddressPreservation(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 1000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	kernel.mu.Lock()
	kernel.failRemove = true
	kernel.mu.Unlock()
	aliases.sessionLost(session)
	require.Equal(t, 1, aliasRecordCount(aliases))
	kernel.mu.Lock()
	kernel.failRemove = false
	kernel.mu.Unlock()
	aliases.sessionLost(session)
	require.Zero(t, aliasRecordCount(aliases))
	fresh, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	require.NoError(t, aliases.reconcile(t.Context(), fresh, aliasNow(t), request, response))
	kernel.mu.Lock()
	kernel.state.Addresses[1].Label = "gc0:manual"
	kernel.state.Addresses[1].Created++
	kernel.mu.Unlock()
	aliases.sessionLost(fresh)
	require.Equal(t, 1, aliasRecordCount(aliases))
	snapshot, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, "gc0:manual", snapshot.Addresses[1].Label)
	_, err = aliases.beginSession(t.Context())
	require.Error(t, err, "never adopt/delete replacement while recovering")
}
func TestGuestControlAliasExpiryAndRestartOnlyCleanOwnedReceipts(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	store := aliasTestStore(t)
	aliases := aliasTestManager(t, kernel, store)
	session, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	request, response := aliasSelfRequest(guestcontrolwire.Associate, 8, 200)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	require.Eventually(t, func() bool {
		snapshot, _ := kernel.snapshot(context.Background())
		return len(snapshot.Addresses) == 1
	}, time.Second, time.Millisecond)
	request, response = aliasSelfRequest(guestcontrolwire.Associate, 9, 1000)
	require.NoError(t, aliases.reconcile(t.Context(), session, aliasNow(t), request, response))
	// Model process loss without calling its cleanup; the durable exact receipt survives.
	aliases.mu.Lock()
	for _, timer := range aliases.timers {
		timer.Stop()
	}
	aliases.closed = true
	aliases.mu.Unlock()
	restarted := aliasTestManager(t, kernel, store)
	require.NoError(t, restarted.recover(t.Context()))
	snapshot, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, aliasPrimarySnapshot(), snapshot)
}

func TestGuestControlAliasGatewayConfiguresBeforeLocalSuccessAndDoesNotReplayHost(t *testing.T) {
	fixture := newControlGatewayFixture(t)
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	fixture.gateway.mu.Lock()
	fixture.gateway.aliases = aliases // Bind the production module before host/session admission.
	fixture.gateway.mu.Unlock()
	host := fixture.connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() {
		for index := 0; index < 2; index++ {
			request, err := guestcontrolwire.DecodeRequest(ctx, host)
			if err != nil {
				served <- err
				return
			}
			response := controlTestResponse(request)
			response.Result.IPv4 = "10.60.0.100"
			if index == 1 {
				response.Result.IPv4 = "10.61.0.100"
			}
			if err := guestcontrolwire.EncodeResponse(ctx, host, request, response); err != nil {
				served <- err
				return
			}
		}
		served <- nil
	}()
	first := controlTestRequest(guestcontrolwire.Associate)
	result, err := controlLocalExchange(ctx, fixture.local, first)
	require.NoError(t, err)
	require.NotNil(t, result.Result)
	snapshot, err := kernel.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Addresses, 2)
	second := controlTestRequest(guestcontrolwire.Associate)
	second.Input.ResourceID = strings.Repeat("b", 32)
	result, err = controlLocalExchange(ctx, fixture.local, second)
	require.NoError(t, err)
	require.NotNil(t, result.Failure)
	require.Equal(t, guestcontrolwire.FailureInternal, result.Failure.Code)
	require.NoError(t, <-served, "configuration failure must not replay a host mutation")
	snapshot, err = kernel.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Addresses, 2)
}

func TestGuestControlAliasUnfinishedCandidateRefusesUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"foreign_name", "symlink", "hardlink", "foreign_owner", "unsafe_mode"} {
		t.Run(kind, func(t *testing.T) {
			store := aliasTestStore(t)
			name := ".b524a876-14b5-4452-bff7-1adb35d65702.tmp"
			if kind == "foreign_name" {
				name = ".unknown.tmp"
			}
			if kind == "symlink" {
				require.NoError(t, unix.Symlinkat("unrelated", store.directory, name))
			} else {
				fd, err := unix.Openat(store.directory, name,
					unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
				require.NoError(t, err)
				_, err = unix.Write(fd, []byte("partial receipt"))
				require.NoError(t, err)
				if kind == "foreign_owner" {
					require.NoError(t, unix.Fchown(fd, 65534, 65534))
				}
				if kind == "unsafe_mode" {
					require.NoError(t, unix.Fchmod(fd, 0644))
				}
				require.NoError(t, unix.Close(fd))
				if kind == "hardlink" {
					require.NoError(t, unix.Linkat(store.directory, name, store.directory, "unrelated", 0))
				}
			}
			var before, after unix.Stat_t
			require.NoError(t, unix.Fstatat(store.directory, name, &before, unix.AT_SYMLINK_NOFOLLOW))
			_, err := store.list(t.Context())
			require.Error(t, err)
			require.NoError(t, unix.Fstatat(store.directory, name, &after, unix.AT_SYMLINK_NOFOLLOW))
			require.True(t, sameGuestControlAliasFile(before, after), "unsafe candidate must remain untouched")
		})
	}
}
