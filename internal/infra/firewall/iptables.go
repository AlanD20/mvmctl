package firewall

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// MaxCommentLen maximum length for iptables comment tags.
const MaxCommentLen = 240

// ── Chain/table mapping ──
// Matches Python's _CHAIN_TO_TABLE.
var chainToTable = map[model.FirewallChain]string{
	model.FirewallChainMVMForward:      "filter",
	model.FirewallChainMVMPostrouting:  "nat",
	model.FirewallChainMVMNocloudNetIn: "filter",
}

// ── IPTablesTracker ──
// Matches src/mvmctl/core/_shared/_iptables_tracker/_tracker.py IPTablesTracker.

// RuleAction matches Python's IPTablesTracker.RuleAction.
type RuleAction string

const (
	ActionCheck  RuleAction = "-C"
	ActionAppend RuleAction = "-A"
	ActionDelete RuleAction = "-D"
)

// IPTablesTracker manages iptables rules with database synchronization.
type IPTablesTracker struct {
	repo               *IPTablesRuleRepository
	xtcommentAvailable bool
}

// NewIPTablesTracker creates a new IPTablesTracker.
func NewIPTablesTracker(repo *IPTablesRuleRepository, xtcommentAvailable bool) *IPTablesTracker {
	return &IPTablesTracker{
		repo:               repo,
		xtcommentAvailable: xtcommentAvailable,
	}
}

// ── Initialize ──
// Matches Python IPTablesTracker.initialize().

func (t *IPTablesTracker) Initialize(ctx context.Context) {
	type chainDef struct {
		chain    model.FirewallChain
		table    model.FirewallTable
		jumpFrom string
	}
	chains := []chainDef{
		{model.FirewallChainMVMForward, model.FirewallTableFilter, "FORWARD"},
		{model.FirewallChainMVMPostrouting, model.FirewallTableNat, "POSTROUTING"},
		{model.FirewallChainMVMNocloudNetIn, model.FirewallTableFilter, "INPUT"},
	}
	for _, c := range chains {
		t.EnsureChain(ctx, c.chain, c.table, c.jumpFrom, 1)
	}
}

// ── CheckCommentAvailable (static method) ──
// Matches Python IPTablesTracker.check_comment_available() static method.

func (t *IPTablesTracker) CheckCommentAvailable(ctx context.Context) bool {
	result := system.RunCmdCompat(ctx,
		[]string{"iptables", "-C", "INPUT", "-m", "comment", "--comment", "mvmctl-probe", "-j", "ACCEPT"},
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	return result.ExitCode == 0
}

// ── Build comment ──
// Matches Python IPTablesTracker._build_comment().

func (t *IPTablesTracker) buildComment(ruleType model.FirewallRuleType, networkName, contextLabel string) string {
	comment := fmt.Sprintf("%s:%s:%s", infra.CLIName, string(ruleType), networkName)
	if contextLabel != "" {
		comment = fmt.Sprintf("%s:%s", comment, contextLabel)
	}
	if len(comment) > MaxCommentLen {
		comment = comment[:MaxCommentLen]
	}
	return comment
}

// ── Shell-safe quote ──
// Matches Python shlex.quote(). Wraps argument in single quotes if it contains
// characters unsafe for shell tokenization. Safe characters:
//
//	ASCII letters, digits, and @%_+=:,./-
func shlexQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) &&
			r != '@' && r != '%' && r != '_' && r != '+' &&
			r != '=' && r != ':' && r != ',' && r != '.' &&
			r != '/' && r != '-' {
			goto quote
		}
	}
	return s
quote:
	// Use single quotes, with embedded single quotes escaped as: '"'"'
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// ── Build iptables args ──
// Matches Python IPTablesTracker._build_iptables_args().

