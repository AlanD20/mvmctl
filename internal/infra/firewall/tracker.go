package firewall

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"mvmctl/internal/infra/system"
)

// ── Firewall enums ──
// Matches src/mvmctl/models/network.py exactly.

// FirewallBackendType selects the firewall implementation.
type FirewallBackendType string

const (
	BackendNFTables FirewallBackendType = "nftables"
	BackendIPTables FirewallBackendType = "iptables"
)

// FirewallTable names — matches Python FirewallTable.
type FirewallTable string

const (
	TableFilter   FirewallTable = "filter"
	TableNat      FirewallTable = "nat"
	TableMangle   FirewallTable = "mangle"
	TableRaw      FirewallTable = "raw"
	TableSecurity FirewallTable = "security"
)

// FirewallChain names — matches Python FirewallChain.
// These are constants, not init()-resolved vars. The CLI name is always "mvm"
// (verdict #12: no init() globals). Matches Python's module-load-time string
// interpolation: f"{CLI_NAME.upper()}-FORWARD".
// Python computes these at import time via os.path.basename(sys.argv[0]),
// but resolveCLIName() always returns "mvm", so we hardcode the chain names.
type FirewallChain string

const (
	ChainMVMForward      FirewallChain = "MVM-FORWARD"
	ChainMVMPostrouting  FirewallChain = "MVM-POSTROUTING"
	ChainMVMNocloudnetIn FirewallChain = "MVM-NOCLOUDNET-INPUT"
)

// FirewallRuleType — matches Python FirewallRuleType.
type FirewallRuleType string

const (
	RuleTypeMasquerade      FirewallRuleType = "masquerade"
	RuleTypeForwardIn       FirewallRuleType = "forward_in"
	RuleTypeForwardOut      FirewallRuleType = "forward_out"
	RuleTypeNocloudnetInput FirewallRuleType = "nocloudnet_input"
)

// FirewallProtocol — matches Python FirewallProtocol.
type FirewallProtocol string

const (
	ProtoTCP  FirewallProtocol = "tcp"
	ProtoUDP  FirewallProtocol = "udp"
	ProtoICMP FirewallProtocol = "icmp"
	ProtoAll  FirewallProtocol = "all"
)

// FirewallTarget — matches Python FirewallTarget.
type FirewallTarget string

const (
	TargetMasquerade FirewallTarget = "MASQUERADE"
	TargetAccept     FirewallTarget = "ACCEPT"
	TargetDrop       FirewallTarget = "DROP"
	TargetReject     FirewallTarget = "REJECT"
	TargetLog        FirewallTarget = "LOG"
	TargetMark       FirewallTarget = "MARK"
)

// FirewallWildcard — matches Python FirewallWildcard.
type FirewallWildcard string

const (
	WildcardAnyCIDR      FirewallWildcard = "0.0.0.0/0"
	WildcardAnyInterface FirewallWildcard = "*"
)

// FirewallPort sentinel — matches Python FirewallPort.ANY.
const FirewallPortAny = 0

// CommentPrefix used in iptables comment tags.
const CommentPrefix = "mvm"

// MaxCommentLen maximum length for iptables comment tags.
// Matches Python CONST_IPTABLES_MAX_COMMENT_LEN.
const MaxCommentLen = 240

// ── splitLines ──
// Matches Python's str.splitlines() behavior: splits on \n, \r\n, \r.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	// Normalize \r\n and standalone \r to \n
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// Trim trailing newline to avoid empty trailing element
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ── processErrorMsg ──
// Builds an error message matching Python's ``str(ProcessError)`` format:
//
//	"Command failed (exit <code>): <binary_name>"
//	+ optional "\n<stderr_preview>"
//
// Matches src/mvmctl/utils/_system.py run_cmd() line 473.
// Python shows only args[0] and truncates stderr to 100 chars.
func processErrorMsg(args []string, code int, stderr string) string {
	var binary string
	if len(args) > 0 {
		binary = args[0]
	}
	msg := fmt.Sprintf("Command failed (exit %d): %s", code, binary)
	cleaned := sanitizeStderr(stderr)
	if cleaned != "" {
		msg += "\n" + cleaned
	}
	return msg
}

const stderrPreviewLimit = 100

// sanitizeStderr matches Python's _sanitize_stderr() in _system.py.
func sanitizeStderr(stderr string) string {
	cleaned := strings.TrimSpace(stderr)
	if len(cleaned) > stderrPreviewLimit {
		return cleaned[:stderrPreviewLimit] + "..."
	}
	return cleaned
}

// ── FirewallRule ──
// Matches src/mvmctl/models/network.py FirewallRule dataclass.

