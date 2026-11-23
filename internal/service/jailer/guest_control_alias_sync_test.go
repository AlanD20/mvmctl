package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"mvmctl/internal/lib/guestcontrolwire"
)

func TestGuestControlSessionDemuxesAliasAckWhileGuestRequestIsOutstanding(t *testing.T) {
	session, agent := guestControlSessionFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	received := make(chan error, 1)
	go func() {
		request, err := session.ReceiveRequest(ctx)
		if err == nil && request.Input.ResourceID != strings.Repeat("b", 32) {
			err = instanceValidationError("wrong guest request")
		}
		received <- err
	}()
	done := make(chan error, 1)
	go func() {
		_, page, err := guestcontrolwire.DecodeHostMessage(ctx, agent, nil)
		if err == nil {
			err = guestcontrolwire.EncodeRequest(ctx, agent, guestControlSessionRequest())
		}
		if err == nil {
			err = guestcontrolwire.EncodeFloatingIPAliasAck(
				ctx,
				agent,
				guestcontrolwire.FloatingIPAliasAck{
					SchemaVersion: 1,
					Feature:       "floating_ip_alias_ack",
					SessionID:     page.SessionID,
					SnapshotID:    page.SnapshotID,
					Digest:        page.Digest,
					Accepted:      true,
				},
			)
		}
		done <- err
	}()
	snapshot := guestcontrolwire.FloatingIPAliasSnapshot{
		ID: "33333333-3333-4333-8333-333333333333",
		Aliases: []guestcontrolwire.FloatingIPAlias{
			{ResourceID: strings.Repeat("c", 32), IPv4: "10.60.0.100", Generation: 9},
		},
	}
	require.NoError(t, session.SyncFloatingIPAliases(ctx, snapshot))
	require.NoError(t, <-received)
	require.NoError(t, <-done)
	session.state.mu.Lock()
	confirmed := session.state.aliasConfirmed
	session.state.mu.Unlock()
	require.NotNil(t, confirmed)
	require.Equal(t, snapshot.ID, confirmed.SnapshotID)
	digest, err := snapshot.Digest()
	require.NoError(t, err)
	require.Equal(t, digest, confirmed.Digest)
}
func TestGuestControlAliasSyncCancellationInterruptsUnacknowledgedStream(t *testing.T) {
	session, agent := guestControlSessionFixture(t)
	receiveCtx, stopReceive := context.WithCancel(t.Context())
	defer stopReceive()
	received := make(chan error, 1)
	go func() { _, err := session.ReceiveRequest(receiveCtx); received <- err }()
	ctx, cancel := context.WithCancel(t.Context())
	synced := make(chan error, 1)
	go func() {
		synced <- session.SyncFloatingIPAliases(ctx, guestcontrolwire.FloatingIPAliasSnapshot{ID: "33333333-3333-4333-8333-333333333333", Aliases: []guestcontrolwire.FloatingIPAlias{}})
	}()
	_, page, err := guestcontrolwire.DecodeHostMessage(t.Context(), agent, nil)
	require.NoError(t, err)
	require.True(t, page.Final)
	cancel()
	require.ErrorIs(t, <-synced, context.Canceled)
	require.Error(t, <-received)
	_, err = session.Principal()
	require.Error(t, err)
}
func TestGuestControlAliasAckStoreRoundTripAndGenerationBinding(t *testing.T) {
	store := guestControlWorkerStoreFixture(t)
	record := guestControlWorkerTestRecord()
	require.NoError(t, store.persist(t.Context(), record))
	receipt := GuestControlAliasAcknowledgement{
		Schema:           1,
		Principal:        record.principal(),
		WorkerGeneration: record.Generation,
		SessionID:        "11111111-1111-4111-8111-111111111111",
		SnapshotID:       "22222222-2222-4222-8222-222222222222",
		Digest:           strings.Repeat("a", 64),
	}
	require.NoError(t, store.writeAliasAcknowledgement(t.Context(), receipt))
	got, found, err := store.readAliasAcknowledgement(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt, got)
	stale := receipt
	stale.WorkerGeneration++
	require.Error(t, store.writeAliasAcknowledgement(t.Context(), stale))
	got, found, err = store.readAliasAcknowledgement(t.Context())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, receipt, got)
}

func TestGuestControlAliasCleanupAcknowledgementRequiresExactEmptyRetiredHistory(t *testing.T) {
	record := guestControlWorkerTestRecord()
	record.Phase = "stopped"
	// CONTRACT: the empty authority projection is the canonical JSON array [], independently hashed here.
	emptyDigest := sha256.Sum256([]byte("[]"))
	receipt := GuestControlAliasAcknowledgement{
		Schema:           1,
		Principal:        record.principal(),
		WorkerGeneration: record.Generation,
		SessionID:        "11111111-1111-4111-8111-111111111111",
		SnapshotID:       "22222222-2222-4222-8222-222222222222",
		Digest:           hex.EncodeToString(emptyDigest[:]),
	}
	for _, phase := range []string{"ready", "stopped", "prepared", "spawned", "stopping"} {
		t.Run(phase, func(t *testing.T) {
			candidate := record
			candidate.Phase = phase
			admitted, err := admitFloatingIPAliasCleanupAcknowledgement(candidate, receipt, record.principal())
			require.NoError(t, err)
			require.Equal(t, phase == "ready" || phase == "stopped", admitted)
		})
	}
	for _, field := range []string{"worker generation", "principal", "nonempty digest"} {
		t.Run(field, func(t *testing.T) {
			changed := receipt
			switch field {
			case "worker generation":
				changed.WorkerGeneration++
			case "principal":
				changed.Principal.LaunchGeneration++
			case "nonempty digest":
				changed.Digest = strings.Repeat("a", 64)
			}
			admitted, err := admitFloatingIPAliasCleanupAcknowledgement(record, changed, record.principal())
			require.NoError(t, err)
			require.False(t, admitted)
		})
	}
}