func (t *IPTablesTracker) buildIptablesArgs(rule *model.FirewallRule, action RuleAction) []string {
	args := []string{
		"iptables",
		"-t", string(rule.TableName),
		string(action),
		string(rule.ChainName),
	}

	// Protocol (only if not ALL)
	if rule.Protocol != model.FirewallProtocolAll {
		args = append(args, "-p", string(rule.Protocol))
	}

	// Source
	if rule.Source != string(model.FirewallWildcardAnyCIDR) {
		args = append(args, "-s", rule.Source)
	}

	// Destination
	if rule.Destination != string(model.FirewallWildcardAnyCIDR) {
		args = append(args, "-d", rule.Destination)
	}

	// Input interface
	if rule.InInterface != string(model.FirewallWildcardAnyInterface) {
		args = append(args, "-i", rule.InInterface)
	}

	// Output interface
	if rule.OutInterface != string(model.FirewallWildcardAnyInterface) {
		args = append(args, "-o", rule.OutInterface)
	}

	// Source port
	if rule.SPort != model.FirewallPortAny {
		args = append(args, "--sport", strconv.Itoa(rule.SPort))
	}

	// Destination port
	if rule.DPort != model.FirewallPortAny {
		args = append(args, "--dport", strconv.Itoa(rule.DPort))
	}

	// Target
	args = append(args, "-j", string(rule.Target))

	// Comment
	if rule.CommentTag != nil && *rule.CommentTag != "" && t.xtcommentAvailable {
		args = append(args, "-m", "comment", "--comment", *rule.CommentTag)
	}

	return args
}

// ── Build restore line ──
// Matches Python IPTablesTracker._build_restore_line().

func (t *IPTablesTracker) buildRestoreLine(rule *model.FirewallRule) string {
	parts := []string{"-A", string(rule.ChainName)}

	if rule.Protocol != model.FirewallProtocolAll {
		parts = append(parts, "-p", string(rule.Protocol))
	}

	if rule.Source != string(model.FirewallWildcardAnyCIDR) {
		parts = append(parts, "-s", rule.Source)
	}

	if rule.Destination != string(model.FirewallWildcardAnyCIDR) {
		parts = append(parts, "-d", rule.Destination)
	}

	if rule.InInterface != string(model.FirewallWildcardAnyInterface) {
		parts = append(parts, "-i", rule.InInterface)
	}

	if rule.OutInterface != string(model.FirewallWildcardAnyInterface) {
		parts = append(parts, "-o", rule.OutInterface)
	}

	if rule.SPort != model.FirewallPortAny {
		parts = append(parts, "--sport", strconv.Itoa(rule.SPort))
	}

	if rule.DPort != model.FirewallPortAny {
		parts = append(parts, "--dport", strconv.Itoa(rule.DPort))
	}

	parts = append(parts, "-j", string(rule.Target))

	if rule.CommentTag != nil && *rule.CommentTag != "" && t.xtcommentAvailable {
		parts = append(parts, "-m", "comment", "--comment", *rule.CommentTag)
	}

	return strings.Join(parts, " ")
}

// ── Build restore input ──
// Matches Python IPTablesTracker._build_restore_input().

func (t *IPTablesTracker) buildRestoreInput(rules []*model.FirewallRule, table string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("*%s", table))

	// Define and flush MVM chains that belong to this table
	for chain, chainTable := range chainToTable {
		if chainTable != table {
			continue
		}
		lines = append(lines, fmt.Sprintf(":%s - [0:0]", string(chain)))
		lines = append(lines, fmt.Sprintf("-F %s", string(chain)))

		// Conntrack rule first for filter chains — preserves established connections
		if table == "filter" {
			lines = append(lines,
				fmt.Sprintf("-A %s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT", string(chain)),
			)
		}
	}

	// Append all DB rules
	for _, rule := range rules {
		lines = append(lines, t.buildRestoreLine(rule))
	}

	lines = append(lines, "COMMIT")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// ── Ensure rule ──
// Matches Python IPTablesTracker.ensure_rule().

