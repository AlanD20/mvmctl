package firewall

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// IPTablesRuleRepository provides CRUD operations for the iptables_rules table.
// Matches src/mvmctl/core/_shared/_iptables_tracker/_repository.py.
type IPTablesRuleRepository struct {
	db *sql.DB
}

// NewIPTablesRuleRepository creates a new IPTablesRuleRepository.
func NewIPTablesRuleRepository(db *sql.DB) *IPTablesRuleRepository {
	return &IPTablesRuleRepository{db: db}
}

// DB returns the underlying database connection.
func (r *IPTablesRuleRepository) DB() *sql.DB {
	return r.db
}

// ListAll returns all iptables rules ordered by id.
func (r *IPTablesRuleRepository) ListAll() ([]*FirewallRule, error) {
	rows, err := r.db.Query("SELECT * FROM iptables_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIPTablesRules(rows)
}

// ListByNetworkID returns all iptables rules for a network.
func (r *IPTablesRuleRepository) ListByNetworkID(networkID string) ([]*FirewallRule, error) {
	rows, err := r.db.Query(
		"SELECT * FROM iptables_rules WHERE network_id = ? ORDER BY id",
		networkID,
	)
	if err != nil {
		return nil, fmt.Errorf("list iptables rules by network %s: %w", networkID, err)
	}
	defer rows.Close()
	return scanIPTablesRules(rows)
}

// ListByNetworkIDBatch returns all iptables rules for multiple networks.
func (r *IPTablesRuleRepository) ListByNetworkIDBatch(networkIDs []string) ([]*FirewallRule, error) {
	if len(networkIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(networkIDs))
	args := make([]interface{}, len(networkIDs))
	for i, id := range networkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		"SELECT * FROM iptables_rules WHERE network_id IN (%s) ORDER BY id",
		strings.Join(placeholders, ","),
	)
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list iptables rules by network batch: %w", err)
	}
	defer rows.Close()
	return scanIPTablesRules(rows)
}

// Get returns a specific rule by ID.
func (r *IPTablesRuleRepository) Get(ruleID int64) (*FirewallRule, error) {
	return scanIPTablesRule(r.db.QueryRow("SELECT * FROM iptables_rules WHERE id = ?", ruleID))
}

