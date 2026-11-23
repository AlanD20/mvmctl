package network

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	ipv4RuleHeaderSize = 12
	// Priorities 1–3 run after the kernel's local rule and before the main default.
	mainSpecificPriority = 1
	namedUplinkPriority  = 2
	noFallbackPriority   = 3
)

type namedRouteRule struct {
	priority        uint32
	table           uint32
	source          netip.Prefix
	ingress         linkName
	action          uint8
	protocol        uint8
	suppressDefault bool
}

type namedRoutingPlan struct {
	table        uint32
	rules        [3]namedRouteRule
	defaultRoute routeIPv4Observation
}

type namedRoutingState struct {
	rules        [3]bool
	defaultRoute bool
}

func namedRoutingFor(record networkAuthorityRecord) (namedRoutingPlan, bool) {
	if record.configuration == nil || record.configuration.uplink.requestedName() == "" {
		return namedRoutingPlan{}, false
	}
	// Keep IDs stable across boots. Never allocate over an occupied table.
	digest := sha256.Sum256([]byte(record.networkID))
	table := uint32(0x40000000) | (binary.BigEndian.Uint32(digest[:4]) & 0x3fffffff)
	rule := namedRouteRule{source: record.configuration.l3.prefix.value,
		ingress: record.managedBridge.name, protocol: unix.RTPROT_STATIC}
	main, named, terminal := rule, rule, rule
	main.priority, main.action, main.table = mainSpecificPriority, unix.FR_ACT_TO_TBL, unix.RT_TABLE_MAIN
	main.suppressDefault = true
	named.priority, named.action, named.table = namedUplinkPriority, unix.FR_ACT_TO_TBL, table
	terminal.priority, terminal.action = noFallbackPriority, unix.FR_ACT_UNREACHABLE

	plan := namedRoutingPlan{table: table, rules: [3]namedRouteRule{main, named, terminal}}
	uplink := record.configuration.uplink.route
	if uplink.destination.IsValid() {
		scope := uint8(unix.RT_SCOPE_LINK)
		if uplink.hasGateway {
			scope = unix.RT_SCOPE_UNIVERSE
		}
		plan.defaultRoute = routeIPv4Observation{destination: uplink.destination,
			table: table, ifIndex: uplink.ifIndex, kind: unix.RTN_UNICAST, scope: scope,
			protocol: unix.RTPROT_STATIC, hasGateway: uplink.hasGateway, gateway: uplink.gateway}
	}
	return plan, true
}

func (plan namedRoutingPlan) inspect(ctx context.Context, adapter *directRouteNetlink, fd int) (
	namedRoutingState, error,
) {
	var state namedRoutingState
	rules, err := adapter.execute(ctx, fd, netlink.Message{
		Header: netlink.Header{Type: unix.RTM_GETRULE, Flags: netlink.Request | netlink.Dump},
		Data:   append([]byte{unix.AF_INET}, make([]byte, ipv4RuleHeaderSize-1)...),
	})
	if err != nil {
		return state, fmt.Errorf("dump IPv4 policy rules: %w", err)
	}
	localRules := 0
	for _, message := range rules {
		observation, err := decodeNamedRouteRule(message)
		if err != nil {
			return state, err
		}
		rule := observation.rule
		if observation.builtinLocal() {
			localRules++
			continue
		}
		matched := false
		for i, want := range plan.rules {
			if rule != want || observation.extra {
				continue
			}
			if state.rules[i] {
				return state, errors.New("duplicate named-uplink policy rule")
			}
			state.rules[i], matched = true, true
			break
		}
		if matched {
			continue
		}
		if rule.table == plan.table ||
			(rule.priority <= noFallbackPriority && observation.couldMatch(plan.rules[0])) {
			return state, fmt.Errorf(
				"foreign IPv4 policy rule conflicts with named uplink at priority %d",
				rule.priority,
			)
		}
	}

	if localRules != 1 {
		return state, errors.New("named uplink requires the kernel IPv4 local rule at priority zero")
	}

	data, err := marshalRouteMessage(
		0,
		unix.RT_TABLE_UNSPEC,
		unix.RTPROT_UNSPEC,
		unix.RT_SCOPE_UNIVERSE,
		unix.RTN_UNSPEC,
	)
	if err != nil {
		return state, err
	}
	data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
		encoder.Uint32(unix.RTA_TABLE, plan.table)
		return nil
	})
	if err != nil {
		return state, err
	}
	routes, err := adapter.execute(ctx, fd, netlink.Message{
		Header: netlink.Header{Type: unix.RTM_GETROUTE, Flags: netlink.Request | netlink.Dump}, Data: data,
	})
	// A previously unused table does not exist in the kernel.
	if errors.Is(err, unix.ENOENT) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("dump named-uplink route table: %w", err)
	}
	for _, message := range routes {
		table, err := ipv4RouteTable(message.Data)
		if err != nil {
			return state, err
		}
		if table != plan.table {
			continue
		}
		route, err := decodeRouteIPv4Message(message)
		if err != nil || route != plan.defaultRoute || state.defaultRoute {
			return state, fmt.Errorf("foreign or duplicate route in named-uplink table %d", plan.table)
		}
		state.defaultRoute = true
	}
	return state, nil
}

