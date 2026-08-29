# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.0] - Unreleased

> **Security release status:** `0.3.0` is still under development. The canonical Jailer, cgroup policy, routed
> service-access policy, root-owned system installer, early privileged-dispatch foundation, and private root-owned VM
> and trusted-release authorities, managed-cache pinning, and base launch-resource pinning substrates described below
> are implemented. The mandatory per-VM network
> namespace, nftables-only `traffic` policy, exact VM-to-VM `exec` policy, final marker-only sudo policy, and remaining
> typed privileged operations are not implemented yet; see
> **Security work still pending** before treating this version as release-ready.

### Added

#### Canonical Firecracker Jailer launch
- VM create, start, reboot, and snapshot restore now require an exact Firecracker/Jailer release pair and run through a root-owned Jailer chroot with no direct-launch fallback.
- Release pairs are installed from a checksum-checked archive; each jail exposes only that VM's kernel, rootfs, volumes,
  runtime files, and selected snapshot. Independent privileged release verification remains a release blocker below.

#### Enforced VM resource envelopes
- Jailed VMs now require cgroup v2 and launch with persisted, verified CPU, memory, swap, and PID limits; `mvm vm inspect` reports requested, observed, and current cgroup state.

#### Routed service-access policies
- `mvm policy` now persists exact network-to-VM TCP/UDP allows while both firewall backends default-deny other managed cross-network and VM-to-host traffic without blocking NoCloud, established replies, or internet egress.

#### Root-owned single-binary installation
- Added the exact administrator bootstrap command `sudo <trusted-mvm-binary> host install-system`, which installs the
  same `mvm` artifact at `/usr/local/bin/mvm` as `root:root` mode `0755`.
- System installation uses descriptor-relative, no-symlink traversal; an exclusive temporary file; complete-copy and
  ownership verification; file and directory `fsync`; and atomic rename.
- `host install-system` is dispatched before ordinary application initialization and ignores user cache, configuration,
  database, logging, and environment overrides.
- `host init` now verifies the canonical system binary before reconciling the managed sudoers drop-in. System installation
  is deliberately separate from host initialization.

#### Privileged-dispatch foundation
- Added the reserved, versioned privileged entry point to the root-owned `mvm` binary. Selection occurs before Cobra or
  normal CLI initialization and malformed reserved invocations fail closed instead of falling through.
- Added strict bounded JSON parsing with unknown, duplicate, case-colliding, trailing, oversized, and structurally invalid
  input rejection.
- Added the version-1 `MVMREQ01`/`MVMRES01` codec with 64 KiB JSON frames, receiver-bounded payload declarations,
  exact action/schema matching, read-only typed success/error outcomes distinct from protocol failures, and bounded
  `DomainError` normalization that omits wrapped causes and sensitive process-output details.
- Added the fixed fd-0 Unix socketpair client transport for
  `/usr/bin/sudo -n -- /usr/local/bin/mvm __mvm_privileged_v1 <fixed-action>`. It uses close-on-exec endpoints,
  concurrent bounded upload and response handling, actual peer EOF, early-response upload interruption, cancellation,
  bounded diagnostics, and bounded process reaping without extending the generic command runner. Capability-specific
  clients and actions are still release blockers below.
- Added authenticated sudo identity and current `mvm` group checks, fixed root environment sanitization, and executable
  device/inode verification against the root-owned `/usr/local/bin/mvm` image.
- Added ADR-0016, which defines the single-binary privilege boundary, trusted state layout, typed operation catalog,
  clean-install constraints, and fail-closed system-binary replacement behavior.

#### Root-owned VM authority substrate
- Added a private typed VM authority under `internal/service/jailer` with durable owner-bound `registered`, `cleaning`,
  and `cleaned` records beneath `/var/lib/mvmctl/instances/<uid>` and persistent ownership tombstones.
- Added descriptor-relative, no-follow traversal; strict bounded record decoding; atomic durable replacement; reusable
  root-owned runtime locks; and the fixed `release -> global index -> VM` lock order.
- Prepared release leases now lock the canonical architecture/version store slot before identity resolution. Launch
  registration transfers that same lease only after durable authority state; removal/replacement retains it while
  checking exact references. Competing identities cannot race through different hash-derived locks.
