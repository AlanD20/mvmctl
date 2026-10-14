# Immutable Kernel Lineage Across Retired Caches

**Status:** Implementation integrated; root Go verification passed, fresh-candidate installed qualification pending
**Date:** 2026-10-14
**Amends:** [ADR-0018](0018-isolated-vm-execution-and-snapshot-authority.md) and
[ADR-0022](0022-explicit-protected-cache-retirement.md), only for immutable kernel succession across caches
**Preserves:** [ADR-0023](0023-protected-branch-authority-layout.md) authority placement and retirement proof

## Context

Before this extension, explicit identical-byte kernel reimport retained the original cache/store binding. After
completed cache retirement, the same operator could not acquire that kernel in a fresh cache: the global logical
identity still bound the retired cache. That refusal followed ADR-0022; it did not authorize deleting or reconstructing
the old authority.

The approved extension permits explicit acquisition of identical immutable bytes in a new active cache while
preserving the old store record, object tombstone, digest and lock identities. Kernel removal alone is insufficient:
cross-cache succession also requires completed old-store retirement. Mutable backing remains cache-bound.

The existing typed kernel import occurs before SQLite publication. Protected kernel publication already checks a
retained root digest and advances its generation; cleaned-instance relaunch likewise advances a generation only
after terminal cleanup. These precedents support a kernel-specific lineage, not a generic authority-move operation.

## Decision

- Retain `ProtectedKernelClient.Import(ctx, ProtectedKernelImportRequest{CacheRoot, KernelID})` and its authenticated
  dispatcher action. Root derives the source leaf and destination namespace. No caller-selected digest, generation,
  source path, force capability, CLI generation option or launch fallback is added.
- Introduce kernel-only logical schema 2 beneath the existing permanent global operator/kernel-ID lock. Record the
  owner and ID, monotonic last generation, committed current binding, optional pending successor, and immutable
  terminal generation receipts. Receipts bind cache, recorded store and backing identities, digest and generation.
  Per-cache tombstones remain intact; mutable kinds retain their existing cache-bound logical schema.
- Allocate every destination backing generation from that global lineage. Do not reset it when changing caches or
  reuse an aborted allocation. Generation exhaustion refuses. Same-cache identical reimport retains its digest,
  identity, reference and durability guards.
- Under retained host admission, pin the authenticated destination cache/store and its shared store-use lease.
  Hold the existing nonblocking global kernel lock; acquire predecessor store-use and object locks without waiting,
  preserving existing lock ordering and releasing leases on contention. Never reopen or recreate the old cache.
- Require synchronized terminal kernel removal, completed old-store retirement, authoritative root reference and
  branch retirement proof, empty current/next references, a retained digest, no pending predecessor and available
  generation capacity. Admit and synchronize the retained old authority directories. Missing, changed, old-schema
  or ambiguous authority refuses before effects; public SQLite absence does not prove retirement.
- Persist and synchronize pending intent binding predecessor, destination and allocated generation before exclusive
  destination object/candidate creation. Record and synchronize the exact candidate inode before copying. Reuse the
  existing ACL, bounded-copy, pinned SHA-256 and no-replace publication checks. Compare copied bytes with the
  predecessor's root digest and rehash before publication.
- Synchronize destination bytes and namespace, publish the ready destination object, then commit and synchronize
  the logical current pointer. Kernel lookup, launch and restore require the committed current cache/generation
  and refuse pending succession. A ready object alone cannot expose the successor between these two publications;
  previous retained bindings remain stale.
- Separate consumers from the trusted initial snapshot producer. VM launch/restore consumers require committed
  current authority. The producer may finish only its own sealed initial root-owned `0400`, `O_RDONLY` source
  descriptor, matching its source-owned pending/aborted identity under the full restore locks. This does not reopen
  removed backing or revive a cross-cache predecessor, and it adds no ordinary lookup fallback. Conversely, explicit
  operator import of that initial aborted authority is refused before allocating another generation.
- Before publication, cancellation or failure may remove only the recorded identity-matched candidate beneath its
  root-private directory. Synchronize absence and retain an aborted-attempt receipt without recycling its generation.
  Uncertain publication retains intent for exact typed retry. Retry seals visible record writes, verifies the same
  recorded candidate and completes once; changed or missing recorded candidates refuse. Preserve caller staging.
- Create schema 2 only for fresh kernel authority under the v0.3.0 clean break. Existing schema 1 is not adopted,
  migrated or reconstructed. Missing-authority reconstruction, retired-cache recreation and mutable tombstone
  reimport remain prohibited. Historical refusals on schema-1 authority are not successor recovery inputs.

## Alternatives considered

Retaining the cross-cache ban preserves the current restriction but prevents the approved same-operator lifecycle.
Rebinding or deleting the old record loses stale-generation protection and retirement evidence. Permission loosening,
generic authority movement, caller-selected paths or automatic schema adoption enlarge the privilege boundary beyond
the observed immutable-kernel need. The selected lineage preserves prior authority and publishes a distinct successor.

## Consequences and Validation

Cross-filesystem publication spans destination object authority and the global logical pointer. Pending intent must
keep reads closed until both are durable; failure recovery cannot infer success from visible files alone. Current and
pending admission use direct recorded bindings, while digest work remains bounded by kernel size.

Implementation `0a8ff31b` integrates kernel import/reimport, logical admission, record codecs, backing copy/lookup/binding
and cache-retirement consumers coherently. Focused root behavioral verification must cover generation exhaustion,
duplicate/competing imports, busy/referenced/transitional refusal, changed bytes/cache/store, stale bindings,
cancellation and crash points around both publications and synchronization retries, unchanged same-cache reimport,
and unchanged mutable anti-resurrection behavior. Required engineering gates include focused format, vet, root and
race checks inside the approved disposable runner.

Root Go verification used the exact 18-file implementation overlay on `f905f3ea`; those reviewed/tested bytes were
integrated unchanged. Affected vet passed. Focused ordinary and race runs each recorded 147 test/subtest PASS,
zero FAIL and two explicitly skipped auxiliary crash-worker helpers. The complete root Jailer run recorded 2,795
PASS, zero FAIL and the same two auxiliary skips, native exit zero. An earlier complete run with 2,776 PASS and
three FAIL remains failed history. Fault/recovery tests cover corrected publication durability and same-cache
aborted-inventory handling; producer/consumer regressions include refusal of operator takeover before generation
allocation. These source/Go results do not establish a zero-skip run, release build or installed qualification.

A fresh candidate built from the implemented source must pass the real same-operator CLI journey: import, boot,
remove, cache cleanup, identical import into a fresh cache, boot and exact cleanup, with unchanged old records and
locks. Ordinary CLI import of changed bytes derives a different kernel ID; it does not exercise same-ID digest
refusal. Same-ID changed-byte refusal and cross-filesystem publication/crash guards belong to root behavioral and
race verification, without public test-only flags. Actual installed succession across distinct filesystems must
be qualified where available or reported as an explicit gap; a single-filesystem journey does not establish it.
Full kernel22 remains a separate qualification gate. No fresh CLI release build, installation or installed-system
qualification has run for this implementation. Earlier candidate evidence predates it and does not qualify the new
candidate or lineage. Integrated source and root Go verification are not a release PASS.