type FirewallRule struct {
	TableName    FirewallTable    `json:"table_name"`
	ChainName    FirewallChain    `json:"chain_name"`
	RuleType     FirewallRuleType `json:"rule_type"`
	Protocol     FirewallProtocol `json:"protocol"`
	Source       string           `json:"source"`
	Destination  string           `json:"destination"`
	InInterface  string           `json:"in_interface"`
	OutInterface string           `json:"out_interface"`
	Target       FirewallTarget   `json:"target"`
	SPort        int              `json:"sport"`
	DPort        int              `json:"dport"`
	NetworkID    string           `json:"network_id"`
	IsActive     bool             `json:"is_active"`

	ID             *int64  `json:"id,omitempty"`
	NetworkName    *string `json:"network_name,omitempty"`
	CommentTag     *string `json:"comment_tag,omitempty"`
	CommandString  *string `json:"command_string,omitempty"`
	CreatedAt      *string `json:"created_at,omitempty"`
	LastVerifiedAt *string `json:"last_verified_at,omitempty"`
}

// ── FirewallRuleResult ──
// Matches src/mvmctl/models/network.py FirewallRuleResult.
//
//	command_executed is *string to match Python's ``str | None``.

type FirewallRuleResult struct {
	Success         bool           `json:"success"`
	Rule            *FirewallRule  `json:"rule,omitempty"`
	ErrorMessage    *string        `json:"error_message,omitempty"`
	CommandExecuted *string        `json:"command_executed,omitempty"`
}

// NetworkRef is a minimal reference to a network used by firewall operations.
// Matches Python's use of NetworkItem for count_orphaned_rules.
type NetworkRef struct {
	ID   string
	Name string
}

// ── Tracker interface ──
// All methods that both IPTablesTracker and NFTablesTracker must implement.
// Matches the public methods of Python's IPTablesTracker and NFTablesTracker.

type Tracker interface {
	Initialize()
	Teardown()
	EnsureRule(rule FirewallRule, context string) FirewallRuleResult
	BatchEnsureRules(rules []FirewallRule) FirewallRuleResult
	RemoveRule(rule FirewallRule) FirewallRuleResult
	BatchRemoveRules(rules []FirewallRule) FirewallRuleResult
	EnsureChain(chainName FirewallChain, table FirewallTable, autoJumpFrom string, position int) bool
	FlushChain(chainName FirewallChain, table FirewallTable) bool
	CountOrphanedRules(network NetworkRef) int
}

// ── runFirewallCmd helper ──
// Runs a privileged firewall command with optional check.
// Matches Python's run_cmd(privileged=True, check=check).

type firewallCmdResult struct {
	stdout     string
	stderr     string
	returnCode int
}

func runFirewallCmd(args []string, check bool) *firewallCmdResult {
	if len(args) == 0 {
		return &firewallCmdResult{returnCode: 1}
	}

	result := system.RunCmdCompat(context.Background(), args, system.RunCmdOptions{
		Privileged: true,
		Check:      check,
		Capture:    true,
		Text:       true,
	})

	fwResult := &firewallCmdResult{
		stdout:     result.Stdout,
		stderr:     result.Stderr,
		returnCode: result.ExitCode,
	}

	if check && !result.Success {
		slog.Warn("Firewall command failed",
			"cmd", args[0],
			"args", args[1:],
			"exit_code", fwResult.returnCode,
			"stderr", result.Stderr,
		)
	}
	return fwResult
}

// runFirewallCmdWithInput runs a command with stdin input.
func runFirewallCmdWithInput(args []string, input string, check bool) *firewallCmdResult {
	if len(args) == 0 {
		return &firewallCmdResult{returnCode: 1}
	}

	result := system.RunCmdCompat(context.Background(), args, system.RunCmdOptions{
		Privileged: true,
		Check:      check,
		Capture:    true,
		Text:       true,
		Input:      input,
	})

	fwResult := &firewallCmdResult{
		stdout:     result.Stdout,
		stderr:     result.Stderr,
		returnCode: result.ExitCode,
	}

	if check && !result.Success {
		slog.Warn("Firewall command failed",
			"cmd", args[0],
			"args", args[1:],
			"exit_code", fwResult.returnCode,
			"stderr", result.Stderr,
		)
	}
	return fwResult
}

// ── FirewallTracker (dispatcher) ──
// Matches src/mvmctl/core/_shared/_firewall_tracker.py FirewallTracker.

type FirewallTracker struct {
	db         *sql.DB
	fwRepo     any // *IPTablesRuleRepository or *NFTablesRuleRepository
	backend    Tracker
	batchMode  bool
	batchRules []FirewallRule
}