- Global VM-ID claims now fail closed on foreign, duplicate, corrupt, unreadable, or inconsistent authority records, and
  trusted release removal can acquire a lease only when no active authority record references the exact release.
- This substrate is private and not yet wired into public VM lifecycle operations; that integration remains a release
  blocker below.

#### Private trusted-release authority substrate

- Added root-owned, descriptor-pinned trusted-release admission with fixed source derivation, independently checked
  digests, a closed parser for the audited x86_64 archives, strict manifest decoding, full-file hashing, and ELF checks.
- Firecracker, Jailer, and the manifest are staged anonymously, assembled into one recoverable exact candidate, and
  durably published into an absent canonical version slot with descriptor-relative `renameat2(RENAME_NOREPLACE)`.
  Existing versions return unchanged only after exact three-leaf shared admission and an identical canonical manifest,
  without a reference scan. An absent-only request conflicts with a different complete release, while unsafe or corrupt
  state fails closed.
- Explicit replacement fully admits a different installed release, proves its exact manifest-derived identity
  unreferenced under the active release-slot lease, rechecks both directory bindings, and commits with
  descriptor-relative `renameat2(RENAME_EXCHANGE)` without an absent-install or non-atomic fallback. The candidate
  becomes close-only immediately after exchange; cancellation-independent retirement removes only the three fixed old
  leaves after the first architecture-directory fsync, fsyncs the retired directory, rechecks its name binding, removes
  it, and fsyncs the architecture directory again. Post-commit failures preserve the primary error and report
  `release_replaced`, `durability_uncertain`, and `retired_release_retained` state where applicable.
- This authority remains private and unwired. Root-origin fetch, the aarch64 archive audit, typed caller/privileged
  transport integration, actual release removal, and L2/system release qualification are still release blockers.

#### Descriptor-pinned managed-cache and base launch-resource substrates

- Added a private typed `MVM_CACHE_DIR` locator and lease under `internal/service/jailer`. Root opens the fixed
  filesystem root once, traverses canonical components with beneath/no-symlink/no-magic-link `openat2`, and retains the
  complete descriptor chain.
- Caller ownership and safe modes use synchronized metadata. Cache identity records device, inode, and a typed unique,
  legacy, or unavailable mount ID; later re-pin rejects any mismatch.
- The final cache root accepts ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or ZFS. FUSE, network,
  overlay/stacked, and automount cache roots fail closed. Ancestor mounts are constrained by descriptor traversal,
  owner/mode, and automount checks without unnecessarily requiring the cache filesystem allowlist.
- Added a private typed base launch-resource lease for the fixed VM rootfs, Firecracker configuration, kernel, and
  optional cloud-init ISO. It opens every component relative to the pinned cache with beneath/no-symlink/no-magic-link/
  no-cross-mount resolution and never reopens an admitted resource by pathname.
- Base launch admission verifies caller ownership, regular-file type, exactly one hard link, resource-specific owner
  access, safe modes, synchronized stable metadata, and hard size limits: rootfs 128 MiB through 16 TiB, configuration
  1 byte through 1 MiB, kernel 1 byte through 1 GiB, and cloud-init ISO 1 byte through 256 MiB. The bounded expected
  rootfs size is an equality check, not authority to raise the receiver limit.
- Successful admission transfers the complete cache and resource descriptor chain into one private lease. Rejection
  leaves the cache lease reusable; release and failure cleanup attempt every retained descriptor in reverse order with
  an uncancelled context and preserve primary `DomainError` metadata on cleanup failure.
- These are private foundations only. The privileged request, cache identity in the root-owned instance record,
  Firecracker configuration consumption, mount-namespace consumer, remaining resource classes, and public VM lifecycle
  are not wired yet and remain release blockers below.

#### Installed release-candidate qualification
- For T1/T2, the Python system-test runner now treats `MVM_BINARY` as a distinct compatible outer controller and
  `MVM_CANDIDATE_BINARY` as the artifact under test. It rejects path, symlink, and hard-link aliases between them and
  never replaces the controller while building or provisioning a runner. Tier 3 remains a separate host-direct gate
  that is allowed only on a clean disposable qualification host.
- Runner images stage the candidate outside the canonical path, invoke the real `host install-system` administrator
  route, initialize through exact `/usr/local/bin/mvm`, and use that installed path for T1/T2 assertions.