func (t *IPTablesTracker) EnsureRule(
	ctx context.Context,
	rule model.FirewallRule,
	contextLabel string,
) model.FirewallRuleResult {
	// No clone — work directly on the input rule, matching Python behavior
	// where comment_tag and command_string are mutated in place.
	r := &rule

	// Build comment if not already set
	if r.CommentTag == nil || *r.CommentTag == "" {
		networkName := ""
		if r.NetworkName != nil {
			networkName = *r.NetworkName
		}
		comment := t.buildComment(r.RuleType, networkName, contextLabel)
		r.CommentTag = &comment
	}

	// Generate command strings
	checkArgs := t.buildIptablesArgs(r, ActionCheck)
	addArgs := t.buildIptablesArgs(r, ActionAppend)

	// Python: rule.command_string = " ".join(shlex.quote(arg) for arg in add_args)
	quotedArgs := make([]string, len(addArgs))
	for i, arg := range addArgs {
		quotedArgs[i] = shlexQuote(arg)
	}
	cmdStr := strings.Join(quotedArgs, " ")
	r.CommandString = &cmdStr

	// Check if rule exists in database
	existingDBRule, err := t.repo.FindByAttributes(ctx,
		r.TableName,
		r.ChainName,
		r.RuleType,
		r.NetworkID,
		r.Protocol,
		r.Source,
		r.Destination,
		r.InInterface,
		r.OutInterface,
		r.SPort,
		r.DPort,
	)
	if err != nil {
		slog.Warn("Error querying iptables rule in DB", "error", err)
	}

	// Check if rule exists in iptables
	iptablesExists := false
	checkResult := system.RunCmdCompat(
		ctx,
		checkArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if checkResult.ExitCode == 0 {
		iptablesExists = true
	}

	// If rule exists in both DB and iptables, update verification timestamp
	if existingDBRule != nil && iptablesExists {
		if existingDBRule.ID != nil {
			_ = t.repo.UpdateVerifiedAt(ctx, *existingDBRule.ID)
		}
		return model.FirewallRuleResult{Success: true, Rule: existingDBRule}
	}

	// If rule exists in iptables but not in DB, record it
	if iptablesExists && existingDBRule == nil {
		recorded, err := t.repo.Insert(ctx, r)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to record rule: %v", err)
			return model.FirewallRuleResult{
				Success:      false,
				ErrorMessage: &errMsg,
			}
		}
		return model.FirewallRuleResult{Success: true, Rule: recorded}
	}

	// Create the rule in iptables
	addResult := system.RunCmdCompat(
		ctx,
		addArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: true},
	)
	if addResult.ExitCode != 0 {
		errMsg := fmt.Sprintf(
			"Failed to create rule: command %s failed (exit %d): %s",
			addArgs[0],
			addResult.ExitCode,
			addResult.Stderr,
		)
		return model.FirewallRuleResult{
			Success:         false,
			ErrorMessage:    &errMsg,
			CommandExecuted: &cmdStr,
		}
	}

	// Record in database (insert new or reactivate existing)
	var recorded *model.FirewallRule
	if existingDBRule != nil {
		if existingDBRule.ID != nil {
			_ = t.repo.UpdateVerifiedAt(ctx, *existingDBRule.ID)
		}
		r.ID = existingDBRule.ID
		recorded = r
	} else {
		recorded, err = t.repo.Insert(ctx, r)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to insert rule: %v", err)
			return model.FirewallRuleResult{
				Success:         false,
				ErrorMessage:    &errMsg,
				CommandExecuted: &cmdStr,
			}
		}
	}

	return model.FirewallRuleResult{
		Success:         true,
		Rule:            recorded,
		CommandExecuted: &cmdStr,
	}
}

// ── Remove rule ──
// Matches Python IPTablesTracker.remove_rule().

