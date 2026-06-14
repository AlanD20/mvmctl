# How Agents Write System Tests

## Purpose

This document is a **specification**, not a tutorial. It defines:

- The **scenario catalog** — every CLI command and flag that must be tested
- The **verification depth** required for each scenario
- The **rules** agents must follow when writing or modifying test code

Agents do NOT invent test scenarios from scratch. They translate the
scenario catalog (section 4) into working Python test code.

The authoritative source for what-is-tested vs what-is-not is the
**COVERAGE_MATRIX.md** at `tests/system/COVERAGE_MATRIX.md`.

**If a CLI command or flag is NOT listed in the scenario catalog (section 4),
the catalog is incomplete.** The agent MUST:
1. Flag it as a coverage gap
2. Propose a new row in the catalog with the appropriate min depth level
3. Get user approval before writing tests

No test code is written for an uncataloged command without explicit approval.

System tests are written in **Python** and run the Go-built `mvm` binary as a
black-box subprocess. They live in `tests/system/`.

---

## Table of Contents

1. [Verification Depth Standard](#1-verification-depth-standard)
2. [Skip Discipline](#2-skip-discipline)
3. [Structured Error Assertions](#3-structured-error-assertions)
4. [Per-Domain Scenario Catalogs](#4-per-domain-scenario-catalogs)
5. [System Build & Test Execution](#5-system-build--test-execution)
6. [Test Writing Rules](#6-test-writing-rules)
7. [Available Fixtures](#7-available-fixtures)
8. [What Agents Must NOT Do](#8-what-agents-must-not-do)
9. [Before Submitting, Self-Check](#9-before-submitting-self-check)

---

## 1. Verification Depth Standard

Every test must achieve a minimum verification depth. The old "cheapest
resource wins" rule produced shallow coverage. Replace it with this
four-level standard:

| Level | Name | What It Verifies | When Required |
|-------|------|------------------|---------------|
| L0 | Returncode | Process exit code | Never alone — only valid as part of a failure assertion |
| L1 | Output | stdout/stderr content | Commands with human-readable output (table, tree, help) |
| L2 | Structured JSON | Parsed JSON field correctness | ALL `--json` commands — verify specific fields and types |
| L3 | System State (Option C) | Deepest practical verification: filesystem, process table, iptables, SQLite DB, guest-visible devices | ALL infrastructure operations (bridge, iptables, volume disk, binary file, SSH connectivity) |

### Minimum Requirement

```
L0 → NEVER in isolation (only for negative assertions)
L1 → Only for human-readable commands (help, table output)
L2 → MINIMUM for ALL commands
L3 → REQUIRED for: network, volume, host, ssh, console, logs, cache, config
```

### Examples

**L2 (minimum) — `vm ls --json`:**
```python
result = _run_mvm(mvm_binary, "vm", "ls", "--json")
data = json.loads(result.stdout)
assert isinstance(data, list)
assert len(data) > 0
assert "name" in data[0]
assert data[0]["status"] == "running"
```

**L3 (Option C) — network bridge verification:**
```python
# Verify the bridge actually exists on the system
bridge_result = subprocess.run(
    ["ip", "link", "show", bridge_name],
    capture_output=True, text=True, check=False,
)
assert bridge_result.returncode == 0

# Verify firewall rules reference the bridge
nft_result = subprocess.run(
    ["sudo", "nft", "list", "chain", "ip", "filter", "MVM-FORWARD"],
    capture_output=True, text=True, check=False,
)
assert bridge_name in nft_result.stdout
```

**L3 (Option C) — log file verification:**
```python
# Don't just check vm["status"] == "running" — check the log file exists
vm_inspect = json.loads(
    _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json").stdout
)
log_path = Path(vm_inspect["vm_dir"]) / "firecracker.log"
assert log_path.exists(), f"Firecracker log not found at {log_path}"
assert log_path.stat().st_size > 0, "Firecracker log is empty"
```

---

## 2. Skip Discipline

`pytest.skip()` is a **confession of incomplete coverage**, not a valid
test strategy. Every skip erodes confidence.

### Rules

1. **Every skip must have a reason comment** explaining:
   - What condition triggers the skip
   - What would need to change for the test to run unconditionally

   ```python
   # Skip-reason: Requires network access to remote registry.
   # When running in air-gapped environments without MVM_ASSET_MIRROR,
   # remote listing is unavailable.
   if result.returncode != 0:
       pytest.skip("Remote listing not available (network?)")
   ```

2. **Every skip must have a fallback assertion where possible.**
   If the primary verification path is unavailable (e.g., no VM with an IP),
   try a simpler assertion instead of skipping entirely:

   ```python
   # Preferred: fallback assertion
   ip = vm_info.get("ipv4", "")
   if not ip:
       # No IP yet — at minimum verify the VM record exists
       assert vm_info["status"] in ("running", "created")
       pytest.skip("VM has no IP address (DHCP may be slow)")
   ```

3. **CI enforces a skip ratio gate.** If a test file has >10% skip rate
   (skips / (passes + failures + skips) > 0.10), CI fails.
   The gate is implemented in `scripts/check_skip_ratio.py` and is
   automatically invoked by `scripts/run_tests.py --system`.

4. **Skips for missing external dependencies** (qemu-img, mkfs.ext4, zstd)
   are acceptable but MUST use `shutil.which()` rather than `try/except`:

   ```python
   qemu_img = shutil.which("qemu-img")
   if not qemu_img:
       pytest.skip("qemu-img not available on this system")
   ```

---

## 3. Structured Error Assertions

Error message assertions must match against a canonical structure, not a
substring guessing game.

### Forbidden Pattern

```python
# ❌ FORBIDDEN — substring guessing
assert any(s in combined for s in ["not found", "no such", "invalid"])
```

This pattern passes if ANY of the strings appears ANYWHERE in the output,
including in unrelated parts of stdout. It can match a different error than
intended.

### Required Patterns

```python
# ✅ Preferred: match against a specific error code or phrase
assert "not found" in result.stderr.lower()
# or
assert f"network '{net_name}' not found" in result.stderr.lower()

# ✅ Acceptable: match against a specific condition (not a list)
assert result.returncode != 0
assert "not found" in result.stderr.lower()
```

### When to Use Which

| Condition | Assertion Pattern |
|-----------|-------------------|
| CLI rejects invalid input | `assert result.returncode != 0` + match specific error phrase in stderr |
| Resource not found | `assert result.returncode != 0` + `"not found" in result.stderr.lower()` |
| Operation blocked (in-use) | `assert result.returncode != 0` + `"in use" in result.stderr.lower()` or `"attached" in result.stderr.lower()` |
| Idempotent operation | `assert result.returncode == 0` (skip-only, no output check needed) |

---

## 4. Per-Domain Scenario Catalogs

Each domain below lists every CLI command and its required scenarios.
Every scenario includes:

- **Command**: The CLI invocation
- **Required scenarios**: What must be tested
- **Min depth**: Minimum verification level (L0-L3)
- **Skip ok?**: Can it skip under specific conditions?
- **Destructive?**: Does it modify system state?

### 4.1 Root CLI (`mvm`)

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `--version` | Returns non-empty version string with digits | L1 | No | No |
| `--verbose` | Does not break config get; output shows additional info | L1 | No | No |
| `--debug` | stderr contains DEBUG-level output | L1 | No | No |
| `help` | Shows Usage: for root, subcommand, subsubcommand | L1 | No | No |
| `help <nonexistent>` | Returns non-zero exit | L1 | No | No |
| `version` (command) | Shows version | L1 | No | No |
| `completion bash\|zsh\|fish` | Generates shell completion script | L1 | No | No |

### 4.2 `mvm init`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `init --non-interactive --skip-host` | Returns 0, shows ready/setup message | L1 | No | No |
| `init --non-interactive` (no skip-host) | Returns non-zero, mentions sudo/privilege | L1 | No | No |
| `init` idempotent | Two runs both succeed | L1 | No | No |
| `init --non-interactive --skip-host --skip-network` | Returns 0, mentions success | L1 | No | No |

### 4.3 `mvm config`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `config get <category> <key>` | Returns value for known key | L2 (parse JSON-like output) | No | No |
| `config get <category>` (no key) | Lists all keys in category | L1 | No | No |
| `config get <category> <nonexistent>` | Returns guidance, exit 0 | L1 | No | No |
| `config set <category> <key> <value>` | Round-trips with get | L2 | No | Yes |
| `config set <invalid category>` | Returns non-zero | L1 | No | No |
| `config set <invalid value>` (string for int) | Returns non-zero | L1 | No | No |
| `config reset <category> <key>` | Clears override, get shows default | L2 | No | Yes |
| `config reset <category>` | Clears all overrides in category | L2 | No | Yes |
| `config reset --all` | Clears all overrides globally | L2 | No | Yes |
| `config list` | Shows at least [defaults.vm] section | L1 | No | No |

### 4.4 `mvm network`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `network create <name> --subnet <cidr>` | Bridge exists, IP assigned, firewall rules present | L3 | No | Yes |
| `network create` without --subnet | Returns non-zero, missing option error | L1 | No | No |
| `network create --non-interactive` | Creates network without confirmation prompt | L2 (ls --json) | No | Yes |
| `network create --subnet <invalid>` | Returns non-zero | L1 | No | No |
| `network create --subnet /32` | Returns non-zero (too small) | L1 | No | No |
| `network create` duplicate name | Returns non-zero | L1 | No | No |
| `network create` duplicate subnet | Returns non-zero | L1 | No | No |
| `network create --no-nat` | Bridge exists, no MASQUERADE rule | L3 | No | Yes |
| `network create --ipv4-gateway <gw>` | Bridge uses custom gateway | L3 | No | Yes |
| `network create --nat-gateways <iface>` | NAT uses specified interface | L3 | No | Yes |
| `network ls` | Table output contains network name | L1 | No | No |
| `network ls --json` | JSON list with id, name, subnet | L2 | No | No |
| `network inspect <name>` | Shows name in output | L1 | No | No |
| `network inspect <name> --json` | JSON with name, subnet, bridge, vm_count | L2 | No | No |
| `network inspect <name> --tree` | Tree characters in output | L1 | No | No |
| `network rm <name>` | Bridge gone, firewall rules cleaned | L3 | No | Yes |
| `network rm <nonexistent>` | Returns non-zero | L1 | No | No |
| `network rm <name> --force` | Removes even with VMs | L3 | No | Yes |
| `network rm <name1> <name2>` | Both removed | L3 | No | Yes |
| `network default <name>` | Sets as default, is_default=true in ls | L2 | No | Yes |
| `network default <nonexistent>` | Returns non-zero | L1 | No | No |
| `network sync` | Bridge and firewall rules recreated | L3 | No | No |
| `network sync --json` | Returns dict with per-network stats | L2 | No | No |
| `network sync <specific>` | Specific network synced | L3 | No | No |
| `network sync` idempotent | Two syncs produce same rule count | L3 | No | No |
| Sync after bridge deletion | Sync recreates deleted bridge | L3 | No | Yes |

### 4.5 `mvm vm`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `vm create <name> --image <img> --network <net>` | VM is running, status=running, PID exists | L2 | No | Yes |
| `vm create` with custom `--vcpus` | vcpu_count matches | L2 | No | Yes |
| `vm create` with custom `--mem` | mem_size_mib matches | L2 | No | Yes |
| `vm create` with custom `--disk-size` | disk_size_mib matches | L2 | No | Yes |
| `vm create` with custom `--kernel` | kernel_id matches | L2 | No | Yes |
| `vm create` with custom `--boot-args` | boot_args contains the string | L2 | No | Yes |
| `vm create` with custom `--ip` | ipv4 matches | L2 | No | Yes |
| `vm create` with custom `--mac` | mac matches | L2 | No | Yes |
| `vm create` with `--no-console` | enable_console=false | L2 | No | Yes |
| `vm create` with `--no-pci` | pci_enabled=false | L2 | No | Yes |
| `vm create` with `--enable-logging` | Log file exists on disk | L3 | No | Yes |
| `vm create` with `--no-enable-logging` | VM runs (succeeds) | L2 | No | Yes |
| `vm create` with `--enable-metrics` | Metrics file exists on disk | L3 | No | Yes |
| `vm create` with `--no-enable-metrics` | VM runs (succeeds) | L2 | No | Yes |
| `vm create` with `--user-data` | User-data is injected into guest seed dir | L3 (guest SSH) | If SSH unavailable: L2 | Yes |
| `vm create` with `--cloud-init-mode <mode>` | cloud_init_mode matches in inspect | L2 | No | Yes |
| `vm create` with `--nocloud-net-port <port>` | nocloud_net_port matches in inspect | L2 | No | Yes |
| `vm create` with `--count 3` | All 3 VMs created | L2 | No | Yes |
| `vm create --atomic --count 2` | Both VMs created | L2 | No | Yes |
| `vm create --count with --ip` (validation) | Returns non-zero | L1 | No | No |
| `vm create --count with --mac` (validation) | Returns non-zero | L1 | No | No |
| `vm create --count negative` | Returns non-zero | L1 | No | No |
| `vm create` with `--volume <name>` | Volume status=attached | L2 | No | Yes |
| `vm create` with `--volume <id-prefix>` | Volume status=attached, resolved by prefix | L2 | No | Yes |
| `vm create` with `--ssh-key <key>` | Key injected, SSH reachable | L3 | If SSH unavailable: L2 | Yes |
| `vm ls` | Table output | L1 | No | No |
| `vm ls --json` | JSON list with id, name, status, ipv4, pid, vcpu_count, mem_size_mib, disk_size_mib | L2 | No | No |
| `vm ls --json` empty | Valid empty list | L2 | No | No |
| `vm ps` | Running VMs listed | L1 | No | No |
| `vm inspect <name>` | Name in output | L1 | No | No |
| `vm inspect <name> --json` | JSON with all expected keys | L2 | No | No |
| `vm inspect <name> --tree` | Tree characters in output | L1 | No | No |
| `vm inspect` by IP | Resolves by IP | L2 | No | No |
| `vm start <name>` | Status=stopped → running | L2 | No | Yes |
| `vm start` on already-running | Idempotent (exit 0) | L1 | No | Yes |
| `vm stop <name>` | Status=running → stopped | L2 | No | Yes |
| `vm stop <name> --force` | Force stop works, no orphan process | L3 | No | Yes |
| `vm stop` on already-stopped | Idempotent (exit 0) | L1 | No | Yes |
| `vm stop` by IP | Resolves by IP | L2 | No | Yes |
| `vm stop` graceful (no --force) | Status=stopped | L2 | No | Yes |
| `vm pause <name>` | Status=running → paused | L2 | No | Yes |
| `vm pause` on stopped | Returns non-zero | L1 | No | No |
| `vm resume <name>` | Status=paused → running | L2 | No | Yes |
| `vm resume` on running | Idempotent (exit 0) | L1 | No | Yes |
| `vm reboot <name>` | Status=running after reboot | L2 | No | Yes |
| `vm reboot <name> --force` | Status=running after force reboot | L2 | No | Yes |
| `vm rm <name>` | VM removed from listing | L2 | No | Yes |
| `vm rm <name> --force` | Force remove, resources cleaned | L2 | No | Yes |
| `vm rm <name1> <name2>` | Both removed | L2 | No | Yes |
| `vm rm <nonexistent>` | Returns non-zero | L1 | No | No |
| `vm snapshot <name> <mem> <state>` | Files created, VM still running | L3 | No | Yes |
| `vm snapshot` on stopped | Returns non-zero | L1 | No | No |
| `vm load <name> <mem> <state>` | VM loaded | L2 | No | Yes |
| `vm export <name>` | JSON config | L2 | No | No |
| `vm export <name> <file>` | File written | L2 | No | No |
| `vm import <config>` | VM created from config | L2 | No | Yes |
| `vm attach-volume <vm> <vol>` | Volume status=attached | L2 | No | Yes |
| `vm attach-volume` on running | Returns non-zero | L1 | No | No |
| `vm attach-volume` nonexistent | Returns non-zero | L1 | No | No |
| `vm detach-volume <vm> <vol>` | Volume status=available | L2 | No | Yes |
| `vm detach-volume` on running | Returns non-zero | L1 | No | No |
| `vm detach-volume` nonexistent | Returns non-zero | L1 | No | No |
| `vm rm --force` with attached volume | Volume transitions to available | L2 | No | Yes |
| `vm rm --force` with crashed firecracker | VM removed, cleanup works | L2 | No | Yes |
| Stop/start cycle (3x fatigue) | Status=running after 3 cycles | L2 | No | Yes |
| Create-pause-remove | VM removed after pause | L2 | No | Yes |
| Crash recovery (kill firecracker PID) | vm stop succeeds, vm rm succeeds | L2 | No | Yes |
| Boot time within limits | Finished in <30s | L1 | No | Yes |
| No orphaned processes after stop | PID no longer alive | L3 | No | Yes |
| Volume persists across stop/start | Device visible in guest after restart | L3 | If SSH unavailable: L2 | Yes |
| Volume attach + mkfs + mount in guest | File writable on volume | L3 | No | Yes |
| DNS resolution inside VM | getent hosts google.com resolves | L3 | If DNS unavailable: skip with /etc/resolv.conf diagnostic | No |
| Config precedence: CLI flag overrides config default | vcpus=2 with --vcpus overrides config=4 | L2 | No | Yes |
| `--enable-logging`: log file exists on disk | Log file in vm_dir non-empty | L3 | No | Yes |
| `vm create` with `--nested-virt` | cpu-config in Firecracker JSON, boot args contain kvm-intel.nested=1 or kvm-amd.nested=1, pci_enabled=true | L3 | If host doesn't support nested virt | Yes |
| `vm create` with `--no-nested-virt` | No cpu-config in Firecracker JSON, no nested boot args | L3 | No | Yes |
| `vm create` with `--cpu-template <path>` | cpu-config contains user's merged template plus nested_virt base kvm_capabilities if --nested-virt also set | L3 | If file doesn't exist | Yes |
| `vm create` fresh_env full pipeline (noble, 6.19.9 kernel, nested-virt) | Full pipeline: image pull, kernel pull, VM create with all flags, SSH, in-guest KVM verify | L3 | If kernel build fails | Yes |
| Inside-guest-host-status | Run mvm inside a nested VM -- verify host status | L3 | If host lacks nested virt | No |
| Inside-guest-config-roundtrip | Config set/get/reset inside isolated guest | L2 | If host lacks nested virt | Yes |
| Inside-guest-key-lifecycle | Key create/ls/rm inside isolated guest | L2 | If host lacks nested virt | Yes |
| Inside-guest-volume-lifecycle | Volume create/resize/rm inside isolated guest | L2 | If host lacks nested virt | Yes |
| Inside-guest-network | Network create/ls/rm inside isolated guest | L3 | If guest lacks iptables | Yes |
| Inside-guest-nested-vm | Create VM inside nested guest (triple nesting) | L3 | If /dev/kvm not in guest | Yes |

### 4.6 `mvm volume`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `volume create <name> <size>` | size_bytes matches, status=available | L2 | No | Yes |
| `volume create` with `--format qcow2` | format=qcow2 | L2 | No | Yes |
| `volume create` with `--format raw` | format=raw | L2 | No | Yes |
| `volume create` invalid size | Returns non-zero | L1 | No | No |
| `volume create` invalid format | Returns non-zero | L1 | No | No |
| `volume create` duplicate name | Returns non-zero | L1 | No | No |
| `volume create` negative size | Returns non-zero | L1 | No | No |
| `volume create` zero size | Returns non-zero | L1 | No | No |
| `volume ls` | Contains volume name | L1 | No | No |
| `volume ls --json` | JSON list with entries | L2 | No | No |
| `volume ls` empty | Returns 0 | L1 | No | No |
| `volume inspect <name>` | Name in output | L1 | No | No |
| `volume inspect <name> --json` | name, size_bytes, format, status, path present | L2 | No | No |
| `volume inspect` nonexistent | Returns non-zero | L1 | No | No |
| `volume rm <name>` | Remove from listing | L2 | No | Yes |
| `volume rm <name> --force` | Remove even attached | L2 | No | Yes |
| `volume rm <nonexistent>` | Returns non-zero | L1 | No | No |
| `volume rm <name1> <name2>` | Both removed | L2 | No | Yes |
| `volume rm` partial (one exists, one not) | Non-zero, existing still removed | L2 | No | Yes |
| `volume resize <name> <size>` | size_bytes matches new size | L2 | No | Yes |
| `volume resize` nonexistent | Returns non-zero | L1 | No | No |
| `volume resize` shrink | Documents shrink behavior | L2 | No | Yes |
| Invariants: available→attached→available | vm_id transitions correctly, path exists | L3 | No | Yes |
| Cross-VM attach rejection | Volume attached to VM-A rejects attach to VM-B | L2 | No | Yes |
| Volume rm with running VM | Returns non-zero | L1 | No | No |
| Volume rm --force with running VM | Removed | L2 | No | Yes |
| Volume resize with running VM | Succeeds | L2 | No | Yes |
| Volume hotplug: attach to running | Device visible inside guest | L3 | No | Yes |
| Volume hotunplug: detach from running | Device gone from guest | L3 | No | Yes |

### 4.7 `mvm key`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `key create <name> --algorithm ed25519` | Listed in ls --json | L2 | No | Yes |
| `key create <name> --algorithm rsa` | Listed | L2 | No | Yes |
| `key create <name> --algorithm ecdsa` | Listed | L2 | No | Yes |
| `key create` with `--bits` | Listed with custom bits | L2 | No | Yes |
| `key create` with `--comment` | comment field in inspect | L2 | No | Yes |
| `key create` with `--out` | Key files on disk | L3 | No | Yes |
| `key create` with `--default` | is_default=true | L2 | No | Yes |
| `key create` with `--force` (overwrite) | Overwrites existing | L2 | No | Yes |
| `key add <name> <pubkey>` | Listed | L2 | No | Yes |
| `key add` duplicate | Returns non-zero | L1 | No | No |
| `key add` with `--force` | Overwrites existing | L2 | No | Yes |
| `key ls` | Contains key name | L1 | No | No |
| `key ls --json` | JSON list | L2 | No | No |
| `key inspect <name>` | Name in output | L1 | No | No |
| `key inspect <name> --json` | name field present | L2 | No | No |
| `key inspect <name> --tree` | Tree characters | L1 | No | No |
| `key rm <name>` | Removed from listing | L2 | No | Yes |
| `key rm <nonexistent>` | Returns non-zero | L1 | No | No |
| `key rm <name> --force` | Remove even if in use | L2 | No | Yes |
| `key rm <name1> <name2>` | Both removed | L2 | No | Yes |
| `key default <name>` | is_default=true | L2 | No | Yes |
| `key default --clear` | is_default=false | L2 | No | Yes |
| `key export <name> --out <dir>` | Files on disk | L3 | No | No |
| `key export <name> --out <dir>` (overwrite) | Returns non-zero | L1 | No | No |
| `key export --force` | Overwrites | L3 | No | No |
| Multiple defaults | Both keys appear in VM's ssh_keys | L2 | No | Yes |
| Delete default key when only key | Succeeds | L2 | No | Yes |

### 4.8 `mvm image`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `image pull <type>:<version>` | Pull succeeds, listed | L2 | If network unavailable | Yes |
| `image pull --force` | Re-downloads | L1 | If network unavailable | Yes |
| `image pull --default` | Sets as default | L2 | If network unavailable | Yes |
| `image pull --skip-optimization` | Succeeds | L1 | If network unavailable | Yes |
| `image pull --type <type>` | Pulls specified type | L2 | If network unavailable | Yes |
| `image pull --version <ver>` | Pulls specified version | L2 | If network unavailable | Yes |
| `image pull nonexistent` | Returns non-zero | L1 | No | No |
| `image pull` with `--disable-detector` | Succeeds | L1 | If network unavailable | Yes |
| `image pull` with `--arch` | Pulls image for specified architecture | L1 | If network unavailable | Yes |
| `image pull` with `--no-cache` | Pulls image bypassing local cache | L1 | If network unavailable | Yes |
| `image ls` | Table output | L1 | No | No |
| `image ls --json` | JSON list with is_present, type | L2 | No | No |
| `image ls --remote` | Lists remote images | L1 | If network unavailable | No |
| `image inspect <prefix>` | Succeeds | L1 | If no cached images | No |
| `image inspect <prefix> --json` | id, name present | L2 | If no cached images | No |
| `image inspect <prefix> --tree` | Tree characters | L1 | If no cached images | No |
| `image default <prefix>` | Sets as default | L2 | No | Yes |
| `image default <nonexistent>` | Returns non-zero | L1 | No | No |
| `image rm <prefix>` | Removed | L2 | No | Yes |
| `image rm` (rm transitions -- force path) | Handled normally | L2 | No | Yes |
| `image warm <id>` | Warmed/ready message | L1 | No | Yes |
| `image warm --all` | Warms all | L1 | No | Yes |
| `image warm <nonexistent>` | Returns non-zero | L1 | No | No |
| `image import <name> <path>` | Imported image in listing | L2 | No | Yes |
| `image import` with `--format qcow2` | Imported | L2 | If qemu-img unavailable | Yes |
| `image import` with `--format raw` | Imported | L2 | No | Yes |
| `image import` with `--format tar-rootfs` | Imported | L2 | No | Yes |
| `image import` with `--root-partition` | Imported | L2 | If qemu-img unavailable | Yes |
| `image import` with `--force` | Overwrite | L2 | No | Yes |
| `image import` with `--default` | Set as default | L2 | No | Yes |
| `image import` with `--arch` | Imported | L2 | If qemu-img unavailable | Yes |
| `image import` without --format (auto-detect) | Imported | L2 | No | Yes |
| `image import` nonexistent path | Returns non-zero | L1 | No | No |
| Full end-to-end: import + VM create | VM runs from imported image | L2 | No | Yes |
| Default migrates on force re-pull | Default moves to new record | L2 | No | No |

### 4.9 `mvm kernel`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `kernel ls --json` | JSON list with id, name, version, type | L2 | No | No |
| `kernel ls` empty | Valid empty list | L2 | No | No |
| `kernel ls --remote` | Lists remote kernels available for download | L1 | If network unavailable | No |
| `kernel pull --type firecracker` | Pull succeeds | L2 | If network unavailable | Yes |
| `kernel pull --type official` | Pull or build | L1 | If network/build tools unavailable | Yes |
| `kernel pull --arch` | Pulls kernel for specified architecture | L1 | If network unavailable | Yes |
| `kernel pull --jobs` | Controls parallel build jobs | L1 | If kernel_build tools unavailable | Yes |
| `kernel pull --keep-build-dir` | Preserves build directory after build | L1 | If kernel_build tools unavailable | Yes |
| `kernel pull --clean-build` | Forces clean rebuild from source | L1 | If kernel_build tools unavailable | Yes |
| `kernel inspect <prefix>` | Name or prefix in output | L1 | If no kernel | No |
| `kernel inspect <prefix> --json` | Fields present | L2 | If no kernel | No |
| `kernel inspect <prefix> --tree` | Tree characters | L1 | If no kernel | No |
| `kernel default <id>` | Sets as default | L2 | No | Yes |
| `kernel rm <id>` | Removed | L2 | No | Yes |
| `kernel rm <nonexistent>` | Returns non-zero | L1 | No | No |
| `kernel import <name> <path>` | Imported, listed | L2 | No | Yes |

### 4.10 `mvm bin`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `bin ls --json` | JSON list with version, id, is_present | L2 | No | No |
| `bin ls --json` empty cache | Valid empty list | L2 | No | No |
| `bin ls --remote` | List returns data | L1 | If network unavailable | No |
| `bin ls --remote --limit N` | Respects limit | L1 | If network unavailable | No |
| `bin pull <version>` | Binary appears in listing | L2 | If network unavailable | Yes |
| `bin pull --force` | Re-downloads | L1 | If network unavailable | Yes |
| `bin pull --default` | Sets as default | L2 | If network unavailable | Yes |
| `bin pull nonexistent` | Returns non-zero | L1 | No | No |
| `bin rm <id>` | Removed from listing, file gone | L3 | No | Yes |
| `bin rm --version <ver>` | Removed by version | L2 | No | Yes |
| `bin rm <nonexistent>` | Returns non-zero | L1 | No | No |
| `bin default <id>` | Sets as default | L2 | No | Yes |
| `bin default <nonexistent>` | Returns non-zero | L1 | No | No |
| Service symlinks survive cache clean → cache init | Symlinks recreated | L3 | No | No |

### 4.11 `mvm ssh`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `ssh <vm> --cmd <cmd>` | Command executes, returns stdout | L3 | No | No |
| `ssh <vm> -u <user> --cmd <cmd>` | Specifies user | L3 | No | No |
| `ssh <vm> --cmd exit` (connectivity) | Returns 0 | L1 | No | No |
| `ssh <vm> --key <path>` (file path) | SSH connection uses specified private key file | L3 | No | No |

### 4.12 `mvm console`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `console <vm> --state` | Returns state info | L1 | No | No |
| `console <nonexistent> --state` | Returns non-zero | L1 | No | No |

### 4.13 `mvm logs`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `logs <vm>` | Returns log content | L1 | No | No |
| `logs <vm> --os` | Returns OS log | L1 | No | No |
| `logs <vm> --lines N` | Returns N lines | L1 | No | No |
| `logs <ip>` | Resolves by IP | L1 | No | No |
| `logs <vm> --follow` | Streams live log output | L1 | No | No |

### 4.14 `mvm host`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `host status` | Table output with Check/Status/Detail columns | L1 | No | No |
| `host status --json` | JSON with kvm_accessible, required_binaries, ip_forward, etc. | L3 | No | No |
| `host info` | Human-readable tree output | L1 | If not initialized | No |
| `host info --json` | JSON with cpu, memory, storage, limits, capacity, virtualization, etc. | L3 | If not initialized | No |
| `host info --refresh` | Re-detects and shows updated data | L1 | If not initialized | No |
| `host info --refresh --json` | JSON with refreshed detected_at field | L3 | If not initialized | No |
| `host init` | Probe failure: when system has critical issues | L1 | No | No |
| `host clean --force` | Exits 0 | L1 | If no sudo access | Yes |
| `host clean` blocked by running VM | Returns non-zero, mentions running | L1 | No | No |
| `host reset --force` | Exits 0 | L1 | If no sudo access | Yes |
| `host reset` blocked by running VM | Returns non-zero, mentions running | L1 | No | No |

### 4.15 `mvm cache`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `cache init` | Succeeds | L1 | No | Yes |
| `cache init` idempotent | Two runs succeed | L1 | No | Yes |
| `cache prune` (no resource) | Returns non-zero | L1 | No | No |
| `cache prune <resource> --force` | Prunes resource | L2 | No | Yes |
| `cache prune --all --dry-run` | Shows DRY RUN, doesn't remove | L1 | No | No |
| `cache clean --dry-run` | Shows what would be removed | L1 | No | No |
| `cache clean --force` | Cleans all | L1 | No | Yes |

### 4.16 CLI Consistency

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `vm --help` lists subcommands | All expected subcommands present | L1 | No | No |
| `image --help` lists subcommands | All expected subcommands present | L1 | No | No |
| `vm rm <nonexistent>` (actionable error) | Error message >20 chars, contains helpful phrase | L1 | No | No |
| `--help` structure (all command groups) | Contains Usage:, Commands:, --help reference | L1 | No | No |
| `help vm` ≡ `vm --help` | Both show create subcommand | L1 | No | No |

### 4.17 `mvm cp`

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `cp <src> <dst>` (host→VM, single file) | File exists on VM after copy, verified via `mvm ssh --cmd "test -f"` | L3 | If SSH unavailable | Yes |
| `cp <src> <dst>` (host→VM, directory) | Directory exists on VM with contents, verified via SSH | L3 | If SSH unavailable | Yes |
| `cp <src> <dst>` (VM→host, single file) | File exists on host filesystem at destination with expected content | L3 | If SSH unavailable | Yes |
| `cp <src> <dst>` (VM→host, directory) | Directory exists on host with contents | L3 | If SSH unavailable | Yes |
| `cp <src> <dst>` nonexistent source | Non-zero exit, error mentions "not found" | L1 | No | No |
| `cp <src> <dst>` with `--force` | Overwrites existing destination file | L3 | If SSH unavailable | Yes |
| `cp <src> <dst>` without `--force` when dest exists | Non-zero exit, error mentions "force" or "exists" | L1 | No | No |
| `cp vm1:/src vm2:/dst` (VM→VM) | File exists on VM2 after copy | L3 | If SSH unavailable on either VM | Yes |

#### 4.17.2 Multi-source copy

| Command | Required Scenarios | Min Depth | Skip OK? | Destructive? |
|---------|-------------------|-----------|----------|--------------|
| `cp <src1> <src2> <vm>:/dst/` (two files) | Both files exist on VM with correct content via SSH | L3 | If SSH unavailable | Yes |
| `cp <src> <dir> <vm>:/dst/` (file + directory) | File and directory (with nested content) exist on VM via SSH | L3 | If SSH unavailable | Yes |
| `cp <src> <vm>:/dst/` (single source, multi-source code path) | Backward compatibility — file transferred correctly via SSH | L3 | If SSH unavailable | Yes |
| `cp <src1> <src2> <local-dest>` (rejects non-VM dest) | Non-zero exit, error mentions multi-source requires VM destination | L1 | No | No |

---

## 5. System Build & Test Execution

### 5.1 Building the Go Binary

```bash
# Build the mvm binary
go build -o ~/.local/bin/mvm ./cmd/mvm

# Verify
~/.local/bin/mvm --version
```

The binary MUST be built to `~/.local/bin/mvm` (can be referenced via
`MVM_BINARY` env var in tests). Set `MVM_ASSET_MIRROR` before running:

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
```

### 5.2 Running Tests

```bash
# Run all system tests (from project root or as single script)
MVM_BINARY=~/.local/bin/mvm python scripts/run_tests.py --system

# Run a specific domain
MVM_BINARY=~/.local/bin/mvm python scripts/run_tests.py --system --domain vm

# Run a single test file
MVM_BINARY=~/.local/bin/mvm python scripts/run_tests.py --system --test tests/system/vm/test_vm_lifecycle.py

# Run a single test class
sg mvm -c 'MVM_BINARY=/usr/bin/mvm python -m pytest tests/system/vm/test_vm_lifecycle.py::TestVMLifecycle -xvs'
```

### 5.3 CI Commands (Go code quality — run before submitting)

```bash
gofmt -l .          # must return empty
golines --max-len=120 --list-files .
go mod tidy
go vet ./...
go build -trimpath ./...
go test ./...        # unit tests (not system tests)
```

---

## 6. Test Writing Rules

### 6.1 File Structure

```python
"""Docstring describing what this file covers."""

from __future__ import annotations

import json
import uuid
from typing import Any

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet

pytestmark = [pytest.mark.system, pytest.mark.domain_<category>]


class TestCategoryName:
    """Tests for <category>."""

    def test_example(
        self, mvm_binary, unique_vm_name, unique_key_name
    ) -> None:
        """Docstring describing the scenario."""
        # ... setup, action, assertions, cleanup ...
```

### 6.2 Resource Cost Discipline

The resource hierarchy still matters for **test execution time**:

```
VM create          →  30-120s  ← EXPENSIVE
Network create     →  5-10s   ← MODERATE
Volume create      →  1-3s    ← CHEAP
Key create         →  0.5-1s  ← CHEAPEST
CLI invocation     →  0.1s    ← FREE
```

But the old "use the cheapest resource" rule is **replaced** by:

**Use the cheapest resource that can achieve the required verification depth.**

If a test needs to verify that `--enable-logging` creates a log file, it
needs a real VM (expensive). The verification depth (L3: check log file
exists on disk) requires it. Do NOT test `--enable-logging` with only
`vm["status"] == "running"` (L2) just because it's cheaper.

### 6.3 Module-Scoped Fixtures for Read-Only Tests

When a class has multiple tests that read the same resource without
modifying it, use a module-scoped fixture:

```python
@pytest.fixture(scope="module")
def shared_network(mvm_binary) -> Generator[str, None, None]:
    name = f"sys-shared-{uuid.uuid4().hex[:6]}"
    _run_mvm(mvm_binary, "network", "create", name,
             "--subnet", _unique_subnet(name), "--non-interactive")
    try:
        yield name
    finally:
        _run_mvm(mvm_binary, "network", "rm", name, check=False)


class TestNetworkReadOnly:
    def test_inspect_json(self, mvm_binary, shared_network):
        ...
    def test_ls_after_create(self, mvm_binary, shared_network):
        ...
```

DO NOT create a new resource per test when the resource is not being
modified.

### 6.4 Destructive Tests Must Be Last

Tests that remove or destroy resources must appear at the END of the
file, after all read-only and state-inspection tests. This ensures
that if the test file is interrupted mid-run, the read-only tests
have already passed.

### 6.5 Modifying Existing Test Files

Tests MAY be modified when the change:
- Deepens verification (L1 → L2, L2 → L3)
- Removes unnecessary `pytest.skip()` calls
- Consolidates duplicated resource creation
- Fixes a brittle assertion
- Adds coverage for a previously untested flag

Tests MUST NOT be modified to:
- Silence a failing assertion without fixing the root cause
- Reduce verification depth
- Remove cleanup code

### 6.6 Rationale Comments

Every test must have a `# Rationale:` comment explaining:
1. Why this resource level is needed
2. What real bug or behavior it protects against

```python
def test_create_with_enable_logging(self, ...):
    # Rationale: Verifies that --enable-logging creates the firecracker.log
    # file on disk. A regression where logging silently fails (file not created)
    # would not be caught by L2 (status=running) checks.
```

---

## 7. Available Fixtures

| Fixture | Scope | Creates | Best For |
|---------|-------|---------|----------|
| `mvm_binary` | session | Nothing | All tests — provides the mvm binary path (reads `MVM_BINARY` env or defaults to `mvm`) |
| `unique_vm_name` | function | Nothing | Tests that create their own VM |
| `unique_network_name` | function | Nothing | Tests that create their own network |
| `unique_key_name` | function | Nothing | Tests that create their own key |
| `created_vm` | function | VM + key + network | Tests needing a running VM with SSH |
| `minimal_vm` | function | VM + network (no SSH) | Tests needing a running VM, no SSH |
| `module_vm` | module | 1 VM shared | Read-only tests across a module |
| `module_network` | module | 1 network shared | Read-only network tests across a module |
| `created_network` | function | 1 network | Tests needing a network |
| `created_key` | function | 1 key | Tests needing a key |
| `tmp_path` | function | Temp directory | File operations |
| `timing_targets` | session | Nothing | SSH wait timeouts per image |
| `system_cache_dir` | session | Nothing | Cache directory path |

**Helper functions available from `tests/system/conftest.py`:**

| Function | Purpose |
|----------|---------|
| `_run_mvm(binary, *args, check=True)` | Execute `mvm` subcommand, returns `subprocess.CompletedProcess` |
| `_unique_subnet(network_name)` | Generate a unique subnet based on network name |
| `_ensure_kernel(binary)` | Ensure at least one kernel is pulled |
| `_ensure_image(binary, image)` | Ensure an image is pulled |
| `_ensure_binary(binary)` | Ensure a Firecracker binary is available |
| `ensure_vm_deps(binary)` | Ensure all VM dependencies (kernel, image, binary) |
| `_print_prep(msg)` | Print a preparation message |
| `_cleanup_stale_processes()` | Clean up any stale Firecracker processes |

---

## 8. What Agents Must NOT Do

- ❌ Decide what to test — the scenario catalog decides
- ❌ Research test scenarios from the internet — the scenario catalog is the source
- ❌ Use `assert any(s in combined for s in [...])` for error assertions
- ❌ Add `pytest.skip()` without a skip-reason comment
- ❌ Use L0 (returncode-only) verification for success paths
- ❌ Settle for L1/L2 when L3 is practical
- ❌ Add `requires_kvm` to tests that don't create VMs
- ❌ Add `slow` marker to tests that run in <5 seconds
- ❌ Run the full system test suite as validation — CI handles execution.
  The self-check below is for the developer's local verification only.
- ❌ Forget to update `tests/system/COVERAGE_MATRIX.md` when adding or modifying tests — both the per-domain entries AND the summary statistics must be updated before submitting.
- ❌ Import from `mvmctl.*` — tests are black-box subprocess only

---

## 9. Before Submitting, Self-Check

```
[ ] Did I check the scenario catalog for this domain?
[ ] Is every CLI flag in the command covered by a scenario?
[ ] Did I update `tests/system/COVERAGE_MATRIX.md` with the new entries AND recalculate summary statistics?
[ ] Did I achieve the minimum verification depth (L2 or L3)?
[ ] Is every pytest.skip() justified with a skip-reason comment?
[ ] Did I avoid the forbidden assertion pattern?
[ ] Does every destructive test appear AFTER read-only tests?
[ ] Did I consolidate duplicated resource creation?
[ ] Did I add a # Rationale: comment?
[ ] Does `ruff check` and `ruff format` pass on the test file? (System tests are Python)
[ ] Does the test pass? (run with `python scripts/run_tests.py --system --test <file>`)
```
