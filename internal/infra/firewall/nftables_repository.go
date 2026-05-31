package firewall

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

const nftablesColumns = `id, chain AS chain_name, rule_type, table_name, protocol, source, destination, in_interface, out_interface, target, sport, dport, network_id, comment_tag, command_string, created_at, last_verified_at, is_active`

type NFTablesRuleRepository struct {
	db *sqlx.DB
}

func NewNFTablesRuleRepository(db *sqlx.DB) *NFTablesRuleRepository {
	return &NFTablesRuleRepository{db: db}
}

func (r *NFTablesRuleRepository) ListAll(ctx context.Context) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	return rules, r.db.SelectContext(ctx, &rules, "SELECT "+nftablesColumns+" FROM nftables_rules ORDER BY id")
}

func (r *NFTablesRuleRepository) ListByNetworkID(ctx context.Context, networkID string) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	return rules, r.db.SelectContext(ctx, &rules, "SELECT "+nftablesColumns+" FROM nftables_rules WHERE network_id = ? ORDER BY id", networkID)
}

func (r *NFTablesRuleRepository) ListByNetworkIDBatch(ctx context.Context, networkIDs []string) ([]*model.FirewallRule, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In("SELECT "+nftablesColumns+" FROM nftables_rules WHERE network_id IN (?) ORDER BY id", networkIDs)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	var rules []*model.FirewallRule
	return rules, r.db.SelectContext(ctx, &rules, query, args...)
}

func (r *NFTablesRuleRepository) Get(ctx context.Context, ruleID int64) (*model.FirewallRule, error) {
	var rule model.FirewallRule
	err := r.db.GetContext(ctx, &rule, "SELECT "+nftablesColumns+" FROM nftables_rules WHERE id = ?", ruleID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &rule, err
}

func (r *NFTablesRuleRepository) GetByNetworkID(ctx context.Context, networkID string, activeOnly bool) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	if activeOnly {
		return rules, r.db.SelectContext(ctx, &rules, "SELECT "+nftablesColumns+" FROM nftables_rules WHERE network_id = ? AND is_active = 1", networkID)
	}
	return rules, r.db.SelectContext(ctx, &rules, "SELECT "+nftablesColumns+" FROM nftables_rules WHERE network_id = ?", networkID)
}

func (r *NFTablesRuleRepository) GetByNetworkIDAndInterface(ctx context.Context, networkID string, iface string, activeOnly bool) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	if activeOnly {
		return rules, r.db.SelectContext(ctx, &rules, "SELECT "+nftablesColumns+" FROM nftables_rules WHERE network_id = ? AND is_active = 1 AND (in_interface = ? OR out_interface = ?)", networkID, iface, iface)
	}
	return rules, r.db.SelectContext(ctx, &rules, "SELECT "+nftablesColumns+" FROM nftables_rules WHERE network_id = ? AND (in_interface = ? OR out_interface = ?)", networkID, iface, iface)
}

func (r *NFTablesRuleRepository) Insert(ctx context.Context, rule *model.FirewallRule) (*model.FirewallRule, error) {
	createdAt := rule.CreatedAt
	if createdAt == nil || *createdAt == "" {
		now := time.Now().Format(time.RFC3339)
		createdAt = &now
	}
	isActive := 0
	if rule.IsActive {
		isActive = 1
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO nftables_rules (
			chain, rule_type, table_name, protocol, source, destination,
			in_interface, out_interface, target, sport, dport,
			network_id, comment_tag, command_string, created_at, is_active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, string(rule.ChainName), string(rule.RuleType), string(rule.TableName),
		string(rule.Protocol), rule.Source, rule.Destination,
		rule.InInterface, rule.OutInterface, string(rule.Target),
		rule.SPort, rule.DPort, rule.NetworkID,
		infra.DerefOrNil(rule.CommentTag), infra.DerefOrNil(rule.CommandString),
		*createdAt, isActive,
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	rule.ID = &id
	return rule, nil
}

func (r *NFTablesRuleRepository) UpdateVerifiedAt(ctx context.Context, ruleID int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE nftables_rules SET last_verified_at = CURRENT_TIMESTAMP WHERE id = ?", ruleID)
	return err
}

func (r *NFTablesRuleRepository) MarkDeleted(ctx context.Context, ruleID int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE nftables_rules SET is_active = 0 WHERE id = ?", ruleID)
	return err
}

func (r *NFTablesRuleRepository) MarkDeletedByChain(ctx context.Context, chain string) (int64, error) {
	result, err := r.db.ExecContext(ctx, "UPDATE nftables_rules SET is_active = 0 WHERE chain = ? AND is_active = 1", chain)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *NFTablesRuleRepository) UpdateHandle(ctx context.Context, ruleID int64, nftHandle int) error {
	_, err := r.db.ExecContext(ctx, "UPDATE nftables_rules SET nft_handle = ? WHERE id = ?", nftHandle, ruleID)
	return err
}

func (r *NFTablesRuleRepository) FindAndUpsertRules(ctx context.Context, rules []*model.FirewallRule) ([]*model.FirewallRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	var newRules []*model.FirewallRule
	for _, rule := range rules {
		existing, err := r.FindByAttributes(ctx,
			rule.TableName, rule.ChainName, rule.RuleType, rule.NetworkID,
			rule.Protocol, rule.Source, rule.Destination, rule.InInterface,
			rule.OutInterface, rule.SPort, rule.DPort,
		)
		if err != nil {
			return nil, err
		}
		if existing != nil && existing.ID != nil {
			_ = r.UpdateVerifiedAt(ctx, *existing.ID)
		} else {
			inserted, err := r.Insert(ctx, rule)
			if err != nil {
				return nil, err
			}
			newRules = append(newRules, inserted)
		}
	}
	return newRules, nil
}

func (r *NFTablesRuleRepository) FindByAttributes(
	ctx context.Context,
	tableName model.FirewallTable, chainName model.FirewallChain,
	ruleType model.FirewallRuleType, networkID string,
	protocol model.FirewallProtocol, source, destination string,
	inInterface, outInterface string, sport, dport int,
) (*model.FirewallRule, error) {
	var rule model.FirewallRule
	err := r.db.GetContext(ctx, &rule, `SELECT `+nftablesColumns+` FROM nftables_rules
		WHERE chain = ? AND rule_type = ? AND table_name = ?
		AND network_id = ? AND protocol = ? AND source = ?
		AND destination = ? AND in_interface = ? AND out_interface = ?
		AND sport = ? AND dport = ? AND is_active = 1`,
		string(chainName), string(ruleType), string(tableName),
		networkID, string(protocol), source, destination,
		inInterface, outInterface, sport, dport,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &rule, err
}
