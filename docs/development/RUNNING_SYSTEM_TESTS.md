# Running System Tests

## Prepare

### One-time system setup

```bash
# 1. System packages
# Ubuntu/Debian:
sudo apt-get install -y iproute2 iptables procps kmod sudo genisoimage qemu-utils cloud-image-utils e2fsprogs squashfs-tools util-linux tar openssh-client libguestfs0 libguestfs-tools supermin
# Arch Linux:
sudo pacman -S --needed iproute2 iptables procps-ng kmod sudo libisoburn qemu-img cloud-utils e2fsprogs squashfs-tools util-linux tar coreutils openssh libguestfs supermin

# 2. KVM
egrep -c '(vmx|svm)' /proc/cpuinfo   # must be > 0
sudo usermod -aG kvm $USER

# 3. Initialize mvm (requires sudo for first-time host setup)
uv run mvm init --non-interactive
# Or manually: sudo mvm host init && uv run mvm bin fetch
```

### Auto-fetch on test run

The `prepare_system_env` fixture (session-scoped, autouse) automatically fetches
any missing assets when you run the tests **serially**:

- **Official kernel** — fetched if not cached
- **Alpine 3.21 image** — fetched if not cached
- **Ubuntu 24.04 minimal image** — fetched if not cached
- **Firecracker binary** — latest version fetched if none cached

This means you only need steps 1–3 above. Asset fetches are handled
automatically on the first serial run. Progress messages are printed to stderr:

```
[prepare] Checking system environment...
[prepare] No kernel cached. Fetching official kernel...
[prepare] Image 'ubuntu-24.04-minimal' not cached. Fetching (this may take a while)...
[prepare] System environment ready.
```

> **Note:** Asset fetches download over the network and may take several minutes
> on the first run. Subsequent runs are fast (cache-hit).
>
> **Parallel runs (`-n auto`) skip auto-fetch** to avoid race conditions where
> multiple workers download the same asset simultaneously. Pre-fetch assets
> manually before running in parallel:
> ```bash
> uv run mvm kernel fetch --type official --set-default
> uv run mvm image fetch alpine-3.21
> uv run mvm image fetch ubuntu-24.04-minimal
> uv run mvm bin fetch <version> --set-default
> ```

## Run

| Suite | Command | What it tests |
|-------|---------|---------------|
| Network | `pytest tests/system/test_network.py -v` | CRUD, inspect (table+JSON), iptables, bridge, --no-nat, set-default, ls --json, rm multiple, error paths (15 tests) |
| Keys | `pytest tests/system/test_keys.py -v` | CRUD (ed25519, rsa, ecdsa), add existing, export, inspect (table+JSON), set-default/clear, ls --json, error paths (15 tests) |
| Images | `pytest tests/system/test_images.py -v` | Fetch (alpine+ubuntu), list (table/JSON/remote), inspect (table+JSON), set/get-default, warm, remove, fetch error (11 tests) |
| Kernel | `pytest tests/system/test_kernel.py -v` | Fetch official + set-default, list (empty/JSON), set-default, remove (6 tests) |
| Binary | `pytest tests/system/test_bin.py -v` | List local/JSON/remote (with limit), fetch+set-default, set-default by ID, remove by version (7 tests) |
| VM Lifecycle | `pytest tests/system/test_vm_lifecycle.py -v` | Create per image, state transitions (pause/resume/stop/start/reboot), stop/reboot --force, remove running w/o force, SSH, list, remove/force-remove, duplicate reject, nonexistent error, inspect, export, cleanup --dry-run (21 tests) |
| Host | `pytest tests/system/test_host.py -v` | Host status check (table+JSON), graceful skip if uninitialized (2 tests) |
| Full Journeys | `pytest tests/system/test_full_journeys.py -v` | Create+SSH, network→VM, network→VM+explicit IP, key→VM, full state chain, explicit IP, 2 VMs same net, reboot chain, SSH CLI cmd, multiple SSH keys (10 tests) |

