# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- `image_import` destroy now removes the imported image from the database and disk.
- All steps now support `removes` field to destroy resources mid-pipeline after the step completes.
- New top-level `ephemeral: true` field — auto-runs `env destroy` after successful apply. Zero cleanup overhead. See `docs/ENV_SPEC_REFERENCE.md`.
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

### Changed

#### Listing visibility for soft-deleted resources
- Networks, images, kernels, and binaries with `deleted_at` set are now shown in listings with a `[x]` suffix in red, instead of being hidden.
- Binaries show a `Status` column in long mode (`--long`) indicating "deleted".
- `ListAll` SQL no longer filters `WHERE deleted_at IS NULL` — returns all records.
- `GetByName` and `FindByPrefix` accept an optional `includeDeleted` parameter (default `false`). Resolvers thread this through so individual operations can opt in to resolving deleted resources.


### Fixed

#### `mvm vm create`
- `/etc/hosts` is now appended to instead of fully overwritten during provisioning, preserving entries from the base image.

#### `mvm image import`
- Fixed deduplication that silently skipped importing a different version of the same type.
- Image name is now automatically set to `type version` on import.
- Success output now shows the source path or VM name instead of the internal cached path.

#### `mvm cp`
- Recursive directory copies now follow symlinks and skip broken symlinks, non-regular files, and symlink cycles instead of aborting.
- Single-directory copies to a destination without a trailing slash (e.g. `mvm cp ./my-dir vm:/path/to/dest`) now create the destination as a directory.

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

[Unreleased]: https://github.com/AlanD20/mvmctl/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.1.0
