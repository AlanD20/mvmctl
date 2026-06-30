# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

#### `mvm cp`
- Recursive directory copies now follow symlinks and skip broken symlinks, non-regular files, and symlink cycles instead of aborting.
- Single-directory copies to a destination without a trailing slash (e.g. `mvm cp ./my-dir vm:/path/to/dest`) now create the destination as a directory.

#### `mvm exec`
- Interactive shell sessions now forward the host terminal size and `SIGWINCH` resize events to the guest PTY, so TUI apps (vim, htop, etc.) draw correctly when the terminal or tmux pane is resized.

#### `mvm console`
- Fixed duplicate error messages when the console relay is not running.

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
