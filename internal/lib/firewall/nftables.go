package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
)

// ── Chain/table mapping ──
// Matches Python's _CHAIN_TO_TABLE for nftables.
var nftChainToTable = map[model.FirewallChain]string{
	model.FirewallChainMVMForward:      "filter",
	model.FirewallChainMVMPostrouting:  "nat",
	model.FirewallChainMVMNocloudNetIn: "filter",
}

// Jump rule definitions: (family, table, builtin_chain, target_chain).
// Matches Python's _JUMP_RULES.
var nftJumpRules = []struct {
	family  string
	table   string
	builtin string
	target  string
}{
	{"ip", "filter", "FORWARD", string(model.FirewallChainMVMForward)},
	{"ip", "nat", "POSTROUTING", string(model.FirewallChainMVMPostrouting)},
	{"ip", "filter", "INPUT", string(model.FirewallChainMVMNocloudNetIn)},
}

// Base chain hook definitions: keyed by (family, table, chain_name).
// Matches Python's _BASE_CHAINS.
var nftBaseChains = map[string]string{
	"ip/filter/FORWARD":  "{ type filter hook forward priority filter; policy accept; }",
	"ip/filter/INPUT":    "{ type filter hook input priority filter; policy accept; }",
	"ip/nat/POSTROUTING": "{ type nat hook postrouting priority srcnat; policy accept; }",
}

func nftBaseChainKey(family, table, chain string) string {
	return fmt.Sprintf("%s/%s/%s", family, table, chain)
}

// ── NFTablesTracker ──
// Matches src/mvmctl/core/_shared/_nftables_tracker/_tracker.py NFTablesTracker.

type NFTablesTracker struct {
	repo *NFTablesRuleRepository
}

// NewNFTablesTracker creates a new NFTablesTracker.
func NewNFTablesTracker(repo *NFTablesRuleRepository) *NFTablesTracker {
	return &NFTablesTracker{repo: repo}
}

// ── Chain existence check ──
// Matches Python NFTablesTracker._chain_exists().

func (t *NFTablesTracker) chainExists(ctx context.Context, family, table, chain string) bool {
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"nft", "list", "chain", family, table, chain},
		system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
	)
	return result.Success()
}

// ── Jump rule existence check ──
// Matches Python NFTablesTracker._jump_rule_exists().

func (t *NFTablesTracker) jumpRuleExists(ctx context.Context, family, table, builtinChain, targetChain string) bool {
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"nft", "list", "chain", family, table, builtinChain},
		system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
	)
	if !result.Success() {
		return false
	}
	return strings.Contains(result.Stdout, fmt.Sprintf("jump %s", targetChain))
}

// ── Find jump rule handle ──
// Matches Python NFTablesTracker._find_jump_rule_handle().

func (t *NFTablesTracker) findJumpRuleHandle(
	ctx context.Context,
	family, table, builtinChain, targetChain string,
) *int {
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"nft", "-a", "list", "chain", family, table, builtinChain},
		system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
	)
	if !result.Success() {
		return nil
	}

	targetStr := fmt.Sprintf("jump %s", targetChain)
	for line := range strings.SplitSeq(strings.TrimRight(result.Stdout, "\n"), "\n") {
		stripped := strings.TrimSpace(line)
		if !strings.Contains(stripped, targetStr) || !strings.Contains(stripped, "# handle ") {
			continue
		}
		// Python: stripped.split(" # handle ")[-1]  — always takes last element
		parts := strings.Split(stripped, "# handle ")
		handleStr := strings.TrimSpace(parts[len(parts)-1])
		handle, err := strconv.Atoi(handleStr)
		if err == nil {
			return &handle
		}
	}
	return nil
}

// ── Initialize ──
// Matches Python NFTablesTracker.initialize().

