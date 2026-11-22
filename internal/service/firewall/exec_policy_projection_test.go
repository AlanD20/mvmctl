package firewall

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestExecPolicyProjectionPreservesExactGrantAfterLabelRevocation(t *testing.T) {
	intent, _ := labelTrafficTestIntent(t)
	source := intent.Endpoints[0].VMID
	otherWorker := intent.Endpoints[1].VMID
	destination := intent.Endpoints[2].VMID
	foreign := intent.Endpoints[3].VMID
	policy := ExecPolicyIntent{
		OwnerUID: 1000, TargetUser: "alice",
		Source:      VMPolicyEndpoint{Labels: model.VMLabelSelector{"role": "worker"}},
		Destination: VMPolicyEndpoint{Labels: model.VMLabelSelector{"role": "controller"}},
		Incarnation: "11111111-1111-4111-8111-111111111111",
	}
	var err error
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	exact := policy
	exact.Source, exact.Destination = VMPolicyEndpoint{VMID: source}, VMPolicyEndpoint{VMID: destination}
	exact.ID, err = exact.identity()
	require.NoError(t, err)
	intent.ExecPolicies = []ExecPolicyIntent{policy, exact}
	require.NoError(t, intent.validateDurablePolicies())

	granted := func(projection execPolicyProjection, from, to, user string) bool {
		data := projection.encodeIndex(1000, true)
		allowed, err := lookupExecPolicyIndex(bytes.NewReader(data), int64(len(data)), 1000, from, to, user)
		require.NoError(t, err)
		return allowed
	}
	projection, err := intent.projectExecPolicies(1000)
	require.NoError(t, err)
	assert.True(t, granted(projection, source, destination, "alice"))
	assert.True(t, granted(projection, otherWorker, destination, "alice"))
	assert.False(t, granted(projection, foreign, destination, "alice"))
	assert.False(t, granted(projection, source, destination, "bob"))
	assert.False(t, granted(projection, destination, source, "alice"))
	assert.Len(t, projection.bitmaps, 1, "the two source rows share identical destinations")

	revised := cloneIntent(intent)
	_, err = revised.VMLabels.replace(1000, destination, 1, model.VMLabels{"role": "worker"})
	require.NoError(t, err)
	require.NoError(t, revised.validateDurablePolicies())
	projection, err = revised.projectExecPolicies(1000)
	require.NoError(t, err)
	assert.True(t, granted(projection, source, destination, "alice"))
	assert.False(t, granted(projection, otherWorker, destination, "alice"))
}

func TestExecPolicyIndexRefusesPendingForeignAndTruncatedAuthority(t *testing.T) {
	intent, _ := labelTrafficTestIntent(t)
	source, destination := intent.Endpoints[0].VMID, intent.Endpoints[2].VMID
	policy := ExecPolicyIntent{
		OwnerUID: 1000, TargetUser: "alice",
		Source: VMPolicyEndpoint{VMID: source}, Destination: VMPolicyEndpoint{VMID: destination},
		Incarnation: "11111111-1111-4111-8111-111111111111",
	}
	var err error
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	intent.ExecPolicies = []ExecPolicyIntent{policy}
	projection, err := intent.projectExecPolicies(1000)
	require.NoError(t, err)

	for _, tc := range []struct {
		name  string
		data  []byte
		owner uint32
		error bool
	}{
		{"pending", projection.encodeIndex(1000, false), 1000, false},
		{"foreign", projection.encodeIndex(1000, true), 1001, true},
		{"truncated", projection.encodeIndex(1000, true)[:execPolicyIndexHeaderSize+1], 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, err := lookupExecPolicyIndex(bytes.NewReader(tc.data), int64(len(tc.data)), tc.owner,
				source, destination, "alice")
			if tc.error {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, allowed)
		})
	}
}

func TestExecPolicyIndexStoreDeniesDuringPublication(t *testing.T) {
	ctx := t.Context()
	store := openTestGenerationRecordStore(t, prepareGenerationRecordTestRoot(t))
	t.Cleanup(func() { require.NoError(t, store.Release(ctx)) })
	intent, _ := labelTrafficTestIntent(t)
	source, destination := intent.Endpoints[0].VMID, intent.Endpoints[2].VMID
	policy := ExecPolicyIntent{
		OwnerUID: 1000, TargetUser: "alice",
		Source: VMPolicyEndpoint{VMID: source}, Destination: VMPolicyEndpoint{VMID: destination},
		Incarnation: "11111111-1111-4111-8111-111111111111",
	}
	var err error
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	intent.ExecPolicies = []ExecPolicyIntent{policy}
	projection, err := intent.projectExecPolicies(1000)
	require.NoError(t, err)
	require.NoError(t, store.writeExecPolicyIndex(ctx, 1000, projection, true))
	verify := func(want bool) {
		t.Helper()
		allowed, err := store.execPolicyAllowed(ctx, 1000, source, destination, "alice")
		require.NoError(t, err)
		assert.Equal(t, want, allowed)
	}
	verify(true)
	require.NoError(t, store.writeExecPolicyIndex(ctx, 1000, execPolicyProjection{}, false))
	verify(false)
	require.NoError(t, store.writeExecPolicyIndex(ctx, 1000, execPolicyProjection{}, true))
	verify(false)
}

func TestExecPolicyIndexRecoverySelectsTheCommittedIntent(t *testing.T) {
	ctx := t.Context()
	store := openTestGenerationRecordStore(t, prepareGenerationRecordTestRoot(t))
	t.Cleanup(func() { require.NoError(t, store.Release(ctx)) })
	before, _ := labelTrafficTestIntent(t)
	source, destination := before.Endpoints[0].VMID, before.Endpoints[2].VMID
	policy := ExecPolicyIntent{
		OwnerUID: 1000, TargetUser: "alice",
		Source:      VMPolicyEndpoint{VMID: source},
		Destination: VMPolicyEndpoint{Labels: model.VMLabelSelector{"role": "controller"}},
		Incarnation: "11111111-1111-4111-8111-111111111111",
	}
	var err error
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	before.ExecPolicies = []ExecPolicyIntent{policy}

	compiled, err := before.compile()
	require.NoError(t, err)
	kernel := &fakeKernelGenerationApplication{observations: []kernelObservation{
		kernelObservationFromCompiled(compiled), kernelObservationFromCompiled(compiled),
	}}
	transaction := &logicalPolicyMutation{store: store, kernel: kernel, before: before, compiled: compiled}
	verify := func(want bool) {
		t.Helper()
		allowed, err := store.execPolicyAllowed(ctx, 1000, source, destination, "alice")
		require.NoError(t, err)
		assert.Equal(t, want, allowed)
	}
	require.NoError(t, store.writeExecPolicyIndex(ctx, 1000, execPolicyProjection{}, false))
	verify(false)
	require.NoError(t, transaction.recoverExecPolicyIndex(ctx, 1000))
	verify(true)

	committed := cloneIntent(before)
	_, err = committed.VMLabels.replace(1000, destination, 1, model.VMLabels{"role": "worker"})
	require.NoError(t, err)
	transaction.before = committed
	require.NoError(t, store.writeExecPolicyIndex(ctx, 1000, execPolicyProjection{}, false))
	require.NoError(t, transaction.recoverExecPolicyIndex(ctx, 1000))
	verify(false)
}
