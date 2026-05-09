# Running System Tests

## Prepare

### One-time system setup

```bash
# 1. Python + uv
# Requires Python 3.13+. Install uv if not present:
curl -LsSf https://astral.sh/uv/install.sh | sh

# 2. System packages
# Ubuntu/Debian:
sudo apt-get install -y iproute2 iptables procps kmod sudo cloud-image-utils \
  qemu-utils e2fsprogs squashfs-tools util-linux tar openssh-client
# Arch Linux:
sudo pacman -S --needed iproute2 iptables procps-ng kmod sudo cloud-utils \
  qemu-img e2fsprogs squashfs-tools util-linux tar coreutils openssh

# 3. KVM check
egrep -c '(vmx|svm)' /proc/cpuinfo   # must be > 0
sudo usermod -aG kvm $USER

# 4. User groups
# mvm group — required to run vm create/rm and other privileged operations
sudo usermod -aG mvm $USER
# disk group — required for image import system tests (sfdisk/parted on loop devices)
# Optional — skip if not running image import tests
sudo usermod -aG disk $USER

# 5. Log out and back in for group changes to take effect
# Verify with: groups
```

### Initialize mvm environment

```bash
# Clone and install dependencies
git clone <repo-url> && cd mvmctl
uv sync --group dev

# Initialize mvm (creates cache dirs, iptables chains)
# --non-interactive skips the wizard prompts
# --skip-host skips the sudo host init (done separately below)
uv run mvm init --non-interactive --skip-host

# Host init (requires sudo — sets up iptables chains, sysctl params)
sudo ~/.pyenv/shims/uv run mvm host init

# Disable guestfs (loop-mount is the primary provisioning backend)
uv run mvm config set settings guestfs_enabled false

# Build the mvm-services combined binary (Nuitka multidist for loop-mount backend + console relay + nocloud server)
uv run python scripts/build_services.py

# Pull default assets
uv run mvm kernel pull --type firecracker --set-default
uv run mvm image pull alpine-3.21
uv run mvm bin pull 1.15.1 --set-default
```

> **Note:** The `disk` group and `guestfs` packages are optional. Skip them if you
> are only running volume, VM lifecycle, or other domain tests (not image import).

### Local asset mirror (speed up repeated runs)

System tests download large files (kernel vmlinux, Ubuntu images, Firecracker
binaries) from the internet. A local asset mirror makes repeated test runs
**dramatically faster** by copying cached files instead of re-downloading.

The mirror is at `~/.cache/mvm-asset-mirror/` — deliberately **outside**
`~/.cache/mvmctl/` so that `cache clean --force` does not wipe it.

#### How it works

`HttpDownload.download_file()` in `utils/http.py` checks the `MVM_ASSET_MIRROR`
environment variable before making an HTTP request. If the file exists in the
mirror directory (matched by URL basename), it copies it locally and verifies
the SHA256 checksum. After a successful HTTP download, the file is automatically
copied **into** the mirror for future use.

This is completely transparent — no changes anywhere in `core/` or `api/`.

#### Seeding the mirror

```bash
# One-time: download everything and cache in the mirror
task sys-setup-seed

# Or manually:
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run mvm kernel pull --type firecracker --set-default
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run mvm image pull alpine-3.21
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run mvm image pull ubuntu-24.04-minimal
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run mvm bin pull 1.15.1 --set-default
```

The first run downloads from the internet (slow). Subsequent runs copy from
the mirror (fast):

| Asset | First run (HTTP) | Subsequent (mirror) |
|-------|-----------------|-------------------|
| Firecracker kernel (43 MB) | ~30-60s | **< 1s** |
| Alpine image (203 MB) | ~2-5 min | **~1.5s** |
| Ubuntu 24.04 (220 MB) | ~5-10 min | **~1s download + ~40s processing** |
| Firecracker binary (7.3 MB) | ~10-20s | **< 1s** |

> **Note:** Image "processing" (extracting tar.xz, converting to ext4, shrinking,
> compressing with zstd) is unavoidable and adds 15-40s per image even with the
> mirror. Only the network download is eliminated.

The `sys-test` and `sys-test-fast` tasks automatically set `MVM_ASSET_MIRROR`,
so the mirror is used whenever it has been seeded.

### Auto-fetch on test run

The `prepare_system_env` fixture (session-scoped, autouse) automatically
**pulls any missing assets** when you run the tests **serially**:

- **Firecracker kernel** — fetched if not cached
- **Alpine 3.21** — fetched if not cached
- **Ubuntu 24.04** — fetched if not cached
- **Ubuntu 24.04 minimal** — fetched if not cached
- **Firecracker binary** — fetched if none cached

