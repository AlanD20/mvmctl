# Routed Service-Access Policies

**Status:** Active
**Date:** 2026-08-09
**See also:** [Project Hosting with MicroVMs](../roadmaps/TENANT_MICROVM.md), [ADR-0009: Firewall Backend Mutual Exclusion](0009-firewall-backend-mutual-exclusion.md)

## Context

mvmctl networks are independent Linux bridges and subnets. VMs on one bridge communicate directly at layer 2, while traffic between bridges is routed by the host because IPv4 forwarding is enabled. Before this decision was implemented, the managed firewall permitted outbound NAT and installed no default deny between mvmctl bridges.

Project orchestration remains outside mvmctl. The infrastructure requirement is narrower: permit a source mvmctl network to reach one exact destination VM service, such as TCP port 5432 on a shared PostgreSQL VM, while denying other routed traffic between managed networks. The policy must survive VM restarts and firewall reconciliation without exposing raw nftables or iptables syntax.

The persisted policy also needs a stable meaning if same-network filtering is added later. Routed packets belong in the IP-family FORWARD path. Same-bridge packets bypass that path because `bridge-nf-call-iptables=0` and would require nftables bridge-family enforcement. These mechanisms are additive, not replacements for one another.

## Decision

mvmctl will persist a typed **service-access policy** identified by source network, destination VM, protocol, and destination port or port range.

- The policy stores resource identities, not interface names, IP addresses, chain names, or backend expressions.
- The API layer resolves the source network and destination VM, then passes trusted model values to the network domain. This preserves core-domain isolation.
- The network domain owns policy persistence and compiles policies into derived firewall rules. `internal/lib/firewall` continues to own only backend-specific rule application and reconciliation.
- Different-network policies compile to IP-family FORWARD rules for both nftables and iptables.
- Explicit allows precede a final default DROP for traffic routed between mvmctl-managed bridges. Established and related reply traffic remains allowed, and internet egress remains unchanged.
- VM-to-host traffic is controlled separately in INPUT. Required NoCloud access and explicitly approved host services precede a final drop for traffic originating from mvmctl networks.
- Future same-network VM-to-VM policies will add a typed source-VM operation and compile to nftables bridge-family rules. Existing policies, commands, and stored records will not change.
- No raw firewall-rule editor, generic selector registry, or silent backend fallback will be introduced.

The implemented IP-family compiler uses `MVM-ROUTED-POLICY` and `MVM-HOST-INPUT`. Both backends place connection-state handling first, explicit service allows next, and an internally derived terminal drop last. Managed-interface matching is semantic in persisted derived state and expands to the backend's interface-prefix syntax only when rules are rendered. The initial host exception set contains NoCloud; additional host services require a future typed operation rather than raw INPUT rules.

## Consequences

**Benefits:**

- External orchestrators can model environments and shared services without introducing tenant concepts into mvmctl.
- Policy intent remains stable when VM IPs, TAP names, or firewall backends change.
- Routed policies work with both existing firewall backends.
- Bridge-family enforcement can be added without replacing the policy model.
- Idempotent reconciliation can restore rules after host reboot or UFW reload.

**Costs and limitations:**

- Rule ordering becomes a correctness requirement. Service allows and connection-state handling must always precede default deny.
- Removing or recreating a VM or network invalidates identity-bound policies; policies do not silently attach to replacement resources.
- VM-to-host restrictions require careful NoCloud and host-service system tests.
- The initial host-input policy permits NoCloud and established replies but no user-defined host services.
- Same-network filtering remains open until the bridge-family compiler is implemented. When implemented, that compiler will require nftables; routed policies retain iptables parity.

## Related Decisions

- ADR-0009: Firewall backend mutual exclusion remains unchanged.
- ADR-0010: The API layer resolves cross-domain policy inputs; Core domains remain isolated.
- ADR-0011: Policy inputs use typed `Validate` and `Resolve` methods.