func (t *IPTablesTracker) RemoveRule(ctx context.Context, rule model.FirewallRule) model.FirewallRuleResult {
	dbRuleID := rule.ID
	// Matches Python's reference assignment: effective_rule = rule
	effectiveRule := &rule

	// Find the rule in database first to get its comment_tag
	if dbRuleID == nil {
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
			dbRuleID = existing.ID
			// Use the DB rule's comment_tag so iptables -D can match.
			if (rule.CommentTag == nil || *rule.CommentTag == "") && existing.CommentTag != nil {
				effectiveRule = existing
			}
		}
	}

	deleteArgs := t.buildIptablesArgs(effectiveRule, ActionDelete)
	var cmdStrPtr *string
	cmdStr := strings.Join(deleteArgs, " ")
	cmdStrPtr = &cmdStr

	// Remove from iptables
	deleteResult := system.RunCmdCompat(
		ctx,
		deleteArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)

	if deleteResult.ExitCode != 0 {
		// Deletion failed — try by line number
		if !t.removeByLineNumber(ctx, effectiveRule) {
			// Best-effort: check if any rule with these interfaces remains
			if t.ruleExistsByInterfaces(ctx, effectiveRule) {
				errMsg := fmt.Sprintf("Failed to remove rule: %s", deleteResult.Stderr)
				return model.FirewallRuleResult{
					Success:         false,
					ErrorMessage:    &errMsg,
					CommandExecuted: cmdStrPtr,
				}
			}
		}
	}

	// Mark as deleted in database if we found it
	if dbRuleID != nil {
		_ = t.repo.MarkDeleted(ctx, *dbRuleID)
	}

	return model.FirewallRuleResult{
		Success:         true,
		Rule:            effectiveRule,
		CommandExecuted: cmdStrPtr,
	}
}

// ── Remove by line number ──
// Matches Python IPTablesTracker._remove_by_line_number().

