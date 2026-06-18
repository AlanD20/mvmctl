package firewall

import (
	"context"
	"log/slog"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/model"
)

// Tracker interface that both IPTablesTracker and NFTablesTracker implement.
type Tracker interface {
	Initialize(ctx context.Context)
	Teardown(ctx context.Context)
	EnsureRule(ctx context.Context, rule model.FirewallRule, contextLabel string) model.FirewallRuleResult
	BatchEnsureRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult
	RemoveRule(ctx context.Context, rule model.FirewallRule) model.FirewallRuleResult
	BatchRemoveRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult
	EnsureChain(
		ctx context.Context,
		chainName model.FirewallChain,
		table model.FirewallTable,
		autoJumpFrom string,
		position int,
	) bool
	FlushChain(ctx context.Context, chainName model.FirewallChain, table model.FirewallTable) bool
	CountOrphanedRules(ctx context.Context, network *model.NetworkItem) int
}

// firewallRuleRepo is the interface both rule repository backends implement.
type firewallRuleRepo interface {
	GetByNetworkID(ctx context.Context, networkID string, activeOnly bool) ([]*model.FirewallRule, error)
	GetByNetworkIDAndInterface(
		ctx context.Context,
		networkID string,
		iface string,
		activeOnly bool,
	) ([]*model.FirewallRule, error)
}

// --- FirewallTracker (dispatcher) ---

type FirewallTracker struct {
	firewallRepo firewallRuleRepo
	backend      Tracker
	batchMode    bool
	batchRules   []model.FirewallRule
}

func NewFirewallTracker(backend model.FirewallBackendType, xtcommentAvail bool, db *sqlx.DB) *FirewallTracker {
	ft := &FirewallTracker{}
	switch backend {
	case model.FirewallBackendNFTables:
		repo := NewNFTablesRuleRepository(db)
		ft.firewallRepo = repo
		ft.backend = NewNFTablesTracker(repo)
	case model.FirewallBackendIPTables:
		repo := NewIPTablesRuleRepository(db)
		ft.firewallRepo = repo
		ft.backend = NewIPTablesTracker(repo, xtcommentAvail)
	}
	return ft
}

// --- Batch context ---

func (ft *FirewallTracker) flushBatch(ctx context.Context) model.FirewallRuleResult {
	ft.batchMode = false
	if len(ft.batchRules) == 0 {
		return model.FirewallRuleResult{Success: true}
	}
	result := ft.backend.BatchEnsureRules(ctx, ft.batchRules)
	ft.batchRules = ft.batchRules[:0]
	if !result.Success {
		errMsg := ""
		if result.ErrorMessage != nil {
			errMsg = *result.ErrorMessage
		}
		slog.Warn("Batch firewall rule flush failed", "error", errMsg)
	}
	return result
}

func (ft *FirewallTracker) WithBatch(ctx context.Context, fn func()) {
	ft.batchMode = true
	ft.batchRules = ft.batchRules[:0]
	defer ft.flushBatch(ctx)
	fn()
}

// --- Rule lifecycle ---

func (ft *FirewallTracker) EnsureRule(
	ctx context.Context,
	rule model.FirewallRule,
	contextLabel string,
) model.FirewallRuleResult {
	if ft.batchMode {
		ft.batchRules = append(ft.batchRules, rule)
		return model.FirewallRuleResult{Success: true}
	}
	return ft.backend.EnsureRule(ctx, rule, contextLabel)
}

func (ft *FirewallTracker) BatchEnsureRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult {
	return ft.backend.BatchEnsureRules(ctx, rules)
}

func (ft *FirewallTracker) RemoveRule(ctx context.Context, rule model.FirewallRule) model.FirewallRuleResult {
	return ft.backend.RemoveRule(ctx, rule)
}

func (ft *FirewallTracker) BatchRemoveRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult {
	return ft.backend.BatchRemoveRules(ctx, rules)
}

func (ft *FirewallTracker) CountOrphanedRules(ctx context.Context, network *model.NetworkItem) int {
	return ft.backend.CountOrphanedRules(ctx, network)
}

// --- Chain lifecycle ---

func (ft *FirewallTracker) EnsureChain(
	ctx context.Context,
	chainName model.FirewallChain,
	table model.FirewallTable,
	autoJumpFrom string,
	position int,
) bool {
	return ft.backend.EnsureChain(ctx, chainName, table, autoJumpFrom, position)
}

func (ft *FirewallTracker) FlushChain(
	ctx context.Context,
	chainName model.FirewallChain,
	table model.FirewallTable,
) bool {
	return ft.backend.FlushChain(ctx, chainName, table)
}

func (ft *FirewallTracker) Initialize(ctx context.Context) {
	ft.backend.Initialize(ctx)
}

func (ft *FirewallTracker) Teardown(ctx context.Context) {
	ft.backend.Teardown(ctx)
}

// --- DB query methods ---

func (ft *FirewallTracker) GetByNetworkID(
	ctx context.Context,
	networkID string,
	activeOnly bool,
) ([]*model.FirewallRule, error) {
	return ft.firewallRepo.GetByNetworkID(ctx, networkID, activeOnly)
}

func (ft *FirewallTracker) GetByNetworkIDAndInterface(
	ctx context.Context,
	networkID string,
	iface string,
	activeOnly bool,
) ([]*model.FirewallRule, error) {
	return ft.firewallRepo.GetByNetworkIDAndInterface(ctx, networkID, iface, activeOnly)
}
