# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-05-16

### Added

#### CLI Commands (15 top-level groups, 65+ subcommands)
- **`mvm vm`** -- Full VM lifecycle: ls, ps, create, rm, start, stop, reboot, pause, resume, snapshot, load, inspect, export, import, attach-volume, detach-volume
- **`mvm console`** -- Interactive serial console access via PTY-over-vsock relay with --state and --kill options (attach is default)
- **`mvm host`** -- Host configuration: init (KVM, modules, sysctl, mvm group, sudoers), ls, clean, reset
- **`mvm network`** -- Named bridge networks with NAT: ls, default, create, rm, inspect, sync
- **`mvm key`** -- SSH key management: ls, add, create, rm, inspect, export, default
- **`mvm config`** -- Runtime configuration: get, set, list, reset
- **`mvm init`** -- Interactive setup wizard with non-interactive mode
- **`mvm kernel`** -- Kernel management: ls, inspect, pull, default, rm
- **`mvm image`** -- Image management: ls, pull, default, rm, inspect, import, warm
- **`mvm bin`** -- Firecracker binary management: ls, pull, rm, default
- **`mvm cp`** -- Copy files between host and microVMs via SCP-over-SSH
- **`mvm cache`** -- Cache lifecycle: init, prune (per-resource or all), clean
- **`mvm logs`** -- Log streaming: boot logs (serial console) and Firecracker OS logs with --follow
- **`mvm ssh`** -- SSH into VMs by name, ID, IP, or MAC with custom user, key, and connection timeout
- **`mvm volume`** -- Persistent data disk management: create, rm, ls, inspect, resize

#### Architecture
- **Three-layer architecture** (CLI -> API -> Core) with strict import boundaries
- **LazyMVMGroup** -- Custom Click group with lazy-loaded Typer sub-apps for sub-150ms startup
- **Controller / Service / Repository / Resolver** pattern across all 14 core domains
- **Input -> Request -> Resolved** pipeline for type-safe, validated VM operations
- **Provisioning backend abstraction** (LoopMount vs Guestfs) with factory pattern
- **SQLite database** (`db/migrations/` directory) with migration system (`001_initial_schema.sql`) for persistent state: images, kernels, binaries, volumes, networks, network_leases, vm_instances, host_state, host_state_changes, iptables_rules, nftables_rules, ssh_keys, user_settings
- **Shared utility helpers** (`utils/`): fs, _system, http, network, crypto, template, yaml, _validators, _io, _lazy_import, progress, cli, operation_utils, auditlog, common, _disk, timinglog
- **Relation enrichment** system with batch loading to prevent N+1 queries
- **Privilege delegation** model via `mvm` unix group and sudoers drop-in (no sudo for normal operations)

#### VM Lifecycle
- Create VMs with configurable vCPUs, memory, disk size, PCI, console, logging, and metrics
- Batch VM creation via `--count N` and all-or-nothing `--atomic` flag
- Root filesystem images via durable incremental copying with reflink/FICLONE support
- Snapshot and restore (memory + VM state) via Firecracker API socket
- Export VM configurations as portable JSON using semantic references (os_slug, version, name)
- Import previously exported VM configurations
- Cloud-init provisioning in four modes: inject (libguestfs), net (nocloud-net HTTP server), iso, off
- Firecracker process lifecycle: spawn, monitor, signal (SIGTERM/SIGKILL), exit code tracking
- Per-VM isolated nocloud-net HTTP servers with source-based iptables firewall rules

#### Networking
- Linux bridge and TAP device management for guest connectivity
- NAT/masquerade with iptables for outbound internet access
- IP lease management with automatic allocation and release
- iptables rule tracking with generic IPTablesTracker infrastructure
- Network reconciliation (sync DB state with live bridge state)
- Restore all networks after host reboot

#### Image Management
- Fetch images by OS slug (ubuntu-24.04, ubuntu-22.04, archlinux, debian-bookworm, alpine-3.21)
- Import local image files with automatic format detection
- Format support: qcow2, raw, vhd, vhdx, tar-rootfs, squashfs
- Automated conversion pipeline: download -> decompress -> format conversion -> root partition extraction -> filesystem optimization
- SHA256 checksum verification for downloaded images
- Image warm pool for fast VM creation (pre-extracted ready-to-copy images)
- Incremental copy with reflink/FICLONE when filesystem supports it
- Loop-mount provisioner backend for rootfs operations without libguestfs dependency

#### Kernel Management
- Download pre-built Firecracker CI kernels (optimized, fast boot)
- Build official upstream kernels from source with Firecracker-compatible configs
- Configurable kernel options via YAML specs (enable/disable/set-val)
- Automatic architecture detection and kernel config application

#### Binary Management
- Download Firecracker and jailer binaries from GitHub releases
- Version management with default version selection
- Automatic CI version resolution for template-based image URLs

#### SSH Key Management
- Generate ED25519, RSA, and ECDSA keypairs via ssh-keygen
- Import existing public keys with fingerprint detection
- Set one or more default keys for automatic VM injection
- Export keypairs to standard ~/.ssh location

#### Host Initialization
- Enable IP forwarding and persist sysctl settings
- Load KVM kernel modules (kvm, kvm_intel/kvm_amd)
- Create mvm unix group and sudoers drop-in with passwordless access to privileged binaries
- Setup iptables chains for VM traffic management
- Idempotent -- safe to run multiple times
- Full reset: revert all host changes including networking, sysctl, sudoers, and group

#### Services
- **Console relay** -- PTY-over-vsock bridge for interactive serial console without SSH
- **nocloud-net server** -- Per-VM HTTP server for cloud-init datasource delivery
- **mvm-provision** -- Loop-mount rootfs provisioning binary for SSH key injection, hostname setup, DNS config, cloud-init disable, and filesystem resize (~200ms per VM, replaces libguestfs as primary path)

#### Developer Experience
- **Python API** -- All CLI commands map 1:1 to `*Operation` static methods in `mvmctl.api`
- **Strict mypy** -- Full type annotations across the codebase, no `type: ignore` suppressions
- **Ruff** -- Linting and formatting, zero-tolerance for violations
- **Test suite** -- Unit, integration, system, and layer compliance tests
- **uv** -- Fast Python package and project management
- **Build scripts** (`scripts/`): build_services.py, run_tests.py, setup-test-environment.py, profile_test_memory.py, post-release.py, check_skip_ratio.py, check_skip_ratio_verify.py

#### Distribution
- Standalone Nuitka-compiled binary (zero Python runtime dependency)
- Nuitka is the primary build tool (PyInstaller is available as a fallback)
- PyPI package (`mvmctl`)
- Distribution packages: .deb (Debian/Ubuntu), .rpm (RHEL/Fedora), PKGBUILD (Arch Linux)
- Man page (`mvm.1`)

#### Performance
- CLI startup under 200ms via lazy-loading LazyMVMGroup
- Batch relation loading with deduplication (O(relations) queries, not O(entities x relations))
- Durable incremental image copying with reflink support (instant copy on btrfs/XFS)
- SQL-level computation (COUNT, WHERE IN) instead of fetch-all + Python filtering

[Unreleased]: https://github.com/AlanD20/mvmctl/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.1.0
