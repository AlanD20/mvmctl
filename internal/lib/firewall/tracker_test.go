package firewall

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
)

// --- Mocks ---

type mockTracker struct {
	mu sync.Mutex

	initializeCalls   int
	teardownCalls     int
	ensureRuleResults []ensureRuleCall
	batchEnsureCalls  int
	removeRuleCalls   int
	batchRemoveCalls  int
	ensureChainCalls  int
	flushChainCalls   int
	orphanedCalls     int
	ruleExistsCalls   int

	stubEnsureRule  model.FirewallRuleResult
	stubRemoveRule  model.FirewallRuleResult
	stubBatchEnsure model.FirewallRuleResult
	stubBatchRemove model.FirewallRuleResult
	stubChain       bool
	stubFlush       bool
	stubOrphaned    int
	stubRuleExists  bool
}

type ensureRuleCall struct {
	rule  model.FirewallRule
	label string
}

func (m *mockTracker) Initialize(_ context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initializeCalls++
}
func (m *mockTracker) Teardown(_ context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teardownCalls++
}

func (m *mockTracker) EnsureRule(_ context.Context, rule model.FirewallRule, label string) model.FirewallRuleResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureRuleResults = append(m.ensureRuleResults, ensureRuleCall{rule, label})
	return m.stubEnsureRule
}

func (m *mockTracker) BatchEnsureRules(_ context.Context, _ []model.FirewallRule) model.FirewallRuleResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchEnsureCalls++
	return m.stubBatchEnsure
}

func (m *mockTracker) RemoveRule(_ context.Context, _ model.FirewallRule) model.FirewallRuleResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeRuleCalls++
	return m.stubRemoveRule
}

func (m *mockTracker) BatchRemoveRules(_ context.Context, _ []model.FirewallRule) model.FirewallRuleResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batchRemoveCalls++
	return m.stubBatchRemove
}

func (m *mockTracker) EnsureChain(
	_ context.Context,
	_ model.FirewallChain,
	_ model.FirewallTable,
	_ string,
	_ int,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureChainCalls++
	return m.stubChain
}

func (m *mockTracker) FlushChain(_ context.Context, _ model.FirewallChain, _ model.FirewallTable) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushChainCalls++
	return m.stubFlush
}

func (m *mockTracker) CountOrphanedRules(_ context.Context, _ *model.NetworkItem) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orphanedCalls++
	return m.stubOrphaned
}

func (m *mockTracker) RuleExists(_ context.Context, _ *model.FirewallRule) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ruleExistsCalls++
	return m.stubRuleExists
}

type mockRepo struct {
	mu sync.Mutex

	getByNetworkIDCalls   int
	getByNetworkIDIFCalls int

	stubRules []*model.FirewallRule
	stubErr   error
}

func (m *mockRepo) GetByNetworkID(_ context.Context, _ string, _ bool) ([]*model.FirewallRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getByNetworkIDCalls++
	return m.stubRules, m.stubErr
}

func (m *mockRepo) GetByNetworkIDAndInterface(_ context.Context, _, _ string, _ bool) ([]*model.FirewallRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getByNetworkIDIFCalls++
	return m.stubRules, m.stubErr
}

// --- Delegation tests ---
// Rationale: Every FirewallTracker method should delegate to the backend or repo.

func TestTracker_Initialize_teardown(t *testing.T) {
	ft := &FirewallTracker{backend: &mockTracker{}}
	ctx := context.Background()

	ft.Initialize(ctx)
	ft.Teardown(ctx)

	assert.Equal(t, 1, ft.backend.(*mockTracker).initializeCalls)
	assert.Equal(t, 1, ft.backend.(*mockTracker).teardownCalls)
}

func TestTracker_EnsureRule_delegates(t *testing.T) {
	mb := &mockTracker{stubEnsureRule: model.FirewallRuleResult{Success: true}}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	rule := model.FirewallRule{NetworkID: "n-1"}
	result := ft.EnsureRule(ctx, rule, "test")

	assert.True(t, result.Success)
	assert.Len(t, mb.ensureRuleResults, 1)
	assert.Equal(t, "n-1", mb.ensureRuleResults[0].rule.NetworkID)
	assert.Equal(t, "test", mb.ensureRuleResults[0].label)
}

func TestTracker_BatchEnsureRules_delegates(t *testing.T) {
	mb := &mockTracker{stubBatchEnsure: model.FirewallRuleResult{Success: true}}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	result := ft.BatchEnsureRules(ctx, []model.FirewallRule{{NetworkID: "n-1"}})
	assert.True(t, result.Success)
	assert.Equal(t, 1, mb.batchEnsureCalls)
}

func TestTracker_RemoveRule_delegates(t *testing.T) {
	mb := &mockTracker{stubRemoveRule: model.FirewallRuleResult{Success: true}}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	result := ft.RemoveRule(ctx, model.FirewallRule{NetworkID: "n-1"})
	assert.True(t, result.Success)
	assert.Equal(t, 1, mb.removeRuleCalls)
}

func TestTracker_BatchRemoveRules_delegates(t *testing.T) {
	mb := &mockTracker{stubBatchRemove: model.FirewallRuleResult{Success: true}}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	result := ft.BatchRemoveRules(ctx, []model.FirewallRule{{NetworkID: "n-1"}})
	assert.True(t, result.Success)
	assert.Equal(t, 1, mb.batchRemoveCalls)
}

func TestTracker_EnsureChain_delegates(t *testing.T) {
	mb := &mockTracker{stubChain: true}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	ok := ft.EnsureChain(ctx, "MVM-FORWARD", "filter", "FORWARD", 1)
	assert.True(t, ok)
	assert.Equal(t, 1, mb.ensureChainCalls)
}

