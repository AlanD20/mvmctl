# Protected Branch Authority Outside Cache Data

**Status:** Implemented; focused and complete integration checks passed, installed qualification pending
**Date:** 2026-10-10
**Amends:** [ADR-0018](0018-isolated-vm-execution-and-snapshot-authority.md) and
[ADR-0022](0022-explicit-protected-cache-retirement.md), for fresh protected-store layouts only

## Context

Snapshot removal deliberately retains its independent lock inode. Managed volume-source removal retains a receipt
and capture marker. These currently reside within the selected cache's protected data branches, so ADR-0022 correctly
refuses to remove those branches even after public pruning completes. Deleting permanent locks would permit a second
lock domain; copying them to another filesystem would not preserve inode authority.

The user approved a clean 0.3.0 installation without backward compatibility. Fresh authority placement can close this
gap without adopting or migrating existing layouts. Permanent logical/object locks already live in the root-owned
anchor outside the cache; named volume removal already uses receipt-before-capture and verified same-filesystem
quarantine. Those existing patterns define the extension, not a new public cleanup capability.

## Decision

- Introduce a required fresh protected-store layout version. Publish its fixed authority identities durably during
  initial creation and validate it through every store-admission/reopening path. Old or missing layout authority
  refuses before effects. Never recreate missing recorded authority for a populated store.
- Create permanent snapshot locks beneath a fixed root-owned `snapshot-locks` directory in the existing per-operator,
  per-cache root anchor. Retain its exact identity in the store record and its descriptor in snapshot leases. Snapshot
  artifacts and restore references remain protected data under the managed cache.
- Snapshot operations use the existing canonical-ID, root-owned, single-link, zero-length lock and nonblocking flock
  contract. New locks are durably created; existing locks are never unlinked, moved or recreated. Shared store-use
  authority covers data and lock access; restored VM references retain their existing lifetime contract.
- Store initial raw-volume-removal receipt, quarantine identity and closed progress phases in the existing root
  volume record under its logical/object lock. No second volume lock domain or generic record/action is introduced.
  Only volume records may carry that authority.
- Keep temporary volume capture on the selected cache filesystem. Its canonical-ID quarantine may contain only the
  admitted `data.raw`, then become an identity-recorded empty directory. Record preparation before creation and
  capture, preserve exact source checks, synchronize both rename parents and publish captured authority before unlink.
  Synchronize after unlink before terminal removal. Retry re-synchronizes visible publication before resumed effects.
- Completed removal is idempotent from its exact terminal authority; it never adopts a replacement source or restarts
  removal. Missing-source completion needs explicit synchronized evidence. Unrecorded, nonempty, replaced or ambiguous
  quarantine refuses; interrupted volume removal belongs to that typed operation, not cache cleanup.
- Extend ADR-0022 preflight under exclusive store-use authority. Preserve and validate the entire permanent snapshot
  lock inventory, acquiring existing locks without creation in canonical order. Require absent or genuinely empty
  snapshot data and no live backing/current/next/restore references. A held operation or unknown entry refuses.
- Admit only identity-matched, root-owned empty volume quarantines with terminal volume and source-removal authority.
  Add this one fixed child kind to the retirement manifest. Remove its admitted empty children before branch parents
  using the existing root-private store capture and durable retry sequence. External locks, receipts, generations,
  digests and logical bindings survive unchanged; cache cleanup cannot rebind retired identities.
- Keep the existing typed `ProtectedCacheClean` action and public dry-run/failure/result contracts. No caller-selected
  child path, recursive root removal, chmod, force adoption, reset, cross-filesystem move or receipt migration.
- Producers and cleanup consumers activate together in one coherent implementation. Existing lock/receipt layouts
  remain strict refusals. Clean installation, not automatic conversion, is the transition.

## Consequences and Validation

This relocates authority for newly created stores, not snapshot artifact bytes. The cache and root anchor may be on
different filesystems. Temporary raw capture therefore cannot target the external anchor. Permanent branch retirement
is implemented in `b254f010`, including refusal before new logical authority publication and preserved recovery
details for interrupted snapshot captures. Named-runner format and vet checks passed, followed by 149 focused
root tests/subtests and the same 149 under the race detector, with zero failures or skips. These focused checks
alone do not qualify the complete package or release build. Clean `4bc51ee7` subsequently passed complete
ordinary CI, the full root Jailer suite (2,707 test/subtest passes, no failures or skips) and the real-agent
release build inside the named runner. The new candidate is not installed; public snapshot/volume/cache
cleanup on shared and distinct filesystems remains unqualified. Existing stores remain strictly rejected;
no migration or authority rebinding was added.

Required evidence covers stable lock identity through snapshot removal/cache retirement; held-worker, restored-reference
and live-backing refusals; exact volume-source capture and completed-removal idempotence; interrupted publication,
rename, parent sync, unlink and terminal commit; changed/unknown/old-layout preservation; dry-run/result projection;
and terminal snapshot/volume/cache cleanup on both shared and distinct filesystems. Meaningful focused Go checks and
installed public journeys run inside the approved disposable runner. Complete integration CI is passed;
installed public qualification remains a separate gate.