func (t *NFTablesTracker) Initialize(ctx context.Context) {
	// ── Ensure system tables exist ──
	seenTables := make(map[string]bool)
	for _, table := range nftChainToTable {
		if !seenTables[table] {
			system.DefaultRunner.Run(
				ctx,
				[]string{"nft", "add", "table", "ip", table},
				system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
			)
			seenTables[table] = true
		}
	}

	// ── Create chains in system tables ──
	for chain, table := range nftChainToTable {
		if t.chainExists(ctx, "ip", table, string(chain)) {
			slog.Debug("Chain already exists", "chain", string(chain), "table", table)
			continue
		}
		cmd := []string{"nft", "add", "chain", "ip", table, string(chain)}
		result, _ := system.DefaultRunner.Run(
			ctx,
			cmd,
			system.RunCmdOpts{Privileged: true, Capture: true, Check: true},
		)
	if !result.Success() {
		slog.Error("Failed to create nftables chain",
				"chain", string(chain),
				"table", table,
				"error", fmt.Sprintf("command %s failed (exit %d): %s", cmd[0], result.ExitCode, result.Stderr),
			)
			return
		}
		slog.Info("Created nftables chain", "chain", string(chain), "table", table)
	}

	// ── Ensure built-in base chains exist ──
	for _, jr := range nftJumpRules {
		key := nftBaseChainKey(jr.family, jr.table, jr.builtin)
		hookDef, ok := nftBaseChains[key]
		if !ok {
			continue
		}
		if t.chainExists(ctx, jr.family, jr.table, jr.builtin) {
			continue
		}
		cmd := []string{
			"nft", "add", "chain", jr.family, jr.table, jr.builtin,
			hookDef,
		}
		result, _ := system.DefaultRunner.Run(
			ctx,
			cmd,
			system.RunCmdOpts{Privileged: true, Capture: true, Check: true},
		)
		if !result.Success() {
			slog.Error("Failed to create built-in chain",
				"chain", jr.builtin,
				"family", jr.family,
				"table", jr.table,
				"error", fmt.Sprintf("command %s failed (exit %d): %s", cmd[0], result.ExitCode, result.Stderr),
			)
			return
		}
		slog.Info("Created built-in chain",
			"chain", jr.builtin,
			"family", jr.family,
			"table", jr.table,
		)
	}

	// ── Insert jump rules at position 0 of built-in chains ──
	for _, jr := range nftJumpRules {
		if t.jumpRuleExists(ctx, jr.family, jr.table, jr.builtin, jr.target) {
			slog.Debug("Jump rule already exists",
				"builtin", jr.builtin,
				"target", jr.target,
				"family", jr.family,
				"table", jr.table,
			)
			continue
		}
		cmd := []string{
			"nft", "insert", "rule",
			jr.family, jr.table, jr.builtin,
			"jump", jr.target,
		}
		result, _ := system.DefaultRunner.Run(
			ctx,
			cmd,
			system.RunCmdOpts{Privileged: true, Capture: true, Check: true},
		)
	if !result.Success() {
		slog.Error("Failed to insert jump rule",
				"builtin", jr.builtin,
				"target", jr.target,
				"family", jr.family,
				"table", jr.table,
				"error", fmt.Sprintf("command %s failed (exit %d): %s", cmd[0], result.ExitCode, result.Stderr),
			)
			return
		}
		slog.Info("Inserted jump rule",
			"builtin", jr.builtin,
			"target", jr.target,
			"family", jr.family,
			"table", jr.table,
		)
	}
}

// ── Ensure chain ──
// Matches Python NFTablesTracker.ensure_chain().
// Python raises RuntimeError on failure; Go returns false.

func (t *NFTablesTracker) EnsureChain(
	ctx context.Context,
	chainName model.FirewallChain,
	table model.FirewallTable,
	autoJumpFrom string,
	position int,
) bool {
	if t.chainExists(ctx, "ip", string(table), string(chainName)) {
		slog.Debug("Chain already exists", "chain", string(chainName), "table", string(table))
		return false
	}

	// Ensure the table exists before adding a chain to it
	system.DefaultRunner.Run(
		ctx,
		[]string{"nft", "add", "table", "ip", string(table)},
		system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
	)

	cmd := []string{"nft", "add", "chain", "ip", string(table), string(chainName)}
	result, _ := system.DefaultRunner.Run(
		ctx,
		cmd,
		system.RunCmdOpts{Privileged: true, Capture: true, Check: true},
	)
	if !result.Success() {
		slog.Error("Failed to create chain",
			"chain", string(chainName),
			"error", fmt.Sprintf("command %s failed (exit %d): %s", cmd[0], result.ExitCode, result.Stderr),
		)
		return false
	}

	slog.Info("Created chain", "chain", string(chainName), "table", string(table))
	return true
}

