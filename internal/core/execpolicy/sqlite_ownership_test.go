package execpolicy_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

const execOwnerClaim = "11111111-1111-4111-8111-111111111111"
const execForeignClaim = "22222222-2222-4222-8222-222222222222"

func execOwnershipIntent() *model.VMToVMExecAllowIntent {
	return &model.VMToVMExecAllowIntent{
		ID: strings.Repeat("a", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
		CreatedAt: "2026-10-16T00:00:00Z", UpdatedAt: "2026-10-16T00:00:00Z", ClaimID: execOwnerClaim,
	}
}

func TestExecPolicySQLiteOwnershipContinuity(t *testing.T) {
	// Rationale: A semantic policy ID must not transfer a creation claim across callers or incarnations.
	ctx := t.Context()
	database, repo := seedExecPolicyResources(t)
	input := execOwnershipIntent()
	created, err := repo.EnsureOwned(ctx, input, "")
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, created.Incarnation)

	// Incarnation is generated nondeterministically; the contract requires durable continuity, not a fixed UUID.
	want := *input
	want.Incarnation = created.Incarnation
	var persisted model.VMToVMExecAllowIntent
	require.NoError(t, database.GetContext(ctx, &persisted, "SELECT * FROM exec_policies WHERE id=?", input.ID))
	if diff := cmp.Diff(want, persisted); diff != "" {
		t.Errorf("created policy (-want +got):\n%s", diff)
	}

	for _, tc := range []struct{ name, claim, expected string }{
		{"same claim after lost receipt", execOwnerClaim, ""},
		{"same claim with receipt", execOwnerClaim, created.Incarnation},
		{"foreign claim cannot adopt", execForeignClaim, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := *input
			request.ClaimID = tc.claim
			observed, err := repo.EnsureOwned(ctx, &request, tc.expected)
			require.NoError(t, err)
			if diff := cmp.Diff(&want, observed); diff != "" {
				t.Errorf("observed ownership (-want +got):\n%s", diff)
			}
			var stored model.VMToVMExecAllowIntent
			require.NoError(t, database.GetContext(ctx, &stored, "SELECT * FROM exec_policies WHERE id=?", input.ID))
			if diff := cmp.Diff(want, stored); diff != "" {
				t.Errorf("retained ownership (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecPolicySQLiteOwnershipConditionalRemoval(t *testing.T) {
	// Rationale: Cleanup after interruption may lack a receipt, but cannot remove a different claim or incarnation.
	for _, tc := range []struct {
		name                                       string
		foreign, replace, missing, pending, ensure bool
		wantConflict                               bool
	}{
		{name: "foreign claim preserves grant", foreign: true, wantConflict: true},
		{name: "old receipt preserves replacement", replace: true, wantConflict: true},
		{name: "old receipt ensure refuses replacement", replace: true, ensure: true, wantConflict: true},
		{name: "expected absent grant is not recreated", missing: true, ensure: true, wantConflict: true},
		{name: "pending matching claim removes grant", pending: true},
		{name: "exact receipt removes grant"},
		{name: "missing grant cleanup is idempotent", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			database, repo := seedExecPolicyResources(t)
			input := execOwnershipIntent()
			original, err := repo.EnsureOwned(ctx, input, "")
			require.NoError(t, err)
			preserved := original
			if tc.replace || tc.missing {
				require.NoError(t, repo.DeleteMany(ctx, []string{input.ID}))
				preserved = nil
			}
			if tc.replace {
				preserved, err = repo.EnsureOwned(ctx, input, "")
				require.NoError(t, err)
				require.NotEqual(t, original.Incarnation, preserved.Incarnation)
			}
			claim, incarnation := execOwnerClaim, original.Incarnation
			if tc.foreign {
				claim = execForeignClaim
			}
			if tc.pending {
				incarnation = ""
			}
			t.Run("operation", func(t *testing.T) {
				var operationErr error
				if tc.ensure {
					_, operationErr = repo.EnsureOwned(ctx, input, incarnation)
				} else {
					operationErr = repo.DeleteOwned(ctx, input.ID, claim, incarnation)
				}
				if tc.wantConflict {
					var domainErr *errs.DomainError
					require.ErrorAs(t, operationErr, &domainErr)
					assert.Equal(t, errs.CodePolicyAlreadyExists, domainErr.Code)
					return
				}
				assert.NoError(t, operationErr)
			})
			// Read independently after the rejected mutation: the error alone does not prove preservation.
			var stored []*model.VMToVMExecAllowIntent
			require.NoError(t, database.SelectContext(ctx, &stored, "SELECT * FROM exec_policies WHERE id=?", input.ID))
			var want []*model.VMToVMExecAllowIntent
			if tc.wantConflict && preserved != nil {
				want = []*model.VMToVMExecAllowIntent{preserved}
			}
			if diff := cmp.Diff(want, stored); diff != "" {
				t.Errorf("policy after operation (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecPolicySQLiteOwnershipRollback(t *testing.T) {
	// Rationale: A database failure during owned deletion must leave the original grant usable for retry.
	ctx := t.Context()
	database, repo := seedExecPolicyResources(t)
	input := execOwnershipIntent()
	created, err := repo.EnsureOwned(ctx, input, "")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `CREATE TRIGGER refuse_owned_delete BEFORE DELETE ON exec_policies
 BEGIN SELECT RAISE(ABORT, 'injected deletion failure'); END`)
	require.NoError(t, err)
	t.Run("deletion reports database failure", func(t *testing.T) {
		assert.Error(t, repo.DeleteOwned(ctx, input.ID, input.ClaimID, created.Incarnation))
	})
	var stored model.VMToVMExecAllowIntent
	require.NoError(t, database.GetContext(ctx, &stored, "SELECT * FROM exec_policies WHERE id=?", input.ID))
	if diff := cmp.Diff(created, &stored); diff != "" {
		t.Errorf("grant after rollback (-want +got):\n%s", diff)
	}
	_, err = database.ExecContext(ctx, "DROP TRIGGER refuse_owned_delete")
	require.NoError(t, err)
	require.NoError(t, repo.DeleteOwned(ctx, input.ID, input.ClaimID, created.Incarnation))
	remaining, err := repo.Get(ctx, input.ID)
	require.NoError(t, err)
	assert.Nil(t, remaining)
}