This means you only need the one-time setup steps above. Asset fetches are
handled automatically. If `MVM_ASSET_MIRROR` is set (it is when using `task
sys-test`), fetches copy from the local mirror instead of downloading from the
internet.

## Run

### Quick start (recommended)

```bash
# All system tests (asset mirror, serial execution, cache cleanup last)
task sys-test

# Fast subset — no slow downloads, no KVM
task sys-test-fast

# Seed the asset mirror (one-time, then fast copies thereafter)
task sys-setup-seed
```

### Test execution order

The full suite runs in 4 phases to surface failures early and avoid
destructive interference:

| Phase | Scope | Tests | Command |
|-------|-------|-------|---------|
| 1 | Isolated domain tests (parallel) | bin, config, init, keys, kernel, images, volume | `-m "domain_bin or domain_config or domain_init or domain_key or domain_kernel or domain_image or domain_volume"` |
| 2a | Serial VM/network (iptables, bridges) | network, host, shared VM state, edge cases | `-m "domain_vm and serial"` |
| 2b | Parallel VM tests | VM lifecycle, batch create, volume integration, console, logs, SSH, full journeys | `-m "domain_vm and not serial"` |
| 3 | Cache cleanup (last) | cache clean --force (destroys everything) | `tests/system/test_cache.py` |

### Manual test execution

```bash
# All system tests (serial, uses mirror, cache last)
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run pytest tests/system/ -v -n 0

# Run a single domain with the mirror
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run pytest tests/system/ -m domain_volume -v

# Run a single test class
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run pytest tests/system/test_vm_lifecycle.py::TestVMBatchCreate -v

# Run a specific test
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror uv run pytest tests/system/test_volume.py::TestVolumeLifecycle::test_volume_create -v
```

> **IMPORTANT:** Always use `sg mvm -c '...'` to wrap test commands when running
> as a non-root user in the `mvm` group. The `sg mvm` switches to the `mvm` group
> context, which is required for VM and network operations:
> ```bash
> MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror sg mvm -c 'uv run pytest tests/system/test_volume.py -v'
> ```

### Individual test suites

| Suite | Tests | What it covers |
|-------|-------|----------------|
| **Volume** (`test_volume.py`) | 17 | CRUD lifecycle: create (raw/qcow2), ls (table/JSON), inspect (table/JSON), rm (single/multiple/--force/nonexistent), resize, empty ls, duplicate name rejection, invalid size/format rejection, nonexistent inspect/resize |
| **VM Batch Create** (`test_vm_lifecycle.py::TestVMBatchCreate`) | 12 | --count default, --count 3, --atomic with/without count, --count 0/-1 rejected, --count + --ip/mac rejected, --count + --volume rejected, explicit --count 1, atomic rollback on name collision |
| **VM-Volume Integration** (`test_vm_lifecycle.py::TestVMVolumeIntegration`) | 2 | vm create --volume (create + attach at boot), vm attach-volume + detach-volume (stop → attach → start → stop → detach) |
| **VM Lifecycle** (`test_vm_lifecycle.py` other classes) | 49 | Create per image, pause/resume/stop/start/reboot, --force variants, remove running without --force, SSH, list (table/JSON), inspect/export, export/import roundtrip, vcpus/memory/disk flags, static IP, MAC, named network, kernel override, boot-args, no-console, SSH key file, enable-pci, enable-logging, enable-metrics, ps, user-data, cloud-init-mode, duplicate name rejection, nonexistent errors |
| **VM Snapshot/Load** (`test_vm_snapshot_load.py`) | 7 | snapshot/load, export/import roundtrip, error paths |
| **Network** (`test_network.py`) | 23 | CRUD, inspect (table/JSON), iptables, bridge, --no-nat, set-default, ls --json, rm multiple, error paths, sync, gateway |
| **Keys** (`test_keys.py`) | 22 | CRUD (ed25519, rsa, ecdsa), add existing, export, inspect (table+JSON), set-default/clear, ls --json, error paths, bits/comment/out/force |
| **Images** (`test_images.py`) | 20 | Fetch (alpine+ubuntu), list (table/JSON/remote), inspect (table/JSON/tree), set/get-default, warm, remove, fetch error, import qcow2/raw, pull --force, pull --set-default |
| **Kernel** (`test_kernel.py`) | 12 | Fetch official + set-default, list (empty/JSON), set-default, remove, inspect table/JSON/tree, nonexistent |
| **Binary** (`test_bin.py`) | 10 | List local/JSON/remote (with limit), fetch+set-default, set-default by ID, remove by version, pull --force |
| **Host** (`test_host.py`) | 4 | Host status check (table+JSON), clean/reset blocked by running VM |
| **Full Journeys** (`test_full_journeys.py`) | 12 | Create+SSH, network→VM, network→VM+explicit IP, key→VM, full state chain, explicit IP, 2 VMs same net, reboot chain, SSH CLI cmd, multiple SSH keys, ping, export/import, concurrent creation |
| **Console** (`test_console.py`) | 4 | Console state/kill |
| **Logs** (`test_logs.py`) | 4 | Log streaming |
| **SSH** (`test_ssh.py`) | 6 | SSH config, command execution, timeout flag |
| **Config** (`test_config.py`) | 5 | Config CRUD |
| **Init** (`test_init.py`) | 6 | Init wizard (non-interactive mode) |
| **Cache** (`test_cache.py`) | 13 | Cache prune (dry-run and actual), init, clean --force |
| **CLI Edge Cases** (`test_cli_edge_cases.py`) | 32 | Root flags (--version, --verbose, --debug), domain-specific edge cases |
| **Image Import + VM Create** (`test_image_import_create_vm.py`) | 2 | Import image then create VM from it |
| **Total** | **283** | |

