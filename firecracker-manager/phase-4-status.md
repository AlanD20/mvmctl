# Phase 4 — Status

> Source: `python-cli-phase-4.md`
> Last updated: 2026-03-23

---

## §1 `key` — SSH key management (revised)

| Requirement | Status | Notes |
|---|---|---|
| Key cache stores **named public keys only** | ✅ | Private keys never cached |
| `key ls` — columns: Name, Fingerprint, Algorithm, Comment, Date Added | ✅ | |
| `key add <name> <path-to-public-key>` — fail if name exists (use `--overwrite`); print fingerprint on success | ✅ | |
| `key create <name>` — generate ED25519; private to `--output` (default `~/.ssh/<name>`); public to `--output/<name>.pub`; register in cache; print private key path + fingerprint | ✅ | |
| `key remove <name>` — remove from cache registry and delete `.pub` from cache; NOT private key on disk; warn if used by VM; alias `key rm` | ✅ | |
| `--ssh-key` resolution: try name first, then path; fail clearly listing available names if neither resolves | ✅ | |

---

## §2 `network` — Network management (revised and clarified)

| Requirement | Status | Notes |
|---|---|---|
| Network = named Linux bridge with CIDR, gateway IP, NAT rules | ✅ | |
| Networks are persistent — survive reboots; remain until explicitly removed | ✅ | |
| Default network created during `configure` / `host init` using `10.10.0.0/24` | ✅ | |
| Default network named `default` | ✅ | |
| Default network never auto-torn-down when last VM is removed | ✅ | |
| Default network can be removed with `network remove default` | ✅ | |
| `network ls` — columns: Name, Bridge Device, CIDR, Gateway, VM Count, NAT Enabled; default marked | ✅ | |
| `network create <name>` — `--cidr <cidr>` required; validate + reject overlapping CIDRs | ✅ | |
| `network create --gateway <ip>` — default: first usable host in CIDR | ✅ | |
| `network create --no-nat` | ✅ | |
| `network remove <name>` — fail if VMs attached; tear down bridge + iptables rules; alias `network rm` | ✅ | |
| `network inspect <name>` — CIDR, gateway, bridge, NAT, iptables rules, attached VMs + IPs, creation date | ✅ | |
| IP allocation: lease table at `networks/<name>/leases.json` | ✅ | |
| Auto-allocate unused IP from CIDR (excluding gateway) when no `--ip` | ✅ | |
| `--ip` validates IP is within CIDR and not in use, then reserves in lease table | ✅ | |
| IP assignment via Firecracker boot args (`ip=` kernel parameter) + cloud-init `network-config` | ✅ | |
| No DHCP — all assignment is static at VM creation time | ✅ | |

---

## §3 `vm` — Revised behaviours

### Graceful shutdown in `vm remove`

| Requirement | Status | Notes |
|---|---|---|
| Read PID from `<cache-root>/vms/<name>/firecracker.pid` | ✅ | |
| If `enable_api_socket: true` in VM config: send `SendCtrlAltDel` via Firecracker HTTP API; wait up to 5s | ✅ | |
| If still running: send SIGTERM; wait 1s | ✅ | |
| If still running: send SIGKILL | ✅ | |
| Clean up: remove PID file, socket file, tap device, release IP from lease table | ✅ | |
| **DO NOT** run `ssh-keygen -R <ip>`; use `-o StrictHostKeyChecking=no` for SSH | ✅ | Fixed in previous session: `ssh-keygen -R` call removed from `cli/vm.py` |
| Delete VM's cache directory `<cache-root>/vms/<name>/` | ✅ | |
| Last VM on a network does NOT tear down the network (networks persist independently) | ✅ | |

### No pause / resume

| Requirement | Status | Notes |
|---|---|---|
| `vm pause` and `vm resume` NOT implemented as subcommands | ✅ | Removed per Phase 4 §3 |
| Typing `vm pause` or `vm resume` produces a message (not unrecognised command error) | ✅ | |

### No `vm setup`

| Requirement | Status | Notes |
|---|---|---|
| No `vm setup` subcommand | ✅ | Does not exist |

### MAC address

| Requirement | Status | Notes |
|---|---|---|
| No `--mac`: generate random locally-administered MAC (prefix `02:`) | ✅ | |
| `--mac` passed: validate as well-formed unicast MAC before use | ✅ | |
| No deterministic generation from VM name | ✅ | Random only |

### User data (`--user-data`)

| Requirement | Status | Notes |
|---|---|---|
| Validate file exists and is readable | ✅ | |
| Warn (not fail) if file does not begin with `#cloud-config` or valid MIME boundary | ✅ | |
| `--user-data` + `--ssh-key` together: inject SSH key into user-data in memory without modifying original file | ✅ | |
| If `ssh_authorized_keys` section exists: append key for each user and root | ✅ | |
| If no `ssh_authorized_keys` section: add minimal block | ✅ | |

---

## §4 `host` — Revised and extended

### `host prune`

| Requirement | Status | Notes |
|---|---|---|
| `host prune` — remove ALL networking config added by this tool | ✅ | |
| Print clear warning listing everything to be removed | ✅ | |
| Ask for explicit confirmation unless `--force` | ✅ | |
| Refuse to run if any VM is currently running (check live PID files); list running VMs | ✅ | |
| After removing networking: update host state snapshot to reflect restored state | ✅ | |
| Does NOT remove VM cache files, images, kernels, or binaries | ✅ | |
| Does NOT revert IP forwarding sysctl (only `host restore` does that) | ✅ | |
| `host prune` replaces `asset cache clear` for networking cleanup | ✅ | |