```bash
# All system tests (serial — safest, auto-fetches missing assets)
pytest tests/system/ -v

# All system tests (parallel — requires pre-fetched assets)
pytest tests/system/ -v -n auto

# Fast subset (no slow tests, no KVM-required tests)
pytest tests/system/ -v -m "system and not slow and not requires_kvm"
```

## What is NOT tested (known gaps)

System tests are black-box CLI tests via subprocess that require real hardware (KVM, mvm group, real networking, pre-cached assets). The following functionality has known coverage gaps at the system level, each with a documented rationale:

### Partially Tested Domains

| Domain | Commands Tested | Untested Commands | Rationale for Untested |
|--------|----------------|-------------------|------------------------|
| **`host`** | `ls` (table+JSON in `test_host.py`) | `init`, `clean`, `reset` | `host init` requires `sudo` and creates persistent system state (group, sudoers, bridges). `host clean/reset` are destructive (teardown bridges/iptables that other tests depend on). Integration tests (`test_host_init_reset.py`) cover these with mocks. Manual testing recommended for real hardware. |
| **`cache`** | None | `init`, `prune` | `cache prune` is destructive (removes cached assets, potentially including running VMs). Would clobber the shared test environment's cache. Integration test coverage via CliRunner + mocks is the appropriate layer. |
| **`init`** | None | `init` (wizard) | The init wizard is entirely interactive (prompts for sudo, confirms downloads, spawns `sudo mvm host init`). Not automatable in a subprocess-based test. |

### VM Operations

| Gap | Rationale |
|-----|-----------|
| `vm snapshot` / `vm load` | Requires snapshot file path args and produces real memory/state files. Integration tests (`test_vm_lifecycle.py`) cover the workflow with mocked Firecracker socket. |
| State transition negatives (pause paused VM, resume running VM, start running VM, stop stopped VM) | Each requires the VM to be in a specific state that's hard to guarantee in a shared, sequential test environment. These state machine violations raise exceptions in the Firecracker API — better covered by unit tests on the VMController. |
| `vm cleanup` (actual, not `--dry-run`) | `cleanup` without `--dry-run` removes stale VMs and resources. Destructive in a shared test environment where other tests may have VMs. |
| `vm create` with flags: `--kernel`, `--image-path`, `--kernel-path`, `--vcpus`, `--mem`, `--disk-size`, `--mac`, `--user-data`, `--cloud-init-mode`, `--nocloud-net-port`, `--user`, `--enable-pci`, `--no-console`, `--lsm-flags`, `--enable-logging`, `--enable-metrics`, `--firecracker-bin`, `--skip-cleanup` | Each flag combination would require a dedicated VM (3-30s boot time). Combinatorial explosion makes this impractical at the system level. These are better validated at the integration level (CliRunner with mocked subprocess) where flag-to-API-call mapping can be verified cheaply. |

**Closed gaps:** `vm stop --force` ✅, `vm reboot --force` ✅, `vm rm running without --force` ✅ — all now tested in `TestVMStateOperationsIndependent`.

### Network Operations

| Gap | Rationale |
|-----|-----------|
| `network rm --force` with VMs attached | Removing a network that has active VMs without `--force` should fail. Requires two dependent resources (network + VM on it). Covered in integration tests. |
| Overlapping CIDR rejection | Creating two networks with overlapping subnets requires two network creations per test — practical at system level but time-consuming (real bridge creation). |

**Closed gaps:** `network set-default` ✅ — now tested in `TestNetworkLifecycle`.

### Kernel Operations

| Gap | Rationale |
|-----|-----------|
| `kernel fetch --type firecracker` | Requires building a kernel from source (minutes). Impractical per-PR. The fetch logic (download + build) is identical for both types; the `--type official` test validates the download pipeline. |
| `kernel fetch --version`, `--arch`, `--jobs`, `--keep-build-dir`, `--clean-build`, `--config` | Each flag requires a fresh fetch. The kernel build is the expensive part. Flag-to-parameter mapping is better tested at the unit/integration level. |

