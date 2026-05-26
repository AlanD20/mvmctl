package firewall

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// NFTablesRuleRepository provides CRUD operations for the nftables_rules table.
// Matches src/mvmctl/core/_shared/_nftables_tracker/_repository.py.
type NFTablesRuleRepository struct {
	db *sql.DB
}

// NewNFTablesRuleRepository creates a new NFTablesRuleRepository.
func NewNFTablesRuleRepository(db *sql.DB) *NFTablesRuleRepository {
	return &NFTablesRuleRepository{db: db}
}

// DB returns the underlying database connection.
func (r *NFTablesRuleRepository) DB() *sql.DB {
	return r.db
}

// ListAll returns all nftables rules ordered by id.
func (r *NFTablesRuleRepository) ListAll() ([]*FirewallRule, error) {
	rows, err := r.db.Query("SELECT * FROM nftables_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNFTablesRules(rows)
}

// ListByNetworkID returns all nftables rules for a network.
func (r *NFTablesRuleRepository) ListByNetworkID(networkID string) ([]*FirewallRule, error) {
	rows, err := r.db.Query(
		"SELECT * FROM nftables_rules WHERE network_id = ? ORDER BY id",
		networkID,
	)
	if err != nil {
		return nil, fmt.Errorf("list nftables rules by network %s: %w", networkID, err)
	}
	defer rows.Close()
	return scanNFTablesRules(rows)
}

// ListByNetworkIDBatch returns all nftables rules for multiple networks.
func (r *NFTablesRuleRepository) ListByNetworkIDBatch(networkIDs []string) ([]*FirewallRule, error) {
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
		"SELECT * FROM nftables_rules WHERE network_id IN (%s) ORDER BY id",
		strings.Join(placeholders, ","),
	)
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list nftables rules by network batch: %w", err)
	}
	defer rows.Close()
	return scanNFTablesRules(rows)
}

// Get returns a specific rule by ID.
// Uses dict-based row conversion matching Python's _row_to_item.
func (r *NFTablesRuleRepository) Get(ruleID int64) (*FirewallRule, error) {
	return querySingleNFTablesRule(r.db,
		"SELECT * FROM nftables_rules WHERE id = ?", ruleID)
}

