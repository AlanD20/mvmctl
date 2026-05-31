package firewall

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
)

type IPTablesRuleRepository struct {
	db *sqlx.DB
}

func NewIPTablesRuleRepository(db *sqlx.DB) *IPTablesRuleRepository {
	return &IPTablesRuleRepository{db: db}
}

func (r *IPTablesRuleRepository) ListAll(ctx context.Context) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules ORDER BY id")
}

func (r *IPTablesRuleRepository) ListByNetworkID(ctx context.Context, networkID string) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE network_id = ? ORDER BY id", networkID)
}

func (r *IPTablesRuleRepository) ListByNetworkIDBatch(ctx context.Context, networkIDs []string) ([]*model.FirewallRule, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	query, args, err := sqlx.In("SELECT * FROM iptables_rules WHERE network_id IN (?) ORDER BY id", networkIDs)
	if err != nil {
		return nil, err
	}
	query = r.db.Rebind(query)
	var rules []*model.FirewallRule
	return rules, r.db.SelectContext(ctx, &rules, query, args...)
}

func (r *IPTablesRuleRepository) Get(ctx context.Context, ruleID int64) (*model.FirewallRule, error) {
	var rule model.FirewallRule
	err := r.db.GetContext(ctx, &rule, "SELECT * FROM iptables_rules WHERE id = ?", ruleID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &rule, err
}

func (r *IPTablesRuleRepository) GetByNetworkID(ctx context.Context, networkID string, activeOnly bool) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	if activeOnly {
		return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE network_id = ? AND is_active = 1", networkID)
	}
	return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE network_id = ?", networkID)
}

func (r *IPTablesRuleRepository) GetByNetworkIDAndInterface(ctx context.Context, networkID string, iface string, activeOnly bool) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	if activeOnly {
		return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE network_id = ? AND is_active = 1 AND (in_interface = ? OR out_interface = ?)", networkID, iface, iface)
	}
	return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE network_id = ? AND (in_interface = ? OR out_interface = ?)", networkID, iface, iface)
}

func (r *IPTablesRuleRepository) GetByTableChainName(ctx context.Context, tableName, chainName string, activeOnly bool) ([]*model.FirewallRule, error) {
	var rules []*model.FirewallRule
	if activeOnly {
		return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE table_name = ? AND chain_name = ? AND is_active = 1", tableName, chainName)
	}
	return rules, r.db.SelectContext(ctx, &rules, "SELECT * FROM iptables_rules WHERE table_name = ? AND chain_name = ?", tableName, chainName)
}

func (r *IPTablesRuleRepository) Insert(ctx context.Context, rule *model.FirewallRule) (*model.FirewallRule, error) {
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
		INSERT INTO iptables_rules (
			table_name, chain_name, rule_type, protocol, source, destination,
			in_interface, out_interface, target, sport, dport,
			network_id, comment_tag, command_string, created_at, is_active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, string(rule.TableName), string(rule.ChainName), string(rule.RuleType),
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

func (r *IPTablesRuleRepository) UpdateVerifiedAt(ctx context.Context, ruleID int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE iptables_rules SET last_verified_at = CURRENT_TIMESTAMP WHERE id = ?", ruleID)
	return err
}

func (r *IPTablesRuleRepository) MarkDeleted(ctx context.Context, ruleID int64) error {
	_, err := r.db.ExecContext(ctx, "UPDATE iptables_rules SET is_active = 0 WHERE id = ?", ruleID)
	return err
}

func (r *IPTablesRuleRepository) DeleteByNetworkID(ctx context.Context, networkID string) (int64, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM iptables_rules WHERE network_id = ?", networkID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *IPTablesRuleRepository) DeleteInactive(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, "DELETE FROM iptables_rules WHERE is_active = 0")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *IPTablesRuleRepository) MarkDeletedByTableChainName(ctx context.Context, chainName model.FirewallChain, tableName model.FirewallTable) (int64, error) {
	result, err := r.db.ExecContext(ctx, "UPDATE iptables_rules SET is_active = 0 WHERE table_name = ? AND chain_name = ? AND is_active = 1", string(tableName), string(chainName))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *IPTablesRuleRepository) FindAndUpsertRules(ctx context.Context, rules []*model.FirewallRule) ([]*model.FirewallRule, error) {
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

func (r *IPTablesRuleRepository) FindByAttributes(
	ctx context.Context,
	tableName model.FirewallTable, chainName model.FirewallChain,
	ruleType model.FirewallRuleType, networkID string,
	protocol model.FirewallProtocol, source, destination string,
	inInterface, outInterface string, sport, dport int,
) (*model.FirewallRule, error) {
	var rule model.FirewallRule
	err := r.db.GetContext(ctx, &rule, `SELECT * FROM iptables_rules
		WHERE table_name = ? AND chain_name = ? AND rule_type = ?
		AND network_id = ? AND protocol = ? AND source = ?
		AND destination = ? AND in_interface = ? AND out_interface = ?
		AND sport = ? AND dport = ? AND is_active = 1`,
		string(tableName), string(chainName), string(ruleType),
		networkID, string(protocol), source, destination,
		inInterface, outInterface, sport, dport,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &rule, err
}