### Running with sudo

Some operations require `sudo` (host init, iptables, bridge creation). The
system tests handle this automatically via `sg mvm` group switching. For manual
commands:

```bash
# One-time host setup (requires sudo)
sudo ~/.pyenv/shims/uv run mvm host init
sudo ~/.pyenv/shims/uv run mvm init

# Regular mvm commands — use sg mvm, NOT sudo
sg mvm -c 'uv run mvm vm ls'
sg mvm -c 'uv run mvm network ls'
```

> **NEVER** run `mvm` commands with bare `sudo` — always use `sg mvm -c '...'`
> or `sudo -u www sg mvm -c '...'`. The `sg mvm` wrapper ensures correct group
> permissions without breaking uv's virtual environment path resolution.

## What is NOT tested (known gaps)

System tests are black-box CLI tests via subprocess that require real hardware
(KVM, mvm group, real networking, pre-cached assets). The following
functionality has known coverage gaps at the system level, each with a
documented rationale:

### Partially Tested Domains

| Domain | Commands Tested | Untested Commands | Rationale for Untested |
|--------|----------------|-------------------|------------------------|
| **`host`** | `ls` (table+JSON in `test_host.py`) | `init`, `clean`, `reset` | `host init` requires `sudo` and creates persistent system state (group, sudoers, bridges). `host clean/reset` are destructive (teardown bridges/iptables that other tests depend on). Integration tests (`test_host_init_reset.py`) cover these with mocks. Manual testing recommended for real hardware. |
| **`cache`** | `prune --dry-run`, `clean --force` (in `test_cache.py`) | `prune` (non-dry-run) | `cache prune` without `--dry-run` is destructive (removes cached assets, potentially including running VMs). Runs last in Phase 3 to avoid clobbering the shared test environment. |
| **`init`** | `--non-interactive --skip-host` | Interactive wizard | The init wizard is entirely interactive (prompts for sudo, confirms downloads, spawns `sudo mvm host init`). Not automatable in a subprocess-based test. |
| **Volume** | Full CRUD (create, ls, inspect, rm, resize) | `vm attach-volume`/`detach-volume` on running VM | attach/detach requires VM to be in STOPPED state. The happy path (stop → attach → start → stop → detach) is tested. The error path (attach to running VM) returns a clear error message tested at the unit level. |

### VM Operations

| Gap | Rationale |
|-----|-----------|
| `vm snapshot` / `vm load` | Requires snapshot file path args and produces real memory/state files. Integration tests cover the workflow with mocked Firecracker socket. |
| `vm import` | Requires importable config files. Integration tests cover the config parsing. |
| State transition negatives (pause paused VM, resume running VM, start running VM, stop stopped VM) | Each requires the VM to be in a specific state that's hard to guarantee in a shared test environment. Better covered by unit tests on the VMController. |
| `vm cleanup` (non-dry-run) | Destructive — removes stale VMs and resources that other tests may depend on. |
| All `vm create` flag combinations | Each flag would require a dedicated VM (3-30s boot time). Combinatorial explosion. Validated at integration level (CliRunner with mocked subprocess). |

### Other Domains

