package firewall

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestGenerationRecordStoreReplaceRequiresExactRecordAndFileEvidence(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	store := openTestGenerationRecordStore(t, root)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	current := testCommittedGenerationRecord(t)
	evidence, err := store.create(t.Context(), current)
	require.NoError(t, err)
	replacement := testApplyingGenerationRecord(t, current)

	tests := map[string]struct {
		record   generationRecord
		evidence generationRecordFileEvidence
	}{
		"record mismatch": {
			record: mustCommittedGenerationRecord(
				t,
				testGenerationRecordBinding(),
				generationRecordIdentity{id: testFirewallGenerationB},
			),
			evidence: evidence,
		},
		"file evidence mismatch": {
			record: current,
			evidence: func() generationRecordFileEvidence {
				result := evidence
				result.inode++
				return result
			}(),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			proof, replaceErr := store.replace(t.Context(), tc.record, tc.evidence, replacement)
			require.Error(t, replaceErr)
			assert.Zero(t, proof)
			got, gotEvidence, found, readErr := store.read(t.Context())
			require.NoError(t, readErr)
			assert.True(t, found)
			if diff := cmp.Diff(current, got, generationRecordCmpOptions...); diff != "" {
				t.Errorf("record changed after rejected replacement (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(
				evidence,
				gotEvidence,
				cmp.AllowUnexported(generationRecordFileEvidence{}),
			); diff != "" {
				t.Errorf("evidence changed after rejected replacement (-want +got):\n%s", diff)
			}
			assertNoGenerationRecordTemps(t, root)
		})
	}
}

func TestGenerationRecordStoreReplacePublishesNewInodeAndExactPayload(t *testing.T) {
	t.Parallel()

	root := prepareGenerationRecordTestRoot(t)
	store := openTestGenerationRecordStore(t, root)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	current := testCommittedGenerationRecord(t)
	oldEvidence, err := store.create(t.Context(), current)
	require.NoError(t, err)
	replacement := testApplyingGenerationRecord(t, current)

	newEvidence, err := store.replace(t.Context(), current, oldEvidence, replacement)
	require.NoError(t, err)
	assert.NotZero(t, newEvidence.inode)
	assert.NotEqual(t, oldEvidence.inode, newEvidence.inode)
	got, gotEvidence, found, err := store.read(t.Context())
	require.NoError(t, err)
	assert.True(t, found)
	if diff := cmp.Diff(replacement, got, generationRecordCmpOptions...); diff != "" {
		t.Errorf("replacement payload mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(newEvidence, gotEvidence, cmp.AllowUnexported(generationRecordFileEvidence{})); diff != "" {
		t.Errorf("replacement evidence mismatch (-want +got):\n%s", diff)
	}
	assertNoGenerationRecordTemps(t, root)
}

// Rationale: Every precommit boundary must preserve the admitted record. Every postrename failure must explicitly
// report replacement, and parent-directory sync is the only durability-uncertain outcome.
func TestGenerationRecordStoreReplaceFaultsHaveClosedOutcomes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject              func(*generationRecordStoreDeps)
		wantReplaced        bool
		wantDurabilityIssue bool
		wantProof           bool
	}{
		"write": {
			inject: func(deps *generationRecordStoreDeps) {
				deps.write = func(context.Context, int, []byte) (int, error) {
					return 0, errors.New("injected replacement write failure")
				}
			},
		},
		"temporary sync": {
			inject: func(deps *generationRecordStoreDeps) {
				deps.fsync = func(context.Context, int) error {
					return errors.New("injected replacement sync failure")
				}
			},
		},
		"temporary close": {
			inject: func(deps *generationRecordStoreDeps) {
				realClose := deps.close
				var calls atomic.Int32
				deps.close = func(ctx context.Context, fd int) error {
					if calls.Add(1) == 1 {
						return errors.Join(
							realClose(ctx, fd),
							errors.New("injected replacement close failure"),
						)
					}
					return realClose(ctx, fd)
				}
			},
		},
		"rename": {
			inject: func(deps *generationRecordStoreDeps) {
				deps.renameReplace = func(context.Context, int, string, int, string) error {
					return errors.New("injected replacement rename failure")
				}
			},
		},
		"parent sync": {
			inject: func(deps *generationRecordStoreDeps) {
				realFsync := deps.fsync
				var calls atomic.Int32
				deps.fsync = func(ctx context.Context, fd int) error {
					if calls.Add(1) == 2 {
						return errors.New("injected replacement parent sync failure")
					}
					return realFsync(ctx, fd)
				}
			},
			wantReplaced:        true,
			wantDurabilityIssue: true,
			wantProof:           true,
		},
		"postrename readback": {
			inject: func(deps *generationRecordStoreDeps) {
				realOpen := deps.openAt2
				var recordOpens atomic.Int32
				deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
					if name == generationRecordName && recordOpens.Add(1) == 2 {
						return -1, errors.New("injected replacement readback failure")
					}
					return realOpen(ctx, parentFD, name, how)
				}
			},
			wantReplaced: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareGenerationRecordTestRoot(t)
			seed := openTestGenerationRecordStore(t, root)
			current := testCommittedGenerationRecord(t)
			evidence, err := seed.create(t.Context(), current)
			require.NoError(t, err)
			require.NoError(t, seed.Release(t.Context()))
			deps := realGenerationRecordStoreDeps()
			tc.inject(&deps)
			store, err := openGenerationRecordStore(t.Context(), deps, testGenerationRecordStorePolicy(root))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
			replacement := testApplyingGenerationRecord(t, current)

			proof, replaceErr := store.replace(t.Context(), current, evidence, replacement)
			require.Error(t, replaceErr)
			domainErr := errs.AsDomainError(replaceErr)
			require.NotNil(t, domainErr)
			assert.Equal(t, tc.wantReplaced, detailBool(domainErr, "record_replaced"))
			assert.Equal(t, tc.wantDurabilityIssue, detailBool(domainErr, "durability_uncertain"))
			assert.Equal(t, tc.wantProof, proof.inode != 0)
			got := readGenerationRecordFixture(t, generationRecordTestPath(root))
			want := current
			if tc.wantReplaced {
				want = replacement
			}
			if diff := cmp.Diff(want, got, generationRecordCmpOptions...); diff != "" {
				t.Errorf("record after fault mismatch (-want +got):\n%s", diff)
			}
			assertNoGenerationRecordTemps(t, root)
		})
	}
}