func (t *IPTablesTracker) removeByLineNumber(ctx context.Context, rule *model.FirewallRule) bool {
	listArgs := []string{
		"iptables", "-t", string(rule.TableName),
		"-L", string(rule.ChainName),
		"-n", "--line-numbers", "-v",
	}
	result := system.RunCmdCompat(
		ctx,
		listArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if result.ExitCode != 0 {
		return false
	}

	inIface := rule.InInterface
	outIface := rule.OutInterface
	if inIface == string(model.FirewallWildcardAnyInterface) {
		inIface = "*"
	}
	if outIface == string(model.FirewallWildcardAnyInterface) {
		outIface = "*"
	}

	for line := range strings.SplitSeq(strings.TrimRight(result.Stdout, "\n"), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 9 {
			continue
		}
		// Format: num pkts bytes target prot opt in out source destination
		if len(parts) >= 8 {
			lineIn := parts[6]
			lineOut := parts[7]
			if lineIn == inIface && lineOut == outIface {
				lineNum := parts[0]
				delArgs := []string{
					"iptables", "-t", string(rule.TableName),
					"-D", string(rule.ChainName),
					lineNum,
				}
				delResult := system.RunCmdCompat(
					ctx,
					delArgs,
					system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
				)
				return delResult.ExitCode == 0
			}
		}
	}
	return false
}

// ── Rule exists by interfaces ──
// Matches Python IPTablesTracker._rule_exists_by_interfaces().

func (t *IPTablesTracker) ruleExistsByInterfaces(ctx context.Context, rule *model.FirewallRule) bool {
	listArgs := []string{
		"iptables", "-t", string(rule.TableName),
		"-L", string(rule.ChainName),
		"-n", "-v",
	}
	result := system.RunCmdCompat(
		ctx,
		listArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if result.ExitCode != 0 {
		return false
	}

	inIface := rule.InInterface
	outIface := rule.OutInterface
	if inIface == string(model.FirewallWildcardAnyInterface) {
		inIface = "*"
	}
	if outIface == string(model.FirewallWildcardAnyInterface) {
		outIface = "*"
	}

	for line := range strings.SplitSeq(strings.TrimRight(result.Stdout, "\n"), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 9 {
			continue
		}
		if len(parts) >= 8 {
			lineIn := parts[6]
			lineOut := parts[7]
			if lineIn == inIface && lineOut == outIface {
				return true
			}
		}
	}
	return false
}

// ── Batch ensure rules ──
// Matches Python IPTablesTracker.batch_ensure_rules().

func (t *IPTablesTracker) BatchEnsureRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult {
	var filterRules []*model.FirewallRule
	var natRules []*model.FirewallRule

	for i := range rules {
		rule := rules[i]
		switch rule.TableName {
		case model.FirewallTableFilter:
			filterRules = append(filterRules, &rule)
		case model.FirewallTableNat:
			natRules = append(natRules, &rule)
		}
	}

	if len(filterRules) > 0 {
		restoreInput := t.buildRestoreInput(filterRules, "filter")
		result := system.RunCmdCompat(
			ctx,
			[]string{"iptables-restore", "-n"},
			system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: true, Input: restoreInput},
		)
		if result.ExitCode != 0 {
			errMsg := fmt.Sprintf("command iptables-restore -n failed (exit %d): %s", result.ExitCode, result.Stderr)
			return model.FirewallRuleResult{
				Success:      false,
				ErrorMessage: &errMsg,
			}
		}
	}

	if len(natRules) > 0 {
		restoreInput := t.buildRestoreInput(natRules, "nat")
		result := system.RunCmdCompat(
			ctx,
			[]string{"iptables-restore", "-n"},
			system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: true, Input: restoreInput},
		)
		if result.ExitCode != 0 {
			errMsg := fmt.Sprintf("command iptables-restore -n failed (exit %d): %s", result.ExitCode, result.Stderr)
			return model.FirewallRuleResult{
				Success:      false,
				ErrorMessage: &errMsg,
			}
		}
	}

	// DB sync: find existing rules, update verified_at, insert new ones.
	// Uses a single compound query instead of N individual lookups.
	// This also fixes a bug in the Python original where iptables batch
	// did not update last_verified_at for existing rules.
	var ptrs []*model.FirewallRule
	for i := range rules {
		ptrs = append(ptrs, &rules[i])
	}
	if _, err := t.repo.FindAndUpsertRules(ctx, ptrs); err != nil {
		slog.Error("Failed to sync iptables rules in DB after batch", "error", err)
	}

	return model.FirewallRuleResult{Success: true}
}

// ── Batch remove rules ──
// Matches Python IPTablesTracker.batch_remove_rules().

func (t *IPTablesTracker) BatchRemoveRules(ctx context.Context, rules []model.FirewallRule) model.FirewallRuleResult {
	for _, rule := range rules {
		t.RemoveRule(ctx, rule)
	}
	return model.FirewallRuleResult{Success: true}
}

// ── Count orphaned rules ──
// Matches Python IPTablesTracker.count_orphaned_rules().

