package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

type guestControlAliases struct {
	mu      sync.Mutex
	kernel  guestControlAliasKernel
	store   *guestControlAliasStore
	bootID  string
	active  string
	records map[string]guestControlAliasReceipt
	timers  map[string]*time.Timer
	closed  bool
}

func newGuestControlAliases(
	kernel guestControlAliasKernel, store *guestControlAliasStore,
) (*guestControlAliases, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	return &guestControlAliases{kernel: kernel, store: store, bootID: strings.TrimSpace(string(boot)),
		records: make(map[string]guestControlAliasReceipt), timers: make(map[string]*time.Timer)}, nil
}
func guestControlAliasNow() (uint64, error) {
	var clock unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &clock); err != nil {
		return 0, err
	}
	if clock.Sec < 0 {
		return 0, errors.New("invalid guest boottime")
	}
	return uint64(clock.Nano()), nil
}
func (aliases *guestControlAliases) beginSession(ctx context.Context) (string, error) {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	if aliases.closed {
		return "", errors.New("guest alias manager is closed")
	}
	if err := aliases.recoverLocked(ctx); err != nil {
		return "", err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	aliases.active = id.String()
	return aliases.active, nil
}
func (aliases *guestControlAliases) recover(ctx context.Context) error {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	return aliases.recoverLocked(ctx)
}
func (aliases *guestControlAliases) recoverLocked(ctx context.Context) error {
	records, err := aliases.store.list(ctx)
	if err != nil {
		return err
	}
	aliases.active = ""
	for _, timer := range aliases.timers {
		timer.Stop()
	}
	aliases.timers = make(map[string]*time.Timer)
	aliases.records = make(map[string]guestControlAliasReceipt, len(records))
	for _, record := range records {
		aliases.records[record.Resource] = record
	}
	for _, record := range records {
		if err := aliases.cleanup(ctx, record); err != nil {
			return err
		}
	}
	return nil
}
func (aliases *guestControlAliases) sessionLost(session string) {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	if aliases.closed {
		return
	}
	if aliases.active == session {
		aliases.active = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), guestControlAliasIOTimeout)
	defer cancel()
	for _, record := range aliases.records {
		if record.Session == session {
			_ = aliases.cleanup(ctx, record)
		}
	}
}
func (aliases *guestControlAliases) close() error {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	if aliases.closed {
		return nil
	}
	aliases.closed = true
	aliases.active = ""
	for _, timer := range aliases.timers {
		timer.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), guestControlAliasIOTimeout)
	defer cancel()
	var result error
	for _, record := range aliases.records {
		result = errors.Join(result, aliases.cleanup(ctx, record))
	}
	return result
}

