# Pressure-Only VM Memory Control

**Status:** Accepted; implemented, sustained saturation not qualified
**Date:** 2026-09-19
**Last Updated:** 2026-09-27
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

The v0.3.0 policy-v2 implementation and root-brokered inspection are in source. Focused installed-candidate Python
cases in `mvmctl-030-test` observed unlimited VM-leaf `memory.max`, charged a bounded guest tmpfs allocation, and
retained guest responsiveness. Those cases do not establish sustained saturation survival, diagnose the original
reported kill, or measure ancestor/host OOM safety. Those remain release qualification work.
