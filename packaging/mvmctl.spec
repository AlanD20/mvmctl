Name:           mvmctl
Version:        0.1.0
Release:        1%{?dist}
Summary:        MicroVM Manager - Container speed, VM isolation

License:        MIT
URL:            https://github.com/AlanD20/mvmctl
Source0:        https://github.com/AlanD20/mvmctl/releases/download/v%{version}/mvm
Source1:        https://raw.githubusercontent.com/AlanD20/mvmctl/v%{version}/docs/mvm.1

BuildArch:      x86_64

Requires:       iproute, iptables, nftables, qemu-img, openssh-clients, e2fsprogs, util-linux, shadow-utils, sudo, procps-ng, kmod, tar
Recommends:     cloud-utils, libguestfs

%description
mvmctl is a production-grade CLI for managing microVMs on Linux.
It handles VM lifecycle: downloading kernels/images, networking, VM creation,
SSH access, log streaming, snapshots, and cleanup.

%prep
# No prep needed for binary distribution

%build
# Binary is already built

%install
install -D -m 755 %{SOURCE0} %{buildroot}/usr/bin/mvm
install -D -m 644 %{SOURCE1} %{buildroot}/usr/share/man/man1/mvm.1
gzip -9 %{buildroot}/usr/share/man/man1/mvm.1

%post
/usr/sbin/mandb >/dev/null 2>&1 || :

%postun
/usr/sbin/mandb >/dev/null 2>&1 || :

%files
%license LICENSE
/usr/bin/mvm
%{_mandir}/man1/mvm.1.gz