// ── List chain rules ──
// Matches Python NFTablesTracker._list_chain_rules().

type chainRule struct {
	Handle int
	Text   string
}

func (t *NFTablesTracker) listChainRules(ctx context.Context, chain model.FirewallChain, table string) []chainRule {
	result, _ := system.DefaultRunner.Run(ctx,
		[]string{"nft", "-a", "list", "chain", "ip", table, string(chain)},
		system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
	)
	if !result.Success() {
		return nil
	}

	var rules []chainRule
	for line := range strings.SplitSeq(strings.TrimRight(result.Stdout, "\n"), "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == "" ||
			strings.HasPrefix(stripped, "#") ||
			strings.HasPrefix(stripped, "chain ") ||
			strings.HasPrefix(stripped, "type ") ||
			strings.HasPrefix(stripped, "}") {
			continue
		}
		if !strings.Contains(stripped, "# handle ") {
			continue
		}
		// Python: stripped.split(" # handle ")[-1] — always last element
		parts := strings.Split(stripped, "# handle ")
		handleStr := strings.TrimSpace(parts[len(parts)-1])
		handle, err := strconv.Atoi(handleStr)
		if err != nil {
			continue
		}
		// Python: stripped.rsplit(" # handle ", 1)[0] — split from right,
		// take everything before the LAST " # handle ".
		lastIdx := strings.LastIndex(stripped, "# handle ")
		if lastIdx < 0 {
			continue
		}
		ruleText := strings.TrimSpace(stripped[:lastIdx])
		rules = append(rules, chainRule{Handle: handle, Text: ruleText})
	}
	return rules
}

// ── Find rule handle ──
// Matches Python NFTablesTracker._find_rule_handle().

func (t *NFTablesTracker) findRuleHandle(ctx context.Context, rule *model.FirewallRule) *int {
	nftExpr := t.ruleToNftExpr(rule)
	expected := strings.Join(nftExpr, " ")

	for _, cr := range t.listChainRules(ctx, rule.ChainName, string(rule.TableName)) {
		if strings.Contains(cr.Text, expected) {
			return &cr.Handle
		}
	}
	return nil
}

// ── Rule to nftables expression ──
// Matches Python NFTablesTracker._rule_to_nft_expr().

func (t *NFTablesTracker) ruleToNftExpr(rule *model.FirewallRule) []string {
	var expr []string

	// nftables requires L3 (network layer) matches BEFORE L4 (transport layer).
	// The protocol keyword (tcp/udp) must appear after ip saddr/daddr but
	// immediately before dport/sport when port matches are present.

	// Source address (L3)
	if rule.Source != string(model.FirewallWildcardAnyCIDR) {
		expr = append(expr, "ip", "saddr", rule.Source)
	}

	// Destination address (L3)
	if rule.Destination != string(model.FirewallWildcardAnyCIDR) {
		expr = append(expr, "ip", "daddr", rule.Destination)
	}

	// Input interface
	// Matches Python: f'"{rule.in_interface}"' — uses double quotes around value
	if rule.InInterface != string(model.FirewallWildcardAnyInterface) {
		expr = append(expr, "iifname", fmt.Sprintf(`"%s"`, rule.InInterface))
	}

	// Output interface
	if rule.OutInterface != string(model.FirewallWildcardAnyInterface) {
		expr = append(expr, "oifname", fmt.Sprintf(`"%s"`, rule.OutInterface))
	}

	// Protocol — standalone when no L4 port matches follow
	if rule.Protocol != model.FirewallProtocolAll && rule.SPort == model.FirewallPortAny &&
		rule.DPort == model.FirewallPortAny {
		expr = append(expr, string(rule.Protocol))
	}

	// Source port (L4) — protocol keyword is required immediately before port expression
	if rule.SPort != model.FirewallPortAny {
		expr = append(expr, string(rule.Protocol), "sport", strconv.Itoa(rule.SPort))
	}

	// Destination port (L4)
	if rule.DPort != model.FirewallPortAny {
		expr = append(expr, string(rule.Protocol), "dport", strconv.Itoa(rule.DPort))
	}

	// Target (lowercase for nftables)
	targetLower := strings.ToLower(string(rule.Target))
	expr = append(expr, targetLower)

	// Comment — matches Python: f'"{rule.comment_tag}"'
	if rule.CommentTag != nil && *rule.CommentTag != "" {
		expr = append(expr, "comment", fmt.Sprintf(`"%s"`, *rule.CommentTag))
	}

	return expr
}

