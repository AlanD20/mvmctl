# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - Unreleased

### Added

- Added mandatory Jailer launch with isolated per-VM system identities, private API/vsock sockets, and
  authenticated root-brokered control. Create, start, reboot, and restore verify the selected resources and live
  process; direct Firecracker launch fallback is removed. See [runtime guidance](docs/RUNTIME.md).
- Added one root-owned system installation at `/usr/local/bin/mvm`. Administrators install a trusted artifact with
  `sudo ./mvm host install-system`, then enroll operators with `sudo /usr/local/bin/mvm host init`.
  Operators run the public CLI without sudo; the managed sudoers policy grants only the fixed privileged marker.
  See [installation](README.md#installation) and
  [ADR-0016](docs/adr/0016-root-owned-single-binary-privileged-dispatch.md).
- Added resumable administrator setup, multiple-operator enrollment, and root-owned host admission.
  Direct-root setup/reset requires explicit `--operator USER`; authenticated sudo selects its invoking operator.
  `host reset` requires exclusive quiescence, refuses live/ambiguous or changed administrator state, restores
  recorded settings, and leaves admission disabled. See [host commands](docs/REFERENCES.md#mvm-host).
- Added a root-created network namespace for every VM, with receiver-derived fixed link/runtime names and
  default-deny managed same-network and cross-network traffic. Firewall enforcement uses direct Go nftables
  transactions with anti-spoofing, connection tracking, and conditional internet egress/NAT.
- Added separate `mvm policy traffic` and `mvm policy exec` families. Traffic grants permit source-network or
  exact source-VM TCP/UDP access to a destination VM; exec grants permit directional execution as an exact
  non-root destination user, independently of same-network, cross-network, or networkless placement.
  Neither policy grants the other's authority. See [policy reference](docs/REFERENCES.md#mvm-policy).
- Added explicit VM-to-VM execution opt-in through `mvm exec --allow-vm-exec` and
  `mvm-agent remote --user USER DESTINATION -- COMMAND`. The authenticated relay binds both live VM identities,
  supports separate output streams and cancellation, and records command-redacted audit correlation.
  See [exec reference](docs/REFERENCES.md#mvm-exec) and
  [ADR-0017](docs/adr/0017-per-vm-network-namespaces-and-typed-policies.md).
- Added VM-to-VM relay limits of eight host-global sessions, a five-minute absolute lifetime, a 32-KiB command,
  8-MiB combined output, and 64-KiB frames. Cancellation terminates the admitted command process group;
  audit files are operator-owned observability, not tamper-proof root authority.
- Added `mvm vm create --no-network` with an isolated loopback-only namespace, no NIC/IP/MAC allocation, and
  vsock guest access. It conflicts with network/IP/MAC selection and NoCloud network mode.
  See [VM creation](docs/REFERENCES.md#mvm-vm) and
  [ADR-0020](docs/adr/0020-networkless-vm-placement.md).
- Added protected active rootfs/raw-volume backing and root-controlled full snapshots covering rootfs,
  RAM/vmstate, and attached data volumes under exclusive write ownership. Competing writable VM access refuses
  capture. Restore creates independent writable disks and retains immutable captured memory backing.
  See [snapshot reference](docs/REFERENCES.md#mvm-snapshot-alias-ss) and
  [ADR-0018](docs/adr/0018-isolated-vm-execution-and-snapshot-authority.md).
- Added snapshot recovery through typed inspect/remove operations and retained capture intent.
  `snapshot create` emits its full request ID to stderr before capture, including JSON mode; retain that ID
  to inspect or remove a capture whose local projection was interrupted.
- Added authenticated live-volume attachment readiness: Running attach requires the exact guest disk
  serial/capacity and unchanged root attachment identity. Paused attach stages the binding with a retryable
  deferred-readiness error; resume and retry the same attach. Stopped attach takes effect on next start.
  See [volume behavior](docs/RUNTIME.md#volumes-snapshots-and-recovery).
- Added cgroup-v2 CPU, swap, PID, and pressure-only memory control with verified root readback.
  `memory.high` is guest RAM plus configured VMM headroom; `memory.max=max` permits exceeding that threshold.
  `vm inspect` reports requested/observed policy and usage. See
  [ADR-0019](docs/adr/0019-pressure-only-vm-memory-control.md).
- Added global `--json` schema-1 success/error/partial envelopes, typed command data, structured error
  code/class/details, and per-item batch outcomes. Empty array listings remain `[]`; partial results exit
  unsuccessfully. See [output contract](docs/REFERENCES.md#command-reference).
- Added `mvm init --binary-version VERSION` for deterministic exact-pair selection when no local pair exists.
  See [initialization](docs/REFERENCES.md#mvm-init).

### Changed

- Changed the approved VM launch contract to the exact matching Firecracker/Jailer **1.17.0** pair, selecting the
  latest stable official release for this dependency update. The earlier 1.16.2 update fixed the upstream
  1.16.1 pause/resume-vsock defect. Older audited pairs remain manageable binary artifacts, not launch fallbacks.
  Future release updates review and qualify the latest stable matching pair before changing the exact pin.
  Sealed launch supports x86 nested virtualization; ARM nested launch remains refused.
  See [release requirements](docs/RUNTIME.md#vm-launch-and-resource-control).
- Changed binary pull/remove to use the checksum-verified root release store and architecture-scoped pair
  projections. Removal accepts exact versions; `--force` cannot bypass root references or ownership checks.
  A failed local projection after root success is repaired by retry. See [binary commands](docs/REFERENCES.md#mvm-bin).
- Changed VM lifecycle/status to use bounded root-authoritative observation rather than SQLite status or PID
  mirrors. Observation errors propagate; paused display requires a live verified process. Cleanup reconciles
  stopped/crashed/prior-boot records and finishes root removal before deleting the local projection.
- Changed traffic creation to apply the root grant before local publication, and removal to prove revocation or
  absence first. Stop preserves endpoint reservations/grants for restart; VM removal invalidates them.
- Changed snapshot restore to preserve the captured release, drive IDs, guest CID, and managed network/IP/MAC.
  Restore reserves the exact endpoint; conflicts and a different requested network refuse restoration.
  Fresh address allocation, cross-network restore, and simultaneous endpoint clones are unsupported.
- Changed volume growth and `volume rm --force` to permit an attached volume only when root observation proves
  its VM stopped; detaching first is unnecessary. Force does not hot-unplug a running VM, and retained writers
  still block protected-backing removal. See [volume commands](docs/REFERENCES.md#mvm-volume).
- Changed NoCloud network delivery to one gateway-bound TCP **8000** listener per network, with typed
  apply/revoke authority. Custom ports and bind collisions refuse; the grant reserves a service slot rather
  than authenticating its listener process. See [cloud-init guidance](docs/RUNTIME.md#root-filesystem-provisioning-and-cloud-init).
- Changed LoopMount provisioning, read-only inspection, and optimization to closed typed root operations.
  Retained account creation, SSH/sudo setup, agent/seed injection, hostname/DNS, automatic deblob, fstab repair,
  supported filesystem growth/shrink, and Btrfs-to-ext4 conversion with durable recovery.
- Changed XFS support to typed inspection/provisioning, deblob, and fstab repair at existing capacity.
  XFS resize/shrink refuse before mutation. GuestFS remains an optional mutually exclusive provisioner.
  See [provisioner guidance](docs/RUNTIME.md#root-filesystem-provisioning-and-cloud-init).
- Changed managed artifacts to fixed receiver-derived names: volume IDs select `volumes/<id>.raw`, kernel
  IDs select `kernels/<id>`, VM rootfs uses `rootfs.img`, and snapshots use `rootfs.img`, `memory`, and
  `vmstate`. Custom cloud-init ISO inputs are copied into the fixed managed leaf.
- Changed storage admission to pin ownership, file/mount identity, and local-filesystem placement.
  Explicit kernel same-content reimport verifies the retained digest; VM/snapshot references block retirement.
  See [protected cache retirement](docs/adr/0022-explicit-protected-cache-retirement.md) and
  [protected authority layout](docs/adr/0023-protected-branch-authority-layout.md).
- Changed console discovery and relay-only stop to root-verified relay/endpoint authority. Stopping the console
  preserves Firecracker and vsock; caller PID mirrors and counterfeit socket peers cannot authorize signaling.
  Persistent logs remain available after stop. See [console commands](docs/REFERENCES.md#mvm-console).
- Changed VM-to-image import to copy the current protected root disk: Running sources pause for copy and resume
  before extraction; Paused/Stopped sources retain their state. Independent temporary exports use
  `/var/lib/mvmctl/vm-rootfs-export-data` and need additional space there.
- Changed interrupted image-import recovery to typed exact-ID forced removal, with busy-export refusal and
  explicit source-state recovery errors. Ext4 journal replay occurs only on a disposable inspection copy.
  See [VM image import](docs/REFERENCES.md#mvm-image).
- Changed `host clean` to consult the caller's root-owned VM inventory, including records absent from SQLite.
  It refuses running or incompletely observed VMs; it is caller-scoped cleanup, not global reset authority.
  Cache cleanup propagates errors and uses typed operations.

### Breaking changes

- Required a clean installation: v0.3.0 does not migrate, backfill, adopt, or preserve legacy databases, running
  VMs, jails, cgroups, TAPs, firewall state, policy rows, or remote-exec flags. Stop/remove old VMs and preserve
  desired user artifacts separately before administrator cleanup and fresh initialization; ambiguous state refuses.
  See [installation requirements](README.md#installation) and
  [clean-break contract](docs/adr/0016-root-owned-single-binary-privileged-dispatch.md).
- Removed user-owned binaries as sudo targets. Canonical system upgrades require administrator
  `host install-system`; `self-update apply` refuses `/usr/local/bin/mvm` but remains available for user-owned
  artifacts. Invoking/system privileged protocol mismatch stops before effects.
- Required cgroup v2 with the necessary controllers and kernel nftables bridge/inet, connection-tracking, and
  NAT support. Managed networks require non-overlapping RFC1918 prefixes with usable hosts (`/30` or larger).
  Enabled NAT requires one unambiguous eligible IPv4 default-route uplink; caller-selected uplinks are removed.
- Removed flat policy commands, `allow_remote_exec`/legacy VM-wide trust state, the `remote_vm` protocol,
  iptables/`nft` CLI backends and selectors, raw host-tool sudo grants, and public `run provision`/`run jailer`.
  Recreate typed traffic/exec grants; previously unrestricted managed peer traffic now starts denied.
- Removed `volume create --format`, including `--format raw`, and QCOW2 data-volume create/resize.
  Non-raw records may be inspected/removed but cannot be attached, resized, or relaunched.
  QCOW2 image import/conversion and volume read-only/shareable/writeback options remain supported.
- Removed public `api_socket_path`, custom Firecracker/cloud-init/console filenames, and kernel API destination
  fields `output_dir`, `name`, and `output_path`. Control uses the root broker; managed paths are receiver-owned.
- Changed JSON reads from bare objects/lists to envelope `data`. Streaming/interactive exec, SSH, logs,
  completion, service run, init wizard, and console attachment reject JSON before effects; console state/kill support it.
- Rejected legacy remote-exec databases, unique guest-CID schemas, older root journals, and incompatible protected
  layouts without adoption. Separate vsock UDS endpoints now permit independent CID namespaces.
- Rejected FUSE, network, overlay/stacked, and automount cache roots. Custom `MVM_CACHE_DIR` remains supported
  on ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or ZFS.
- Required fresh 1.17.0 snapshots and matching-pair restore qualification; earlier snapshot results do not qualify
  this pair. Historically, Firecracker 1.16.2 snapshots are incompatible with 1.16.0/1.16.1 in both directions.
  Older snapshot state is neither converted nor relabeled, and cross-version compatibility is not asserted. See
  [upstream release notes](https://github.com/firecracker-microvm/firecracker/releases/tag/v1.16.2).

### Fixed

- Fixed OpenRC agent startup to run before networking, chronyd, and cloud-init, without extending exec deadlines
  or changing authentication. See [ADR-0024](docs/adr/0024-early-openrc-agent-startup.md).
- Fixed resume to consult root-owned pending-export admission and authenticated Firecracker state before success;
  unresolved export recovery cannot be bypassed by cached Running/Starting status.
- Fixed prior-boot VM/network cleanup and shared-state directory admission while retaining private permissions,
  exact process absence, and root ownership. Interrupted creation without a database row can recover through
  exact full-ID forced VM removal; original caller staging files are retained and reported.
- Fixed protected snapshot/volume/cache retirement to separate permanent authority from removable data branches,
  including distinct-filesystem caches and retry after interrupted deletion.
- Fixed same-network traffic grants under `br_netfilter` by adding the exact inet companion allow;
  default deny and the host's bridge-netfilter setting remain intact.
- Fixed stale guest-neighbor removal to invalidate only the admitted dynamic IPv4 neighbor on its exact bridge.
  Fixed rapid same-name/subnet network recreation to use a fresh identity.
- Fixed Jailer startup credential/sealing races, actual leaf-cgroup verification/removal, disabled-log admission,
  and slow disk preparation consuming the child's setup deadline.
- Fixed live attach/detach namespace handling and authenticated guest-device removal before backing access revocation.
  Known-applied attachment failures retain the binding for retry rather than falsely making the volume available.
- Fixed cloud-init OFF independently of deblob/`--skip-deblob`, safe seed-ISO permissions, Ubuntu usrmerged
  systemd admission, and NoCloud readiness-pipe deadlines.
- Fixed read-only LoopMount inspection access-time drift and fixed-path `blkid` fallback.
  Partition extraction falls back to `parted` after failed/malformed `sfdisk` probes instead of assuming no table.
- Fixed image SHA-512 sidecar verification, including Alpine bare-token checksums; malformed/unavailable configured
  metadata refuses publication. Fixed automatic import format resolution and long image name/version selectors.
- Fixed image pull/import and `kernel pull --default` to return committed default state and propagate write errors.
  Kernel builds honor explicit `MVM_TEMP_DIR`; official kernel configuration includes bridge connection tracking.
- Fixed GuestFS probe timeout units and removed guessed global orphan/process cleanup.
  Cache previews no longer prune unrelated GuestFS state.
- Fixed moduleless built-in-KVM host initialization, first-read capacity reporting, and failed sudo setup being
  marked complete. Administrator reset errors now report their stage and bounded cause.
- Fixed mixed VM creation failures to retain attempted IDs and primary/recovery errors in
  `error.details.failed_vms`/`created_vm_ids` with `data: null`; atomic rollback does not promise removal.
  Fixed per-selector volume outcomes, snapshot partial errors, cache error propagation, and exec-port validation.
- Fixed ambiguous image-prefix errors and human-readable byte sizes in inspection output.
- Fixed system-test runner base-image reuse to require one present exact type/version match and rebuild removal
  to use that admitted image's full ID. This tooling change does not alter public image resolver semantics.
- Fixed system-test command timeouts to interrupt the owned CLI caller first, with bounded kill/reap fallback,
  retained output and cleanup errors. A timeout remains a failure even if shutdown completes successfully.

### Known limitations and qualification

- Kept v0.3.0 unreleased and not release-qualified. The complete applicable
  installed Python matrix, reusable-base qualification, clean-host Tier 3, remote CI, and
  final approval remain release gates. Earlier focused results bind only their tested inputs; see the
  [coverage matrix](tests/system/COVERAGE_MATRIX.md) for exact evidence.
- Retained the observed Firecracker 1.16.2 live PCI detach/reattach failure: the disk did not reappear after BAR relocation.
  The approved 1.17.0 dependency update includes an upstream source remedy; attachment readiness detects failure but
  cannot repair it. Matching-pair launch, hotplug, pause/resume-vsock, and fresh snapshot qualification remain pending.
- Deferred aarch64 runtime qualification pending suitable hardware; cross-compilation does not establish runtime support.
- Retained host/ancestor/global OOM risk under pressure-only memory control. Bounded pressure/guest-exhaustion
  observations neither attribute the original saturation incident nor guarantee survival under arbitrary pressure.
- Retained failure visibility for ambiguous ownership, interrupted effects, and durability/cleanup errors.
  Unknown state is preserved rather than adopted or deleted. Snapshot projection recovery may require the emitted
  full ID; VM image import is a root-disk copy, not an application-consistent RAM/data-volume snapshot.
- Kept release-derived device budgets, the 10,000,000-byte complete vmstate limit, and trusted captured-RAM length.
  No new per-data-volume or aggregate storage quota is promised; operators must budget capture, restore, and recovery
  space. See [resource-limit decisions](docs/improvements/volume-snapshot-resource-limits.md).

## [0.2.0] - 2026-07-10

### Added

#### `mvm vm create`
- New `--allow-remote-exec` flag. When set, the VM can both issue and accept remote exec commands to/from other flagged VMs. Configurable via `defaults.vm.allow_remote_exec` (default `false`).

#### Vsock remote exec (VM → Host → VM relay)
- Guest agent (`mvm-vsock-agent`) now has a `remote <destination> -- <command>` subcommand that connects to the daemon's local Unix socket and requests execution on another VM.
- Guest agent daemon opens a local Unix socket (`/var/run/mvm-vsock-agent.sock`) for in-VM IPC. The daemon forwards `remote_vm` frames through the existing host→guest vsock connection.
- Host-side `Client.Exec()` read loop dispatches unknown frame types to `OnHostFrame` callback when set.
- New `internal/vsockhandler/` package receives guest-initiated frames, resolves target VMs, checks `RemoteExec` on both source and target, and performs a streaming relay (frame-by-frame, no buffering) via exported `vsock.SendFrame`/`vsock.ReadFrame`/`vsock.DialVM`.
- Protocol primitives in `internal/core/vsock/protocol.go` exported: `SendFrame`, `ReadFrame` (returns type + data bytes), `DialVM`. Internal `readFrameRaw` helper for typed reads.
- `RemoteVMRequest` and `RemoteVMResponse` types defined in `internal/service/vsockagent/protocol.go`.
- Both source and target VM must have `remote_exec = true`. Source is checked before parsing the request payload.
- Error codes added: `CodeUnauthorized`, `CodeVMNotRunning`, `CodeVsockConfigNotFound`.

#### `mvm vm inspect`
- Now shows the vsock agent configuration (guest CID, UDS path, port, agent version, and upgrade state) when a VM has a vsock record. The auth token and redundant `vm_id` are intentionally omitted, and `agent_version` is persisted at VM creation and corrected on first agent contact.
- The `networking.network` block no longer includes the network's full DHCP lease list.
- Now shows `allow_remote_exec` and `nested_virt` flags.

#### `mvm kernel pull`
- `--features` now accepts `all` or `*` as a wildcard to enable every feature defined in the selected kernel spec.
- Feature names are now validated against the spec's `features` map instead of a hardcoded list.
- Enabled features are persisted and shown in `mvm kernel inspect`.
- Kernel files are now stored with their content-addressed ID as the filename.
- New `--skip-checksum` flag to bypass SHA256 verification when the checksum server is unavailable.

#### `mvm env`
- New `image_import` step type for importing local images and VM rootfs in environment specs.
- Exec/SSH steps now support `ignore_errors: true` to continue on non-zero exit codes.
- `image_import` destroy is a no-op — imported images persist in the database and on disk.
- `env apply` now accepts a remote URL (`https://` or `http://`) in place of a spec file path. The spec is fetched over HTTP and parsed identically to a local file. `env diff` and `env destroy` also support URLs.
- `image_import` apply now always delegates to the API layer, enabling `force: true` to re-import and replace existing images.
- All steps now support `removes` field to destroy resources mid-pipeline after the step completes.
- New top-level `ephemeral: true` field — auto-runs `env destroy` on pipeline completion (success or failure). Zero cleanup overhead. See `docs/ENV_SPEC_REFERENCE.md`.
- `removes` now updates the workflow state after destroying each resource, so a subsequent `env destroy` doesn't try to tear down already-removed resources.
- `NetworkStep.Destroy` and `KeyStep.Destroy` now treat "not found" as success — already-deleted resources during destroy no longer abort the process.

#### `mvm image import`
- Renamed `source_path` to `source` in the input struct (breaking — no backward compat).
- Now runs `sync` on running source VMs via vsock before importing their rootfs.

#### `mvm self-update`
- New command to check for and apply updates from GitHub releases.
- `mvm self-update check` — compare current version against latest release.
- `mvm self-update apply` — download, verify SHA256, and atomic binary swap.
- `mvm self-update` — check + apply if newer.
- Supports `--force` to re-install same version.
- Refactored GitHub release fetching into reusable `download.Remote` struct.

#### `mvm completion`
- Removed PowerShell completion support.

#### `mvm network inspect`
- Now shows active firewall rules per network.

#### `mvm kernel pull`
- Friendlier error messages when checksum server is temporarily unavailable.

#### `mvm image ls`
- Now shows the `Version` column in the default listing.

### Changed

#### `mvm vm create`
- Renamed `--no-enable-logging` → `--disable-logging`, `--no-enable-metrics` → `--disable-metrics`. Added `--deny-remote-exec` (mutually exclusive with `--allow-remote-exec`).

#### Guest agent: `mvm-vsock-agent` renamed to `mvm-agent`
- Package directory moved from `internal/service/vsockagent/` to `internal/service/agent/`.
- In-VM binary: `/usr/bin/mvm-vsock-agent` → `/usr/bin/mvm-agent`.
- In-VM socket: `/var/run/mvm-vsock-agent.sock` → `/var/run/mvm-agent.sock`.
- Auth token: `/var/run/mvm-vsock-agent.token` → `/var/run/mvm-agent.token`.
- Systemd unit: `mvm-vsock-agent.service` → `mvm-agent.service`.
- OpenRC init: `/etc/init.d/mvm-vsock-agent` → `/etc/init.d/mvm-agent`.
- Go interface: `InjectVsockAgent()` → `InjectAgent()`, `BuildVsockAgentOps()` → `BuildAgentOps()`, error code `CodeVsockAgentUnreachable` → `CodeAgentUnreachable`.
- No backward compatibility — old paths will not work.

#### `kernels.yaml`
- Renamed `config_url_template` to `base_config_url_template` to clarify that it provides the base kernel `.config`.
- Removed the redundant duplicate URL from `config_fragments` in the bundled `kernel-official` spec.
- Added `CONFIG_IKCONFIG` and `CONFIG_IKCONFIG_PROC` to the `containers` feature enforce map.
- Added `CONFIG_NF_CONNTRACK` to the `iptables` feature enforce map.
- Added `CONFIG_NETFILTER_XT_TARGET_CT`, `CONFIG_IP_SET`, `CONFIG_IP_SET_HASH_IP`, `CONFIG_IP_SET_HASH_NET`, and `CONFIG_VXLAN` to the `iptables` feature enforce map.

#### `mvm env spec parsing`
- Replaced custom `UnmarshalYAML` with `yaml:",inline"` on `Steps` map for automatic parsing.

#### `mvm net / image / kernel / bin rm`
- `rm` on a soft-deleted resource (orphan) now hard-deletes it instead of returning "not found". The resolver chain now threads `includeDeleted` from input → resolver → repo, so remove operations can resolve orphaned resources.

#### Listing visibility for soft-deleted resources
- Networks, images, kernels, and binaries with `deleted_at` set are now shown in listings with a `[x]` suffix in red, instead of being hidden.
- Binaries show a `Status` column in long mode (`--long`) indicating "deleted".
- `ListAll` SQL no longer filters `WHERE deleted_at IS NULL` — returns all records.
- `GetByName` and `FindByPrefix` accept an optional `includeDeleted` parameter (default `false`). Resolvers thread this through so individual operations can opt in to resolving deleted resources.

#### Env spec format [Major]
- Spec format redesigned for clarity and consistency. Step sections are now maps (key = step name) instead of lists with a `name` field. An optional `name` field inside the params overrides the resource name (e.g., the bridge name for a network, the VM name). Cross-resource references use `@type:name` format (e.g., `"@network:default"`) with the `@` sigil making references visually distinct from literal values. Both `depends_on` and reference fields (`network`, `key`, `image`, `kernel`, `binary`) accept the new format. Backward compat for bare names is preserved.

  **Before:**
  ```yaml
  network:
    - name: default
      subnet: "172.27.0.0/24"
  vm:
    - name: dev-vm
      network: default
      depends_on:
        - network:default
  ```

  **After:**
  ```yaml
  network:
    default:
      subnet: "172.27.0.0/24"
  vm:
    dev-vm:
      network: "@network:default"
      depends_on:
        - "@network:default"
  ```

### Fixed

#### `mvm image pull` / `mvm kernel pull`
- Fixed version-blind cleanup that could delete a different version's files. Pulling `alpine:3.21` no longer soft-deletes `alpine:3.23` and removes its cached file. The lookup now uses `GetByVersionAndType` instead of `GetByType`, ensuring cleanup only touches the same version being pulled. Same fix applied to firecracker kernels.

#### `mvm env`
- Fixed state file structure: `state.spec` now stores the input spec fields (what the user configured), and `state.output` stores the created resource state (IDs, properties). Previously output was incorrectly stored in `state.spec` and `state.output` was never populated.

#### `mvm kernel`
- New `fqdn-proxy` feature set with `CONFIG_NETFILTER_XT_TARGET_TPROXY`, `CONFIG_NETFILTER_XT_TARGET_CT`, and `CONFIG_NETFILTER_XT_MATCH_SOCKET`.
- New `bandwidth` feature set with `CONFIG_NET_SCH_FQ` (Fair Queuing packet scheduler).
- Added eBPF/BTF features: `CONFIG_BPF_EVENTS`, `CONFIG_PERF_EVENTS`, `CONFIG_NET_CLS_BPF`, `CONFIG_NET_CLS_ACT`, `CONFIG_NET_SCH_INGRESS`.
- Added CNI overlay features: `CONFIG_GENEVE`, `CONFIG_FIB_RULES`.
- Added crypto features: `CONFIG_CRYPTO_SHA1`, `CONFIG_CRYPTO_USER_API_HASH`.
- New `iscsi-target` feature set with `CONFIG_CONFIGFS_FS`, `CONFIG_TARGET_CORE`, `CONFIG_ISCSI_TARGET`, `CONFIG_ISCSI_TCP`, `CONFIG_SCSI_ISCSI_ATTRS`, `CONFIG_BLK_DEV_SD`, `CONFIG_SCSI_CONSTANTS` (required by Longhorn block storage).
- New `ebpf-cni` feature set with eBPF/BTF + tunneling + iptables + L7 proxy + connection tracking configs (required by Cilium, Hubble, kube-proxy replacement).
- New `wireguard` feature set with `CONFIG_WIREGUARD`, `CONFIG_WIREGUARD_DEBUG`, `CONFIG_DST_CACHE`, `CONFIG_NET_UDP_TUNNEL`, `CONFIG_CRYPTO_LIB_CURVE25519`, `CONFIG_CRYPTO_LIB_CHACHA20POLY1305`, `CONFIG_CRYPTO_LIB_POLY1305`, `CONFIG_CRYPTO_LIB_UTILS` (modern VPN kernel module).

#### `mvm env`
- `env destroy` completion now shows workflow IDs from saved state alongside file paths (was previously blocked by `FilterFileExt` directive).
- `env destroy` and `removes` mid-pipeline cleanup now pass `IncludeDeleted: true` for network, image, kernel, and binary removes, so soft-deleted resources are properly hard-deleted instead of left orphaned.


#### `mvm image import`
- Fixed "target is busy" flakiness during image shrink/grow: when the first `umount` fails, `shrinkExt4` and `growExt4` now fall through to `CleanupMount` (which scans `/proc`, kills orphan processes, and retries) before returning an error.

#### `mvm inspect` (all — vm, image, network, key, etc.)
- Fixed scientific notation display for large numbers in tree dict output. Whole-number `float64` values are now formatted as plain integers (e.g., `3.827e+03` → `3826`).

#### `mvm vm create`
- `/etc/hosts` is now appended to instead of fully overwritten during provisioning, preserving entries from the base image.

#### `mvm image import`
- Fixed deduplication that silently skipped importing a different version of the same type.
- Image name is now automatically set to `type version` on import.
- Success output now shows the source path or VM name instead of the internal cached path.

#### `mvm cp`
- Recursive directory copies now follow symlinks and skip broken symlinks, non-regular files, and symlink cycles instead of aborting.
- Single-directory copies to a destination without a trailing slash (e.g. `mvm cp ./my-dir vm:/path/to/dest`) now create the destination as a directory.

#### `mvm vm create`
- Fixed `--allow-remote-exec` flag being silently ignored — `remote_exec` column was missing from the SQL upsert.

#### `mvm image import`
- Force import now hard-deletes the old DB record when no VMs reference it (instead of always soft-deleting).
- Progress message during image optimize changed to "Debloating and shrinking filesystem..." for clarity.

#### `mvm exec`
- Interactive shell sessions now forward the host terminal size and `SIGWINCH` resize events to the guest PTY, so TUI apps (vim, htop, etc.) draw correctly when the terminal or tmux pane is resized.
- Fixed resize frame handling in the guest agent so that `SIGWINCH` frames interleaved with stdin bytes are applied instead of being written to the shell as literal input.
- Fixed host-side concurrent writes to the vsock connection, which could split JSON resize frames and corrupt the byte stream seen by the guest agent.

#### `mvm console`
- Fixed duplicate error messages when the console relay is not running.

#### vsock agent upgrade
- Fixed a deadlock where `systemctl restart mvm-vsock-agent` waited for the agent to stop while the agent was waiting for the upgrade command to finish, causing a 30s timeout and a confusing EOF error.
- Fixed a shell syntax error in the restore/rollback command (`&;`).
- Upgrade and restore commands now detach the service restart with `nohup` and support both systemd and OpenRC.
- The DB upgrade lock is now cleared immediately when an upgrade fails, instead of forcing a 60s wait.
- Fixed version comparison for git-describe strings (`0.1.0-9-g<hash>`) so that random hex hashes are not compared lexicographically; only the tag distance is used for ordering.
## [0.1.0] - 2026-06-28

### Added

#### CLI Commands (18 top-level groups, 70+ subcommands)
- **`mvm vm`** -- Full VM lifecycle: ls, ps, create, rm, start, stop, reboot, pause, resume, inspect
- **`mvm console`** -- Interactive serial console access via PTY-over-Unix-socket relay with --state and --kill options
- **`mvm host`** -- Host configuration: init (KVM, modules, sysctl, mvm group, sudoers), status, info, clean, reset
- **`mvm network`** -- Named bridge networks with NAT: ls, default, create, rm, inspect, sync
- **`mvm key`** -- SSH key management: ls, create, import, rm, inspect, export, default
- **`mvm config`** -- Runtime configuration: get, set, ls, reset
- **`mvm init`** -- Interactive setup wizard with non-interactive mode
- **`mvm kernel`** -- Kernel management: ls, inspect, pull, default, import, rm
- **`mvm image`** -- Image management: ls, pull, default, rm, inspect, import, warm
- **`mvm bin`** -- Firecracker binary management: ls, pull, rm, default
- **`mvm exec`** -- Run commands inside VMs via vsock agent without SSH
- **`mvm cp`** -- Copy files between host and microVMs via vsock binary frame protocol
- **`mvm cache`** -- Cache lifecycle: init, prune (per-resource or all), clean
- **`mvm logs`** -- Log streaming: boot logs (serial console) and Firecracker OS logs with --follow
- **`mvm ssh`** -- SSH into VMs by name, ID, IP, or MAC with custom user, key, and connection timeout
- **`mvm volume`** -- Persistent data disk management: create, rm, ls, inspect, resize, attach, detach
- **`mvm env`** -- Environment workflow management: apply, diff, ls, destroy
- **`mvm snapshot`** -- Snapshot lifecycle: create, list, inspect, restore, remove

#### Architecture
- **Three-layer architecture** (CLI -> API -> Core) with strict import boundaries enforced by Go compiler
- **Cobra CLI framework** with root command and subcommand hierarchy
- **Controller / Service / Repository / Resolver** pattern across 16 core domains (not all domains implement all four; simpler domains have fewer components)
- **Input Validate/Resolve** pattern for type-safe, validated operations across 11 domains (ADR-0011)
- **Provisioning backend abstraction** (LoopMount vs GuestFS) with mutual exclusion
- **Firewall backend abstraction** (nftables vs iptables) with mutual exclusion
- **SQLite database** (`internal/lib/db/migrations/`) with migration system for persistent state (16 tables): images, kernels, binaries, volumes, networks, network_leases, vm_instances, host_state, host_state_changes, iptables_rules, nftables_rules, ssh_keys, user_settings, vm_vsock_config, snapshots, db_migrations
- **Relation enrichment** system with batch loading to prevent N+1 queries
- **Privilege delegation** model via `mvm` unix group and sudoers drop-in (no sudo for normal operations)
- **Single error type** (`pkg/errs.DomainError`) with Code, Class, Message, Op, Entity, Details, Err
- **Parallel execution** via `internal/infra/pool/` with bounded concurrency

#### VM Lifecycle
- Create VMs with configurable vCPUs, memory, disk size, PCI, nested virt, console, logging, and metrics
- Batch VM creation via `--count N` and all-or-nothing `--atomic` flag
- Snapshot and restore (memory + VM state) via Firecracker API socket
- Cloud-init provisioning in four modes: inject, net (nocloud-net HTTP server), iso, off
- Firecracker process lifecycle: spawn, monitor, signal (SIGTERM/SIGKILL), exit code tracking
- Per-VM isolated nocloud-net HTTP servers with source-based firewall rules

#### Networking
- Linux bridge and TAP device management for guest connectivity
- NAT/masquerade with nftables (default) or iptables (legacy) for outbound internet access
- IP lease management with automatic allocation and release
- Firewall rule tracking with FirewallTracker and backend-specific repositories
- Network reconciliation (sync DB state with live bridge state)
- UFW compatibility via non-hook chains with jump rules at position 0

#### Image Management
- Fetch images by type:version (ubuntu:24.04, archlinux, debian:12, alpine, firecracker)
- Import local image files with automatic format detection
- Format support: qcow2, raw, tar-rootfs, vhd, vhdx
- Automated conversion pipeline: download -> decompress -> format conversion -> root partition extraction -> filesystem optimization
- SHA256 checksum verification for downloaded images
- Image warm pool for fast VM creation (pre-extracted ready-to-copy images)
- Loop-mount provisioner backend for rootfs operations (default, no external dependencies)

#### Kernel Management
- Download pre-built Firecracker CI kernels (optimized, fast boot)
- Build official upstream kernels from source with Firecracker-compatible configs
- Configurable kernel features via YAML specs (e.g., kvm, nftables)
- Automatic architecture detection and kernel config application

#### Binary Management
- Download Firecracker and jailer binaries from GitHub releases
- Version management with default version selection

#### SSH Key Management
- Generate ED25519, RSA, and ECDSA keypairs via ssh-keygen
- Import existing public keys with fingerprint detection
- Set one or more default keys for automatic VM injection
- Export keypairs to standard ~/.ssh location

#### Host Initialization
- Enable IP forwarding and persist sysctl settings
- Load KVM kernel modules (kvm, kvm_intel/kvm_amd)
- Create mvm unix group and sudoers drop-in with passwordless access to privileged binaries
- Setup nftables/iptables chains for VM traffic management
- Idempotent -- safe to run multiple times
- Full reset: revert all host changes including networking, sysctl, sudoers, and group

#### Services (compiled into single `mvm` binary)
- **Console relay** (`mvm run console relay`) -- PTY-to-Unix-socket bridge for interactive serial console without SSH
- **nocloud-net server** (`mvm run nocloudnet serve`) -- Per-VM HTTP server for cloud-init datasource delivery
- **Loop-mount provisioner** (`mvm run provision`) -- Rootfs provisioning for SSH key injection, hostname setup, DNS config, cloud-init disable, and filesystem resize
- **Vsock guest agent** (embedded) -- Cross-compiled guest agent binary, zstd-compressed and embedded in the `mvm` binary, injected into VMs at runtime for vsock-based exec, file transfer, and console

#### Developer Experience
- **Go API** (`pkg/api/`) -- Operation struct with methods for each domain, sole cross-domain orchestrator
- **Go toolchain** -- Standard `go build`, `go vet`, `go test`
- **System test suite** -- Python-based black-box CLI tests in `tests/system/`
- **Build scripts** (`scripts/`): build.sh, bump-version.py, common.py, fresh_env.py, post-release.py, run-system-tests.py, setup-test-environment.py

#### Distribution
- Single statically-linked Go binary (no runtime dependencies)
- Distribution packages: .deb (Debian/Ubuntu), .rpm (RHEL/Fedora), PKGBUILD (Arch Linux)
- Man page (`docs/mvm.1`)
- Initial RPM release
- Distribution packages support

#### Performance
- VM creation ~2.3s average (loop-mount), ~3.9s (GuestFS) per benchmark data
- VM-ready ~2.9s average (loop-mount), ~5.8s (GuestFS) per benchmark data
- SQL-level computation (COUNT, WHERE IN) instead of fetch-all-then-filter

#### Testing
- Comprehensive test suite (~2500 tests: ~850 Go test functions + ~1185 Go subtests + ~520 Python system tests)
- System tests run in nested VM with unprivileged user
- Coverage matrix tracking every CLI subcommand and flag

[0.3.0]: https://github.com/AlanD20/mvmctl/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.2.0
[0.1.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.1.0