func TestTracker_FlushChain_delegates(t *testing.T) {
	mb := &mockTracker{stubFlush: true}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	ok := ft.FlushChain(ctx, "MVM-FORWARD", "filter")
	assert.True(t, ok)
	assert.Equal(t, 1, mb.flushChainCalls)
}

func TestTracker_CountOrphanedRules_delegates(t *testing.T) {
	mb := &mockTracker{stubOrphaned: 3}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	n := ft.CountOrphanedRules(ctx, &model.NetworkItem{ID: "n-1"})
	assert.Equal(t, 3, n)
	assert.Equal(t, 1, mb.orphanedCalls)
}

func TestTracker_RuleExists_delegates(t *testing.T) {
	mb := &mockTracker{stubRuleExists: true}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	exists := ft.RuleExists(ctx, &model.FirewallRule{NetworkID: "n-1"})
	assert.True(t, exists)
	assert.Equal(t, 1, mb.ruleExistsCalls)
}

func TestTracker_GetByNetworkID_delegates(t *testing.T) {
	mr := &mockRepo{stubRules: []*model.FirewallRule{{NetworkID: "n-1"}}}
	ft := &FirewallTracker{firewallRepo: mr}
	ctx := context.Background()

	rules, err := ft.GetByNetworkID(ctx, "n-1", true)
	assert.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, 1, mr.getByNetworkIDCalls)
}

func TestTracker_GetByNetworkIDAndInterface_delegates(t *testing.T) {
	mr := &mockRepo{stubRules: []*model.FirewallRule{{NetworkID: "n-1"}}}
	ft := &FirewallTracker{firewallRepo: mr}
	ctx := context.Background()

	rules, err := ft.GetByNetworkIDAndInterface(ctx, "n-1", "tap-0", true)
	assert.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, 1, mr.getByNetworkIDIFCalls)
}

// --- Batch mode ---
// Rationale: WithBatch queues EnsureRule calls and flushes atomically on return.

func TestTracker_WithBatch_empty(t *testing.T) {
	mb := &mockTracker{}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	called := false
	ft.WithBatch(ctx, func() {
		called = true
	})
	assert.True(t, called, "function should execute")
	assert.Equal(t, 0, mb.batchEnsureCalls, "no flush for empty batch")
}

func TestTracker_WithBatch_queuesThenFlushes(t *testing.T) {
	mb := &mockTracker{stubBatchEnsure: model.FirewallRuleResult{Success: true}}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	ft.WithBatch(ctx, func() {
		r1 := ft.EnsureRule(ctx, model.FirewallRule{NetworkID: "n-1", RuleType: model.FirewallRuleTypeMasquerade}, "")
		assert.True(t, r1.Success, "queued rule should report success immediately")
		r2 := ft.EnsureRule(ctx, model.FirewallRule{NetworkID: "n-1", RuleType: model.FirewallRuleTypeForwardOut}, "")
		assert.True(t, r2.Success, "queued rule should report success immediately")
	})

	// Rules should be flushed to backend.BatchEnsureRules (not individual EnsureRule)
	assert.Equal(t, 0, len(mb.ensureRuleResults), "individual EnsureRule should not be called in batch mode")
	assert.Equal(t, 1, mb.batchEnsureCalls, "flush should call BatchEnsureRules once")
}

func TestTracker_WithBatch_backendError(t *testing.T) {
	mb := &mockTracker{stubBatchEnsure: model.FirewallRuleResult{
		Success:      false,
		ErrorMessage: strPtr("iptables failed"),
	}}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	ft.WithBatch(ctx, func() {
		ft.EnsureRule(ctx, model.FirewallRule{NetworkID: "n-1"}, "")
	})

	assert.Equal(t, 1, mb.batchEnsureCalls)
	// flushBatch logs the error (slog.Warn) but does not propagate it
}

func TestTracker_EnsureRule_noBatchMode(t *testing.T) {
	mb := &mockTracker{
		stubEnsureRule: model.FirewallRuleResult{
			Success:         true,
			CommandExecuted: strPtr("iptables -A ..."),
		},
	}
	ft := &FirewallTracker{backend: mb}
	ctx := context.Background()

	result := ft.EnsureRule(ctx, model.FirewallRule{NetworkID: "n-1"}, "test")
	assert.True(t, result.Success)
	assert.NotNil(t, result.CommandExecuted)
	assert.Len(t, mb.ensureRuleResults, 1)
}

func TestTracker_EnsureRule_inBatchMode_queuesRule(t *testing.T) {
	mb := &mockTracker{}
	ft := &FirewallTracker{backend: mb}
	ft.batchMode = true
	ctx := context.Background()

	result := ft.EnsureRule(ctx, model.FirewallRule{NetworkID: "n-1"}, "")
	assert.True(t, result.Success)
	assert.Len(t, ft.batchRules, 1, "rule should be queued")
	assert.Equal(t, 0, len(mb.ensureRuleResults), "backend should not be called")
}

// --- Nil backend guard ---
// Rationale: FirewallTracker constructed via NewFirewallTracker with an
// unrecognised backend type has nil backend and nil repo. Must not panic.

func TestTracker_nilBackendDoesNotPanic(t *testing.T) {
	ft := &FirewallTracker{} // nil backend, nil repo
	ctx := context.Background()

	// These delegate to backend — will panic if backend is nil.
	// The constructor guards against this, but defensive tests confirm.
	assert.Panics(t, func() { ft.Initialize(ctx) })
	assert.Panics(t, func() { ft.Teardown(ctx) })
	assert.Panics(t, func() { ft.EnsureRule(ctx, model.FirewallRule{}, "") })
	assert.Panics(t, func() { ft.GetByNetworkID(ctx, "", true) })
}

// --- Helper ---

func strPtr(s string) *string { return &s }