// ── Ensure rule ──
// Matches Python NFTablesTracker.ensure_rule().

func (t *NFTablesTracker) EnsureRule(
	ctx context.Context,
	rule model.FirewallRule,
	contextLabel string,
) model.FirewallRuleResult {
	nftExpr := t.ruleToNftExpr(&rule)

	// Check if rule exists in database
	existingDBRule, err := t.repo.FindByAttributes(ctx,
		rule.TableName,
		rule.ChainName,
		rule.RuleType,
		rule.NetworkID,
		rule.Protocol,
		rule.Source,
		rule.Destination,
		rule.InInterface,
		rule.OutInterface,
		rule.SPort,
		rule.DPort,
	)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to query existing nftables rule: %v", err)
		return model.FirewallRuleResult{
			Success:      false,
			ErrorMessage: &errMsg,
		}
	}

	if existingDBRule != nil {
		if existingDBRule.ID != nil {
			_ = t.repo.UpdateVerifiedAt(ctx, *existingDBRule.ID)
		}
		return model.FirewallRuleResult{Success: true, Rule: existingDBRule}
	}

	// Add rule in the system table
	addCmd := []string{
		"nft", "add", "rule",
		"ip", string(rule.TableName),
		string(rule.ChainName),
	}
	addCmd = append(addCmd, nftExpr...)

	cmdStr := strings.Join(addCmd, " ")

	// Python: command_string is set BEFORE potential failure
	ruleCmdStr := cmdStr
	rule.CommandString = &ruleCmdStr

	addResult, _ := system.DefaultRunner.Run(
		ctx,
		addCmd,
		system.RunCmdOpts{Privileged: true, Capture: true, Check: true},
	)
	if !addResult.Success() {
		errMsg := fmt.Sprintf(
			"Failed to create nftables rule: command %s failed (exit %d): %s",
			addCmd[0],
			addResult.ExitCode,
			addResult.Stderr,
		)
		return model.FirewallRuleResult{
			Success:         false,
			ErrorMessage:    &errMsg,
			CommandExecuted: &cmdStr,
		}
	}

	recorded, err := t.repo.Insert(ctx, &rule)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to insert nftables rule: %v", err)
		return model.FirewallRuleResult{
			Success:         false,
			ErrorMessage:    &errMsg,
			CommandExecuted: &cmdStr,
		}
	}

	return model.FirewallRuleResult{
		Success:         true,
		Rule:            recorded,
		CommandExecuted: &cmdStr,
	}
}

// ── Remove rule ──
// Matches Python NFTablesTracker.remove_rule().

func (t *NFTablesTracker) RemoveRule(ctx context.Context, rule model.FirewallRule) model.FirewallRuleResult {
	// Try to find the rule in the database first
	dbRule := &rule
	if rule.ID == nil {
		existing, err := t.repo.FindByAttributes(ctx,
			rule.TableName,
			rule.ChainName,
			rule.RuleType,
			rule.NetworkID,
			rule.Protocol,
			rule.Source,
			rule.Destination,
			rule.InInterface,
			rule.OutInterface,
			rule.SPort,
			rule.DPort,
		)
		if err == nil && existing != nil {
			dbRule = existing
		}
	}

	handle := t.findRuleHandle(ctx, dbRule)
	if handle == nil {
		errMsg := "Rule not found in nftables (no matching handle)"
		return model.FirewallRuleResult{
			Success:      false,
			ErrorMessage: &errMsg,
		}
	}

	delCmd := []string{
		"nft", "delete", "rule",
		"ip", string(dbRule.TableName),
		string(dbRule.ChainName),
		"handle",
		strconv.Itoa(*handle),
	}

	deleteResult, _ := system.DefaultRunner.Run(
		ctx,
		delCmd,
		system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
	)
	cmdStr := strings.Join(delCmd, " ")
	if !deleteResult.Success() {
		errMsg := fmt.Sprintf("Failed to remove nftables rule: %s", deleteResult.Stderr)
		return model.FirewallRuleResult{
			Success:         false,
			ErrorMessage:    &errMsg,
			CommandExecuted: &cmdStr,
		}
	}

	if dbRule.ID != nil {
		_ = t.repo.MarkDeleted(ctx, *dbRule.ID)
	}

	return model.FirewallRuleResult{
		Success:         true,
		Rule:            dbRule,
		CommandExecuted: &cmdStr,
	}
}

