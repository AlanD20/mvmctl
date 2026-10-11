# Snapshot Functionality

This guide describes the current v0.3.0 snapshot implementation. Capture and restore use root-authenticated VM
authority and protected storage. Installed-candidate snapshot runtime qualification remains incomplete: the latest
six-case attempt reported one pass and five failures. Earlier focused results do not qualify the current full matrix;
see the [coverage matrix](../../tests/system/COVERAGE_MATRIX.md).

## Architecture

The unprivileged CLI calls `pkg/api`; the API resolves public selectors and coordinates local bookkeeping through
the VM controller. `VMSnapshotClient` sends named Capture, Inspect, Restore, and Remove requests to the authenticated
privileged dispatcher. Its root receiver, `VMSnapshotServer`, owns process admission, capture, protected artifacts,
and restore composition with the Jailer lifecycle.

The core snapshot domain owns only repository and selector resolution. It has no snapshot Controller or Service.
SQLite rows and returned capture results are display/bookkeeping projections, not authority to select disk files,
machine configuration, or network state. The closed root requests carry cache and snapshot identities, the source or
target VM ID where applicable, and pause/resume intent; they accept no caller-selected artifact paths.

[ADR-0018](../adr/0018-isolated-vm-execution-and-snapshot-authority.md) defines protected backing and consistent
capture. [ADR-0017](../adr/0017-per-vm-network-namespaces-and-typed-policies.md#snapshot-restore-endpoint-identity)
defines captured endpoint identity, and [ADR-0020](../adr/0020-networkless-vm-placement.md) defines networkless restore.

## Entry points

| Command | API operation | Behavior |
|---------|---------------|----------|
| `mvm snapshot create VM [--name NAME] [--pause]` | `SnapshotCreate` | Full capture of an active VM; disk-only capture of a fully cleaned stopped VM |
| `mvm snapshot ls` | `SnapshotList` | List local projections with best-effort relation enrichment |
| `mvm snapshot inspect SELECTOR` | `SnapshotInspect` | Inspect local metadata, or root metadata for an exact full ID missing locally |
| `mvm snapshot restore SELECTOR NAME [--resume]` | `SnapshotRestore` | Restore exactly one target VM from an indexed capture |
| `mvm snapshot rm SELECTOR... [--force]` | `SnapshotRemove` | Root removal before deleting local projections |

The CLI still exposes `--count` and `--network` on restore. The API requires `Count == 1`; a supplied network may
only resolve to the captured managed network. It cannot move a networkless capture onto a managed network.
`--resume` defaults to false. `--force` on removal skips confirmation and cannot bypass root reference checks.
See the [command reference](../REFERENCES.md#mvm-snapshot-alias-ss).

## Capture

1. The API validates/resolves the source, generates `crypto.SnapshotID(sourceVMID, createdAt)`, and publishes the
   full request ID through the progress callback before root effects. The CLI prints it to stderr even with `--json`.
2. Root locks the owned VM, rejects an outstanding rootfs export, and admits either its active process/device
   authority or its fully cleaned stopped device archive. Active capture pins and verifies the exact Firecracker
   process, jail root, current attached-volume inventory, and private API peer.
3. Root admits the cache and protected backing graph, then locks the captured backings in canonical order.
   Each backing must pass exclusive active-source or stopped-source capture checks. Competing writable access
   refuses capture; pausing only one VM does not authorize coordinated multi-VM capture.
4. Root creates an exclusive snapshot entry and records capture progress before pause/copy/publication effects.
   For a Running source it requests pause through the admitted API session; an already Paused source stays paused.
5. An active source produces a Firecracker `Full` memory/vmstate snapshot in an isolated, disk-backed directory
   beneath its admitted jail root. While the source remains paused and backing leases remain held, root copies
   rootfs, attached data volumes, kernel, optional cloud-init seed, memory, and vmstate into protected artifacts.
   A fully cleaned stopped source produces mode `disks`, with no memory or vmstate.
6. Root publishes `capture.json` binding the source, exact release, machine/device configuration, network placement,
   captured IP/MAC, guest drive IDs, and artifact identities/extents/digests. Only after publication and producer
   cleanup does it resume an originally Running source, unless `--pause` was requested. An originally Paused
   source remains paused; a stopped source remains stopped.
7. The VM controller updates its local status projection. The API then upserts the snapshot projection.
   A database failure after root publication preserves the durable capture and reports its full ID.

The Full capture policy coordinates one paused guest's RAM, rootfs, and attached disks. It does not promise
application transaction consistency or complete release qualification. Memory length must equal trusted captured RAM
in MiB × 1,048,576. The complete vmstate file is limited to 10,000,000 bytes. Storage admission reserves no physical
space and adds no per-data-volume or aggregate storage quota.

## Protected storage and projections

Artifacts live beneath the admitted cache at `.mvm-protected/snapshots/<snapshot-id>/`, bound to the root-owned
protected-store anchor. Snapshot operations use a permanent root-owned identity lock outside the removable entry.
Replacing an artifact directory cannot create a second lock for the same snapshot ID.

| Leaf | Purpose |
|------|---------|
| `capture.json` | Root-owned immutable source/configuration and artifact record |
| `capture-progress.json` | Durable stage, publication, and source-state recovery evidence |
| `rootfs.img`, `volume-<guest-drive-id>.raw` | Captured protected disks |
| `kernel`, optional `cloud-init.iso` | Captured boot resources |
| `memory`, `vmstate` | Immutable Full-capture state; absent for disk-only captures |
| `refs/<target-vm-id>.json` | Root-owned restore references |

The `snapshots` SQLite table stores names, source/display identities, resource sizes, derived display paths, and
`extra_config` bookkeeping such as guest-agent information. Restore obtains authoritative configuration and artifact
selection from root metadata, not these path/configuration columns. Listing does not discover unindexed root captures.
Keep the emitted full ID: inspect/remove can recover that exact selection without a local row; restore currently
resolves through the local snapshot repository.

## Restore

1. The API rejects multiple targets, resolves the snapshot, and obtains its root-authenticated projection.
   It checks the destination name and any explicit network selection before creating a fresh target ID.
2. A managed restore reserves the captured IP in the local lease repository before local VM publication or root
   launch. Root endpoint authority separately enforces the exact captured network, IPv4, and MAC. Conflicts,
   including a source that still owns the endpoint, refuse restoration. No fresh guest address/MAC is allocated.
3. Root locks the immutable capture, verifies its owner/cache/artifacts, and creates a durable target reference.
   The prepared restore capability derives launch intent from the capture. The captured release identity must
   remain supported; the current launch contract requires exact Firecracker/Jailer 1.17.0. Captures are not converted
   across versions, and vCPU/RAM configuration is not replaced by caller input.
4. The Jailer lifecycle registers the blocked target before mutable launch effects. Under that authority it
   creates independent protected rootfs and data-volume backing, preserving each guest-visible drive ID and access/cache
   intent while assigning target-specific logical volume IDs. Launch grants access to the isolated execution identity,
   seals configuration, and mounts only admitted resources.
5. After activation, root verifies the target process/release/placement and opens its authenticated API session.
   Full restore loads `/snapshot/vmstate` and immutable `/snapshot/memory`. A root-derived TAP override reconnects
   the captured NIC to new host topology while preserving guest IP/MAC. Vsock uses the fixed private runtime UDS
   override and retains its captured guest CID.
6. A networkless capture remains networkless: a fresh isolated loopback-only namespace, no managed endpoint,
   TAP, NIC, IP/MAC, or lease allocation. Full restore sends no network override.
7. Full restore keeps immutable memory backing and its reference until verified process cleanup releases them.
   Disk-only restore cold-boots the independent disks and releases its capture reference after successful setup.
   Without `--resume`, Full restore remains paused; disk-only restore pauses its cold boot after the API is ready.
8. The controller projects the root result into the VM row; the API projects new attached volume rows. Projection
   failures do not authorize deleting an already launched VM or reusing its endpoint.

## Failure ownership and removal

Capture progress records distinguish unpublished, unknown publication, and published outcomes, with the source VM
state and exact request ID. An uncertain or failed capture does not automatically resume the source or erase partial
root evidence. Reusing that capture ID refuses; inspect/remove the retained transaction before a new capture.
Historical pause progress is not proof of current VM state. Stopped VM cleanup removes only admitted producer
temporary outputs after verified process absence.

Before root restore is attempted, the API may unwind its new local rows, directory, and endpoint lease.
After an attempt, it retains the target identity and reservation unless an authoritative root response proves
`restore_no_commit`. That proof requires no committed VM authority, no surviving launch child, and completed
pre-registration reference cleanup. Transport failure alone cannot establish it. Once target registration transfers
ownership, lifecycle cleanup owns backing, process, topology, and snapshot-reference recovery.

Snapshot removal locks the exact protected entry and refuses any retained restore reference. Full restored VMs can
still depend on captured memory despite having independent writable disks. Root removes only admitted fixed leaves
and syncs the protected store; absent entries and safe interrupted removal suffixes converge on retry. Unexpected
entries or ambiguous ownership remain errors. Only a successful root removal permits the API to delete its local row;
a later database error is reported and can be retried. `--force` does not remove references or terminate VMs.

## Key files

| File | Responsibility |
|------|----------------|
| [internal/cli/snapshot.go](../../internal/cli/snapshot.go) | Commands, flags, progress, structured output |
| [pkg/api/snapshot.go](../../pkg/api/snapshot.go) | Public orchestration, endpoint reservation, projection/recovery |
| [pkg/api/inputs/snapshot.go](../../pkg/api/inputs/snapshot.go) | Public validation and selector resolution |
| [internal/core/snapshot/](../../internal/core/snapshot/) | Repository and resolver |
| [internal/core/vm/controller.go](../../internal/core/vm/controller.go) | Typed capture/restore client and VM projection |
| [snapshot_wire.go](../../internal/service/jailer/snapshot_wire.go), [snapshot_handler.go](../../internal/service/privileged/snapshot_handler.go) | Closed requests and authenticated root dispatch |
| [snapshot_capture.go](../../internal/service/jailer/snapshot_capture.go), [snapshot_api.go](../../internal/service/jailer/snapshot_api.go), [snapshot_producer.go](../../internal/service/jailer/snapshot_producer.go) | Admitted source capture and private producer outputs |
| [snapshot_store.go](../../internal/service/jailer/snapshot_store.go), [snapshot_progress.go](../../internal/service/jailer/snapshot_progress.go), [snapshot_references.go](../../internal/service/jailer/snapshot_references.go) | Protected publication, progress, locks, and references |
| [snapshot_restore.go](../../internal/service/jailer/snapshot_restore.go), [snapshot_load.go](../../internal/service/jailer/snapshot_load.go), [lifecycle_restore.go](../../internal/service/jailer/lifecycle_restore.go) | Immutable restore preparation, independent backing, and typed lifecycle load |
| [snapshot.go](../../internal/lib/model/snapshot.go) | Local projections and path-free controller configuration |