func TestAnnotateGenerationRecordReplacementPreservesPrimaryIdentityAndDetails(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	sibling := errors.New("joined sibling")
	primary := &errs.DomainError{
		Code: errs.CodeNetworkAlreadyExists, Message: "classified replacement failure", Op: "firewall-replace",
		Entity: "generation-a", Class: errs.ClassConflict, Err: primaryCause,
		Details: map[string]any{"evidence": "original"},
	}
	outer := errors.Join(errors.New("outer replacement wrapper"), primary, sibling)

	annotated := annotateGenerationRecordReplacement(outer, true)
	assert.Equal(t, primary.Code, annotated.Code)
	assert.Equal(t, primary.Message, annotated.Message)
	assert.Equal(t, primary.Op, annotated.Op)
	assert.Equal(t, primary.Entity, annotated.Entity)
	assert.Equal(t, primary.Class, annotated.Class)
	assert.ErrorIs(t, annotated, primary)
	assert.ErrorIs(t, annotated, primaryCause)
	assert.ErrorIs(t, annotated, sibling)
	wantDetails := map[string]any{
		"evidence": "original", "record_replaced": true, "durability_uncertain": true,
	}
	if diff := cmp.Diff(wantDetails, annotated.Details); diff != "" {
		t.Errorf("replacement details mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, map[string]any{"evidence": "original"}, primary.Details)
}

func testApplyingGenerationRecord(t *testing.T, committed generationRecord) generationRecord {
	t.Helper()
	result, err := beginGenerationRecordApplication(
		committed,
		committed.committed,
		generationRecordIdentity{id: testFirewallGenerationB},
	)
	require.NoError(t, err)
	return result
}

func detailBool(domainErr *errs.DomainError, key string) bool {
	value, _ := domainErr.Details[key].(bool)
	return value
}