// ── Batch ensure rules ──
// Matches Python NFTablesTracker.batch_ensure_rules().

func (t *NFTablesTracker) BatchEnsureRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult {
	var lines []string

	// 1. Flush only MVM custom chains
	for chain, table := range nftChainToTable {
		lines = append(lines, fmt.Sprintf("flush chain ip %s %s", table, string(chain)))
	}
	lines = append(lines, "")

	// 2. Conntrack rule first — preserves established connections
	for chain, table := range nftChainToTable {
		if table == "filter" {
			lines = append(lines,
				fmt.Sprintf("add rule ip %s %s ct state established,related accept",
					table, string(chain)))
		}
	}
	lines = append(lines, "")

	// Also add individual conntrack rules for FORWARD and NOCLOUDNET-INPUT
	lines = append(lines,
		fmt.Sprintf("add rule ip filter %s ct state established,related accept",
			string(model.FirewallChainMVMForward)))
	lines = append(lines,
		fmt.Sprintf("add rule ip filter %s ct state established,related accept",
			string(model.FirewallChainMVMNocloudNetIn)))
	lines = append(lines, "")

	// 3. Add all DB rules
	var newRules []*model.FirewallRule
	for i := range rules {
		rule := rules[i]
		nftExpr := t.ruleToNftExpr(&rule)
		lines = append(lines,
			fmt.Sprintf("add rule ip %s %s %s",
				string(rule.TableName),
				string(rule.ChainName),
				strings.Join(nftExpr, " ")),
		)
		r := rule
		newRules = append(newRules, &r)
	}

	nftScript := strings.Join(lines, "\n") + "\n"

	result, _ := system.DefaultRunner.Run(
		ctx,
		[]string{"nft", "-f", "-"},
		system.RunCmdOpts{Privileged: true, Capture: true, Check: true, Input: nftScript},
	)
	if !result.Success() {
		errMsg := fmt.Sprintf("command nft -f - failed (exit %d): %s", result.ExitCode, result.Stderr)
		return model.FirewallRuleResult{
			Success:      false,
			ErrorMessage: &errMsg,
		}
	}

	// DB sync: find existing rules, update verified_at, insert new ones.
	// Uses a single compound query instead of N individual lookups.
	if _, err := t.repo.FindAndUpsertRules(ctx, newRules); err != nil {
		slog.Error("Failed to sync nftables rules in DB after batch", "error", err)
	}

	return model.FirewallRuleResult{Success: true}
}

// ── Batch remove rules ──
// Matches Python NFTablesTracker.batch_remove_rules().

func (t *NFTablesTracker) BatchRemoveRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult {
	var lastError string

	for _, rule := range rules {
		handle := t.findRuleHandle(ctx, &rule)
		if handle == nil {
			lastError = fmt.Sprintf("Rule not found in nftables: %s in=%s out=%s",
				string(rule.ChainName), rule.InInterface, rule.OutInterface)
			slog.Warn("batch_remove_rules: rule not found",
				"chain", string(rule.ChainName),
				"in", rule.InInterface,
				"out", rule.OutInterface,
			)
			// Already gone from kernel — clean up DB entry too
			if rule.ID != nil {
				_ = t.repo.MarkDeleted(ctx, *rule.ID)
			}
			continue
		}

		delCmd := []string{
			"nft", "delete", "rule",
			"ip", string(rule.TableName),
			string(rule.ChainName),
			"handle",
			strconv.Itoa(*handle),
		}

		delResult, _ := system.DefaultRunner.Run(
			ctx,
			delCmd,
			system.RunCmdOpts{Privileged: true, Capture: true, Check: false},
		)
		if !delResult.Success() {
			lastError = delResult.Stderr
			if lastError == "" {
				lastError = fmt.Sprintf("exit %d", delResult.ExitCode)
			}
			slog.Warn("Failed to delete nftables rule", "error", lastError)
		} else if rule.ID != nil {
			_ = t.repo.MarkDeleted(ctx, *rule.ID)
		}
	}

	if lastError != "" {
		errMsg := fmt.Sprintf("Some nftables rules could not be deleted: %s", lastError)
		return model.FirewallRuleResult{
			Success:      false,
			ErrorMessage: &errMsg,
		}
	}

	slog.Info("Removed nftables rules", "count", len(rules))
	return model.FirewallRuleResult{Success: true}
}