- Added 10 L2 installer cases covering canonical metadata and content, safe ancestors, idempotence, malformed and
  unprivileged invocation, symlink and read-only-target failures, invoking-user ownership after privileged init,
  self-update refusal, restoration, and cleanup.
- The focused `system_install` domain passed inside a disposable nested-virt runner using `0.3.0-rc.1`; the outer host
  controller remained byte-for-byte unchanged.
- Tier 3 and `--all` now require explicit pre-mutation `--host-direct` consent. The dedicated
  `--release-qualification` gate additionally requires an unfiltered full matrix, a fresh explicit-version release
  build, and byte/version identity with exact root-owned `/usr/local/bin/mvm` before host resource preparation.
- The runner rejects unknown, repeated, mixed, zero-domain, or empty-file test selections before binary probes, builds,
  or resource mutation instead of warning and returning successful but incomplete evidence.
- The runner now validates its complete registry before any work: domain names and test files are unique, registrations
  are non-empty canonical regular files under `tests/system/`, and every discovered system-test file is registered
  exactly once. The missing Tier 2 `exec` registration is included and the duplicate Tier 3 `fresh_env` alias is gone.
- Every T1/T2/T3 domain now emits a strict bounded pytest outcome report. A domain passes only when collection is
  non-empty, the process/report exit statuses match, every selected item passed, and failures, errors, collection errors,
  deselections, skips, XFAIL, and XPASS are all zero. Missing, malformed, duplicate-field, oversized reports and report
  cleanup failures fail the domain while preserving pytest output and the validation reason.
- The system-test coverage matrix now recounts its 431 current CLI rows and separates transitional/legacy coverage from
  the still-missing v0.3 root-authority, namespace, nftables, traffic, exec-policy, reconciliation, and zero-leak release
  evidence. A legacy `Deep` row is no longer presented as clean-break security signoff.

#### Deterministic initialization
- Added `mvm init --binary-version <version>` to request an exact Firecracker/Jailer pair when no local pair exists.
  The explicit selection survives the administrator host-init interaction; existing local/default behavior is
  unchanged.

### Changed

#### VM launch and release identity
- VM create, start, reboot, and snapshot restore no longer fall back to launching Firecracker directly when Jailer setup
  fails.
- VM and snapshot state now carry the exact Firecracker/Jailer pair needed by jailed launch paths.
- Trusted release installation and Jailer launch are internal services compiled into the single `mvm` artifact.

#### Host setup and updates
- Initial setup is now two explicit administrator actions: install the trusted system image, then run
  `sudo /usr/local/bin/mvm host init`.
- A system-installed `/usr/local/bin/mvm` refuses in-place `self-update apply`. Administrators replace it by running
  `host install-system` from the newly downloaded trusted artifact.
- During development, the generated sudoers policy targets `/usr/local/bin/mvm` instead of a user-owned executable.
  Its wildcard and raw-tool grants remain transitional and are not the final `0.3.0` security boundary.

#### System-test asset preparation
- Shared runner assets are seeded from `MVM_ASSET_MIRROR` with descriptor-pinned, no-follow access and
  `mkfs.ext4 -d`; the runner no longer loop-mounts and copies the mirror with raw sudo on the host.
- Builder asset pulls run sequentially with bounded connect/operation timeouts so image extraction cannot exhaust the
  constrained nested builder through concurrent pulls.
- Builder pulls capture and re-emit bounded diagnostics, require explicit local-mirror artifact-read evidence, and fail
  qualification on pull errors, checksum fallback, or HTTP-downloaded mirror auto-population. Fixed checksum/version
  metadata may still use its upstream source.
- Missing or stale `asset-mirror` database records are rebuilt only when the recorded backing path is the exact managed
  test-volume path; unexpected paths fail closed without mutation.

#### Managed storage identity
- Newly created volume backing files are named from the immutable 64-character volume ID rather than the user-visible
  display name. Raw and qcow2 formats remain explicit suffixes; database paths are derived metadata, not root authority.
- Kernel download, build, and import now stage files under the kernel service's configured managed directory and install
  final artifacts as `kernels/<kernel-id>`. Import identity is computed from the receiver-owned staged bytes, so a
  caller-controlled display name or a source-file replacement cannot select or escape the managed destination.