func (t *IPTablesTracker) CountOrphanedRules(ctx context.Context, network *model.Network) int {
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

	result := system.RunCmdCompat(
		ctx,
		[]string{"iptables-save"},
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if result.ExitCode != 0 {
		return 0
	}

	orphaned := 0
	for line := range strings.SplitSeq(strings.TrimRight(result.Stdout, "\n"), "\n") {
		if !strings.HasPrefix(line, "-A MVM-") {
			continue
		}
		// Use strings.Fields for whitespace splitting (equivalent to shlex.split()
		// for MVM comments which use colons, not spaces).
		parts := strings.Fields(line)
		var comment string
		for i, part := range parts {
			if part == "--comment" && i+1 < len(parts) {
				comment = parts[i+1]
				break
			}
		}
		if comment != "" && strings.Contains(comment, network.Name) && !dbComments[comment] {
			orphaned++
			slog.Warn("Orphaned iptables rule on host for network",
				"network", network.Name,
				"rule", line,
			)
		}
	}

	return orphaned
}

// ── Ensure chain ──
// Matches Python IPTablesTracker.ensure_chain().

func (t *IPTablesTracker) EnsureChain(
	ctx context.Context,
	chainName model.FirewallChain,
	table model.FirewallTable,
	autoJumpFrom string,
	position int,
) bool {
	chainNameStr := string(chainName)
	tableStr := string(table)

	// Check if chain exists
	checkArgs := []string{"iptables", "-t", tableStr, "-L", chainNameStr, "-n"}
	checkResult := system.RunCmdCompat(
		ctx,
		checkArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if checkResult.ExitCode == 0 {
		slog.Debug("Chain already exists", "chain", chainNameStr)
		return false
	}

	// Create the chain
	createArgs := []string{"iptables", "-t", tableStr, "-N", chainNameStr}
	createResult := system.RunCmdCompat(
		ctx,
		createArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if createResult.ExitCode != 0 {
		// Check if chain already exists (race condition)
		errStr := fmt.Sprintf(
			"command %s failed (exit %d): %s",
			createArgs[0],
			createResult.ExitCode,
			createResult.Stderr,
		)
		if strings.Contains(errStr, "Chain already exists") {
			slog.Debug("Chain already exists", "chain", chainNameStr)
			return false
		}
		slog.Warn("Failed to create chain", "chain", chainNameStr)
		return false
	}

	slog.Info("Created iptables chain", "chain", chainNameStr)

	// Add jump rule if requested
	if autoJumpFrom != "" {
		jumpResult := t.EnsureJumpRule(ctx, autoJumpFrom, chainNameStr, table, position)
		if !jumpResult.Success {
			errMsg := ""
			if jumpResult.ErrorMessage != nil {
				errMsg = *jumpResult.ErrorMessage
			}
			slog.Warn("Failed to add jump rule",
				"from", autoJumpFrom,
				"to", chainNameStr,
				"error", errMsg,
			)
			return false
		}
	}

	return true
}

// ── Ensure jump rule ──
// Matches Python IPTablesTracker.ensure_jump_rule().

func (t *IPTablesTracker) EnsureJumpRule(
	ctx context.Context,
	fromChain, toChain string,
	table model.FirewallTable,
	position int,
) model.FirewallRuleResult {
	tableStr := string(table)

	// Check if jump rule exists
	checkArgs := []string{"iptables", "-t", tableStr, "-C", fromChain, "-j", toChain}
	checkResult := system.RunCmdCompat(
		ctx,
		checkArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if checkResult.ExitCode == 0 {
		slog.Debug("Jump rule already exists", "from", fromChain, "to", toChain)
		return model.FirewallRuleResult{Success: true}
	}

	// Insert jump rule at specified position
	insertArgs := []string{
		"iptables", "-t", tableStr,
		"-I", fromChain, strconv.Itoa(position),
		"-j", toChain,
	}
	insertResult := system.RunCmdCompat(
		ctx,
		insertArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: true},
	)
	if insertResult.ExitCode != 0 {
		errMsg := fmt.Sprintf("Failed to add jump rule %s -> %s: %s", fromChain, toChain, insertResult.Stderr)
		slog.Error("Failed to add jump rule", "from", fromChain, "to", toChain, "error", insertResult.Stderr)
		return model.FirewallRuleResult{
			Success:      false,
			ErrorMessage: &errMsg,
		}
	}

	slog.Info("Inserted jump rule", "from", fromChain, "to", toChain, "position", position)
	cmdStr := strings.Join(insertArgs, " ")
	return model.FirewallRuleResult{
		Success:         true,
		CommandExecuted: &cmdStr,
	}
}

// ── Teardown ──
// Matches Python IPTablesTracker.teardown().

func (t *IPTablesTracker) Teardown(ctx context.Context) {
	type chainDef struct {
		chain    model.FirewallChain
		table    model.FirewallTable
		jumpFrom string
	}
	chains := []chainDef{
		{model.FirewallChainMVMForward, model.FirewallTableFilter, "FORWARD"},
		{model.FirewallChainMVMPostrouting, model.FirewallTableNat, "POSTROUTING"},
		{model.FirewallChainMVMNocloudNetIn, model.FirewallTableFilter, "INPUT"},
	}
	for _, c := range chains {
		chainName := string(c.chain)
		table := string(c.table)

		// 1. Delete the jump rule from the parent chain
		system.RunCmdCompat(ctx,
			[]string{"iptables", "-t", table, "-D", c.jumpFrom, "-j", chainName},
			system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
		)

		// 2. Flush the custom chain
		system.RunCmdCompat(ctx,
			[]string{"iptables", "-t", table, "-F", chainName},
			system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
		)

		// 3. Delete the custom chain
		system.RunCmdCompat(ctx,
			[]string{"iptables", "-t", table, "-X", chainName},
			system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
		)
	}
}

// ── Flush chain ──
// Matches Python IPTablesTracker.flush_chain().

func (t *IPTablesTracker) FlushChain(
	ctx context.Context,
	chainName model.FirewallChain,
	table model.FirewallTable,
) bool {
	chainNameStr := string(chainName)
	tableStr := string(table)

	// Check if chain exists first
	checkArgs := []string{"iptables", "-t", tableStr, "-L", chainNameStr, "-n"}
	checkResult := system.RunCmdCompat(
		ctx,
		checkArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if checkResult.ExitCode != 0 {
		slog.Debug("Chain doesn't exist, nothing to flush", "chain", chainNameStr)
		return false
	}

	// Flush the chain in iptables
	flushArgs := []string{"iptables", "-t", tableStr, "-F", chainNameStr}
	flushResult := system.RunCmdCompat(
		ctx,
		flushArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: true},
	)
	if flushResult.ExitCode != 0 {
		slog.Warn("Failed to flush chain", "chain", chainNameStr)
		return false
	}

	slog.Info("Flushed all rules from chain", "chain", chainNameStr)

	// Mark all rules for this chain as deleted in database
	deleted, err := t.repo.MarkDeletedByTableChainName(ctx, chainName, table)
	if err != nil {
		slog.Warn("Failed to mark rules as deleted for chain",
			"chain", chainNameStr,
			"error", err,
		)
	} else {
		slog.Debug("Marked rules as deleted for chain",
			"count", deleted,
			"chain", chainNameStr,
		)
	}

	return true
}

// ── Remove chain ──
// Matches Python IPTablesTracker.remove_chain().

func (t *IPTablesTracker) RemoveChain(
	ctx context.Context,
	chainName model.FirewallChain,
	table model.FirewallTable,
) bool {
	chainNameStr := string(chainName)
	tableStr := string(table)

	// Check if chain exists
	checkArgs := []string{"iptables", "-t", tableStr, "-L", chainNameStr, "-n"}
	checkResult := system.RunCmdCompat(
		ctx,
		checkArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: false},
	)
	if checkResult.ExitCode != 0 {
		slog.Debug("Chain doesn't exist, nothing to remove", "chain", chainNameStr)
		return false
	}

	// Mark all rules for this chain as deleted in database
	_, _ = t.repo.MarkDeletedByTableChainName(ctx, chainName, table)

	// Delete the chain
	deleteArgs := []string{"iptables", "-t", tableStr, "-X", chainNameStr}
	deleteResult := system.RunCmdCompat(
		ctx,
		deleteArgs,
		system.RunCmdOptions{Privileged: true, Capture: true, Text: true, Check: true},
	)
	if deleteResult.ExitCode != 0 {
		slog.Warn(
			"Failed to delete chain",
			"chain",
			chainNameStr,
			"error",
			fmt.Sprintf("command %s failed (exit %d): %s", deleteArgs[0], deleteResult.ExitCode, deleteResult.Stderr),
		)
		return false
	}

	slog.Info("Deleted chain", "chain", chainNameStr)
	return true
}