// reconcile is called while the session request gate is retained, before local success delivery.
// Receipts authorize guest-local cleanup only; they never authorize a host association or renewal.
func (aliases *guestControlAliases) reconcile(
	ctx context.Context, session string, started uint64,
	request guestcontrolwire.Request, response guestcontrolwire.Response,
) error {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	if aliases.closed || aliases.active != session {
		return errors.New("guest alias session is no longer current")
	}
	if err := response.ValidateFor(request); err != nil {
		return err
	}
	current, owned := aliases.records[request.Input.ResourceID]
	if owned && current.Session != session {
		return errors.New("guest alias retains another session receipt")
	}
	if response.Failure != nil {
		if response.Failure.Code == guestcontrolwire.FailureOutcomeUnknown && owned {
			return aliases.cleanup(ctx, current)
		}
		return nil
	}
	result := response.Result
	if owned && result.Generation < current.Generation {
		return errors.New("guest alias result generation regressed")
	}
	now, err := guestControlAliasNow()
	if err != nil {
		return err
	}
	if result.Ownership != guestcontrolwire.OwnedBySelf || request.Operation == guestcontrolwire.Release {
		if owned {
			return aliases.cleanup(ctx, current)
		}
		return nil
	}
	if request.Operation == guestcontrolwire.Inspect {
		if owned && now >= current.Deadline {
			return aliases.cleanup(ctx, current)
		}
		return nil
	}
	duration := result.LeaseRemainingMillis * uint64(time.Millisecond)
	if duration > ^uint64(0)-started || started+duration <= now {
		return aliases.expiredResponse(ctx, current, owned)
	}
	deadline := started + duration
	address, err := netip.ParseAddr(result.IPv4)
	if err != nil {
		return err
	}
	if owned && (current.Generation != result.Generation || current.Address.IP != address) {
		if err := aliases.cleanup(ctx, current); err != nil {
			return err
		}
		owned = false
	}
	if owned {
		snapshot, err := aliases.kernel.snapshot(ctx)
		if err != nil {
			return err
		}
		actual, err := ownedGuestControlAlias(snapshot, current)
		if err != nil {
			return err
		}
		if actual == nil {
			if err := aliases.cleanup(ctx, current); err != nil {
				return err
			}
			owned = false
		} else {
			next := current
			next.Deadline = deadline
			next.Revision++
			if next.Revision == 0 {
				return errors.New("guest alias revision exhausted")
			}
			if err := aliases.store.put(ctx, next, &current); err != nil {
				return err
			}
			aliases.records[next.Resource] = next
			now, err = guestControlAliasNow()
			if err != nil {
				return err
			}
			if now >= next.Deadline {
				return aliases.expiredResponse(ctx, next, true)
			}
			aliases.schedule(next, now)
			return nil
		}
	}
	snapshot, err := aliases.kernel.snapshot(ctx)
	if err != nil {
		return err
	}
	link, primary, err := selectGuestControlAliasNIC(snapshot, address)
	if err != nil {
		return err
	}
	nonce, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	available := 15 - len(link.Name) - 1
	if available < 8 {
		return errors.New("guest NIC name cannot carry an exact alias discriminator")
	}
	label := link.Name + ":" + hex.EncodeToString(nonce[:])[:available]
	for _, existing := range snapshot.Addresses {
		if existing.Index == link.Index && existing.Label == label {
			return errors.New("guest alias nonce collides")
		}
	}
	desired := guestControlAliasAddress{Index: link.Index, IP: address, Prefix: 32, Scope: unix.RT_SCOPE_UNIVERSE,
		Flags: unix.IFA_F_PERMANENT | unix.IFA_F_NOPREFIXROUTE, Label: label}
	record := guestControlAliasReceipt{Schema: 1, Resource: result.ResourceID, Generation: result.Generation, Revision: 1,
		Session: session, BootID: aliases.bootID, Deadline: deadline, Link: link, Primary: primary, Address: desired}
	if err := aliases.store.put(ctx, record, nil); err != nil {
		return err
	}
	aliases.records[record.Resource] = record
	now, err = guestControlAliasNow()
	if err != nil {
		return err
	}
	if now >= record.Deadline {
		return aliases.expiredResponse(ctx, record, true)
	}
	aliases.schedule(record, now)
	if err := aliases.kernel.add(ctx, desired); err != nil {
		return err
	}
	snapshot, err = aliases.kernel.snapshot(ctx)
	if err != nil {
		return err
	}
	actual, err := ownedGuestControlAlias(snapshot, record)
	if err != nil {
		return err
	}
	if actual == nil {
		return errors.New("guest alias absent after add")
	}
	next := record
	next.Installed = true
	next.Address = *actual
	next.Address.Flags = stableGuestControlAliasFlags(actual.Flags)
	next.Revision++
	if err := aliases.store.put(ctx, next, &record); err != nil {
		return err
	}
	aliases.records[next.Resource] = next
	now, err = guestControlAliasNow()
	if err != nil {
		return err
	}
	if now >= next.Deadline {
		return aliases.expiredResponse(ctx, next, true)
	}
	aliases.schedule(next, now)
	return nil
}
func (aliases *guestControlAliases) expiredResponse(
	ctx context.Context, current guestControlAliasReceipt, owned bool,
) error {
	if owned {
		if err := aliases.cleanup(ctx, current); err != nil {
			return err
		}
	}
	return errors.New("guest alias acknowledgement arrived after its conservative lease deadline")
}
func (aliases *guestControlAliases) schedule(record guestControlAliasReceipt, now uint64) {
	if timer := aliases.timers[record.Resource]; timer != nil {
		timer.Stop()
	}
	remaining := time.Duration(record.Deadline - now)
	aliases.timers[record.Resource] = time.AfterFunc(remaining, func() { aliases.expire(record) })
}
func (aliases *guestControlAliases) expire(record guestControlAliasReceipt) {
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	current, found := aliases.records[record.Resource]
	if aliases.closed || !found || current.Session != record.Session || current.Revision != record.Revision ||
		current.Generation != record.Generation {
		return
	}
	now, err := guestControlAliasNow()
	if err != nil {
		return
	}
	if now < current.Deadline {
		aliases.schedule(current, now)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), guestControlAliasIOTimeout)
	defer cancel()
	_ = aliases.cleanup(ctx, current)
}

