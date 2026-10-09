# Explicit Protected Cache Retirement

**Status:** Accepted; implementation integrated, installed-candidate qualification pending
**Date:** 2026-10-10
**Amends:** [ADR-0018](0018-isolated-vm-execution-and-snapshot-authority.md), only for explicit cleanup of a retired protected store

## Context

Protected backing resides in a root-owned `0700` directory within the operator's selected cache. Typed artifact
removal retires its bytes but preserves root ownership records and empty protected directories. Consequently,
ordinary unprivileged `cache clean` cannot finish removing that cache, even after a successful kernel removal.
An installed-candidate acquisition case reproduced this cleanup failure; it was not a memory-admission failure.

Relaxing permissions or recursively deleting a caller-selected tree as root would undermine the isolation boundary.
Conversely, treating every missing protected store as initial state would allow stale staging data to resurrect
retired backing. Cleanup therefore needs explicit root-authorized retirement, not an implicit missing-store fallback.

## Decision

- Add one named, typed protected-cache cleanup operation whose request selects only the authenticated operator's
  managed cache. Root derives and pins the cache, recorded store and object identities. No arbitrary child paths,
  commands, force option, general recursive removal or administrator reset are accepted.
- The API calls this operation after its existing prune, orphan and host-clean checks, before ordinary cache-directory
  removal. Dry-run does not perform privileged mutation. A refused or uncertain protected cleanup cannot report
  `CacheDirRemoved=true` for a real cleanup.
- Preflight the complete fixed inventory before removing parents. Known backing kinds are `kernel`, `rootfs`,
  `volume` and `cloud-init`. Only root-recorded terminal removed objects with matching identities, empty directories,
  no current or next references, and authoritative reference absence qualify. Admit the kernel-only
  `kernel-unprotected-removed` state only under its existing retirement contract.
- Unknown entries, changed ownership/modes/identities, nonempty objects, transitional imports, live backing,
  unresolved references or active leases refuse cleanup. Do not infer absence from public SQLite projections.
- Backing and protected-branch users retain shared store-use authority for their complete lease lifetime. Cleanup
  requires independent, nonblocking exclusive store-use authority. Keep existing logical/object lock ordering and
  reference checks; an initialization-only lock does not exclude retained users.
- Record durable root-owned retirement intent before removing any admitted protected parent. Bind the exact cache,
  store and quarantine identities; reconcile an interrupted cleanup only from that recorded authority. The final
  store entry has a caller-owned parent, so a separate pathname check and root `rmdir` cannot establish exact removal.
  Capture the store atomically into a pinned root-controlled quarantine before deleting its admitted empty suffix.
- Before resuming nonterminal effects, synchronize the retained root authority directory. A record rename can be
  visible after its publication-directory synchronization failed; retry must seal that intent before deleting data.
- Create and pin the fixed quarantine child inside the already-admitted root-owned store, not directly under the
  caller-owned cache where creation/open could adopt a replacement. Record preparation and the exact child identity
  before atomically promoting it to the fixed `.mvm-protected-retirement` sibling. Both locations are on the selected
  cache filesystem; no rename to `/var/lib` or cross-filesystem fallback is authorized.
- Record promotion/capture stages. Use no-replace atomic capture into the retained quarantine descriptor and verify
  the captured original store identity before deleting anything. Retry examines only the recorded fixed locations
  and identities. Changed, missing or ambiguous quarantine authority, or a substituted capture, refuses cleanup and
  retains evidence; do not scan for renamed directories or overwrite a destination to recover.
- Remove admitted empty directories only beneath the root-controlled captured store/quarantine, with identity
  rechecks. Synchronize both rename parents, and synchronize the quarantine and cache parent again before durable
  completion, including recovery where the captured store is already absent. Visible absence alone is not durable
  disappearance.
- Root leaves the verified empty quarantine sibling rather than performing another check/unlink under a caller-owned
  parent. After confirmed root retirement, the ordinary unprivileged API removes only that fixed empty directory
  before its existing cache removal. Nonempty or otherwise failed ordinary removal reports incomplete cleanup.
- A later ordinary-removal retry may acknowledge completion without mutation only when validated durable retirement
  is already `removed` and both fixed store and quarantine entries are absent. This finishes the ordinary suffix;
  it does not reconstruct missing nonterminal authority, recreate a store or authorize rebinding.
- Preserve root-owned logical bindings, terminal object records, digests, generations and durable locks outside the
  caller's cache. A cleanup record authorizes only finishing that exact retirement. Ordinary lookup, launch, restore
  and import must still refuse a missing or retired store; cleanup grants no automatic reimport or cache rebinding.
- Admit absent or genuinely empty `snapshots` and `volume-removals` branches. The initial implementation must refuse
  permanent branch locks, snapshot entries and volume-removal quarantine/receipt residues before parent mutation.
  It must not delete/recreate those lock domains or claim that these branches are empty. Their complete retirement
  remains a separate, explicit release gap, not functionality established by this decision.

Immutable kernel reimport remains the existing explicit, identical-byte operation within its retained cache/store.
This decision does not permit moving a globally bound kernel identity to a different cache. Acquisition QA must
respect that binding rather than creating a new cache for each use of the same retired kernel ID.

[ADR-0023](0023-protected-branch-authority-layout.md) defines a fresh-layout extension for permanent snapshot locks
and volume-removal receipts. Its implementation and qualification are pending; this integrated first slice still
refuses the existing branch residues. It does not migrate them or relax immutable bindings.

## Alternatives

Making the protected directory operator-writable loses backing isolation. A privileged recursive deletion capability
has a broader authority surface than the observed need. Leaving every empty protected parent indefinitely makes
ordinary selected-cache cleanup unusable. The chosen operation retains the existing typed authority boundary and
separates intentional retirement from unexplained disappearance.

## Consequences and Validation

Cleanup may still refuse after public pruning has completed; its result must disclose that incomplete state. This is
not a claim that the entire cache operation is atomic, that permanent branch residues are handled, or that a later
ordinary filesystem deletion cannot fail.

Required behavior evidence includes successful terminal-parent cleanup; refusal preserving unknown, changed, live,
referenced or transitional objects; exclusion of held leases; interrupted retirement reconciliation; unchanged
missing-store and mutable-backing anti-resurrection rules; dry-run preservation; and accurate API failure projection.
The final capture must refuse namespace substitution without deleting the substitute, and an interrupted or failed
parent synchronization must be retried before completion. The integrated correction passes focused namespace,
parent-sync recovery and publication-sync retry tests inside the named runner; the defective first source is not
qualified. Complete integration CI and installed-candidate cleanup remain pending.
Focused Go checks and installed-candidate acquisition/teardown qualification run only in the approved disposable
runner. Source implementation and internal tests alone do not qualify the public cleanup journey or the release.