%changelog
* Sat May 16 2026 AlanD20 <aland20@pm.me> - 0.1.0-1
- `mvm vm`** -- Full VM lifecycle: ls, ps, create, rm, start, stop, reboot, pause, resume, snapshot, load, inspect, export, import, attach-volume, detach-volume
- `mvm console`** -- Interactive serial console access via PTY-over-vsock relay with --state and --kill options (attach is default)
- `mvm host`** -- Host configuration: init (KVM, modules, sysctl, mvm group, sudoers), ls, clean, reset
- `mvm network`** -- Named bridge networks with NAT: ls, default, create, rm, inspect, sync
- `mvm key`** -- SSH key management: ls, add, create, rm, inspect, export, default
- `mvm config`** -- Runtime configuration: get, set, list, reset
- `mvm init`** -- Interactive setup wizard with non-interactive mode
- `mvm kernel`** -- Kernel management: ls, inspect, pull, default, rm
- `mvm image`** -- Image management: ls, pull, default, rm, inspect, import, warm
- `mvm bin`** -- Firecracker binary management: ls, pull, rm, default
- `mvm cache`** -- Cache lifecycle: init, prune (per-resource or all), clean
- `mvm logs`** -- Log streaming: boot logs (serial console) and Firecracker OS logs with --follow
- `mvm ssh`** -- SSH into VMs by name, ID, IP, or MAC with custom user, key, and connection timeout
- `mvm volume`** -- Persistent data disk management: create, rm, ls, inspect, resize
- Three-layer architecture** (CLI -> API -> Core) with strict import boundaries
- LazyMVMGroup** -- Custom Click group with lazy-loaded Typer sub-apps for sub-150ms startup
- Controller / Service / Repository / Resolver** pattern across all 14 core domains
- Input -> Request -> Resolved** pipeline for type-safe, validated VM operations
- Provisioning backend abstraction** (LoopMount vs Guestfs) with factory pattern
- SQLite database** (`db/migrations/` directory) with migration system (`001_initial_schema.sql`) for persistent state: images, kernels, binaries, volumes, networks, network_leases, vm_instances, host_state, host_state_changes, iptables_rules, nftables_rules, ssh_keys, user_settings
- Shared utility helpers** (`utils/`): fs, _system, http, network, crypto, template, yaml, _validators, _io, _lazy_import, progress, cli, operation_utils, auditlog, common, _disk, timinglog
- Relation enrichment** system with batch loading to prevent N+1 queries
- Privilege delegation** model via `mvm` unix group and sudoers drop-in (no sudo for normal operations)
- Create VMs with configurable vCPUs, memory, disk size, PCI, console, logging, and metrics
- Batch VM creation via `--count N` and all-or-nothing `--atomic` flag
- Root filesystem images via durable incremental copying with reflink/FICLONE support
- Snapshot and restore (memory + VM state) via Firecracker API socket
- Export VM configurations as portable JSON using semantic references (os_slug, version, name)
- Import previously exported VM configurations
- Cloud-init provisioning in four modes: inject (libguestfs), net (nocloud-net HTTP server), iso, off
- Firecracker process lifecycle: spawn, monitor, signal (SIGTERM/SIGKILL), exit code tracking
- Per-VM isolated nocloud-net HTTP servers with source-based iptables firewall rules
- Linux bridge and TAP device management for guest connectivity
- NAT/masquerade with iptables for outbound internet access
- IP lease management with automatic allocation and release
- iptables rule tracking with generic IPTablesTracker infrastructure
- Network reconciliation (sync DB state with live bridge state)
- Restore all networks after host reboot
- Fetch images by OS slug (ubuntu-24.04, ubuntu-22.04, archlinux, debian-bookworm, alpine-3.21)
- Import local image files with automatic format detection
- Format support: qcow2, raw, vhd, vhdx, tar-rootfs, squashfs
- Automated conversion pipeline: download -> decompress -> format conversion -> root partition extraction -> filesystem optimization
- SHA256 checksum verification for downloaded images
- Image warm pool for fast VM creation (pre-extracted ready-to-copy images)
- Incremental copy with reflink/FICLONE when filesystem supports it
- Loop-mount provisioner backend for rootfs operations without libguestfs dependency
- Download pre-built Firecracker CI kernels (optimized, fast boot)
- Build official upstream kernels from source with Firecracker-compatible configs
- Configurable kernel options via YAML specs (enable/disable/set-val)
- Automatic architecture detection and kernel config application
- Download Firecracker and jailer binaries from GitHub releases
- Version management with default version selection
- Automatic CI version resolution for template-based image URLs
- Generate ED25519, RSA, and ECDSA keypairs via ssh-keygen
- Import existing public keys with fingerprint detection
- Set one or more default keys for automatic VM injection
- Export keypairs to standard ~/.ssh location
- Enable IP forwarding and persist sysctl settings
- Load KVM kernel modules (kvm, kvm_intel/kvm_amd)
- Create mvm unix group and sudoers drop-in with passwordless access to privileged binaries
- Setup iptables chains for VM traffic management
- Idempotent -- safe to run multiple times
- Full reset: revert all host changes including networking, sysctl, sudoers, and group
- Console relay** -- PTY-over-vsock bridge for interactive serial console without SSH
- nocloud-net server** -- Per-VM HTTP server for cloud-init datasource delivery
- mvm-provision** -- Loop-mount rootfs provisioning binary for SSH key injection, hostname setup, DNS config, cloud-init disable, and filesystem resize (~200ms per VM, replaces libguestfs as primary path)
- Python API** -- All CLI commands map 1:1 to `*Operation` static methods in `mvmctl.api`
- Strict mypy** -- Full type annotations across the codebase, no `type: ignore` suppressions
- Ruff** -- Linting and formatting, zero-tolerance for violations
- Test suite** -- Unit, integration, system, and layer compliance tests
- uv** -- Fast Python package and project management
- Build scripts** (`scripts/`): build_services.py, run_tests.py, setup-test-environment.py, profile_test_memory.py
- Standalone Nuitka-compiled binary (zero Python runtime dependency)
- Nuitka is the primary build tool (PyInstaller is available as a fallback)
- PyPI package (`mvmctl`)
- Distribution packages: .deb (Debian/Ubuntu), .rpm (RHEL/Fedora), PKGBUILD (Arch Linux)
- Man page (`mvm.1`)
- CLI startup under 200ms via lazy-loading LazyMVMGroup
- Batch relation loading with deduplication (O(relations) queries, not O(entities x relations))
- Durable incremental image copying with reflink support (instant copy on btrfs/XFS)
- SQL-level computation (COUNT, WHERE IN) instead of fetch-all + Python filtering

* Mon Mar 30 2026 AlanD20 <aland20@pm.me> - 0.1.0-1
- Initial RPM release
- Firecracker microVM management
- Network bridge and TAP management
- SSH key and image management
- VM lifecycle (create, start, stop, remove, snapshot)
- Distribution packages support
- Comprehensive test suite (2300+ tests)
