package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

type guestControlAliasStage struct {
	session  string
	snapshot guestcontrolwire.FloatingIPAliasSnapshot
	digest   string
	next     uint32
}

// receiveAliasPage runs only on the authenticated stream reader. Partial snapshots have no alias effects.
func (session *guestControlSession) receiveAliasPage(
	ctx context.Context,
	page guestcontrolwire.FloatingIPAliasPage,
) error {
	if session.aliases == nil {
		return errors.New("operator alias manager is unavailable")
	}
	if session.aliasSession == "" {
		session.aliasSession = page.SessionID
	}
	if session.aliasSession != page.SessionID {
		return errors.New("operator alias session binding changed")
	}
	if session.aliasStage == nil {
		if page.Index != 0 {
			return errors.New("operator alias snapshot has no first page")
		}
		session.aliasStage = &guestControlAliasStage{
			session: page.SessionID,
			snapshot: guestcontrolwire.FloatingIPAliasSnapshot{
				ID:      page.SnapshotID,
				Aliases: []guestcontrolwire.FloatingIPAlias{},
			},
			digest: page.Digest,
		}
	}
	stage := session.aliasStage
	if page.Index != stage.next || page.SessionID != stage.session || page.SnapshotID != stage.snapshot.ID ||
		page.Digest != stage.digest ||
		stage.next == ^uint32(0) {
		return errors.New("operator alias snapshot pages disagree")
	}
	stage.snapshot.Aliases = append(stage.snapshot.Aliases, page.Aliases...)
	stage.next++
	if !page.Final {
		return session.conn.SetReadDeadline(time.Now().Add(guestControlRequestTimeout))
	}
	digest, err := stage.snapshot.Digest()
	if err != nil || digest != stage.digest {
		return errors.New("operator alias snapshot digest differs")
	}
	if err := session.conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	operation, cancel := context.WithTimeout(ctx, guestControlRequestTimeout)
	defer cancel()
	stop := context.AfterFunc(operation, session.close)
	defer stop()
	applyErr := session.aliases.syncOperatorAliases(operation, session.aliasEpoch, stage.snapshot)
	ack := guestcontrolwire.FloatingIPAliasAck{
		SchemaVersion: 1,
		Feature:       "floating_ip_alias_ack",
		SessionID:     page.SessionID,
		SnapshotID:    page.SnapshotID,
		Digest:        page.Digest,
		Accepted:      applyErr == nil,
	}
	session.aliasStage = nil
	session.writeMu.Lock()
	err = guestcontrolwire.EncodeFloatingIPAliasAck(operation, session.conn, ack)
	session.writeMu.Unlock()
	return err
}

// syncOperatorAliases consumes one complete authenticated root snapshot. Receipts authorize only exact local cleanup.
func (aliases *guestControlAliases) syncOperatorAliases(
	ctx context.Context,
	session string,
	snapshot guestcontrolwire.FloatingIPAliasSnapshot,
) error {
	if err := snapshot.Validate(); err != nil {
		return err
	}
	aliases.mu.Lock()
	defer aliases.mu.Unlock()
	if aliases.closed || aliases.active != session {
		return errors.New("operator alias snapshot session is no longer current")
	}
	desired := make(map[string]guestcontrolwire.FloatingIPAlias, len(snapshot.Aliases))
	for _, alias := range snapshot.Aliases {
		desired[alias.ResourceID] = alias
		if current, found := aliases.records[alias.ResourceID]; found && alias.Generation < current.Generation {
			return errors.New("operator alias generation regressed")
		}
	}
	// Only a complete snapshot may remove stale persistent receipts. Finite guest leases are independent.
	for id, current := range aliases.records {
		target, found := desired[id]
		if current.Lifetime == guestcontrolwire.Persistent &&
			(!found || current.Generation != target.Generation || current.Address.IP.String() != target.IPv4) {
			if err := aliases.cleanup(ctx, current); err != nil {
				return err
			}
		}
	}
	for _, target := range snapshot.Aliases {
		current, found := aliases.records[target.ResourceID]
		if found {
			if current.Lifetime != guestcontrolwire.Persistent {
				if err := aliases.cleanup(ctx, current); err != nil {
					return err
				}
			} else {
				actual, err := aliases.kernel.snapshot(ctx)
				if err != nil {
					return err
				}
				present, err := ownedGuestControlAlias(actual, current)
				if err != nil {
					return err
				}
				if present != nil {
					next := current
					next.Installed = true
					next.Address = *present
					next.Address.Flags = stableGuestControlAliasFlags(present.Flags)
					next.Session = session
					next.Revision++
					if next.Revision == 0 {
						return errors.New("operator alias receipt revision exhausted")
					}
					if err := aliases.store.put(ctx, next, &current); err != nil {
						return err
					}
					aliases.records[next.Resource] = next
					if err := aliases.announceOwnedAlias(ctx, next); err != nil {
						return err
					}
					continue
				}
				if err := aliases.cleanup(ctx, current); err != nil {
					return err
				}
			}
		}
		if err := aliases.installOperatorAlias(ctx, session, target); err != nil {
			return err
		}
	}
	return nil
}

func (aliases *guestControlAliases) installOperatorAlias(
	ctx context.Context,
	session string,
	target guestcontrolwire.FloatingIPAlias,
) error {
	address, err := netip.ParseAddr(target.IPv4)
	if err != nil {
		return err
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
		return errors.New("guest NIC name cannot carry exact operator alias discriminator")
	}
	label := link.Name + ":" + hex.EncodeToString(nonce[:])[:available]
	for _, existing := range snapshot.Addresses {
		if existing.Index == link.Index && existing.Label == label {
			return errors.New("operator alias discriminator collides")
		}
	}
	desired := guestControlAliasAddress{
		Index:  link.Index,
		IP:     address,
		Prefix: 32,
		Scope:  unix.RT_SCOPE_UNIVERSE,
		Flags:  unix.IFA_F_PERMANENT | unix.IFA_F_NOPREFIXROUTE,
		Label:  label,
	}
	record := guestControlAliasReceipt{
		Schema:     2,
		Lifetime:   guestcontrolwire.Persistent,
		Resource:   target.ResourceID,
		Generation: target.Generation,
		Revision:   1,
		Session:    session,
		BootID:     aliases.bootID,
		Link:       link,
		Primary:    primary,
		Address:    desired,
	}
	if err := aliases.store.put(ctx, record, nil); err != nil {
		return err
	}
	aliases.records[record.Resource] = record
	if err := aliases.kernel.add(ctx, desired); err != nil {
		return err
	}
	observed, err := aliases.kernel.snapshot(ctx)
	if err != nil {
		return err
	}
	actual, err := ownedGuestControlAlias(observed, record)
	if err != nil {
		return err
	}
	if actual == nil {
		return errors.New("operator alias is absent after insertion")
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
	return aliases.announceOwnedAlias(ctx, next)
}