func (plan namedRoutingPlan) verify(
	ctx context.Context, adapter *directRouteNetlink, fd int, present bool,
) error {
	state, err := plan.inspect(ctx, adapter, fd)
	if err != nil {
		return err
	}
	want := namedRoutingState{}
	if present {
		want.rules = [3]bool{true, true, true}
		want.defaultRoute = plan.defaultRoute.destination.IsValid()
	}
	if state != want {
		return fmt.Errorf("named-uplink route or policy rule state differs from root authority")
	}
	return nil
}

func (plan namedRoutingPlan) reconcile(
	ctx context.Context, adapter *directRouteNetlink, fd int, present bool,
) error {
	state, err := plan.inspect(ctx, adapter, fd)
	if err != nil {
		return err
	}
	if plan.defaultRoute.destination.IsValid() && present && !state.defaultRoute {
		if err := adapter.changeNamedDefaultRoute(ctx, fd, plan.defaultRoute, true); err != nil {
			return err
		}
	}
	// Install the terminal rule first. Remove it last so retries cannot fall through to the host default.
	for i := len(plan.rules) - 1; i >= 0 && present; i-- {
		if !state.rules[i] {
			if err := adapter.changeNamedRule(ctx, fd, plan.rules[i], true); err != nil {
				return err
			}
		}
	}
	if !present {
		for i, rule := range plan.rules {
			if state.rules[i] {
				if err := adapter.changeNamedRule(ctx, fd, rule, false); err != nil {
					return err
				}
			}
		}
		if state.defaultRoute {
			if err := adapter.changeNamedDefaultRoute(ctx, fd, plan.defaultRoute, false); err != nil {
				return err
			}
		}
	}
	return plan.verify(ctx, adapter, fd, present)
}

func verifyNamedRouting(ctx context.Context, fd int, record networkAuthorityRecord, present bool) error {
	plan, named := namedRoutingFor(record)
	if !named {
		return nil
	}
	if err := plan.verify(ctx, newDirectRouteNetlink(), fd, present); err != nil {
		return networkAuthorityCorruptError("verify named-uplink routing: "+err.Error(), err)
	}
	return nil
}

func (tx *managedNetworkTransaction) configureNamedRouting(ctx context.Context, fd int) error {
	plan, named := namedRoutingFor(tx.record)
	if !named {
		return nil
	}
	link, err := tx.inspectBridge(ctx, false)
	if err != nil {
		return err
	}
	if link.flags&unix.IFF_UP != 0 {
		return verifyNamedRouting(ctx, fd, tx.record, true)
	}
	if err := plan.reconcile(ctx, newDirectRouteNetlink(), fd, true); err != nil {
		return networkAuthorityEffectError("configure named-uplink routing", err)
	}
	return nil
}