- VM artifacts now use a compile-time filename vocabulary: `rootfs.img`, `cloud-init.iso`, `firecracker.json`,
  `firecracker.log`, `firecracker.console.log`, optional `firecracker.metrics`, and fixed API/vsock/console socket and
  PID basenames. VM filesystem type no longer changes the managed rootfs leaf.
- Custom cloud-init ISO paths remain accepted as source data, but the ISO is copied into the VM's fixed
  `cloud-init.iso` leaf before launch. Internal console-relay filename flags and filename settings were removed.
- VM relaunch reconstructs rootfs, configuration, output, metrics, API, PID, and vsock paths from the managed VM
  directory instead of treating SQLite path columns as launch authority. The transitional privileged Jailer also
  rejects alternate manifest basenames and constructs its Firecracker API/config arguments from the compiled names.
- Snapshots now produce and restore `rootfs.img`, `memory`, and `vmstate`. Restore and removal derive the snapshot
  directory and leaves from the validated 64-character lowercase snapshot ID rather than stored file-path columns.
  The temporary `phantom-rootfs.img` mechanism remains until the private mount-namespace overlay lands.

#### Network isolation
- Traffic routed between different managed networks is now denied unless an explicit typed service-access policy permits
  the destination VM protocol and port. Same-network bridge traffic remains unchanged.
- VM-to-host traffic is limited to established replies and required managed services such as NoCloud; internet egress
  remains available.

### Breaking changes

- v0.3.0 requires a clean installation. It does not migrate, backfill, adopt, or preserve legacy databases, running VMs,
  Jailer directories, cgroups, TAP devices, firewall state, policy rows, or remote-exec flags. Initialization must refuse
  ambiguous legacy runtime state rather than silently adopting or deleting it.
- Operators must stop and remove the old installation, retain any desired user artifacts separately, clean legacy host
  runtime state, and recreate VMs and policies under v0.3.0. The exact fail-closed administrator cleanup procedure is
  still a release blocker and must be documented after `host reset`/cleanup hardening lands; operators must not improvise
  raw cleanup from this development changelog.
- The new administrator-controlled system installation is mandatory. A user-owned `~/.local/bin/mvm` is no longer a
  valid final sudo target.
- The system binary and an invoking user binary must support the same privileged protocol version. Version mismatch stops
  before side effects and requires an administrator-approved system-binary replacement.
- Jailed VM launch requires cgroup v2, the required controllers, and an exact Firecracker/Jailer release pair. Hosts that
  previously relied on direct Firecracker fallback no longer start VMs through that fallback.
- Cross-network VM traffic that previously worked without an allow rule is denied after policy reconciliation. Operators
  must create explicit `mvm policy` entries for required TCP or UDP services.
- Scripts that expect `mvm self-update apply` to replace `/usr/local/bin/mvm` must switch to the administrator bootstrap
  flow. Self-update remains available for user-owned candidate artifacts.
- API consumers can no longer set `output_dir`, `name`, or `output_path` on `KernelPullInput`; managed kernel placement
  is receiver-owned. The public CLI did not expose equivalent flags.
- Firecracker/cloud-init/console filename settings are removed. Existing `rootfs.<filesystem>`, alternate snapshot leaf,
  or custom runtime filename layouts are not adopted; clean-install VMs and snapshots use the fixed v0.3 names.
- Custom `MVM_CACHE_DIR` remains supported, but the final v0.3 privileged path requires its resource-bearing
  filesystem to be ext2/ext3/ext4, XFS, Btrfs, F2FS, bcachefs, tmpfs, or ZFS. FUSE, remote, overlay/stacked, and
  automount cache roots are intentionally unsupported.

### Fixed

#### `mvm image inspect`
- Ambiguous ID prefix now reports `"Image ID is ambiguous"` instead of `"No images found"`.

#### `mvm inspect` (all — vm, image, network, key, etc.)
- Byte-size fields (`_size` keys) in tree output are now human-readable (e.g., `8589934592` → `8.0 GiB`).