// GetByNetworkID returns all rules for a network, optionally filtering by active.
func (r *IPTablesRuleRepository) GetByNetworkID(networkID string, activeOnly bool) ([]*FirewallRule, error) {
	var rows *sql.Rows
	var err error
	if activeOnly {
		rows, err = r.db.Query(
			"SELECT * FROM iptables_rules WHERE network_id = ? AND is_active = 1",
			networkID,
		)
	} else {
		rows, err = r.db.Query(
			"SELECT * FROM iptables_rules WHERE network_id = ?",
			networkID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("get iptables rules by network %s: %w", networkID, err)
	}
	defer rows.Close()
	return scanIPTablesRules(rows)
}

// GetByNetworkIDAndInterface returns all active rules for a network that reference a given interface.
func (r *IPTablesRuleRepository) GetByNetworkIDAndInterface(networkID string, iface string, activeOnly bool) ([]*FirewallRule, error) {
	var rows *sql.Rows
	var err error
	if activeOnly {
		rows, err = r.db.Query(
			"SELECT * FROM iptables_rules WHERE network_id = ? AND is_active = 1 AND (in_interface = ? OR out_interface = ?)",
			networkID, iface, iface,
		)
	} else {
		rows, err = r.db.Query(
			"SELECT * FROM iptables_rules WHERE network_id = ? AND (in_interface = ? OR out_interface = ?)",
			networkID, iface, iface,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("get iptables rules by network/interface: %w", err)
	}
	defer rows.Close()
	return scanIPTablesRules(rows)
}

// GetByTableChainName returns all rules for a specific chain.
func (r *IPTablesRuleRepository) GetByTableChainName(tableName, chainName string, activeOnly bool) ([]*FirewallRule, error) {
	var rows *sql.Rows
	var err error
	if activeOnly {
		rows, err = r.db.Query(
			"SELECT * FROM iptables_rules WHERE table_name = ? AND chain_name = ? AND is_active = 1",
			tableName, chainName,
		)
	} else {
		rows, err = r.db.Query(
			"SELECT * FROM iptables_rules WHERE table_name = ? AND chain_name = ?",
			tableName, chainName,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("get iptables rules by table/chain: %w", err)
	}
	defer rows.Close()
	return scanIPTablesRules(rows)
}

// Insert inserts a new iptables rule record and returns it with the generated ID.
func (r *IPTablesRuleRepository) Insert(rule *FirewallRule) (*FirewallRule, error) {
	createdAt := rule.CreatedAt
	if createdAt == nil || *createdAt == "" {
		now := time.Now().Format(time.RFC3339)
		createdAt = &now
	}

	isActive := 0
	if rule.IsActive {
		isActive = 1
	}

	result, err := r.db.Exec(`
		INSERT INTO iptables_rules (
			table_name, chain_name, rule_type, protocol, source, destination,
			in_interface, out_interface, target, sport, dport,
			network_id, comment_tag, command_string, created_at, is_active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		string(rule.TableName),
		string(rule.ChainName),
		string(rule.RuleType),
		string(rule.Protocol),
		rule.Source,
		rule.Destination,
		rule.InInterface,
		rule.OutInterface,
		string(rule.Target),
		rule.SPort,
		rule.DPort,
		rule.NetworkID,
		nullableString(rule.CommentTag),
		nullableString(rule.CommandString),
		*createdAt,
		isActive,
	)
	if err != nil {
		return nil, fmt.Errorf("insert iptables rule: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	rule.ID = &id
	return rule, nil
}

// UpdateVerifiedAt updates the last_verified_at timestamp for a rule.
// Uses SQLite CURRENT_TIMESTAMP to match Python's behavior exactly.
// Python: “UPDATE iptables_rules SET last_verified_at = CURRENT_TIMESTAMP WHERE id = ?“
func (r *IPTablesRuleRepository) UpdateVerifiedAt(ruleID int64) error {
	_, err := r.db.Exec(
		"UPDATE iptables_rules SET last_verified_at = CURRENT_TIMESTAMP WHERE id = ?",
		ruleID,
	)
	return err
}

// MarkDeleted soft-deletes a rule (sets is_active = 0).
func (r *IPTablesRuleRepository) MarkDeleted(ruleID int64) error {
	_, err := r.db.Exec(
		"UPDATE iptables_rules SET is_active = 0 WHERE id = ?",
		ruleID,
	)
	return err
}

// DeleteByNetworkID hard-deletes all rules for a network.
func (r *IPTablesRuleRepository) DeleteByNetworkID(networkID string) (int64, error) {
	result, err := r.db.Exec(
		"DELETE FROM iptables_rules WHERE network_id = ?",
		networkID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteInactive hard-deletes all inactive iptables rules (is_active = 0).
func (r *IPTablesRuleRepository) DeleteInactive() (int64, error) {
	result, err := r.db.Exec("DELETE FROM iptables_rules WHERE is_active = 0")
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// MarkDeletedByTableChainName soft-deletes all active rules for a specific chain.
// Matches Python IPTablesRuleRepository.mark_deleted_by_table_chain_name() which takes
// table_name: FirewallTable, chain_name: FirewallChain.
func (r *IPTablesRuleRepository) MarkDeletedByTableChainName(chainName FirewallChain, tableName FirewallTable) (int64, error) {
	result, err := r.db.Exec(
		"UPDATE iptables_rules SET is_active = 0 WHERE table_name = ? AND chain_name = ? AND is_active = 1",
		string(tableName), string(chainName),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// FindByAttributes finds an active iptables rule by its unique attributes.
// Returns nil if not found.
func (r *IPTablesRuleRepository) FindByAttributes(
	tableName FirewallTable,
	chainName FirewallChain,
	ruleType FirewallRuleType,
	networkID string,
	protocol FirewallProtocol,
	source, destination string,
	inInterface, outInterface string,
	sport, dport int,
) (*FirewallRule, error) {
	return scanIPTablesRule(r.db.QueryRow(`
		SELECT * FROM iptables_rules
		WHERE table_name = ? AND chain_name = ? AND rule_type = ?
		AND network_id = ? AND protocol = ? AND source = ?
		AND destination = ? AND in_interface = ? AND out_interface = ?
		AND sport = ? AND dport = ? AND is_active = 1
	`,
		string(tableName),
		string(chainName),
		string(ruleType),
		networkID,
		string(protocol),
		source,
		destination,
		inInterface,
		outInterface,
		sport,
		dport,
	))
}

// ── Scan helpers ──

// scanIPTablesRule scans a single row into a FirewallRule.
// Returns nil, nil for sql.ErrNoRows.
func scanIPTablesRule(row *sql.Row) (*FirewallRule, error) {
	var r FirewallRule
	var ruleType, protocol, target, tableName, chainName string
	var commentTag, commandString, createdAt, lastVerifiedAt sql.NullString
	var isActive int
	var id sql.NullInt64

	err := row.Scan(
		&id,
		&tableName,
		&chainName,
		&ruleType,
		&protocol,
		&r.Source,
		&r.Destination,
		&r.InInterface,
		&r.OutInterface,
		&target,
		&r.SPort,
		&r.DPort,
		&r.NetworkID,
		&commentTag,
		&commandString,
		&createdAt,
		&lastVerifiedAt,
		&isActive,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	r.TableName = FirewallTable(tableName)
	r.ChainName = FirewallChain(chainName)
	r.RuleType = FirewallRuleType(ruleType)
	r.Protocol = FirewallProtocol(protocol)
	r.Target = FirewallTarget(target)
	r.IsActive = isActive != 0

	if id.Valid {
		r.ID = &id.Int64
	}
	if commentTag.Valid {
		r.CommentTag = &commentTag.String
	}
	if commandString.Valid {
		r.CommandString = &commandString.String
	}
	if createdAt.Valid {
		r.CreatedAt = &createdAt.String
	}
	if lastVerifiedAt.Valid {
		r.LastVerifiedAt = &lastVerifiedAt.String
	}

	return &r, nil
}

// scanIPTablesRules scans multiple rows into FirewallRule slice.
func scanIPTablesRules(rows *sql.Rows) ([]*FirewallRule, error) {
	var rules []*FirewallRule
	for rows.Next() {
		var r FirewallRule
		var ruleType, protocol, target, tableName, chainName string
		var commentTag, commandString, createdAt, lastVerifiedAt sql.NullString
		var isActive int
		var id sql.NullInt64

		err := rows.Scan(
			&id,
			&tableName,
			&chainName,
			&ruleType,
			&protocol,
			&r.Source,
			&r.Destination,
			&r.InInterface,
			&r.OutInterface,
			&target,
			&r.SPort,
			&r.DPort,
			&r.NetworkID,
			&commentTag,
			&commandString,
			&createdAt,
			&lastVerifiedAt,
			&isActive,
		)
		if err != nil {
			return nil, err
		}

		r.TableName = FirewallTable(tableName)
		r.ChainName = FirewallChain(chainName)
		r.RuleType = FirewallRuleType(ruleType)
		r.Protocol = FirewallProtocol(protocol)
		r.Target = FirewallTarget(target)
		r.IsActive = isActive != 0

		if id.Valid {
			r.ID = &id.Int64
		}
		if commentTag.Valid {
			r.CommentTag = &commentTag.String
		}
		if commandString.Valid {
			r.CommandString = &commandString.String
		}
		if createdAt.Valid {
			r.CreatedAt = &createdAt.String
		}
		if lastVerifiedAt.Valid {
			r.LastVerifiedAt = &lastVerifiedAt.String
		}

		rules = append(rules, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

func nullableString(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}
