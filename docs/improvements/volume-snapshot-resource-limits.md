# Volume and snapshot resource-limit proposal

**Status:** Partially approved for implementation on 2026-09-19; qualification remains pending.
**Evidence checked:** 2026-09-13, Firecracker `v1.16.1`, commit
[`2038188f145fb81b8d098147a10e9d9f392fd22f`][release].

The user has approved raw-only VM data volumes while retaining QCOW2 image import and conversion. The earlier
suggestions of 16 data volumes, 16 TiB of data-volume capacity, and 1 GiB of `vmstate` were not approved. This proposal
replaces those unsupported numbers with a device-budget calculation and the upstream state-loader ceiling, and leaves
storage safety budgets open where measurements and a product decision are still needed.

## Approved implementation scope (2026-09-19)

The user approved the derived MMIO device-budget calculation, the 10,000,000-byte complete
`vmstate` ceiling, and exact memory-file length derived from trusted captured RAM. The
approval does not select a uniform 13-volume product limit, qualify PCI or aarch64, approve
new per-volume or aggregate storage caps, or establish snapshot capture/immutability authority.
Those remaining decisions and evidence gaps stay open. Approval records the intended
implementation contract, not completed implementation or validation.

The current pass is implementation-only: no tests are written or run, and no validation or
review passes are performed. Only formatting and linting are permitted. Runtime admission
checks remain part of the production implementation; qualification is deferred, not waived.

## Existing local contract and implementation gaps