| Domain | Gap | Rationale |
|--------|-----|-----------|
| **Network** | `network rm --force` with VMs attached | Requires two dependent resources (network + VM on it). |
| **Network** | Overlapping CIDR rejection | Requires two networks per test — time-consuming (real bridge creation). |
| **Kernel** | `kernel pull --type firecracker` (build from source) | Requires kernel build toolchain (gcc, make, libelf-dev) and 10+ minute compilation. The download pipeline is validated by `--type official`. |
| **Kernel** | `kernel pull --version/--arch/--jobs/--keep-build-dir/--clean-build/--config` | Each flag requires a fresh fetch. Flag-to-parameter mapping covered by integration tests. |
| **Binary** | `bin rm` by ID prefix | Requires a fetched binary with known ID. Chained dependency on `bin fetch --set-default`. |
| **Image** | `image import --root-partition` | Requires custom multi-partition disk images. Blocked by disk group membership for loop device access. |
| **Image** | `image inspect --tree` | Tree format is a display variant of the same data. Table format is tested. |
| **Key** | `key create --bits/--comment/--out/--force` | Integration tests verify flag-to-call mapping. |
| **Key** | `key create` interactive fallback | Interactive algorithm selection cannot be automated via subprocess. |

### Edge Cases Not Worth System-Level Testing

| Edge Case | Rationale |
|-----------|-----------|
| Name validation (empty, special chars, length limits) | CLI-layer concern. Unit-tested in validators. |
| IP/MAC validation | Happens before resource creation. Integration tests verify rejection. |
| 0 vCPUs, negative memory, impossible disk sizes | Validated before resource creation. Integration tests suffice. |
| JSON schema consistency | Contractually defined by `models/*Item` dataclasses. Unit tests cover serialization. |
| Concurrent operations | System tests run sequentially. Would require separate harness. |

## Integration Test Coverage

Many gaps above are covered at the integration level (`tests/integration/`).
Integration tests use CliRunner (in-process Typer invocation) with mocked
subprocess calls, enabling comprehensive flag and edge-case testing without
real hardware:

| Integration Test File | Covers |
|----------------------|--------|
| `test_vm_lifecycle.py` | Snapshot/load, duplicate name, nonexistent VM removal, full lifecycle with mocks |
| `test_volume_integration.py` | Volume CRUD, resize, remove with mocked subprocess |
| `test_network_workflow.py` | Duplicate network, nonexistent remove/inspect, missing/invalid CIDR, custom gateway, --no-nat |
| `test_host_init_reset.py` | Host init workflow, reset rollback, privilege escalation |
| `test_binary_integration.py` | Binary pull, list, set-default, remove with mocked subprocess |
| `test_cache_integration.py` | Cache init, prune operations with mocked filesystem |
| `test_cloud_init_iso.py` | Cloud-init ISO generation and validation |
| `test_config_integration.py` | Config file loading, merging, override resolution |
| `test_console_integration.py` | Console relay subprocess management |
| `test_image_integration.py` | Image pull, list, inspect, remove, import with mocked downloads |
| `test_init_integration.py` | Init wizard flow, non-interactive mode |
| `test_kernel_integration.py` | Kernel pull (official/firecracker), list, inspect, remove |
| `test_key_integration.py` | Key create (ed25519/rsa/ecdsa), list, inspect, export, remove |
| `test_log_integration.py` | Logging and metrics configuration for Firecracker |
| `test_nocloud_net_lifecycle.py` | NoCloud network lifecycle with mocked subprocess |
| `test_ssh_integration.py` | SSH client invocation and key-based auth flow |
| `test_vm_direct_injection.py` | VM direct file injection workflow |
| `test_volume_db.py` | Volume repository CRUD operations with real SQLite |
| `test_cli_smoke.py` | Basic CLI invocation and help output smoke tests |

## Troubleshooting

### "Permission denied" for /dev/loop*

Run `groups` to verify you are in the `disk` group. If not:
```bash
sudo usermod -aG disk $USER
# Log out and back in
```

### "mvm group" errors

Run `groups` to verify you are in the `mvm` group. If not:
```bash
sudo usermod -aG mvm $USER
# Log out and back in
```

### VM creation hangs or times out

Check that the `mvm-provision` binary is built:
```bash
ls -la ~/.cache/mvmctl/bin/mvm-provision
# If missing: uv run python scripts/build_services.py
```

### "iptables" errors

Run `sudo ~/.pyenv/shims/uv run mvm host init` to set up iptables chains.

### Tests fail with "address already in use"

Another test session may still have resources. Run cleanup:
```bash
sg mvm -c 'uv run mvm vm ls --json'    # check for running VMs
sg mvm -c 'uv run mvm vm rm <name> --force'  # remove stale VMs
```
