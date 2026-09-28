# Pressure-Only VM Memory Control

**Status:** Accepted; implemented, bounded local memory QA passed; original incident unresolved
**Date:** 2026-09-19
**Last Updated:** 2026-09-28
**Supersedes:** Per-VM finite memory maximum in [ADR-0015](0015-jailer-canonical-launcher.md) and policy-v1 memory behavior in [ADR-0016](0016-root-owned-single-binary-privileged-dispatch.md)

## Context

The reported saturation failure has not been attributed to a specific OOM mechanism. The existing policy sets
`memory.high` and `memory.max` to guest RAM plus configurable VMM headroom. A hard cgroup ceiling may invoke OOM
when reclaim cannot satisfy an allocation; it is not a transparent cap on guest allocations.

The user explicitly selected pressure-only control and accepted that a VM may exceed its host memory envelope.
This supersedes the earlier instruction to leave the finite policy unchanged pending diagnosis.

## Decision

- Keep configured guest RAM unchanged.
- Keep `memory.high` at guest RAM plus the existing configurable VMM headroom.
- Set the per-VM `memory.max` to the kernel's `max` value, not a fabricated large byte count.
- Preserve the existing explicit swap, CPU, and PID policies. Do not alter host-wide or ancestor cgroup limits.
- Version the persisted/wire policy so finite-policy records cannot silently masquerade as pressure-only intent.
- Propagate configuration-read failures instead of deriving a zero headroom or other zero policy value from an error.

## Consequences

`memory.high` requests reclaim/throttling; it is not a strict ceiling. Host memory consumption may exceed the pressure
threshold. This removes this VM leaf's finite hard memory cap, not all OOM risks: host pressure, ancestor limits,
userspace OOM management, or guest OOM can still terminate processes. The implementation must not claim the reported
incident is reproduced, diagnosed, or proven fixed merely because this policy changes.

The v0.3.0 policy-v2 implementation and root-brokered inspection are in source. Three bounded installed-candidate
Python cases in `mvmctl-030-test` passed twice, including sustained measured cgroup pressure with and without swap.
They observed unlimited VM-leaf `memory.max`, actual `memory.high` events, and retained guest responsiveness.

A separate guest-global RAM-exhaustion case passed once in 10.28s on 2026-09-28. A finite anonymous allocator in a
224 MiB guest was the single identified global-OOM victim; its supervisor recorded exit 137. A fresh `mvm exec`
then succeeded and allocated another MiB. Guest boot and Firecracker PID/start identity remained unchanged, as did
the runner's VM-leaf/visible-ancestor OOM counters and the outer runner's visible-ancestor OOM counters. Configuration
was restored and VM/network/volume/snapshot inventories were empty after cleanup.

That case used test-file SHA-256 `93f2be2c15dd6989bd689597208f80cbb8975886144d217f0824aeb8d374d36b` and installed
candidate SHA-256 `886cb7e5fffc73c7918b17f26a454527396ff8b4b1e989c37312098407237d0a`. These bounded observations
distinguish guest OOM from a VMM kill in the tested workload; they do not diagnose the original reported kill or prove
safety under arbitrary ancestor/host pressure. Original incident attribution and complete release qualification remain
open. The outer runner still uses the installed 0.2.0 controller's finite memory policy; it was not changed by this QA.
