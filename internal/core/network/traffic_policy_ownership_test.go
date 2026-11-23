package network_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// These are receipts supplied to the projection boundary, not fabricated root authority.
// Root grant creation and kernel application require separate real-authority proof.
const trafficOwnerClaim = "11111111-1111-4111-8111-111111111111"
const trafficReplacementClaim = "22222222-2222-4222-8222-222222222222"
const trafficFirstIncarnation = "33333333-3333-4333-8333-333333333333"
const trafficNextIncarnation = "44444444-4444-4444-8444-444444444444"

type trafficProjectionPayload struct {
	ID          string                `db:"id"`
	Source      string                `db:"source"`
	Destination string                `db:"destination_vm_id"`
	Protocol    model.TrafficProtocol `db:"protocol"`
	First       int                   `db:"destination_port_start"`
	Last        int                   `db:"destination_port_end"`
	CreatedAt   string                `db:"created_at"`
	UpdatedAt   string                `db:"updated_at"`
	Claim       string                `db:"claim_id"`
	Incarnation string                `db:"incarnation"`
}

func TestTrafficPolicySQLiteOwnershipProjection(t *testing.T) {
	// Rationale: Root receipts may advance, but projection cannot rewrite a different semantic grant or delete its replacement.
	for _, family := range []struct{ name, table, sourceColumn, source string }{
		{"network", "network_traffic_policies", "source_network_id", "net-a"},
		{"vm", "vm_traffic_policies", "source_vm_id", "vm-a"},
	} {
		t.Run(family.name, func(t *testing.T) {
			ctx := t.Context()
			database, repo := seedTrafficPolicyResources(t)
			initial := trafficProjectionPayload{ID: strings.Repeat("a", 64), Source: family.source, Destination: "vm-b",
				Protocol: model.TrafficProtocolTCP, First: 443, Last: 445,
				CreatedAt: "2026-10-16T00:00:00Z", UpdatedAt: "2026-10-16T00:00:00Z",
				Claim: trafficOwnerClaim, Incarnation: trafficFirstIncarnation}
			project := func(value trafficProjectionPayload) error {
				if family.name == "network" {
					return repo.ProjectNetworkToVM(ctx, &model.NetworkToVMTrafficAllowIntent{
						ID:                   value.ID,
						SourceNetworkID:      value.Source,
						DestinationVMID:      value.Destination,
						Protocol:             value.Protocol,
						DestinationPortStart: value.First,
						DestinationPortEnd:   value.Last,
						CreatedAt:            value.CreatedAt,
						UpdatedAt:            value.UpdatedAt,
						ClaimID:              value.Claim,
						Incarnation:          value.Incarnation,
					})
				}
				return repo.ProjectVMToVM(ctx, &model.VMToVMTrafficAllowIntent{
					ID:                   value.ID,
					SourceVMID:           value.Source,
					DestinationVMID:      value.Destination,
					Protocol:             value.Protocol,
					DestinationPortStart: value.First,
					DestinationPortEnd:   value.Last,
					CreatedAt:            value.CreatedAt,
					UpdatedAt:            value.UpdatedAt,
					ClaimID:              value.Claim,
					Incarnation:          value.Incarnation,
				})
			}
			remove := func(claim, incarnation string) error {
				if family.name == "network" {
					return repo.DeleteOwnedNetworkToVM(ctx, initial.ID, claim, incarnation)
				}
				return repo.DeleteOwnedVMToVM(ctx, initial.ID, claim, incarnation)
			}
			read := func() []trafficProjectionPayload {
				t.Helper()
				var rows []trafficProjectionPayload
				// SQL identifiers come only from this fixed test table, never runtime caller input.
				query := "SELECT id," + family.sourceColumn + " AS source,destination_vm_id,protocol," +
					"destination_port_start,destination_port_end,created_at,updated_at,claim_id,incarnation FROM " + family.table
				require.NoError(t, database.SelectContext(ctx, &rows, query))
				return rows
			}
			require.NoError(t, project(initial))
			if diff := cmp.Diff([]trafficProjectionPayload{initial}, read()); diff != "" {
				t.Errorf("initial projection (-want +got):\n%s", diff)
			}

			conflict := initial
			conflict.Last, conflict.Claim, conflict.Incarnation = 446, trafficReplacementClaim, trafficNextIncarnation
			t.Run("conflicting semantic identity refuses receipt", func(t *testing.T) {
				var domainErr *errs.DomainError
				require.ErrorAs(t, project(conflict), &domainErr)
				assert.Equal(t, errs.CodePolicyAlreadyExists, domainErr.Code)
			})
			if diff := cmp.Diff([]trafficProjectionPayload{initial}, read()); diff != "" {
				t.Errorf("conflict preservation (-want +got):\n%s", diff)
			}

			replacement := initial
			replacement.Claim, replacement.Incarnation = trafficReplacementClaim, trafficNextIncarnation
			replacement.UpdatedAt = "2026-10-16T00:01:00Z"
			require.NoError(t, project(replacement))
			require.NoError(t, project(replacement))
			if diff := cmp.Diff([]trafficProjectionPayload{replacement}, read()); diff != "" {
				t.Errorf("replacement receipt (-want +got):\n%s", diff)
			}

			for _, stale := range []struct{ name, claim, incarnation string }{
				{"foreign pending claim", initial.Claim, ""},
				{"foreign old receipt", initial.Claim, initial.Incarnation},
				{"same claim stale incarnation", replacement.Claim, initial.Incarnation},
			} {
				t.Run(stale.name, func(t *testing.T) {
					require.NoError(t, remove(stale.claim, stale.incarnation))
					if diff := cmp.Diff([]trafficProjectionPayload{replacement}, read()); diff != "" {
						t.Errorf("conditional deletion preservation (-want +got):\n%s", diff)
					}
				})
			}
			require.NoError(t, remove(replacement.Claim, ""))
			if diff := cmp.Diff([]trafficProjectionPayload(nil), read()); diff != "" {
				t.Errorf("pending claim cleanup (-want +got):\n%s", diff)
			}
			require.NoError(t, project(replacement))
			require.NoError(t, remove(replacement.Claim, replacement.Incarnation))
			assert.Empty(t, read(), "matching receipt must remove a live projection")
			require.NoError(t, remove(replacement.Claim, replacement.Incarnation))
			assert.Empty(t, read(), "absent receipt removal remains idempotent")
		})
	}
}
