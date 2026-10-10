# Early OpenRC Management-Agent Startup

**Status:** Implemented for v0.3.0; focused Go and three affected installed cases passed, full release QA pending
**Date:** 2026-10-10
**Supersedes:** Only the OpenRC agent default-runlevel choice in
[ADR-0016](0016-root-owned-single-binary-privileged-dispatch.md)
**Supports:** [ADR-0020](0020-networkless-vm-placement.md)

## Context

Authenticated vsock management does not require a guest IP network. The existing systemd agent starts
at `sysinit.target`, but the OpenRC adapter enables the agent in the default runlevel. A real Alpine
cloud-init-off, network-attached, skip-deblob diagnostic observed sequential DHCP/IPv4LL and chronyd
startup before the agent started around 18 seconds after creation. An early ordinary five-second exec
failed to read the CONNECT response; one ordinary exec after passive boot observation succeeded
immediately, with the agent running in the default runlevel.

This proves a startup delay in that guest variant, not that every directional-exec failure has the same
cause. VM creation confirms host launch, not readiness of every guest service. Extending exec deadlines
would hide this dependency without making network-independent management start independently.

## Decision

- Enable only the fixed OpenRC `mvm-agent` service in the boot runlevel. Retain its local-filesystem
  dependency and order it after module initialization when that service is present, before IP/network,
  clock and cloud-init work. A kernel with built-in vsock support must not acquire a hard dependency
  on an absent module-loading service.
- Keep systemd agent activation at `sysinit.target`, SSH activation in its existing runlevel, and all
  authentication, token ownership, port, process and transport contracts unchanged. Do not parallelize
  the whole guest, disable distribution services, or extend exec deadlines.
- Successful provisioning must leave exactly one admitted agent enablement. Converge a retained
  default-runlevel link only when its exact fixed target and safe identity are proved through the
  confined guest-root descriptor. Refuse foreign or unsafe entries rather than deleting them.
- Retain descriptor-relative mutation, parent synchronization, mutation-state reporting and safe retry
  after interrupted removal or publication. Do not add a caller-selected path or generic cleanup hook.

## Alternatives considered

- **Longer exec deadlines:** rejected as the fix; they mask network/clock delay and change the caller's
  operation budget rather than the agent's startup dependency.
- **Global OpenRC parallel startup or deblob:** rejected because these change unrelated guest services
  and would not preserve the explicitly supported skip-deblob behavior.
- **Ordering only within default:** insufficient to establish precedence over earlier runlevel work.
- **Mandatory module-service dependency:** rejected because supported built-in kernels need no module
  loader; actual driver readiness still requires installed-candidate evidence.

## Implementation and qualification boundary

The confined adapter now implements boot-only enablement and exact legacy-link convergence, including
pre-mutation admission, retained link identity, removal-parent synchronization and safe retry. Behavior-owned
tests cover idempotence, unsafe-link refusal and interrupted mutation/durability boundaries while preserving
systemd and ambiguous-layout refusal. Formatting, line-length, focused vet, affected ordinary tests and the
full root loopmount package passed inside the approved named runner on the exact integrated source.
These are provisioning checks, not evidence of live guest startup or full release qualification.

The real-agent candidate `0.3.0-rc.openrc-c54bfe88` subsequently passed exactly three installed cases
inside `mvmctl-030-test`: immediate ordinary five-second networkless boot-ID exec before the readiness
retry, directional VM-to-VM exec policy, and pause/resume process/guest preservation. The invocation
passed strict 3/3 with zero other outcomes; typed cleanup and the original baseline were preserved.
The cold case selects its preseeded Alpine image explicitly, without changing defaults or deadlines.
These cases establish the affected functional paths, not startup timing for every cloud-init, network,
driver or policy variant, nor the complete release matrix.
