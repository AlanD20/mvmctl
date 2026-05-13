# Running System Tests

## One-time system setup

```bash
# 1. System packages
sudo apt-get install -y iproute2 iptables procps kmod sudo genisoimage \
  cloud-image-utils qemu-utils e2fsprogs squashfs-tools util-linux tar \
  openssh-client coreutils

# 2. KVM check
egrep -c '(vmx|svm)' /proc/cpuinfo   # must be > 0
sudo usermod -aG kvm $USER

# 3. mvm group (required for privileged ops)
sudo usermod -aG mvm $USER
# Log out and back in for group changes to take effect

# 4. Clone, init, host init
git clone <repo-url> && cd mvmctl
uv sync --group dev
sudo "$(which uv)" run mvm host init
```

Note: Guestfs is disabled by default (`settings.guestfs_enabled = False`).
The loop-mount provisioner (mvm-provision) is used for all rootfs operations.

## Run

### Per-file execution — REQUIRED

System tests are **stateful**. Running `pytest tests/system/` as a single batch
causes cross-file state pollution (VMs, bridges, iptables) and undefined results.
Each file MUST be run individually. See [ADR 0007](/docs/adr/0007-system-test-execution-strategy.md).

### Using the convenience script

```bash
# Run system tests via the unified test runner (default source mode):
uv run python scripts/run_tests.py --system

# Build a onefile binary first, then run (faster, sudo handled internally):
uv run python scripts/run_tests.py --system --build

# Run a specific domain:
uv run python scripts/run_tests.py --system --domain vm

# Re-run only previously failed tests:
uv run python scripts/run_tests.py --system --failed-only

# Use a specific binary (skips build):
uv run python scripts/run_tests.py --system --bin /path/to/mvm

# Run all three levels (unit, integration, system):
uv run python scripts/run_tests.py
```

The script:
1. Seeds the asset mirror at `~/.cache/mvm-asset-mirror/` if empty (avoids re-downloading on repeated runs)
2. Builds `dist/mvm` if `--build` is passed
3. Runs each test file one by one with `-n 0` (serial)
4. Saves results to `.reports/system-test-results-latest.txt` (used by `--failed-only`)

### Manual per-file

```bash
# Test files are organized per-domain under tests/system/{domain}/
MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror MVM_BINARY=dist/mvm \
  uv run pytest tests/system/network/test_network.py -n 0
```

## Troubleshooting

| Problem | Fix |
|---------|-----|
| "iptables" errors | Run `sudo "$(which uv)" run mvm host init` |
| VM creation hangs | Build with `--build` flag (needs onefile binary with embedded services) |
| "mvm group" errors | Verify `groups` includes `mvm`; log out and back in |
| "Permission denied" for /dev/loop* | Add user to `disk` group: `sudo usermod -aG disk $USER` |
| Address already in use | Clean up stale VMs: `dist/mvm vm ls --json` then `dist/mvm vm rm <name> --force` |
| mvm-provision not registered | `cache clean --force` removes service binaries; re-run init or `python scripts/build_services.py` |

## AI Agent Instructions

### Execution strategy

MUST run **per-file**, never as a single batch. Use the script:

```bash
uv run python scripts/run_tests.py --system
uv run python scripts/run_tests.py --system --build       # with built binary
uv run python scripts/run_tests.py --system --failed-only  # re-run failures
```

The `prepare_system_env` session fixture automatically pulls missing assets
per-file. The `MVM_BINARY` env var selects the binary (default: `uv run mvm`).
The `MVM_ASSET_MIRROR` env var enables cached asset copies.

### Without vs with built binary

| Mode | Binary | Sudo handling | Build needed |
|------|--------|---------------|--------------|
| Default | `uv run mvm` (from source) | Requires `sg mvm` | No |
| `--build` | `dist/mvm` (35 MB onefile) | Automatic via `run_cmd()` | Yes |

### What works without passwordless sudo

`bin`, `config`, `cache`, `keys`, `logs`, `init`, `invariants`

### What needs mvm group (passwordless sudo)

Network, VM creation, host ops, kernel build, image ops, volume ops — the
application handles sudo internally, tests NEVER call sudo directly.

### Known limitations

| Issue | Cause | Workaround |
|-------|-------|------------|
| Cross-file state pollution | Real system state (VMs, bridges, iptables) | Run per-file, never batch |
| Console relay spawn failure | Nuitka onefile temp dir cleanup | Use `--build` or ensure symlink in bin cache |
| Kernel build >10 min | Full kernel compilation | Use `--keep-build-dir` for incremental |
| Guestfs appliance missing | `mvm cache init` needs sudo | Run `sudo dist/mvm cache init` once |