// GetByNetworkID returns all rules for a network, optionally filtering by active.
func (r *NFTablesRuleRepository) GetByNetworkID(networkID string, activeOnly bool) ([]*FirewallRule, error) {
	var rows *sql.Rows
	var err error
	if activeOnly {
		rows, err = r.db.Query(
			"SELECT * FROM nftables_rules WHERE network_id = ? AND is_active = 1",
			networkID,
		)
	} else {
		rows, err = r.db.Query(
			"SELECT * FROM nftables_rules WHERE network_id = ?",
			networkID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("get nftables rules by network %s: %w", networkID, err)
	}
	defer rows.Close()
	return scanNFTablesRules(rows)
}

// GetByNetworkIDAndInterface returns all active rules for a network that reference a given interface.
func (r *NFTablesRuleRepository) GetByNetworkIDAndInterface(networkID string, iface string, activeOnly bool) ([]*FirewallRule, error) {
	var rows *sql.Rows
	var err error
	if activeOnly {
		rows, err = r.db.Query(
			"SELECT * FROM nftables_rules WHERE network_id = ? AND is_active = 1 AND (in_interface = ? OR out_interface = ?)",
			networkID, iface, iface,
		)
	} else {
		rows, err = r.db.Query(
			"SELECT * FROM nftables_rules WHERE network_id = ? AND (in_interface = ? OR out_interface = ?)",
			networkID, iface, iface,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("get nftables rules by network/interface: %w", err)
	}
	defer rows.Close()
	return scanNFTablesRules(rows)
}

// Insert inserts a new nftables rule record and returns it with the generated ID.
func (r *NFTablesRuleRepository) Insert(rule *FirewallRule) (*FirewallRule, error) {
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
		INSERT INTO nftables_rules (
			chain, rule_type, table_name, protocol, source, destination,
			in_interface, out_interface, target, sport, dport,
			network_id, comment_tag, command_string, created_at, is_active
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		string(rule.ChainName),
		string(rule.RuleType),
		string(rule.TableName),
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
		return nil, fmt.Errorf("insert nftables rule: %w", err)
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
// Python: “UPDATE nftables_rules SET last_verified_at = CURRENT_TIMESTAMP WHERE id = ?“
func (r *NFTablesRuleRepository) UpdateVerifiedAt(ruleID int64) error {
	_, err := r.db.Exec(
		"UPDATE nftables_rules SET last_verified_at = CURRENT_TIMESTAMP WHERE id = ?",
		ruleID,
	)
	return err
}

// MarkDeleted soft-deletes a rule (sets is_active = 0).
func (r *NFTablesRuleRepository) MarkDeleted(ruleID int64) error {
	_, err := r.db.Exec(
		"UPDATE nftables_rules SET is_active = 0 WHERE id = ?",
		ruleID,
	)
	return err
}

// MarkDeletedByChain soft-deletes all active rules for a specific chain.
func (r *NFTablesRuleRepository) MarkDeletedByChain(chain string) (int64, error) {
	result, err := r.db.Exec(
		"UPDATE nftables_rules SET is_active = 0 WHERE chain = ? AND is_active = 1",
		chain,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// UpdateHandle updates the nft_handle for a rule.
func (r *NFTablesRuleRepository) UpdateHandle(ruleID int64, nftHandle int) error {
	_, err := r.db.Exec(
		"UPDATE nftables_rules SET nft_handle = ? WHERE id = ?",
		nftHandle, ruleID,
	)
	return err
}

// FindByAttributes finds an active nftables rule by its unique attributes.
// Returns nil if not found.
func (r *NFTablesRuleRepository) FindByAttributes(
	tableName FirewallTable,
	chainName FirewallChain,
	ruleType FirewallRuleType,
	networkID string,
	protocol FirewallProtocol,
	source, destination string,
	inInterface, outInterface string,
	sport, dport int,
) (*FirewallRule, error) {
	return querySingleNFTablesRule(r.db, `
		SELECT * FROM nftables_rules
		WHERE chain = ? AND rule_type = ? AND table_name = ?
		AND network_id = ? AND protocol = ? AND source = ?
		AND destination = ? AND in_interface = ? AND out_interface = ?
		AND sport = ? AND dport = ? AND is_active = 1
	`,
		string(chainName),
		string(ruleType),
		string(tableName),
		networkID,
		string(protocol),
		source,
		destination,
		inInterface,
		outInterface,
		sport,
		dport,
	)
}

// ── Scan helpers (dict-based, matching Python's _row_to_item) ──

// scanRowToNFTablesRule scans a single row (map) into a FirewallRule using
// dict-based conversion that matches Python's _row_to_item exactly:
//   - "chain" column is renamed to ChainName (Python pops "chain" → "chain_name")
//   - String columns are cast to their FirewallXxx enum types
//   - "nft_handle" column is intentionally dropped (not in the struct)
//   - "is_active" is converted from int to bool
func scanRowToNFTablesRule(rowMap map[string]interface{}) (*FirewallRule, error) {
	r := &FirewallRule{}

	// Helper: get string value from map (returns "" if nil or missing)
	getStr := func(key string) string {
		if v, ok := rowMap[key]; ok && v != nil {
			if s, ok2 := v.(string); ok2 {
				return s
			}
		}
		return ""
	}

	// id (INTEGER PRIMARY KEY)
	if v, ok := rowMap["id"]; ok && v != nil {
		if id, ok2 := v.(int64); ok2 {
			r.ID = &id
		}
	}

	// chain → ChainName (Python: row_dict["chain_name"] = FirewallChain(row_dict.pop("chain")))
	r.ChainName = FirewallChain(getStr("chain"))

	// rule_type
	r.RuleType = FirewallRuleType(getStr("rule_type"))

	// table_name
	r.TableName = FirewallTable(getStr("table_name"))

	// protocol
	r.Protocol = FirewallProtocol(getStr("protocol"))

	// source, destination
	r.Source = getStr("source")
	r.Destination = getStr("destination")

	// in_interface, out_interface
	r.InInterface = getStr("in_interface")
	r.OutInterface = getStr("out_interface")

	// target
	r.Target = FirewallTarget(getStr("target"))

	// sport, dport (INTEGER, default 0)
	if v, ok := rowMap["sport"]; ok && v != nil {
		if s, ok2 := v.(int64); ok2 {
			r.SPort = int(s)
		}
	}
	if v, ok := rowMap["dport"]; ok && v != nil {
		if d, ok2 := v.(int64); ok2 {
			r.DPort = int(d)
		}
	}

	// network_id
	r.NetworkID = getStr("network_id")

	// nft_handle — Python: row_dict.pop("nft_handle", None) — intentionally dropped
	// (not stored in FirewallRule struct)

	// comment_tag (nullable TEXT)
	if v, ok := rowMap["comment_tag"]; ok && v != nil {
		if s, ok2 := v.(string); ok2 {
			r.CommentTag = &s
		}
	}

	// command_string (nullable TEXT)
	if v, ok := rowMap["command_string"]; ok && v != nil {
		if s, ok2 := v.(string); ok2 {
			r.CommandString = &s
		}
	}

	// created_at (TEXT)
	r.CreatedAt = ptrStr(getStr("created_at"))

	// last_verified_at (nullable TEXT)
	if v, ok := rowMap["last_verified_at"]; ok && v != nil {
		if s, ok2 := v.(string); ok2 {
			r.LastVerifiedAt = &s
		}
	}

	// is_active — Python: bool(row_dict["is_active"]) — uses truthiness, any non-zero int→true
	if v, ok := rowMap["is_active"]; ok && v != nil {
		switch val := v.(type) {
		case int64:
			r.IsActive = val != 0
		case float64:
			r.IsActive = val != 0
		case bool:
			r.IsActive = val
		default:
			r.IsActive = v != nil
		}
	}

	return r, nil
}

// ptrStr returns a pointer to s. If s is empty, returns nil to match "not set" semantics.
func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// scanRowToMap scans a single row from rows into a map[string]interface{} keyed by column name.
// This matches Python's sqlite3.Row dict-like access (row["column_name"]).
func scanRowToMap(rows *sql.Rows) (map[string]interface{}, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range columns {
		valuePtrs[i] = &values[i]
	}

	if err := rows.Scan(valuePtrs...); err != nil {
		return nil, err
	}

	result := make(map[string]interface{}, len(columns))
	for i, col := range columns {
		val := values[i]
		if b, ok := val.([]byte); ok {
			result[col] = string(b)
		} else {
			result[col] = val
		}
	}
	return result, nil
}

// querySingleNFTablesRule queries a single row and returns a FirewallRule via dict-based conversion.
// Returns nil, nil for sql.ErrNoRows.
func querySingleNFTablesRule(db *sql.DB, query string, args ...interface{}) (*FirewallRule, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil
	}

	rowMap, err := scanRowToMap(rows)
	if err != nil {
		return nil, err
	}

	return scanRowToNFTablesRule(rowMap)
}

// scanNFTablesRules scans multiple rows into FirewallRule slice using dict-based conversion.
func scanNFTablesRules(rows *sql.Rows) ([]*FirewallRule, error) {
	var rules []*FirewallRule
	for rows.Next() {
		rowMap, err := scanRowToMap(rows)
		if err != nil {
			return nil, err
		}
		rule, err := scanRowToNFTablesRule(rowMap)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}
