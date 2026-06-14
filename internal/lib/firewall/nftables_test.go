package firewall

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"mvmctl/internal/lib/model"
)

// ─── nftBaseChainKey ───────────────────────────────────────────────────────────
// Rationale: nftBaseChainKey produces the lookup key for base chain hook
// definitions. Mismatched keys cause the tracker to skip base chain setup,
// leaving the firewall in an incomplete state.

func TestNftBaseChainKey(t *testing.T) {
	tests := map[string]struct {
		family string
		table  string
		chain  string
		want   string
	}{
		"filter_forward": {
			family: "ip",
			table:  "filter",
			chain:  "FORWARD",
			want:   "ip/filter/FORWARD",
		},
		"nat_postrouting": {
			family: "ip",
			table:  "nat",
			chain:  "POSTROUTING",
			want:   "ip/nat/POSTROUTING",
		},
		"filter_input": {
			family: "ip",
			table:  "filter",
			chain:  "INPUT",
			want:   "ip/filter/INPUT",
		},
		"non_ip_family": {
			family: "ip6",
			table:  "filter",
			chain:  "INPUT",
			want:   "ip6/filter/INPUT",
		},
		"empty_components": {
			family: "",
			table:  "",
			chain:  "",
			want:   "//",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := nftBaseChainKey(tc.family, tc.table, tc.chain)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("nftBaseChainKey() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── ruleToNftExpr ────────────────────────────────────────────────────────────
// Rationale: ruleToNftExpr builds the nftables expression from a FirewallRule.
// nftables is strict about L3 before L4 ordering and requires lowercase targets.
// Wrong expressions are silently accepted by nft but don't match traffic.

func TestRuleToNftExpr(t *testing.T) {
	tracker := &NFTablesTracker{}

	// Minimal base rule (all wildcards, no comment)
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
		modify func(*model.FirewallRule)
		want   []string
	}{
		// ── Minimal / target ──
		"minimal_accept": {
			want: []string{"accept"},
		},
		"target_drop": {
			modify: func(r *model.FirewallRule) { r.Target = model.FirewallTargetDrop },
			want:   []string{"drop"},
		},
		"target_masquerade": {
			modify: func(r *model.FirewallRule) { r.Target = model.FirewallTargetMasquerade },
			want:   []string{"masquerade"},
		},
		"target_reject_lowercased": {
			modify: func(r *model.FirewallRule) { r.Target = model.FirewallTargetReject },
			want:   []string{"reject"},
		},
		"target_log_lowercased": {
			modify: func(r *model.FirewallRule) { r.Target = model.FirewallTargetLog },
			want:   []string{"log"},
		},

		// ── Source / Destination (L3) ──
		"source_specific": {
			modify: func(r *model.FirewallRule) { r.Source = "10.0.0.0/24" },
			want:   []string{"ip", "saddr", "10.0.0.0/24", "accept"},
		},
		"destination_specific": {
			modify: func(r *model.FirewallRule) { r.Destination = "192.168.1.0/24" },
			want:   []string{"ip", "daddr", "192.168.1.0/24", "accept"},
		},
		"both_source_and_destination": {
			modify: func(r *model.FirewallRule) {
				r.Source = "10.0.0.0/24"
				r.Destination = "192.168.1.0/24"
			},
			want: []string{"ip", "saddr", "10.0.0.0/24", "ip", "daddr", "192.168.1.0/24", "accept"},
		},

		// ── Interfaces (with quoted format) ──
		"in_interface_specific": {
			modify: func(r *model.FirewallRule) { r.InInterface = "eth0" },
			want:   []string{"iifname", `"eth0"`, "accept"},
		},
		"out_interface_specific": {
			modify: func(r *model.FirewallRule) { r.OutInterface = "tap0" },
			want:   []string{"oifname", `"tap0"`, "accept"},
		},
		"both_interfaces": {
			modify: func(r *model.FirewallRule) {
				r.InInterface = "eth0"
				r.OutInterface = "tap0"
			},
			want: []string{"iifname", `"eth0"`, "oifname", `"tap0"`, "accept"},
		},

		// ── Protocol standalone (no L4 ports) ──
		"protocol_tcp_no_ports": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolTCP },
			want:   []string{"tcp", "accept"},
		},
		"protocol_udp_no_ports": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolUDP },
			want:   []string{"udp", "accept"},
		},
		"protocol_icmp_no_ports": {
			modify: func(r *model.FirewallRule) { r.Protocol = model.FirewallProtocolICMP },
			want:   []string{"icmp", "accept"},
		},

		// ── Protocol with L4 ports ──
		"protocol_tcp_with_sport": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.SPort = 80
			},
			want: []string{"tcp", "sport", "80", "accept"},
		},
		"protocol_udp_with_dport": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolUDP
				r.DPort = 53
			},
			want: []string{"udp", "dport", "53", "accept"},
		},
		"protocol_tcp_with_both_ports": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.SPort = 1024
				r.DPort = 443
			},
			want: []string{"tcp", "sport", "1024", "tcp", "dport", "443", "accept"},
		},

		// ── Comment ──
		"comment_nil": {
			want: []string{"accept"},
		},
		"comment_empty": {
			modify: func(r *model.FirewallRule) { r.CommentTag = strPtr("") },
			want:   []string{"accept"},
		},
		"comment_valid": {
			modify: func(r *model.FirewallRule) { r.CommentTag = strPtr("mvm:forward_out:test-net") },
			want:   []string{"accept", "comment", `"mvm:forward_out:test-net"`},
		},

		// ── Full expression (all fields) ──
		"all_fields": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.Source = "10.0.0.0/24"
				r.Destination = "192.168.1.0/24"
				r.InInterface = "eth0"
				r.OutInterface = "tap0"
				r.SPort = 8080
				r.DPort = 443
				r.Target = model.FirewallTargetDrop
				r.CommentTag = strPtr("mvm:forward_out:net-1")
			},
			want: []string{
				"ip", "saddr", "10.0.0.0/24",
				"ip", "daddr", "192.168.1.0/24",
				"iifname", `"eth0"`,
				"oifname", `"tap0"`,
				"tcp", "sport", "8080",
				"tcp", "dport", "443",
				"drop",
				"comment", `"mvm:forward_out:net-1"`,
			},
		},

		// ── Ordering: L3 before L4 ──
		"l3_before_l4_ordering": {
			modify: func(r *model.FirewallRule) {
				r.Protocol = model.FirewallProtocolTCP
				r.Source = "10.0.0.0/24"
				r.Destination = "192.168.1.0/24"
				r.InInterface = "eth0"
				r.OutInterface = "tap0"
				r.SPort = 80
				r.DPort = 443
			},
			want: []string{
				"ip", "saddr", "10.0.0.0/24",
				"ip", "daddr", "192.168.1.0/24",
				"iifname", `"eth0"`,
				"oifname", `"tap0"`,
				"tcp", "sport", "80",
				"tcp", "dport", "443",
				"accept",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rule := *baseRule
			if tc.modify != nil {
				tc.modify(&rule)
			}

			got := tracker.ruleToNftExpr(&rule)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ruleToNftExpr() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