func (aliases *guestControlAliases) cleanup(ctx context.Context, record guestControlAliasReceipt) error {
	snapshot, err := aliases.kernel.snapshot(ctx)
	if err != nil {
		return err
	}
	actual, err := ownedGuestControlAlias(snapshot, record)
	if err != nil {
		return err
	}
	if actual != nil {
		if record.BootID != aliases.bootID {
			return errors.New("historical guest alias kernel identity cannot authorize deletion")
		}
		if err := aliases.kernel.remove(ctx, *actual); err != nil {
			return err
		}
		remaining, err := aliases.kernel.snapshot(ctx)
		if err != nil {
			return err
		}
		observed, err := ownedGuestControlAlias(remaining, record)
		if err != nil {
			return err
		}
		if observed != nil {
			return errors.New("guest alias retained after removal")
		}
	}
	if err := aliases.store.remove(ctx, record); err != nil {
		return err
	}
	if timer := aliases.timers[record.Resource]; timer != nil {
		timer.Stop()
		delete(aliases.timers, record.Resource)
	}
	delete(aliases.records, record.Resource)
	return nil
}
func stableGuestControlAliasFlags(flags uint32) uint32 {
	return flags &^ uint32(unix.IFA_F_DEPRECATED|unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED|unix.IFA_F_OPTIMISTIC)
}
func selectGuestControlAliasNIC(
	snapshot guestControlAliasSnapshot, address netip.Addr,
) (guestControlAliasLink, guestControlAliasAddress, error) {
	for _, existing := range snapshot.Addresses {
		if existing.IP == address {
			return guestControlAliasLink{}, guestControlAliasAddress{},
				errors.New("guest IPv4 is already configured and unmanaged")
		}
	}
	var chosen guestControlAliasLink
	var primary guestControlAliasAddress
	found := false
	for _, link := range snapshot.Links {
		if link.Name == "" || len(link.Name) > 15 || link.Flags&unix.IFF_UP == 0 || link.Flags&unix.IFF_LOOPBACK != 0 ||
			link.MAC == ([6]byte{}) {
			continue
		}
		for _, candidate := range snapshot.Addresses {
			if candidate.Index != link.Index || candidate.Scope != unix.RT_SCOPE_UNIVERSE ||
				candidate.Flags&unix.IFA_F_SECONDARY != 0 {
				continue
			}
			prefix := netip.PrefixFrom(candidate.IP, int(candidate.Prefix)).Masked()
			if !prefix.Contains(address) {
				continue
			}
			connected := false
			for _, route := range snapshot.Routes {
				if route.Index == link.Index && route.Prefix == prefix && route.Source == candidate.IP {
					connected = true
				}
			}
			if !connected {
				continue
			}
			if found {
				return chosen, primary, errors.New("guest alias NIC/primary admission is ambiguous")
			}
			chosen, primary, found = link, candidate, true
		}
	}
	if !found {
		return chosen, primary, errors.New("guest alias lacks a unique current primary and connected route")
	}
	return chosen, primary, nil
}
func ownedGuestControlAlias(
	snapshot guestControlAliasSnapshot, record guestControlAliasReceipt,
) (*guestControlAliasAddress, error) {
	var actual *guestControlAliasAddress
	for _, address := range snapshot.Addresses {
		if address.Index == record.Address.Index && address.IP == record.Address.IP {
			if actual != nil {
				return nil, errors.New("duplicate guest alias address identity")
			}
			copy := address
			actual = &copy
		} else if address.Label == record.Address.Label {
			return nil, errors.New("guest alias discriminator moved or was replaced")
		}
	}
	if actual == nil {
		return nil, nil
	}
	linkMatches, primaryMatches := false, false
	for _, link := range snapshot.Links {
		if link.Index == record.Link.Index && link.Name == record.Link.Name && link.MAC == record.Link.MAC {
			linkMatches = true
		}
	}
	for _, address := range snapshot.Addresses {
		if address.Index == record.Primary.Index && address.IP == record.Primary.IP &&
			address.Prefix == record.Primary.Prefix &&
			address.Flags&unix.IFA_F_SECONDARY == 0 {
			primaryMatches = true
		}
	}
	if !linkMatches || !primaryMatches || actual.Label != record.Address.Label || actual.Prefix != 32 ||
		actual.Scope != record.Address.Scope ||
		stableGuestControlAliasFlags(actual.Flags) != stableGuestControlAliasFlags(record.Address.Flags) ||
		record.Installed && actual.Created != record.Address.Created {
		return nil, errors.New("guest alias exact identity changed; preserving kernel address")
	}
	return actual, nil
}
