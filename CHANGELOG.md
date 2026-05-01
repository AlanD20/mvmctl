# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0]

Initial release of mvmctl — a production-grade Python CLI for managing Firecracker microVMs.

### Added

#### CLI Commands (13 top-level groups, 40+ subcommands)
- **`mvm vm`** — Full VM lifecycle: create, ls, ps, rm, start, stop, reboot, pause, resume, snapshot, load, inspect, export, import
- **`mvm console`** — Interactive serial console access via PTY-over-vsock relay (attach, --state, --kill)
- **`mvm host`** — Host configuration: init (KVM, modules, sysctl, mvm group, sudoers), ls, clean, reset
- **`mvm network`** — Named bridge networks with NAT: create, rm, ls, inspect, set-default, sync
- **`mvm key`** — SSH key management: ls, add, create, rm, inspect, set-default, export
- **`mvm config`** — Runtime configuration: get, set, list, reset
- **`mvm init`** — Interactive setup wizard with non-interactive mode
- **`mvm kernel`** — Kernel management: ls, fetch (official and Firecracker-optimized), set-default, rm
- **`mvm image`** — Image management: ls, fetch, set-default, rm, inspect, import, warm
- **`mvm bin`** — Firecracker binary management: ls, fetch, default, rm
- **`mvm cache`** — Cache lifecycle: init, prune (per-resource or all), clean
- **`mvm logs`** — Log streaming: boot logs (serial console) and Firecracker OS logs with --follow
- **`mvm ssh`** — SSH into VMs by name, ID, IP, or MAC with custom user and key

#### Architecture
- **Three-layer architecture** (CLI → API → Core) with strict import boundaries
- **LazyMVMGroup** — Custom Click group with lazy-loaded Typer sub-apps for sub-150ms startup
- **Controller / Service / Repository / Resolver** pattern across all 13 core domains
- **Input → Request → Resolved** pipeline for type-safe, validated VM operations
- **SQLite database** with migration system for persistent state (vms, networks, images, kernels, binaries, keys, host state, iptables rules, IP leases)
- **Relation enrichment** system with batch loading to prevent N+1 queries
- **Privilege delegation** model via `mvm` unix group and sudoers drop-in (no sudo for normal operations)

#### VM Lifecycle
- Create VMs with configurable vCPUs, memory, disk size, PCI, console, logging, and metrics
- Root filesystem images via durable incremental copying with reflink/FICLONE support
- Snapshot and restore (memory + VM state) via Firecracker API socket
- Export VM configurations as portable JSON
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
- Format support: qcow2, raw, tar-rootfs, squashfs, vhd
- Automated conversion pipeline: download → decompress → format conversion → root partition extraction → filesystem optimization
- SHA256 checksum verification for downloaded images
- Image warm pool for fast VM creation (pre-extracted ready-to-copy images)
- Incremental copy with reflink/FICLONE when filesystem supports it

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
- Idempotent — safe to run multiple times
- Full reset: revert all host changes including networking, sysctl, sudoers, and group

#### Services
- **Console relay** — PTY-over-vsock bridge for interactive serial console without SSH
- **nocloud-net server** — Per-VM HTTP server for cloud-init datasource delivery

#### Developer Experience
- **Python API** — All CLI commands map 1:1 to `*Operation` static methods in `mvmctl.api`
- **Strict mypy** — Full type annotations across the codebase, no `type: ignore` suppressions
- **Ruff** — Linting and formatting, zero-tolerance for violations
- **Test suite** — Unit, integration, system, and layer compliance tests
- **uv** — Fast Python package and project management

#### Distribution
- Standalone Nuitka-compiled binary (zero Python runtime dependency)
- PyInstaller fallback for development builds
- PyPI package (`mvmctl`)
- Distribution packages: .deb (Debian/Ubuntu), .rpm (RHEL/Fedora), PKGBUILD (Arch Linux)
- Man page (`mvm.1`)

#### Performance
- CLI startup under 150ms via lazy-loading LazyMVMGroup
- Batch relation loading with deduplication (O(relations) queries, not O(entities × relations))
- Durable incremental image copying with reflink support (instant copy on btrfs/XFS)
- SQL-level computation (COUNT, WHERE IN) instead of fetch-all + Python filtering

[0.1.0]: https://github.com/AlanD20/mvmctl/releases/tag/v0.1.0