// NewFirewallTracker creates a FirewallTracker, resolving the backend from
// the user_settings table — matching Python's FirewallTracker.__init__() which
// resolves via SettingsService.resolve(self._db, "settings", "firewall_backend").
func NewFirewallTracker(db *sql.DB) (*FirewallTracker, error) {
	ft := &FirewallTracker{
		db: db,
	}

	// Resolve firewall_backend from settings (Python: SettingsService.resolve)
	var backend string
	err := db.QueryRow(
		"SELECT value FROM user_settings WHERE category = 'settings' AND key = 'firewall_backend'",
	).Scan(&backend)
	if err != nil {
		// Default to nftables when settings table doesn't exist or key is missing.
		backend = "nftables"
	}

	// Also try to read iptables_xtcomment setting
	var xtcommentStr string
	xtcommentAvail := true // Python constants.py defaults "iptables_xtcomment": True
	err = db.QueryRow(
		"SELECT value FROM user_settings WHERE category = 'settings.firewall' AND key = 'iptables_xtcomment'",
	).Scan(&xtcommentStr)
	if err == nil {
		xtcommentAvail = xtcommentStr == "1" || xtcommentStr == "true"
	}

	switch strings.ToLower(backend) {
	case "nftables":
		repo := NewNFTablesRuleRepository(db)
		ft.fwRepo = repo
		ft.backend = NewNFTablesTracker(repo)
	default:
		repo := NewIPTablesRuleRepository(db)
		ft.fwRepo = repo
		ft.backend = NewIPTablesTracker(repo, xtcommentAvail)
	}

	return ft, nil
}

// ── Batch context ──

// flushBatch processes all queued batch rules through the backend, then
// resets batch mode.  Called by FirewallBatch.Close().
func (ft *FirewallTracker) flushBatch() FirewallRuleResult {
	ft.batchMode = false
	if len(ft.batchRules) == 0 {
		return FirewallRuleResult{Success: true}
	}
	result := ft.backend.BatchEnsureRules(ft.batchRules)
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

// Batch begins a batch context — queued ensure_rule calls are flushed on
// scope exit.  Matches Python's “with tracker.batch():“ protocol.
func (ft *FirewallTracker) Batch() *FirewallBatch {
	ft.batchMode = true
	ft.batchRules = ft.batchRules[:0] // clear without reallocating
	return &FirewallBatch{tracker: ft}
}

// FirewallBatch is returned by FirewallTracker.Batch().  Callers add rules
// via tracker.EnsureRule() (which queues them due to batchMode), then call
// Close() to atomically apply them.
// Matches Python's context manager: flush on scope exit.
type FirewallBatch struct {
	tracker *FirewallTracker
	flushed bool
}

// Close auto-flushes any remaining batch rules on scope exit.
// Matches Python's finally: block in @contextmanager batch().
// Callers should use “defer batch.Close()“.
func (b *FirewallBatch) Close() {
	if b.flushed {
		return
	}
	b.flushed = true
	b.tracker.flushBatch()
}

// ── Rule lifecycle (delegates to backend) ──

func (ft *FirewallTracker) EnsureRule(rule FirewallRule, context string) FirewallRuleResult {
	if ft.batchMode {
		ft.batchRules = append(ft.batchRules, rule)
		return FirewallRuleResult{Success: true}
	}
	return ft.backend.EnsureRule(rule, context)
}

func (ft *FirewallTracker) BatchEnsureRules(rules []FirewallRule) FirewallRuleResult {
	return ft.backend.BatchEnsureRules(rules)
}

func (ft *FirewallTracker) RemoveRule(rule FirewallRule) FirewallRuleResult {
	return ft.backend.RemoveRule(rule)
}

func (ft *FirewallTracker) BatchRemoveRules(rules []FirewallRule) FirewallRuleResult {
	return ft.backend.BatchRemoveRules(rules)
}

func (ft *FirewallTracker) CountOrphanedRules(network NetworkRef) int {
	return ft.backend.CountOrphanedRules(network)
}

// ── Chain lifecycle (delegates to backend) ──

func (ft *FirewallTracker) EnsureChain(chainName FirewallChain, table FirewallTable, autoJumpFrom string, position int) bool {
	return ft.backend.EnsureChain(chainName, table, autoJumpFrom, position)
}

func (ft *FirewallTracker) FlushChain(chainName FirewallChain, table FirewallTable) bool {
	return ft.backend.FlushChain(chainName, table)
}

func (ft *FirewallTracker) Initialize() {
	ft.backend.Initialize()
}

func (ft *FirewallTracker) Teardown() {
	ft.backend.Teardown()
}

// Repo returns the active firewall rule repository.
// Matches Python's ``FirewallTracker.repo`` property.
// Callers can type-assert to *IPTablesRuleRepository or *NFTablesRuleRepository.
func (ft *FirewallTracker) Repo() any {
	return ft.fwRepo
}