**Closed gaps:** `kernel rm` ✅, `kernel fetch --set-default` ✅ — now tested in `TestKernelRemoveAndFetch`.

### Binary Operations

| Gap | Rationale |
|-----|-----------|
| `bin rm` by ID prefix | Requires a fetched binary with known ID. Chained dependency on `bin fetch`. |

**Closed gaps:** `bin fetch --set-default` ✅, `bin rm --version` ✅, `bin default <id>` ✅ — now tested in `TestBinaryFetchAndLifecycle` (marked `@pytest.mark.slow`).

### Image Operations

| Gap | Rationale |
|-----|-----------|
| `image import` (qcow2, raw, tar-rootfs) | Requires a real image file in each format. The conversion pipeline (qemu-img, tar, etc.) is better tested at the unit level with controlled inputs. |
| `image fetch --force`, `--skip-optimization`, `--disable-detector`, `--type`, `--version`, `--arch` | Each flag requires a fresh fetch + download. Flag-to-parameter mapping covered by integration tests. The core download pipeline is validated by the existing `test_image_fetch`. |
| `image inspect --tree` | Tree format is a display variant of the same data. Table format is tested. Edge case in rendering. |

**Closed gaps:** `image rm` ✅ (replaced `pytest.skip` with `test_image_remove_with_fixture`), `image warm` ✅ — now tested in `TestImageRemove` and `TestImageDefaults`.

### Key Operations

| Gap | Rationale |
|-----|-----------|
| `key create --bits`, `--comment`, `--out`, `--force` | These are flag variants on the same ssh-keygen pipeline. Integration tests verify flag-to-call mapping. |
| `key create` interactive fallback | The interactive algorithm selection (prompt when `--algorithm` is omitted) cannot be automated via subprocess. |

**Closed gaps:** `key ls --json` ✅, `key inspect` table mode ✅, `key create --algorithm ecdsa` ✅ — now tested in `TestKeyLifecycle`.

### Edge Cases Not Worth System-Level Testing

| Edge Case | Rationale |
|-----------|-----------|
| Name validation (empty names, special chars, length limits) | Name validation is a CLI-layer concern (typer string handling + entity name validation). Unit-tested in the validator. System-level testing would be expensive (creates real resources). |
| IP/MAC validation | Validation happens before resource creation — integration tests exercise this with CliRunner + mocked subprocess. System tests would fail at the same validation layer without creating real VMs. |
| 0 vCPUs, negative memory, impossible disk sizes | All validated before resource creation. Integration tests verify rejection messages. No system-level value. |
| JSON schema consistency | Schema is contractually defined by `models/*Item` dataclasses. Unit tests on serialization cover this better than system tests. |
| Concurrent operations | System tests run sequentially. Concurrent testing would require a separate test harness. |
| ID prefix resolution | 6-char hash prefix resolution is tested indirectly by `image inspect` (which uses a prefix). Explicit tests would add cost with marginal benefit since all prefix lookups go through the same resolver. |

## Integration Test Coverage

Many gaps above are covered at the integration level (`tests/integration/`). Integration tests use CliRunner (in-process Typer invocation) with mocked subprocess calls, enabling comprehensive flag and edge-case testing without real hardware:

| Integration Test File | Covers |
|----------------------|--------|
| `test_vm_lifecycle.py` | Snapshot/load workflow, duplicate name, nonexistent VM removal, full lifecycle with mocks |
| `test_network_workflow.py` | Duplicate network, nonexistent remove/inspect, missing CIDR, invalid CIDR, custom gateway, --no-nat, subprocess bridge setup/teardown |
| `test_host_init_reset.py` | Host init workflow, reset rollback, privilege escalation |
| `test_cli_smoke.py` | All command groups load without errors, help output, unknown commands |
