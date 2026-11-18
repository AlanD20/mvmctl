package guestcontrolwire_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"mvmctl/internal/lib/guestcontrolwire"
)

func aliasSnapshot() guestcontrolwire.FloatingIPAliasSnapshot {
	return guestcontrolwire.FloatingIPAliasSnapshot{
		ID: "11111111-1111-4111-8111-111111111111",
		Aliases: []guestcontrolwire.FloatingIPAlias{
			{ResourceID: strings.Repeat("a", 32), IPv4: "10.60.0.100", Generation: 7},
		},
	}
}
func TestAliasSnapshotDigestBindsResourceGenerationAndAddress(t *testing.T) {
	snapshot := aliasSnapshot()
	original, err := snapshot.Digest()
	require.NoError(t, err)
	snapshot.ID = "22222222-2222-4222-8222-222222222222"
	independent, err := snapshot.Digest()
	require.NoError(t, err)
	require.Equal(t, original, independent, "snapshot request identity does not change the authority projection")
	for _, field := range []string{"generation", "resource", "address"} {
		t.Run(field, func(t *testing.T) {
			changed := aliasSnapshot()
			switch field {
			case "generation":
				changed.Aliases[0].Generation++
			case "resource":
				changed.Aliases[0].ResourceID = strings.Repeat("b", 32)
			case "address":
				changed.Aliases[0].IPv4 = "10.60.0.101"
			}
			digest, err := changed.Digest()
			require.NoError(t, err)
			require.NotEqual(t, original, digest)
		})
	}
}
func TestAliasCodecDemuxAndClosedFields(t *testing.T) {
	snapshot := aliasSnapshot()
	digest, err := snapshot.Digest()
	require.NoError(t, err)
	page := guestcontrolwire.FloatingIPAliasPage{
		SchemaVersion: 1,
		Feature:       "floating_ip_alias_sync",
		SessionID:     "22222222-2222-4222-8222-222222222222",
		SnapshotID:    snapshot.ID,
		Final:         true,
		Digest:        digest,
		Aliases:       snapshot.Aliases,
	}
	var stream bytes.Buffer
	require.NoError(t, guestcontrolwire.EncodeFloatingIPAliasPage(t.Context(), &stream, page))
	raw := append([]byte{}, stream.Bytes()...)
	response, decoded, err := guestcontrolwire.DecodeHostMessage(t.Context(), &stream, nil)
	require.NoError(t, err)
	require.Nil(t, response)
	if diff := cmp.Diff(&page, decoded); diff != "" {
		t.Fatalf("page (-want +got):\n%s", diff)
	}
	for _, rewrite := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"generation":7`, `"generation":6,"generation":7`, 1) },
		func(s string) string {
			return strings.Replace(s, `"aliases":`, `"command":"ip addr add","aliases":`, 1)
		},
		func(s string) string { return strings.Replace(s, `"final":true`, `"Final":true`, 1) },
		func(s string) string { return strings.Replace(s, `"aliases":[`, `"aliases":null,"old":[`, 1) },
	} {
		// Frame helper uses the independently edited JSON body; decoder must refuse before alias effects.
		body := rewrite(string(raw[14:]))
		_, _, err := guestcontrolwire.DecodeHostMessage(context.Background(), bytes.NewReader(frame(body)), nil)
		require.Error(t, err)
	}
	ack := guestcontrolwire.FloatingIPAliasAck{
		SchemaVersion: 1,
		Feature:       "floating_ip_alias_ack",
		SessionID:     page.SessionID,
		SnapshotID:    page.SnapshotID,
		Digest:        page.Digest,
		Accepted:      true,
	}
	require.NoError(t, guestcontrolwire.EncodeFloatingIPAliasAck(t.Context(), &stream, ack))
	request, observed, err := guestcontrolwire.DecodeGuestMessage(t.Context(), &stream)
	require.NoError(t, err)
	require.Nil(t, request)
	require.Equal(t, ack, *observed)
}
func TestPersistentInspectionNeverAuthorizesGuestRenewal(t *testing.T) {
	request := responseRequest()
	request.Operation = guestcontrolwire.Inspect
	result := guestcontrolwire.Response{
		RequestID: request.RequestID,
		Operation: request.Operation,
		Result: &guestcontrolwire.FloatingIPResult{
			Lifetime:   guestcontrolwire.Persistent,
			ResourceID: request.Input.ResourceID,
			IPv4:       "10.60.0.100",
			Generation: 7,
			Ownership:  guestcontrolwire.OwnedBySelf,
		},
	}
	require.NoError(t, result.ValidateFor(request))
	request.Operation = guestcontrolwire.Renew
	result.Operation = request.Operation
	require.Error(t, result.ValidateFor(request))
	request.Operation = guestcontrolwire.Associate
	result.Operation = request.Operation
	require.Error(t, result.ValidateFor(request))
}