// ── Count orphaned rules ──
// Matches Python NFTablesTracker.count_orphaned_rules().

func (t *NFTablesTracker) CountOrphanedRules(ctx context.Context, network *model.Network) int {
	dbRules, err := t.repo.GetByNetworkID(ctx, network.ID, true)
	if err != nil {
		return 0
	}

	dbComments := make(map[string]bool)
	for _, r := range dbRules {
		if r.CommentTag != nil {
			dbComments[*r.CommentTag] = true
		}
	}

	chainMapping := []struct {
		chain model.FirewallChain
		table string
	}{
		{model.FirewallChainMVMForward, "filter"},
		{model.FirewallChainMVMPostrouting, "nat"},
		{model.FirewallChainMVMNocloudNetIn, "filter"},
	}

	commentRe := regexp.MustCompile(`comment\s+"([^"]+)"`)
	orphaned := 0

	for _, cm := range chainMapping {
		rules := t.listChainRules(ctx, cm.chain, cm.table)
		for _, cr := range rules {
			match := commentRe.FindStringSubmatch(cr.Text)
			if len(match) < 2 {
				continue
			}
			comment := match[1]
			if strings.Contains(comment, network.Name) && !dbComments[comment] {
				orphaned++
				slog.Warn("Orphaned nftables rule on host for network",
					"network", network.Name,
					"rule", cr.Text,
				)
			}
		}
	}

	return orphaned
}

// ── Teardown ──
// Matches Python NFTablesTracker.teardown().

func (t *NFTablesTracker) Teardown(ctx context.Context) {
	for _, jr := range nftJumpRules {
		// 1. Remove jump rule from built-in chain
		handle := t.findJumpRuleHandle(ctx, jr.family, jr.table, jr.builtin, jr.target)
		if handle != nil {
			system.DefaultRunner.Run(ctx, []string{
				"nft", "delete", "rule",
				jr.family, jr.table, jr.builtin,
				"handle", strconv.Itoa(*handle),
			}, system.RunCmdOpts{Privileged: true, Capture: true, Check: false})
		}

		// 2. Flush the MVM chain (empty it before delete)
		system.DefaultRunner.Run(ctx, []string{
			"nft", "flush", "chain", jr.family, jr.table, jr.target,
		}, system.RunCmdOpts{Privileged: true, Capture: true, Check: false})

		// 3. Delete the MVM chain
		system.DefaultRunner.Run(ctx, []string{
			"nft", "delete", "chain", jr.family, jr.table, jr.target,
		}, system.RunCmdOpts{Privileged: true, Capture: true, Check: false})
	}
}

// ── Flush chain ──
// Matches Python NFTablesTracker.flush_chain().

func (t *NFTablesTracker) FlushChain(
	ctx context.Context,
	chain model.FirewallChain,
	tableName model.FirewallTable,
) bool {
	chainName := string(chain)
	tableStr := string(tableName)

	result, _ := system.DefaultRunner.Run(ctx, []string{
		"nft", "flush", "chain", "ip", tableStr, chainName,
	}, system.RunCmdOpts{Privileged: true, Capture: true, Check: true})
	if !result.Success() {
		slog.Debug("Chain not found, nothing to flush", "chain", chainName)
		return false
	}

	deleted, err := t.repo.MarkDeletedByChain(ctx, chainName)
	if err != nil {
		slog.Warn("Failed to mark rules as deleted for chain",
			"chain", chainName,
			"error", err,
		)
	} else {
		slog.Debug("Marked rules as deleted for chain",
			"count", deleted,
			"chain", chainName,
		)
	}
	return true
}
