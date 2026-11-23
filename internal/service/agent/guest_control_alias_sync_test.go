package agent

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"mvmctl/internal/lib/guestcontrolwire"
)

func operatorAliasSnapshot() guestcontrolwire.FloatingIPAliasSnapshot {
	return guestcontrolwire.FloatingIPAliasSnapshot{
		ID: "11111111-1111-4111-8111-111111111111",
		Aliases: []guestcontrolwire.FloatingIPAlias{
			{ResourceID: strings.Repeat("a", 32), IPv4: "10.60.0.100", Generation: 8},
		},
	}
}
func TestOperatorAliasPersistsAcrossSessionLossAndOnlyCompleteSnapshotRemovesIt(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	epoch, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	snapshot := operatorAliasSnapshot()
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), epoch, snapshot))
	installed := aliasRecord(aliases, snapshot.Aliases[0].ResourceID)
	require.Equal(t, guestcontrolwire.Persistent, installed.Lifetime)
	require.Zero(t, installed.Deadline)
	aliases.sessionLost(epoch)
	observed, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, observed.Addresses, 2)
	next, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	observed, err = kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, observed.Addresses, 2)
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), next, snapshot))
	require.Equal(t, next, aliasRecord(aliases, snapshot.Aliases[0].ResourceID).Session)
	empty := guestcontrolwire.FloatingIPAliasSnapshot{
		ID:      "22222222-2222-4222-8222-222222222222",
		Aliases: []guestcontrolwire.FloatingIPAlias{},
	}
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), next, empty))
	observed, err = kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, aliasPrimarySnapshot().Addresses, observed.Addresses)
	require.Zero(t, aliasRecordCount(aliases))
}
func TestOperatorAliasPartialPagesHaveNoEffectsAndRejectSessionSubstitution(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	aliases := aliasTestManager(t, kernel, aliasTestStore(t))
	epoch, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	snapshot := operatorAliasSnapshot()
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), epoch, snapshot))
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	session := newGuestControlSession(left, left)
	session.aliases = aliases
	session.aliasEpoch = epoch
	partial := guestcontrolwire.FloatingIPAliasSnapshot{
		ID:      "33333333-3333-4333-8333-333333333333",
		Aliases: []guestcontrolwire.FloatingIPAlias{},
	}
	for index := 1; index <= 33; index++ {
		partial.Aliases = append(
			partial.Aliases,
			guestcontrolwire.FloatingIPAlias{
				ResourceID: fmt.Sprintf("%032x", index),
				IPv4:       fmt.Sprintf("10.60.0.%d", 100+index),
				Generation: 9,
			},
		)
	}
	digest, err := partial.Digest()
	require.NoError(t, err)
	page := guestcontrolwire.FloatingIPAliasPage{
		SchemaVersion: 1,
		Feature:       "floating_ip_alias_sync",
		SessionID:     "22222222-2222-4222-8222-222222222222",
		SnapshotID:    partial.ID,
		Index:         0,
		Final:         false,
		Digest:        digest,
		Aliases:       partial.Aliases[:32],
	}
	require.NoError(t, page.Validate())
	// receiveAliasPage is reached only after wire validation; a partial snapshot must never remove existing aliases.
	require.NoError(t, session.receiveAliasPage(context.Background(), page))
	observed, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, observed.Addresses, 2)
	page.SessionID = "44444444-4444-4444-8444-444444444444"
	page.Index = 1
	require.Error(t, session.receiveAliasPage(t.Context(), page))
	observed, err = kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Len(t, observed.Addresses, 2)
}
func TestOperatorAliasRecoveryCleansReceiptWithoutReplayingOwnership(t *testing.T) {
	kernel := &aliasTestKernel{state: aliasPrimarySnapshot()}
	store := aliasTestStore(t)
	aliases := aliasTestManager(t, kernel, store)
	epoch, err := aliases.beginSession(t.Context())
	require.NoError(t, err)
	require.NoError(t, aliases.syncOperatorAliases(t.Context(), epoch, operatorAliasSnapshot()))
	recovered := aliasTestManager(t, kernel, store)
	require.NoError(t, recovered.recover(t.Context()))
	observed, err := kernel.snapshot(t.Context())
	require.NoError(t, err)
	require.Equal(t, aliasPrimarySnapshot().Addresses, observed.Addresses)
	require.Zero(t, aliasRecordCount(recovered))
}
