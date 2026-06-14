package firewall

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"mvmctl/internal/lib/model"
)

// ─── Helpers ───────────────────────────────────────────────────────────────────

// iptablesBaseArgs returns the fixed prefix for every buildIptablesArgs call.
func iptablesBaseArgs(action RuleAction, table model.FirewallTable, chain model.FirewallChain) []string {
	return []string{"iptables", "-t", string(table), string(action), string(chain)}
}

// ─── buildComment ──────────────────────────────────────────────────────────────
// Rationale: buildComment generates iptables comment tags used to identify and
// match rules during deletion and orphan detection. Incorrect format or truncation
// would cause rule deletion to silently fail or orphan rules to go undetected.

func TestBuildComment(t *testing.T) {
	tracker := NewIPTablesTracker(nil, false)

	tests := map[string]struct {
		ruleType     model.FirewallRuleType
		networkName  string
		contextLabel string
		want         string
	}{
		"basic_without_context": {
			ruleType:     model.FirewallRuleTypeMasquerade,
			networkName:  "test-net",
			contextLabel: "",
			want:         "mvm:masquerade:test-net",
		},
		"basic_with_context": {
			ruleType:     model.FirewallRuleTypeForwardOut,
			networkName:  "br-mybridge",
			contextLabel: "n-abc123",
			want:         "mvm:forward_out:br-mybridge:n-abc123",
		},
		"nocloudnet_rule_type": {
			ruleType:     model.FirewallRuleTypeNocloudNetInput,
			networkName:  "cloud-net",
			contextLabel: "",
			want:         "mvm:nocloudnet_input:cloud-net",
		},
		"empty_network_name": {
			ruleType:     model.FirewallRuleTypeForwardIn,
			networkName:  "",
			contextLabel: "ctx",
			want:         "mvm:forward_in::ctx",
		},
		"truncate_without_context": {
			ruleType:     model.FirewallRuleTypeForwardIn,
			networkName:  strings.Repeat("x", 230),
			contextLabel: "",
			want:         "mvm:" + string(model.FirewallRuleTypeForwardIn) + ":" + strings.Repeat("x", 225),
		},
		"truncate_with_context": {
			ruleType:     model.FirewallRuleTypeMasquerade,
			networkName:  strings.Repeat("x", 200),
			contextLabel: strings.Repeat("y", 40),
			want:         "mvm:masquerade:" + strings.Repeat("x", 200) + ":" + strings.Repeat("y", 24),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := tracker.buildComment(tc.ruleType, tc.networkName, tc.contextLabel)

			// Truncation tests must also verify exact length
			if len(tc.want) == MaxCommentLen {
				assert.Equal(t, MaxCommentLen, len(got), "truncated comment must equal MaxCommentLen")
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildComment() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── buildIptablesArgs ─────────────────────────────────────────────────────────
// Rationale: buildIptablesArgs generates the correct iptables command-line
// arguments for check, append, and delete operations. Wrong arguments cause
// iptables to silently fail or modify the wrong rule.

func TestBuildIptablesArgs(t *testing.T) {
	// Base minimal rule (all wildcards, no comment)
	baseRule := &model.FirewallRule{
		TableName:    model.FirewallTableFilter,
		ChainName:    model.FirewallChainMVMForward,
		RuleType:     model.FirewallRuleTypeForwardOut,
		Protocol:     model.FirewallProtocolAll,
		Source:       string(model.FirewallWildcardAnyCIDR),
		Destination:  string(model.FirewallWildcardAnyCIDR),
		InInterface:  string(model.FirewallWildcardAnyInterface),
		OutInterface: string(model.FirewallWildcardAnyInterface),
		Target:       model.FirewallTargetAccept,
		SPort:        model.FirewallPortAny,
		DPort:        model.FirewallPortAny,
	}

	tests := map[string]struct {
		modify    func(*model.FirewallRule)
		action    RuleAction
		xtcomment bool
		appendFn  func(base []string) []string // appends to base args
	}{
		// ── Actions ──
		"action_append": {
			action:   ActionAppend,
			appendFn: func(b []string) []string { return append(b, "-j", "ACCEPT") },
		},
		"action_check": {
			action:   ActionCheck,
			appendFn: func(b []string) []string { return append(b, "-j", "ACCEPT") },
		},
		"action_delete": {
			action:   ActionDelete,
			appendFn: func(b []string) []string { return append(b, "-j", "ACCEPT") },
		},

		// ── Protocol variations ──
		"protocol_tcp": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolTCP },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-p", "tcp", "-j", "ACCEPT")
			},
		},
		"protocol_udp": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolUDP },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-p", "udp", "-j", "ACCEPT")
			},
		},
		// ── Boundary: custom protocol string (not ALL/TCP/UDP) ──
		"protocol_custom_string": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocol("sctp") },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-p", "sctp", "-j", "ACCEPT")
			},
		},

		// ── Source / Destination ──
		"source_specific": {
			modify: func(r *model.FirewallRule) { r.Source = "10.0.0.0/24" },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-s", "10.0.0.0/24", "-j", "ACCEPT")
			},
		},
		"destination_specific": {
			modify: func(r *model.FirewallRule) { r.Destination = "192.168.1.0/24" },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-d", "192.168.1.0/24", "-j", "ACCEPT")
			},
		},

		// ── Interface variations ──
		"in_interface_specific": {
			modify: func(r *model.FirewallRule) { r.InInterface = "eth0" },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-i", "eth0", "-j", "ACCEPT")
			},
		},
		"out_interface_specific": {
			modify: func(r *model.FirewallRule) { r.OutInterface = "tap0" },
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-o", "tap0", "-j", "ACCEPT")
			},
		},

		// ── Port variations ──
		"source_port_specific": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.SPort = 80
			},
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-p", "tcp", "--sport", "80", "-j", "ACCEPT")
			},
		},
		"dest_port_specific": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolUDP
				r.DPort = 53
			},
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b, "-p", "udp", "--dport", "53", "-j", "ACCEPT")
			},
		},

		// ── Target variations ──
		"target_drop": {
			modify:   func(r *model.FirewallRule) { r.Target = model.FirewallTargetDrop },
			action:   ActionAppend,
			appendFn: func(b []string) []string { return append(b, "-j", "DROP") },
		},
		"target_masquerade": {
			modify:   func(r *model.FirewallRule) { r.Target = model.FirewallTargetMasquerade },
			action:   ActionAppend,
			appendFn: func(b []string) []string { return append(b, "-j", "MASQUERADE") },
		},

		// ── Combine all fields ──
		"all_fields_set": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.Source = "10.0.0.0/24"
				r.Destination = "192.168.1.0/24"
				r.InInterface = "eth0"
				r.OutInterface = "tap0"
				r.SPort = 8080
				r.DPort = 443
				r.Target = model.FirewallTargetDrop
			},
			action: ActionAppend,
			appendFn: func(b []string) []string {
				return append(b,
					"-p", "tcp",
					"-s", "10.0.0.0/24",
					"-d", "192.168.1.0/24",
					"-i", "eth0",
					"-o", "tap0",
					"--sport", "8080",
					"--dport", "443",
					"-j", "DROP",
				)
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rule := *baseRule // copy
			if tc.modify != nil {
				tc.modify(&rule)
			}

			tracker := NewIPTablesTracker(nil, tc.xtcomment)
			baseArgs := iptablesBaseArgs(tc.action, baseRule.TableName, baseRule.ChainName)
			want := tc.appendFn(baseArgs)

			got := tracker.buildIptablesArgs(&rule, tc.action)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("buildIptablesArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── buildIptablesArgs — comment variations ────────────────────────────────────
// Rationale: Comment inclusion depends on both rule.CommentTag and the tracker's
// xtcommentAvailable flag. Wrong comment handling causes orphan detection to
// break or deletion to fail by argument mismatch.

func TestBuildIptablesArgs_comments(t *testing.T) {
	baseRule := &model.FirewallRule{
		TableName:    model.FirewallTableFilter,
		ChainName:    model.FirewallChainMVMForward,
		RuleType:     model.FirewallRuleTypeForwardOut,
		Protocol:     model.FirewallProtocolAll,
		Source:       string(model.FirewallWildcardAnyCIDR),
		Destination:  string(model.FirewallWildcardAnyCIDR),
		InInterface:  string(model.FirewallWildcardAnyInterface),
		OutInterface: string(model.FirewallWildcardAnyInterface),
		Target:       model.FirewallTargetAccept,
		SPort:        model.FirewallPortAny,
		DPort:        model.FirewallPortAny,
	}
	baseArgs := iptablesBaseArgs(ActionAppend, baseRule.TableName, baseRule.ChainName)
	tail := []string{"-j", "ACCEPT"}

	tests := map[string]struct {
		commentTag *string
		xtcomment  bool
		want       []string
	}{
		"comment_nil": {
			commentTag: nil,
			xtcomment:  true,
			want:       append(baseArgs, tail...),
		},
		"comment_empty_string": {
			commentTag: strPtr(""),
			xtcomment:  true,
			want:       append(baseArgs, tail...),
		},
		"comment_valid_xtcomment_true": {
			commentTag: strPtr("mvm:forward_out:test-net"),
			xtcomment:  true,
			want: append(baseArgs, append(tail,
				"-m", "comment", "--comment", "mvm:forward_out:test-net")...),
		},
		"comment_valid_xtcomment_false": {
			commentTag: strPtr("mvm:forward_out:test-net"),
			xtcomment:  false,
			want:       append(baseArgs, tail...),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rule := *baseRule
			rule.CommentTag = tc.commentTag

			tracker := NewIPTablesTracker(nil, tc.xtcomment)
			got := tracker.buildIptablesArgs(&rule, ActionAppend)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildIptablesArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── buildRestoreLine ──────────────────────────────────────────────────────────
// Rationale: buildRestoreLine produces the space-joined iptables-restore line
// used in batch mode. Mismatched format causes iptables-restore to reject the
// entire input and silently drop ALL rules.

func TestBuildRestoreLine(t *testing.T) {
	baseRule := &model.FirewallRule{
		TableName:    model.FirewallTableFilter,
		ChainName:    model.FirewallChainMVMForward,
		RuleType:     model.FirewallRuleTypeForwardOut,
		Protocol:     model.FirewallProtocolAll,
		Source:       string(model.FirewallWildcardAnyCIDR),
		Destination:  string(model.FirewallWildcardAnyCIDR),
		InInterface:  string(model.FirewallWildcardAnyInterface),
		OutInterface: string(model.FirewallWildcardAnyInterface),
		Target:       model.FirewallTargetAccept,
		SPort:        model.FirewallPortAny,
		DPort:        model.FirewallPortAny,
	}
	wildTail := "-j ACCEPT"

	tests := map[string]struct {
		modify    func(*model.FirewallRule)
		xtcomment bool
		want      string
	}{
		"minimal_rule": {
			want: "-A MVM-FORWARD " + wildTail,
		},
		"protocol_tcp": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolTCP },
			want:   "-A MVM-FORWARD -p tcp -j ACCEPT",
		},
		"protocol_udp": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolUDP },
			want:   "-A MVM-FORWARD -p udp -j ACCEPT",
		},
		"source_specific": {
			modify: func(r *model.FirewallRule) { r.Source = "10.0.0.0/24" },
			want:   "-A MVM-FORWARD -s 10.0.0.0/24 -j ACCEPT",
		},
		"destination_specific": {
			modify: func(r *model.FirewallRule) { r.Destination = "192.168.1.0/24" },
			want:   "-A MVM-FORWARD -d 192.168.1.0/24 -j ACCEPT",
		},
		"in_interface_specific": {
			modify: func(r *model.FirewallRule) { r.InInterface = "eth0" },
			want:   "-A MVM-FORWARD -i eth0 -j ACCEPT",
		},
		"out_interface_specific": {
			modify: func(r *model.FirewallRule) { r.OutInterface = "tap0" },
			want:   "-A MVM-FORWARD -o tap0 -j ACCEPT",
		},
		"source_port_specific": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.SPort = 80
			},
			want: "-A MVM-FORWARD -p tcp --sport 80 -j ACCEPT",
		},
		"dest_port_specific": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolUDP
				r.DPort = 53
			},
			want: "-A MVM-FORWARD -p udp --dport 53 -j ACCEPT",
		},
		"target_drop": {
			modify: func(r *model.FirewallRule) { r.Target = model.FirewallTargetDrop },
			want:   "-A MVM-FORWARD -j DROP",
		},
		"target_masquerade": {
			modify: func(r *model.FirewallRule) {
				r.Target = model.FirewallTargetMasquerade
				r.Protocol = model.FirewallProtocolAll
				r.Source = string(model.FirewallWildcardAnyCIDR)
				r.Destination = string(model.FirewallWildcardAnyCIDR)
				r.InInterface = string(model.FirewallWildcardAnyInterface)
				r.OutInterface = string(model.FirewallWildcardAnyInterface)
				r.SPort = model.FirewallPortAny
				r.DPort = model.FirewallPortAny
			},
			want: "-A MVM-FORWARD -j MASQUERADE",
		},
		"comment_valid_xtcomment_true": {
			modify: func(r *model.FirewallRule) {
				r.CommentTag = strPtr("mvm:forward_out:test-net")
			},
			xtcomment: true,
			want:      "-A MVM-FORWARD -j ACCEPT -m comment --comment mvm:forward_out:test-net",
		},
		"comment_nil_xtcomment_true": {
			xtcomment: true,
			want:      "-A MVM-FORWARD -j ACCEPT",
		},
		"all_fields_set": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.Source = "10.0.0.0/24"
				r.Destination = "192.168.1.0/24"
				r.InInterface = "eth0"
				r.OutInterface = "tap0"
				r.SPort = 8080
				r.DPort = 443
				r.Target = model.FirewallTargetDrop
			},
			want: "-A MVM-FORWARD -p tcp -s 10.0.0.0/24 -d 192.168.1.0/24 -i eth0 -o tap0 --sport 8080 --dport 443 -j DROP",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rule := *baseRule
			if tc.modify != nil {
				tc.modify(&rule)
			}

			tracker := NewIPTablesTracker(nil, tc.xtcomment)
			got := tracker.buildRestoreLine(&rule)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildRestoreLine() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── buildRestoreInput ─────────────────────────────────────────────────────────
