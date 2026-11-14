# Explicit Protected Cache Retirement

**Status:** Accepted; implemented, installed public cleanup journeys passed
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

[ADR-0025](0025-immutable-kernel-cache-lineage.md), accepted as a v0.3.0 design on 2026-10-14, amends only
that immutable-kernel cross-cache restriction. Fresh kernel logical schema 2 may publish an identical-byte successor
after synchronized old kernel removal and completed old-store retirement, preserving old authority and allocating
a global monotonic generation. Implementation, root Go verification and the installed successor journey passed;
see ADR-0025 for qualification scope. Existing schema 1 is not adopted, and mutable backing and cleanup authority
are unchanged.

[ADR-0023](0023-protected-branch-authority-layout.md) defines a fresh-layout extension for permanent snapshot locks
and volume-removal receipts. Signed `b254f010` implements that extension and passes focused root behavior/race
checks; installed shared- and distinct-filesystem retirement journeys subsequently passed as recorded there.
Existing branch layouts still refuse retirement. The extension does not migrate them or relax immutable bindings.

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
qualified. Clean `752110fa` subsequently passed complete pinned Go CI, the full root Jailer suite
(2,663 test/subtest passes, no failures/skips), and the real-agent release build. Normal installation inside
`mvmctl-030-test` passed with exact original-state preservation except the authorized canonical replacement;
candidate `0.3.0-rc.cache-752110fa` has SHA-256
`ea7bad330e625ff6374e34e6c65604266e93d8c524c702945e5e7fa4d74c87f5`.
Installed public cleanup subsequently passed through the shared- and distinct-filesystem journeys recorded in
ADR-0023 and the candidate-bound kernel successor journey recorded in ADR-0025. The
[current coverage checkpoint](../../tests/system/COVERAGE_MATRIX.md#v030-release-security-qualification-overlay)
records the accepted source-bound evidence join and remaining fault-coverage limits; it is not exhaustive proof
of every interruption or namespace race above.
Focused Go checks and installed-candidate acquisition/teardown qualification run only in the approved disposable
runner. Source implementation and internal tests alone do not qualify the public cleanup journey or the release.