### `host init` idempotency

| Requirement | Status | Notes |
|---|---|---|
| Running `host init` on already-initialised host: no changes, clean exit with confirmation message | ✅ | |
| Fully idempotent — running twice is safe, no duplicate changes, no warnings | ✅ | |

---

## §5 `help` — Consistent help behaviour

| Requirement | Status | Notes |
|---|---|---|
| `<binary> <command> --help` shows help | ✅ | Typer built-in |
| `<binary> <command> -h` shows help | ✅ | Typer built-in |
| `<binary> <command> help` shows help | ✅ | Implemented with `ctx.get_help()` pattern |
| `<binary> help <command>` shows help | ✅ | Typer built-in help subcommand |
| `key help` → same as `key --help` | ✅ | |
| `network help` → same as `network --help` | ✅ | |
| `vm help` → same as `vm --help` | ✅ | |
| `key add help` → same as `key add --help` | ✅ | Fixed in current session: `context_settings + ctx.get_help()` pattern |
| `network create help` → same as `network create --help` | ✅ | Fixed in current session |
| `network remove help` → same as `network remove --help` | ✅ | Fixed in current session |
| `network inspect help` → same as `network inspect --help` | ✅ | Fixed in current session |
| `key create help`, `key remove help`, `key inspect help` → proper help | ✅ | Fixed in current session |
| Tests covering `help` subcommand for key and network commands | ✅ | `test_cli_key.py` + `test_cli_network.py` |

---

## §6 API documentation (`API.md`)

| Requirement | Status | Notes |
|---|---|---|
| `firecracker-manager/docs/API.md` exists | ✅ | |
| Introduction — CLI maps 1:1 to `api/` functions | ✅ | |
| Installation — `pip install firecracker-manager` or `-e .` | ✅ | |
| Module overview table — vms, network, assets, keys, host | ✅ | |
| Data models — all public models with field names, types, descriptions | ✅ | |
| Error handling — exception hierarchy + catching example | ✅ | |
| Function reference — every public function: signature, description, parameters, return, exceptions | ✅ | |
| End-to-end example — complete runnable script (host.init → fetch binary → fetch kernel → fetch image → key add → network create → vm create → vm list → vm remove) | ✅ | |

---

## §7 Distribution — binary and public package

### `pyproject.toml`

| Requirement | Status | Notes |
|---|---|---|
| `[project] name` — single source of truth | ✅ | |
| `version = "0.1.0"` | ✅ | |
| `description = "A CLI for managing Firecracker microVMs"` | ✅ | |
| `readme = "README.md"` | ✅ | |
| `license = { text = "MIT" }` | ✅ | Added in previous session |
| `requires-python = ">=3.13"` | ✅ | |
| All runtime dependencies with minimum version pins | ✅ | |
| `[project.scripts] fcm = "firecracker_manager.cli.main:app"` | ✅ | |
| `[project.optional-dependencies] dev` — pytest, pytest-cov, pytest-mock, ruff, mypy, pyinstaller | ✅ | Added in previous session |
| `[build-system] hatchling` | ✅ | |

### pipx and uvx compatibility

| Requirement | Status | Notes |
|---|---|---|
| `pipx install firecracker-manager` works | ✅ | Entry point declared correctly |
| `uvx firecracker-manager` works | ✅ | Entry point declared correctly |
| No import-time side effects requiring root or specific system state | ✅ | All system interactions are lazy |

### Binary build (PyInstaller)

| Requirement | Status | Notes |
|---|---|---|
| `release.yml` produces binary via `pyinstaller --onefile` | ✅ | |
| Binary named `<project-name>` from `pyproject.toml` | ✅ | |
| All runtime dependencies bundled | ✅ | `--onefile` mode |
| No Python installation required at runtime | ✅ | |
| Built on `ubuntu-22.04` AND `ubuntu-24.04` as separate artifacts | ✅ | Matrix strategy in `release.yml` |
| Binary build command documented in `README.md` "Building from source" | ✅ | |
| Binary build command documented in `CONTRIBUTING.md` "Build System" section | ✅ | Added in current session |

### `README.md` Installation section

| Requirement | Status | Notes |
|---|---|---|
| "Download the binary" — GitHub releases, by Ubuntu version | ✅ | |
| `pip install <project-name>` | ✅ | |
| `pipx install <project-name>` (recommended) | ✅ | |
| `uvx <project-name> --help` | ✅ | |
| "Build from source" link | ✅ | |

### `CONTRIBUTING.md` Build System section

| Requirement | Status | Notes |
|---|---|---|
| "Project Name (Single Source of Truth)" — pyproject.toml propagation explanation | ✅ | Added in current session |
| "Building the Standalone Binary" — PyInstaller commands | ✅ | Added in current session |
| Ubuntu 22.04 vs 24.04 glibc note | ✅ | Added in current session |

---

## Final Verification

| Check | Result |
|---|---|
| `ruff check src/` | ✅ All checks passed |
| `mypy src/` | ✅ Success: no issues found in 42 source files |
| `pytest` | ✅ 551 passed |
| Coverage | ✅ 82.85% (≥ 80% required) |
| `ssh-keygen -R` call removed | ✅ |
| `help` subcommand at every level | ✅ |
| All phase status files created | ✅ |

---

**Overall Phase 4 Status: ✅ COMPLETE**