// Rationale: buildRestoreInput assembles the complete iptables-restore input for
// batch operations. Filter tables get conntrack rules; nat tables must NOT get
// conntrack rules. Missing or extra conntrack rules break production traffic.

func TestBuildRestoreInput(t *testing.T) {
	t.Run("filter_table_no_rules", func(t *testing.T) {
		tracker := NewIPTablesTracker(nil, false)
		result := tracker.buildRestoreInput(nil, "filter")

		assert.True(t, strings.HasPrefix(result, "*filter\n"),
			"must start with *filter")
		assert.True(t, strings.HasSuffix(strings.TrimRight(result, "\n"), "COMMIT"),
			"must end with COMMIT")

		// Both filter chains must be present with flush and conntrack
		assert.Contains(t, result, ":MVM-FORWARD - [0:0]", "MVM-FORWARD chain definition")
		assert.Contains(t, result, "-F MVM-FORWARD", "MVM-FORWARD flush")
		assert.Contains(t, result, "-A MVM-FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
			"MVM-FORWARD conntrack")

		assert.Contains(t, result, ":MVM-NOCLOUDNET-INPUT - [0:0]", "MVM-NOCLOUDNET-INPUT chain definition")
		assert.Contains(t, result, "-F MVM-NOCLOUDNET-INPUT", "MVM-NOCLOUDNET-INPUT flush")
		assert.Contains(t, result, "-A MVM-NOCLOUDNET-INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
			"MVM-NOCLOUDNET-INPUT conntrack")
	})

	t.Run("filter_table_with_rules", func(t *testing.T) {
		tracker := NewIPTablesTracker(nil, false)
		rules := []*model.FirewallRule{
			{
				TableName:    model.FirewallTableFilter,
				ChainName:    model.FirewallChainMVMForward,
				RuleType:     model.FirewallRuleTypeForwardOut,
				Protocol:     model.FirewallProtocolTCP,
				Source:       "10.0.0.0/24",
				Destination:  string(model.FirewallWildcardAnyCIDR),
				InInterface:  string(model.FirewallWildcardAnyInterface),
				OutInterface: "tap0",
				Target:       model.FirewallTargetAccept,
				SPort:        model.FirewallPortAny,
				DPort:        model.FirewallPortAny,
			},
		}

		result := tracker.buildRestoreInput(rules, "filter")

		assert.True(t, strings.HasPrefix(result, "*filter\n"))
		assert.True(t, strings.HasSuffix(strings.TrimRight(result, "\n"), "COMMIT"))

		// Chain lines
		assert.Contains(t, result, ":MVM-FORWARD - [0:0]")
		assert.Contains(t, result, "-F MVM-FORWARD")
		assert.Contains(t, result, ":MVM-NOCLOUDNET-INPUT - [0:0]")
		assert.Contains(t, result, "-F MVM-NOCLOUDNET-INPUT")

		// Conntrack for filter
		assert.Contains(t, result, "-A MVM-FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT")
		assert.Contains(t, result, "-A MVM-NOCLOUDNET-INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT")

		// Rule line (hardcoded — not recomputed from buildRestoreLine to avoid tautology)
		assert.Contains(t, result, "-A MVM-FORWARD -p tcp -s 10.0.0.0/24 -o tap0 -j ACCEPT")
	})

	t.Run("nat_table_no_rules", func(t *testing.T) {
		tracker := NewIPTablesTracker(nil, false)
		result := tracker.buildRestoreInput(nil, "nat")

		assert.True(t, strings.HasPrefix(result, "*nat\n"))
		assert.True(t, strings.HasSuffix(strings.TrimRight(result, "\n"), "COMMIT"))

		// Only MVM-POSTROUTING for nat
		assert.Contains(t, result, ":MVM-POSTROUTING - [0:0]")
		assert.Contains(t, result, "-F MVM-POSTROUTING")

		// Must NOT have filter chains or conntrack
		assert.NotContains(t, result, "MVM-FORWARD")
		assert.NotContains(t, result, "MVM-NOCLOUDNET-INPUT")
		assert.NotContains(t, result, "conntrack")
	})

	t.Run("nat_table_with_rules", func(t *testing.T) {
		tracker := NewIPTablesTracker(nil, false)
		rules := []*model.FirewallRule{
			{
				TableName:    model.FirewallTableNat,
				ChainName:    model.FirewallChainMVMPostrouting,
				RuleType:     model.FirewallRuleTypeMasquerade,
				Protocol:     model.FirewallProtocolAll,
				Source:       string(model.FirewallWildcardAnyCIDR),
				Destination:  string(model.FirewallWildcardAnyCIDR),
				InInterface:  string(model.FirewallWildcardAnyInterface),
				OutInterface: string(model.FirewallWildcardAnyInterface),
				Target:       model.FirewallTargetMasquerade,
				SPort:        model.FirewallPortAny,
				DPort:        model.FirewallPortAny,
			},
		}

		result := tracker.buildRestoreInput(rules, "nat")

		assert.True(t, strings.HasPrefix(result, "*nat\n"))
		assert.True(t, strings.HasSuffix(strings.TrimRight(result, "\n"), "COMMIT"))

		// Nat chain only
		assert.Contains(t, result, ":MVM-POSTROUTING - [0:0]")
		assert.Contains(t, result, "-F MVM-POSTROUTING")

		// No conntrack for nat
		assert.NotContains(t, result, "conntrack")

		// Rule line (hardcoded — not recomputed from buildRestoreLine to avoid tautology)
		assert.Contains(t, result, "-A MVM-POSTROUTING -j MASQUERADE")
	})

	t.Run("empty_rules_slice", func(t *testing.T) {
		tracker := NewIPTablesTracker(nil, false)
		result := tracker.buildRestoreInput([]*model.FirewallRule{}, "filter")

		assert.True(t, strings.HasPrefix(result, "*filter\n"))
		assert.True(t, strings.HasSuffix(strings.TrimRight(result, "\n"), "COMMIT"))

		// Filter chains still present
		assert.Contains(t, result, ":MVM-FORWARD - [0:0]")
		assert.Contains(t, result, ":MVM-NOCLOUDNET-INPUT - [0:0]")
	})

	t.Run("comment_included_in_restore_line", func(t *testing.T) {
		tracker := NewIPTablesTracker(nil, true)
		rule := &model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    model.FirewallChainMVMForward,
			RuleType:     model.FirewallRuleTypeForwardOut,
			Protocol:     model.FirewallProtocolAll,
			Source:       string(model.FirewallWildcardAnyCIDR),
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  string(model.FirewallWildcardAnyInterface),
			OutInterface: string(model.FirewallWildcardAnyInterface),
			Target:       model.FirewallTargetAccept,
			SPort:        model.FirewallPortAny,
			DPort:        model.FirewallPortAny,
			CommentTag:   strPtr("mvm:forward_out:test-net"),
		}

		result := tracker.buildRestoreInput([]*model.FirewallRule{rule}, "filter")
		assert.Contains(t, result, "-m comment --comment mvm:forward_out:test-net")
	})
}