#### Jailed VM cgroup verification
- VM create no longer fails when Jailer places the cgroup at `/sys/fs/cgroup/mvmctl/<vm-id>`; the check previously expected an intermediate `firecracker/` component that Jailer v1.16 does not create.
- Stop and remove now clean the VM's actual leaf cgroup instead of leaving stale ones behind.

#### Privileged initialization ownership
- `sudo mvm host init` now resolves a complete, internally consistent sudo identity before creating invoking-user cache
  or configuration directories. It rejects partial/spoofed identity and sudo-time path overrides before mutation.
- Invoking-user directories are created component by component with descriptor-relative no-follow operations; only
  newly created directories receive the invoking UID/GID, while unsafe existing paths and replacement races fail closed.

### Security work still pending

The following items are release blockers and are intentionally not described above as completed behavior:

- Wire every privileged VM action to the implemented root-owned global VM-ID ownership claim, lifecycle record, exact
  process identity, and per-VM lock.
- Add root-owned managed-network identity and global capacity authority; user SQLite state must not authorize host-global
  namespace, link, firewall, process, mount, cgroup, or admission decisions.
- Carry the managed-cache locator and base launch selection through the privileged protocol, bind the cache identity in
  the root-owned instance record, and create/consume the implemented descriptor lease inside the root process and
  private mount-namespace launch path. Add separate closed descriptor policies for snapshots, volumes, persistent
  outputs, and image provisioning.
- Split root-only locks/handshakes beneath `/run/mvmctl/authority` from caller-owned socket/PID output directories beneath
  `/run/mvmctl/runtime`; stop mounting the whole cache VM directory. Pin persistent config/output leaves individually,
  truncate enabled `0600` log/console/metrics files only after durable launch registration, and retain them after stop.
- Treat the current fixed-name Jailer checks as transitional defense-in-depth only: Firecracker still reads a
  caller-writable configuration from the whole mounted VM directory. The final receiver must consume pinned individual
  resources and derive/verify every jail-visible path after durable registration.
- Wire the private trusted-release authority through typed privileged install/remove operations; complete root-origin
  fetch, the aarch64 archive audit, actual release removal, and CLI-level qualification. The implemented atomic install
  and explicit replacement substrate is not yet reachable from public `mvm bin` operations.
- Migrate Jailer, loopmount, network, firewall, and supported host mutations to distinct typed privileged actions; remove
  the public root `mvm run jailer` and `mvm run provision` entry points.
- Make one root-owned network namespace mandatory per VM, pass its pinned handle to Jailer, and make namespace/link
  creation, activation, rollback, reconciliation, and cleanup part of the owner-bound VM lifecycle.
- Remove the iptables backend, backend selector, and production `nft` CLI subprocess/parser. Apply one atomic generation
  directly through a reviewed, pinned `github.com/google/nftables` version that default-denies both same-network and
  cross-network VM traffic while preserving required system traffic and internet egress.
- Replace routed `ServiceAccessPolicy` with typed `traffic` policies and remove the `allow_remote_exec` trust mesh in
  favor of exact directional, non-root, resource-bounded VM-to-VM `exec` policies.
- Replace the transitional wildcard/raw-tool sudoers entries with access only to the reserved privileged marker of the
  root-owned system binary.
- Separate administrator host state from invoking-user initialization, replace the generic `HostInit` result with a
  typed contract, and propagate every host mutation/state-write failure instead of reporting partial setup as success.
- Make launch, abort, cleanup, reconciliation, snapshots, live volumes, cgroups, and firewall policy crash-consistent and
  fail closed under ownership, PID-reuse, path-race, and partial-failure conditions.
- Reconcile `mvm vm ls`, `mvm vm ps`, and `mvm vm inspect` against root-owned process identity after host reboot instead
  of reporting stale SQLite status. Preserve the configured vsock device across start, reboot, and snapshot restore so
  host-to-guest `mvm exec` remains available after every supported relaunch path.
- Complete adversarial L1 coverage, Python `tests/system/` clean-install/fault qualification, leak audits, full CI, and
  the final documentation accuracy review. Go tests support but do not replace CLI-level L2 release signoff.
- Finish Tier 3 clean-host inventory and ownership-safe cleanup; unify every host-direct CLI call, preserve pre-existing
  resources, surface every teardown failure, and reject skips/xfails/omitted domains before accepting full `--all`
  release evidence.

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