[ADR-0016](../adr/0016-root-owned-single-binary-privileged-dispatch.md#paths-process-identity-and-runtime-state)
accepts base-launch rootfs files from 128 MiB through 16 TiB, exact expected-size equality, pinned descriptors, and
closed resource identities. It explicitly leaves volume and snapshot admission pending. Its rootfs ceiling does not
establish a data-volume, aggregate-storage, or snapshot-copy ceiling. The user's raw-only decision supersedes its
transitional `raw|qcow2` data-volume naming choice; this document does not implement that cutover.

The current [product constants](../../internal/infra/constants.go) admit 128–65,536 MiB of RAM and 1–32 vCPUs.
[Public VM input](../../pkg/api/inputs/vm_create.go) and the
[private launch builder](../../internal/service/jailer/managed_vm_config.go) enforce those ranges. The private builder
emits rootfs, one `eth0`, optional cloud-init and optional vsock, without balloon, entropy, hotplug memory, or data
volumes. It does not enable Firecracker PCI transport. The transitional [Jailer entry](../../internal/service/jailer/entry.go)
does pass `--enable-pci` when `PCIEnabled` is selected; [nested virtualization](../../internal/core/vm/firecracker.go)
forces that selection. Existing public launches therefore are not all MMIO. Those transitional paths are not evidence
that the corresponding private volume admission and lifecycle contracts exist.

[Snapshot creation](../../pkg/api/snapshot.go) currently copies rootfs and requests `memory` and `vmstate` for running
or paused source VMs. Stopped VMs retain the existing rootfs-only snapshot mode, with no memory or VM-state capture.
The memory, `vmstate`, and Full/Diff discussion below applies only to stateful snapshots from running or paused VMs;
it does not remove or change that rootfs-only mode. SQLite RAM/vCPU fields are caller-owned metadata, not trusted
restore authority. The
[Firecracker client](../../internal/lib/firecracker/client.go) omits `snapshot_type`, which upstream defaults to `Full`
([`CreateSnapshotParams`][snapshot-config]). Data-volume content capture, consistency, and restoration remain a separate
contract. [Image import](../../internal/core/image/service.go) must retain QCOW2-to-raw conversion before rootfs extraction.

## Pinned upstream evidence

| Question | Evidence and owning source | Meaning for mvmctl |
|---|---|---|
| MMIO interrupt budget | `GSI_LEGACY_START..=GSI_LEGACY_END` is 5–23 on [x86_64][x86-layout], 0–95 on [aarch64][arm-layout]. [`ResourceAllocator::new`][allocator] initializes those inclusive pools. | Enforced allocator budgets are 19 and 96 shared legacy interrupt slots, not data-volume counts. |
| Cost per MMIO virtio device | [`MMIODeviceManager::register_mmio_virtio_for_boot`][mmio] calls `allocate_mmio_resources(..., 1)`; `MMIO_LEN` is 4,096 bytes. | Each block, network, vsock, balloon, entropy, pmem, or virtio-mem device using this transport consumes one slot. |
| Built-in reservations | [`build_microvm_for_boot`][builder] attaches VMGenID and VMClock; [`VmGenId::new`][vmgenid] and [`VmClock::new`][vmclock] each allocate one legacy GSI. | Reserve two slots on both architectures even when no optional virtio devices are configured. |
| Architecture-specific reservations | [x86 legacy devices][legacy] use fixed GSI 4 and 1, outside its pool. [`attach_legacy_devices_aarch64`][device-manager] creates RTC and creates serial when boot arguments contain `console=`; [MMIO registration][mmio] allocates one GSI for each. | Subtract RTC and, when present, serial on aarch64. Do not subtract x86 serial/keyboard again. Boot timer has no GSI. |
| Transport matters | [`VmResources`][resources] defaults `pci_enabled` to false; [`DeviceManager::attach_virtio_device`][device-manager] selects PCI or MMIO. | MMIO numbers cannot qualify PCI launch or hotplug. A guest `pci=` argument alone does not prove the selected device transport. |
| Raw block capacity | [`DiskProperties::file_size` and `new`][block-device] seek to EOF and set `nsectors = disk_size >> SECTOR_SHIFT`; [`SECTOR_SHIFT`][block-constants] is 9. [`Request::parse`][block-request] checks requests against that sector count. | Guest-visible capacity is logical file length rounded down to 512 bytes. The inspected native block path supplies no 16 TiB product ceiling or QCOW2 virtual-capacity decoding. |
| Serialized VM state | [`SNAPSHOT_DESERIALIZATION_BYTES_LIMIT` and `Snapshot::load`][snapshot-code] reject a complete file exceeding 10,000,000 bytes, including its final 8-byte CRC. | This is an enforced upstream input ceiling, not 10 MiB and not a memory-file ceiling. A 1 GiB `vmstate` allowance cannot make those files loadable. |
| State-size dependencies | [`MicrovmState`][persist] stores KVM/VM state, `Vec<VcpuState>`, and device state. [`MachineConfig::update`][machine-config] rejects zero or more than 32 vCPUs. | State depends on CPU/architecture/device configuration. Guest RAM contents live separately; no measured per-vCPU or per-MiB state-size formula is established here. |
| Memory-file length | [`KvmVm::snapshot_memory_to_file`][vm-memory] sets logical length from total guest memory for both Full and Diff; [`dump`/`dump_dirty`][memory] write slots or dirty pages at their offsets. | Sparse allocation and dirty-page count do not reduce the required logical extent. Current ordinary RAM-only profiles should produce exactly configured MiB × 1,048,576 bytes. |
| Restore checks and trust | [`snapshot_file`][memory] checks summed region sizes against EOF with checked addition. [`snapshot_state_sanity_check`][persist] checks memory-region shape. [Snapshot guidance][snapshot-doc] treats files as trusted and requires integrator storage management. | Size checks and CRC do not authenticate snapshots or replace trusted root metadata, resource accounting, and lifecycle authority. |

## Recommended device-count policy

Recommend deriving the admission bound from the exact selected release, architecture, transport, and complete device
set. For the private builder's current MMIO profile, the interrupt condition is:

```text
data volumes <= GSI pool - built-in reservations - rootfs - cloud-init - NICs - vsock - other virtio devices
x86_64: 19 - 2 - 1 - cloud-init(0|1) - 1 - vsock(0|1) = 15 - cloud-init - vsock
aarch64: 96 - 2 - RTC(1) - serial(0|1) - 1 - cloud-init(0|1) - 1 - vsock(0|1)
```

Thus rootfs + cloud-init + one NIC + vsock leaves **13 data-volume slots on x86_64**, and **88 on aarch64 with serial**
(89 without serial). These are deductions from enforced upstream allocation, not tested mvmctl support claims.
Sixteen data volumes already exceed the x86 MMIO budget even without cloud-init and vsock. Enabling balloon or entropy
would consume another slot each; enabling pmem or virtio-mem would also require accounting for each device. Count a
cloud-init ISO only when it is actually a block device. Kernel/initrd, logs, and metrics are not virtio devices.

Prefer this topology calculation over a constant that ignores reservations. If a uniform product maximum is desired,
**13** is the evidence-based candidate for today's fully provisioned x86/ARM MMIO profile, subject to approval and
boundary qualification. It would deliberately leave additional ARM capacity unused. Do not advertise 88-volume
support from arithmetic alone. Guest command-line space, file descriptors, mount/protocol bounds, and successful
boot/restore still require qualification. A PCI profile needs its own resource and lifecycle review.

## Recommended storage and snapshot admission

For approved raw-only data volumes, bind a canonical raw resource identity, expected logical capacity, and access
intent to the pinned descriptor. Recommend positive, 512-byte-aligned capacity and exact logical-size equality so the
guest sees the intended capacity. The alignment rejection is a proposed local rule: upstream merely hides a partial
last sector. Logical `statx` size represents raw capacity; allocated blocks represent current physical consumption.
A sparse file can be large while using little disk. Neither number alone proves that future writes can succeed.

The inspected block code does not justify a portable maximum raw capacity. Host file-offset representation,
filesystem limits and features, available space, quotas, guest filesystem tooling, and copy/provisioning behavior must
all fit the selected deployment. Do not infer an exabyte-sized supported disk from a `u64` sector field. If 16 TiB is
retained as a candidate for consistency with rootfs policy, identify it as an **unmeasured local product choice** and
decide separately whether it means per-volume or aggregate capacity. This proposal selects neither interpretation.

Recommend a **10,000,000-byte maximum for the complete `vmstate` file**, matching the pinned loader, with bounded
reading and an oversize probe before parsing. This remains a proposed mvmctl admission rule. The upstream loader
also checks architecture magic, version compatibility, and CRC; passing those checks is not evidence of trusted
origin or bounded decoded-memory cost. Reject incomplete/invalid state and bind admitted state to trusted capture
metadata. No extra 8-byte allowance applies. A lower operational limit requires measurements; a higher limit requires
an upstream compatibility change, not a larger local constant.

For the current RAM-only profile, recommend **exact memory length equal to trusted captured RAM MiB × 1,048,576**,
using checked arithmetic within the existing 128–65,536 MiB local range. The derived maximum is **64 GiB**, not 1 GiB.
Equality is a proposed stronger local check: upstream rejects short files but does not require exact EOF equality.
Do not trust the current caller-owned SQLite RAM field for this decision. Retain the source identity and require its
contents and length to remain immutable throughout the restored VM lifetime; descriptor pinning alone does not stop a
caller from writing the same file. Upstream file-backed restore maps memory privately and faults pages on demand;
external changes corrupt guest memory ([restore requirements][snapshot-doc]). Establishing that immutable backing
authority remains part of the pending private snapshot contract.

Keep Full snapshots as the initial private contract unless Diff is separately approved. Upstream Diff files retain the
full logical memory length and may contain holes; a new differential layer is not a standalone complete memory image.
An existing matching-size destination can instead be updated in place. Any future support must define base identity,
merge ordering, completeness, and sparse-preserving copying; never treat sparse allocation as a smaller safety budget.
See [memory persistence][vm-memory] and [snapshot management][snapshot-doc].

Independently account for simultaneous memory output, rootfs/data copies, temporary files, retained snapshots, and
failure recovery. A per-file cap cannot establish a per-VM, per-user, or host storage quota. Snapshot data-volume
consistency and capture scope must be resolved before extending today's rootfs-only copying behavior.

## Decisions and verification gates

| Policy decision | Approval status and evidence still needed |
|---|---|
| Maximum attached data volumes | Derived MMIO topology calculation approved on 2026-09-19. A uniform ceiling of 13 is not selected. Qualify limits before claiming support. |
| Per-volume minimum/maximum capacity | Positive sector-aligned exact capacity is proposed; choose product minimum and maximum after filesystem, guest-tooling, and workload measurements. No new capacity number is selected. |
| Aggregate storage and concurrency | Choose separate per-VM/user/host logical and physical budgets and concurrent snapshot/copy limits from deployment capacity and failure tests. |
| `vmstate` file maximum | Exact upstream 10,000,000-byte loader ceiling approved on 2026-09-19. Any lower operational cap remains unmeasured. |
| Memory artifact bound | Exact trusted captured RAM length approved on 2026-09-19 for today's RAM-only profile, deriving at most 64 GiB from the existing product RAM maximum. Trusted capture and immutable backing remain necessary. |
| Snapshot modes and data-volume contents | Start with Full; define volume consistency/capture/restore semantics before adding data-volume snapshots or differential chains. |

Required gates for accepting and qualifying the implemented policy:

1. Record the complete generated transport/device configuration for both architectures. Verify the derived count and
   one-over-limit failure using the pinned release, including optional-device combinations and boot-argument length.
2. Measure full `vmstate` length, serialization/restore time, and peak resource use across 1 and 32 vCPUs, 128 MiB and
   64 GiB RAM, supported CPU templates/hosts, and the proposed maximum device set. The byte ceiling alone does not
   qualify allocation cost or all configuration combinations.
3. Test exact memory extent and region sums, short/oversized/changed files, sparse files, unsupported release/architecture,
   and corrupt state. Use trusted root capture metadata and retained resource identity through restore and cleanup.
4. Qualify proposed raw capacities on selected allowed host filesystems and guest filesystems: create, format, attach,
   last-sector I/O, growth, copy, cancellation, disk-full/quota failure, and recovery. Record logical and allocated bytes
   separately, including temporary peak usage. Sparse allocation alone does not qualify capacity or throughput.
5. Define independent aggregate storage admission and snapshot consistency before wiring privileged volume/snapshot
   operations. Run resulting system qualification only through the project's disposable-runner workflow.

This was one bounded source pass. No VM, host storage, throughput, deserializer allocation, or boundary measurements
were run. PCI capacity, guest/host filesystem maxima, peak copy budgets, and data-volume snapshot consistency remain
explicit evidence gaps. Approval and implementation must update the accepted contract and its tests; this research
does not authorize a new limit, enable a private runtime path, or claim release qualification.

[release]: https://github.com/firecracker-microvm/firecracker/commit/2038188f145fb81b8d098147a10e9d9f392fd22f
[x86-layout]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/arch/x86_64/layout.rs
[arm-layout]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/arch/aarch64/layout.rs
[allocator]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/vstate/resources.rs
[mmio]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/device_manager/mmio.rs
[builder]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/builder.rs
[vmgenid]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/devices/acpi/vmgenid.rs
[vmclock]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/devices/acpi/vmclock.rs
[legacy]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/device_manager/legacy.rs
[device-manager]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/device_manager/mod.rs
[resources]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/resources.rs
[block-device]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/devices/virtio/block/virtio/device.rs
[block-constants]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/devices/virtio/block/virtio/mod.rs
[block-request]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/devices/virtio/block/virtio/request.rs
[snapshot-code]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/snapshot/mod.rs
[persist]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/persist.rs
[machine-config]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/vmm_config/machine_config.rs
[vm-memory]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/vstate/vm.rs
[memory]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/vstate/memory.rs
[snapshot-config]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/src/vmm/src/vmm_config/snapshot.rs
[snapshot-doc]: https://github.com/firecracker-microvm/firecracker/blob/2038188f145fb81b8d098147a10e9d9f392fd22f/docs/snapshotting/snapshot-support.md
