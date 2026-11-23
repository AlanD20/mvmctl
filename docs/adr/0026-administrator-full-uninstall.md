# Administrator Full Uninstall

**Status:** Implemented; qualification recorded with the change
**Date:** 2026-11-14
**Amends:** [ADR-0022](0022-explicit-protected-cache-retirement.md) and
[ADR-0023](0023-protected-branch-authority-layout.md), for complete administrator removal only

## Context

Operator cache cleanup deliberately preserves permanent authority and retirement history. Administrator host
reset restores setup changes but also retains that history. Neither operation alone produces an empty host for
reinstallation. Deleting root directories before stopping their VMs would discard the authority needed to clean
up processes, namespaces, mounts and firewall rules.

## Decision

Provide an optional root-run Python administrator utility, `scripts/uninstall.py`, with a filesystem helper beside
it. It is not a privileged RPC, an installed runtime dependency, or an additional sudoers grant. The existing
single installed Go binary remains the only ordinary operator privilege boundary.

The utility inventories root-recorded enrollments and current cache bindings, previews its scope, and requires
explicit confirmation. Retired cache locators do not authorize deletion of reused paths. Unknown top-level root
entries refuse before destructive cleanup. Extra cache/config paths require explicit selection and owner/path
checks. A preview must not invoke ordinary CLI initialization.

Actual removal first invokes the canonical installed binary's typed cache cleanup as each enrolled operator.
Only after those operations succeed does it invoke administrator host reset with the recorded initial operator.
Reset retains the existing global quiescence checks; terminal networkless journals are admitted through their
strict decoder and owner/VM binding, while incomplete records and surviving namespaces still refuse.
Committed empty firewall history may belong to a prior boot when its intent has the exact same recorded boot
and namespace binding. Reset still requires an empty logical target, absent current kernel tables and unchanged
records. It neither rebinds that history nor applies or deletes an old generation; current-boot namespace
mismatches continue to refuse.

Final removal holds the root host-admission lock and requires reset-complete authority with admission disabled.
The root tree is captured by a no-replace rename into a fixed root-controlled uninstall directory, then removed
through pinned directory descriptors without crossing mounts, symlinks or shared file inodes. Caller-owned
configuration and personal launchers are removed after dropping to that operator's UID/GID. Package-owned
binaries go through their recognized package manager. External mirrors, original external SSH files and shared
system dependencies are preserved.

## Consequences

Only explicit complete administrator uninstall may discard permanent root history. Ordinary cache clean, VM
removal and host reset keep their existing guarantees. This does not provide legacy-state adoption, a generic
root deletion endpoint, or a guarantee against concurrent changes by other trusted administrators.

Failure stops further cleanup and retains remaining state; completed deletions are irreversible. A captured tree
can be retried while its reset authority remains readable. Interrupted final deletion that removes that authority
requires administrator inspection. Operators must be quiescent throughout the administrator maintenance window.

## Verification

Filesystem tests cover refused symlinks, shared inodes, foreign ownership, bind mounts, overlapping selection,
reused retired locators, cleanup failure, disabled-admission enforcement, no-replace capture and read-only preview.
The disposable runner exercises a real running-VM cleanup and a complete uninstall/reinstall journey. Tests
must never run this utility against the workstation installation.
