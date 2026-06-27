# How Agents Write System Tests

> **See also:** [ADR-0012](../adr/0012-unified-test-architecture.md) for the architectural decisions and rationale behind this document.
>
> **See also:** [HOW_AGENTS_WRITE_UNIT_TESTS.md](HOW_AGENTS_WRITE_UNIT_TESTS.md) for L0/L1 test patterns (table-driven tests, in-memory repos, FakeRunner, in-memory SQLite).

## Table of Contents

- [Purpose](#purpose)
- [The Three-Level Architecture](#the-three-level-architecture)
- [L0 vs L1: The Distinction](#l0-vs-l1-the-distinction)
- [Test Boundary Decision Tree](#test-boundary-decision-tree)
- [Quick-Reference Table](#quick-reference-table)
- [How L2 Tests Run: The Runner VM](#how-l2-tests-run-the-runner-vm)
- [How to Write L2 Tests](#how-to-write-l2-tests)
- [How to Write L0/L1 Tests](#how-to-write-l0l1-tests)
- [The "Zero Blindside" Principle](#the-zero-blindside-principle)
- [Migration Phases](#migration-phases)
- [What Agents Must Not Do](#what-agents-must-not-do)
- [Before Submitting, Self-Check](#before-submitting-self-check)

---

## Purpose

This document is a **specification and how-to reference** for writing and classifying tests at all three levels. It defines:

- **L0** (pure function Go tests) — when to write them, what they prove
- **L1** (hermetic Go tests) — when to use in-memory SQLite, temp dirs, `FakeRunner`
- **L2** (runner VM Python tests) — how to structure tests against the real binary inside a disposable Firecracker VM
- **The boundary** between levels — the decision tree every scenario must pass through
- **The runner VM model** — how the test environment is provisioned, scoped, and destroyed

Agents do NOT invent test architecture. They translate the scenario catalog (the quick-reference table in this document) into working test code at the correct level.

---

## The Three-Level Architecture

```
  ┌─────────────────────────────────────────────┐
  │  L2 (Python, runner VM)                      │
  │  GROUND TRUTH — every user-facing feature    │
  │  Real subprocess, real infra, real binary    │
  │                                              │
  │  ┌─────────────────────────────────────────┐ │
  │  │  L1 (Go, hermetic)                      │ │
  │  │  FAST PRE-FILTER — catches bugs earlier  │ │
  │  │  Real SQLite, real files, FakeRunner    │ │
  │  │  NOT a replacement for L2 coverage      │ │
  │  │                                          │ │
  │  │  ┌───────────────────────────────────┐   │ │
  │  │  │  L0 (Go, pure function)           │   │ │
  │  │  │  FASTEST PRE-FILTER               │   │ │
  │  │  │  Table-driven, no I/O, μs         │   │ │
  │  │  │  NOT a replacement for L2 coverage │   │ │
  │  │  └───────────────────────────────────┘   │ │
  │  └─────────────────────────────────────────┘ │
  └─────────────────────────────────────────────┘

Critical: L2 is the ground truth. L0 and L1 catch bugs earlier during
`go test ./...` but NEVER replace L2 coverage. A feature is not considered
tested until it has an L2 test. Passing L0/L1 alone means nothing — the
feature could be completely broken at the subprocess level.
```

### Level 0: Pure Function Tests (Go)

**Scope:** A single function, no I/O, no dependencies. Input → output.

```go
func TestParseDiskSize(t *testing.T) {
    tests := map[string]struct {
        input   string
        want    int64
        wantErr string
    }{
        "gigabytes": {input: "1G", want: 1073741824},
        "megabytes": {input: "512M", want: 536870912},
        "invalid":   {input: "abc", wantErr: "unable to parse"},
    }
    for name, tc := range tests {
        t.Run(name, func(t *testing.T) {
            got, err := ParseDiskSize(tc.input)
            if tc.wantErr != "" {
                require.Error(t, err)
                assert.Contains(t, err.Error(), tc.wantErr)
                return
            }
            require.NoError(t, err)
            if diff := cmp.Diff(tc.want, got); diff != "" {
                t.Errorf("(-want +got):\n%s", diff)
            }
        })
    }
}
```

**What lives here:** `ParseDiskSize`, `ComputeBridgeName`, `ValidateCIDR`, `ToInt`, state machine transitions (`Controller.Pause` on Stopped VM → error), validators. Any test that can be written as a table with no `t.TempDir()`, no repo, no `FakeRunner`.

### Level 1: Hermetic Integration Tests (Go)

**Scope:** Tests that use real I/O in a controlled environment — real SQLite (file-based, in t.TempDir()), real filesystem (`t.TempDir()`), `FakeRunner` for subprocess calls that can't run in CI. No network, no KVM, no sudo.

```go
func TestVMList_JSONOutput(t *testing.T) {
    repo := testutil.NewVMRepo()     // in-memory map-backed repo (zero arg)
    ctx := context.Background()
    repo.Upsert(ctx, &model.VMItem{ID: "vm-1", Name: "test-vm", Status: "running"})

    items, _ := repo.List(ctx)

    assert.Len(t, items, 1)
    assert.Equal(t, "test-vm", items[0].Name)
}
```

**Key principle:** No mocking of the code under test. `FakeRunner` is used only for subprocess calls that physically cannot run in CI (`firecracker`, `ssh-keygen`). Everything else — DB, filesystem, config — is real, redirected to temp directories.

**What we add to make L1 work:**
- `testutil.NewInMemoryDB(t)` — opens a file-based SQLite in `t.TempDir()`, runs all migrations
- `testutil.NewVMRepo()`, `testutil.NewNetworkRepo()`, etc. — thread-safe in-memory repository implementations (backed by map, not SQLite)

### Level 2: Runner VM E2E Tests (Python)

**Scope:** Real binary, real subprocess, real infrastructure inside a disposable Firecracker VM with nested KVM. This includes:

- **All CRUD operations** (`key create`, `volume create`, `network create`, `vm create`) — the subprocess call IS the operation.
- **All destructive operations** (`cache clean --force`, `host reset`, `volume rm --force`, `vm rm --force`) — inside the runner VM, destruction is safe. The test verifies the removal worked. The VM absorbs the destruction.
- **The workflow engine** (`internal/workflow/`) — any CLI command that triggers a workflow (e.g., `vm create --count N`, multi-step init) is verified at L2.

---

## L0 vs L1: The Distinction

Both levels share Go, `go test`, and file locations (`internal/*/*_test.go`). The distinction is **what the test touches**:

| Dimension | L0 | L1 |
|-----------|----|----|
| **I/O** | None | Temp dirs, in-memory SQLite, `FakeRunner` |
| **Dependencies** | None (standalone function) | Repos, services, config files |
| **Test pattern** | `map[string]struct{...}` table | `t.TempDir()` + seed DB + readback |
| **What it proves** | Function correctness | Component wiring + I/O correctness |
| **Example** | `ParseDiskSize("1G") == 1073741824` | `repo.Upsert(vm) → repo.Get(id) → cmp.Diff(vm, got)` |
| **Mocking** | None | `FakeRunner` for subprocess calls only |
| **Speed** | μs | ms |

**The boundary:** If the function reads a file, queries a DB, or calls a subprocess → **L1**. If it takes args and returns values with no side effects → **L0**.

---

## Test Boundary Decision Tree

Every test scenario MUST be classified using this tree before writing:

```
Q1: Does this test scenario involve ANY subprocess call
    (ssh-keygen, qemu-img, firecracker, ip, nft, kill, truncate,
     git, HTTP download, ssh, tar, sudo, groupadd, etc.)?
    YES ────► L2: Python E2E in runner VM
    NO
      │
      ▼
Q2: Does the function under test have I/O side effects
    (read/write a file, query a DB, call a subprocess)?
    YES ────► L1: Go hermetic integration test
    NO
      │
      ▼
    L0: Go pure function test
```

**Examples through the tree:**

| Scenario | Subprocess? | I/O Side Effects? | Verdict |
|----------|-------------|-------------------|---------|
| `key create --algorithm ed25519` | Yes (`ssh-keygen`) | — | **L2** |
| `network create --subnet 10.0.0.0/24` | Yes (`ip link`, `nft`) | — | **L2** |
| `vm create --vcpu 4` | Yes (`firecracker`) | — | **L2** |
| `config set defaults.vm vcpu_count 4` | No | Yes (file write) | **L1** |
| `network ls --json` (verify output fields) | No | Yes (DB query) | **L1** |
| `--help` output structure | No | No | **L0** |
| `ParseDiskSize("1G")` | No | No | **L0** |
| `Controller.Pause(StoppedVM)` → error | No | No | **L0** |

---

## Quick-Reference Table

Every CLI command and flag that a user can invoke must be tested. This table classifies every scenario into L0, L1, or L2.

### Root CLI, Init, Config, CLI Edge Cases

| Scenario | Verdict | Why |
|----------|---------|-----|
| `--version` output | L1 | Cobra output, no subprocess |
| `--verbose` / `--debug` output | L1 | Cobra output, no subprocess |
| `help` (root, subcommand, subsubcommand) | L1 | Cobra output |
| `help <nonexistent>` → error | L1 | Cobra handler |
| `version` command output | L1 | Cobra output |
| `completion bash\|zsh\|fish` | L1 | Cobra output |
|---|---|---|
| `init --non-interactive --skip-host` | L2 | Makes real filesystem changes (cache dir, DB) |
| `init` idempotent | L2 | Real filesystem state |
| `init` with sudo requirement | L2 | Real privilege check |
|---|---|---|
| `config get <cat> <key>` (existing) | L1 | File I/O in temp dir |
| `config get <cat>` (no key) | L1 | File I/O in temp dir |
| `config get <cat> <nonexistent>` | L1 | File I/O |
| `config set <cat> <key> <value>` | L1 | File I/O in temp dir |
| `config set` invalid category | L1 | Error handling |
| `config set` invalid value type | L1 | Error handling |
| `config reset <cat> <key>` | L1 | File I/O |
| `config reset <cat>` | L1 | File I/O |
| `config reset --all` | L1 | File I/O |
| `config ls` | L1 | Cobra output |

### `mvm key`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `key create --algorithm ed25519` | **L2** | Real `ssh-keygen` subprocess |
| `key create --algorithm rsa` | **L2** | Real `ssh-keygen` subprocess |
| `key create --algorithm ecdsa` | **L2** | Real `ssh-keygen` subprocess |
| `key create --bits` | **L2** | Real `ssh-keygen` subprocess |
| `key create --comment` | **L2** | Real `ssh-keygen` subprocess |
| `key create --out` | **L2** | Real file I/O |
| `key create --default` | **L2** | Real `ssh-keygen` subprocess |
| `key create --force` (overwrite) | **L2** | Real `ssh-keygen` subprocess |
| `key import <name> <path>` | L1 | Temp dir file copy + DB storage |
| `key import` duplicate | L1 | DB query |
| `key import --force` | L1 | DB update |
| `key ls` / `key ls --json` | L1 | DB query + JSON |
| `key inspect` / `--json` / `--tree` | L1 | DB query |
| `key rm <name>` | L1 | DB update |
| `key rm nonexistent` | L1 | Error handling |
| `key rm --force` | L1 | DB update |
| `key rm <name1> <name2>` | L1 | DB updates |
| `key default` / `key default --clear` | L1 | DB update |
| `key export --out` | **L2** | Real file I/O |
| `key export` overwrite / `--force` | **L2** | Real file I/O |
| Multiple defaults | L1 | DB query |
| Delete default key when only key | L1 | DB update |

### `mvm vm`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `vm create` basic | **L2** | Real Firecracker spawn |
| `vm create --vcpu` | **L2** | Real Firecracker spawn |
| `vm create --mem` | **L2** | Real Firecracker spawn |
| `vm create --disk-size` | **L2** | Real Firecracker spawn |
| `vm create --kernel` | **L2** | Real Firecracker spawn |
| `vm create --boot-args` | **L2** | Real Firecracker spawn |
| `vm create --ip` / `--mac` | **L2** | Real Firecracker spawn |
| `vm create --console` | **L2** | Real Firecracker spawn |
| `vm create --no-pci` | **L2** | Real Firecracker spawn |
| `vm create --enable-logging` / `--enable-metrics` | **L2** | Verify files on disk |
| `vm create --cloud-init-mode <mode>` | **L2** | Real Firecracker + cloud-init |
| `vm create --nocloud-net-port` | **L2** | Real Firecracker + network |
| `vm create --count N` | **L2** | Multiple Firecracker spawns |
| `vm create --volume` | **L2** | Real volume attach |
| `vm create --ssh-key` | **L2** | Real SSH key injection |
| `vm create --nested-virt` | **L2** | Real KVM passthrough |
| `vm create --cpu-template` | **L2** | Real Firecracker config |
| `vm create --cloudinit-config` | **L2** | Real cloud-init |
| `vm ls` / `vm ls --json` | L1 | DB query + JSON |
| `vm ls --json` empty | L1 | DB query |
| `vm ps` / `vm ps --json` | **L2** | Real process table |
| `vm inspect` / `--json` / `--tree` | L1 | DB query + JSON |
| `vm inspect` by IP | L1 | DB query + JSON |
| `vm start / stop / reboot / pause / resume` | **L2** | Real process signaling |
| `vm stop --force` | **L2** | Real process kill |
| `vm rm` / `vm rm --force` | **L2** | Real process + file cleanup |
| `vm rm <name1> <name2>` | **L2** | Multiple real process cleanup |
| `vm export / import` | **L2** | Real file I/O |
| Volume persists stop/start | **L2** | Real VM state machine |
| Volume mountable in guest | **L2** | Real SSH + filesystem |
| Crash recovery (kill firecracker PID) | **L2** | Real process management |

### `mvm snapshot`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `snapshot create <vm>` (basic) | **L2** | Real Firecracker snapshot API |
| `snapshot create <vm> --name` / `--pause` | **L2** | Real Firecracker snapshot API |
| `snapshot ls` / `snapshot ls --json` | L1 | DB query + JSON |
| `snapshot inspect` / `--json` / `--tree` | L1 | DB query + JSON |
| `snapshot restore <vm>` (basic) | **L2** | Real Firecracker snapshot restore |
| `snapshot restore <vm> --network` / `--resume` / `--count` | **L2** | Real Firecracker snapshot restore |
| `snapshot rm <name>` / `snapshot rm --force` | **L2** | Real file deletion |
| `snapshot rm <name1> <name2>` | **L2** | Real file deletion |
| `snapshot rm` nonexistent | L1 | Error handling |

### `mvm network`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `network create --subnet <cidr>` | **L2** | Real bridge + firewall |
| `network create` without `--subnet` | L1 | Cobra validation |
| `network create --non-interactive` | **L2** | Real bridge |
| `network create` invalid CIDR | L1 | Cobra validation |
| `network create` duplicate name/subnet | **L2** | Real DB constraint |
| `network create --no-nat` | **L2** | Verify no MASQUERADE rule |
| `network create --ipv4-gateway` | **L2** | Real bridge IP config |
| `network create --nat-gateways` | **L2** | Real NAT config |
| `network ls` / `network ls --json` | L1 | DB query + JSON |
| `network inspect` / `--json` / `--tree` | L1 | DB query + JSON |
| `network rm` / `network rm --force` | **L2** | Real bridge + firewall cleanup |
| `network rm <nonexistent>` | L1 | Error handling |
| `network rm <name1> <name2>` | **L2** | Multiple bridge cleanup |
| `network default` / `network default <nonexistent>` | L1 | DB update |
| `network sync` / `network sync --json` | **L2** | Real bridge + firewall recreation |
| Sync after bridge deletion | **L2** | Real bridge deletion + recovery |

### `mvm volume`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `volume create <name> <size>` | **L2** | Real file creation (truncate/qemu-img) |
| `volume create --format qcow2` | **L2** | Real qemu-img |
| `volume create --format raw` | **L2** | Real file creation |
| `volume create <invalid>` | L1 | Cobra validation |
| `volume create` duplicate name | L1 | DB constraint |
| `volume create --read-only` | **L2** | Real file permissions |
| `volume ls` / `volume ls --json` | L1 | DB query + JSON |
| `volume ls` empty | L1 | DB query |
| `volume inspect` / `--json` | L1 | DB query + JSON |
| `volume inspect` nonexistent | L1 | Error handling |
| `volume rm` / `--force` | **L2** | Real file deletion |
| `volume rm` nonexistent | L1 | Error handling |
| `volume rm <name1> <name2>` | **L2** | Real file deletion |
| `volume rm` partial failure | **L2** | Real file deletion |
| `volume resize` | **L2** | Real file resize |
| `volume resize` shrink | **L2** | Real file resize |
| `volume attach` / `volume detach` | **L2** | Real Firecracker API + VM lifecycle |
| Invariants: available→attached→available | **L2** | Real VM attach/detach |
| Volume hotplug / hotunplug | **L2** | Real Firecracker PCI hotplug |

### `mvm image`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `image pull <type>:<version>` | **L2** | Real HTTP download |
| `image pull --force` / `--default` / `--skip-optimization` | **L2** | Real HTTP download |
| `image pull` nonexistent | L1 | Error handling |
| `image pull --disable-detector` / `--no-cache` | **L2** | Real HTTP download |
| `image ls` / `image ls --json` | L1 | DB query + JSON |
| `image ls --remote` | **L2** | Real network I/O |
| `image inspect` / `--json` / `--tree` | L1 | DB query + JSON |
| `image default` / `default <nonexistent>` | L1 | DB update |
| `image rm` | **L2** | Real file deletion |
| `image warm` / `warm --all` / `warm <nonexistent>` | **L2** | Real file I/O |
| `image import` (all format variants) | **L2** | Real filesystem + tools |
| `image import` nonexistent path | L1 | Error handling |
| Full import → VM-create end-to-end | **L2** | Real pipeline |
| Default migrates on force re-pull | **L2** | Real DB migration |

### `mvm kernel`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `kernel ls --json` | L1 | DB query + JSON |
| `kernel ls --remote` | **L2** | Real network I/O |
| `kernel pull --type firecracker` | **L2** | Real HTTP download |
| `kernel pull --type official` (build) | **L2** | Real build tools |
| `kernel pull --jobs` / `--keep-build-dir` / `--clean-build` | **L2** | Real build tools |
| `kernel inspect` / `--json` / `--tree` | L1 | DB query + JSON |
| `kernel default` | L1 | DB update |
| `kernel rm` / `kernel rm --force` | **L2** | Real file deletion |
| `kernel rm` nonexistent | L1 | Error handling |
| `kernel import` | **L2** | Real file copy |

### `mvm bin`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `bin ls --json` | L1 | DB query + JSON |
| `bin ls --remote` / `--remote --limit` | **L2** | Real network I/O |
| `bin pull <version>` | **L2** | Real HTTP download |
| `bin pull --force` / `--default` / `--git-ref` | **L2** | Real HTTP download / Docker |
| `bin pull` nonexistent | L1 | Error handling |
| `bin rm` / `bin rm --version` | **L2** | Real file deletion |
| `bin rm` nonexistent | L1 | Error handling |
| `bin default` / `bin default <nonexistent>` | L1 | DB update |
| Service symlinks survive cache clean | **L2** | Real filesystem symlinks |

### `mvm exec`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `exec <id> -- <cmd>` | **L2** | Real subprocess execution |
| `exec <id>` (interactive shell via stdin) | **L2** | Real subprocess |
| `exec` with `--port` | **L2** | Real port wiring |
| `exec` with `--timeout` | L1 | Flag parsing |
| `exec` with `--user` | **L2** | Real user context switch |
| `exec` nonexistent VM | L1 | Error handling |
| `exec` invalid port | L1 | Error handling |

### `mvm ssh`, `console`, `logs`, `cp`, `host`, `cache`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `ssh <vm> --cmd` | **L2** | Real SSH subprocess |
| `ssh <vm> -u <user> --cmd` | **L2** | Real SSH subprocess |
| `ssh <vm> --key <name\|path>` | **L2** | Real SSH subprocess |
| `ssh <vm> --timeout` | **L2** | Real SSH subprocess |
|---|---|---|
| `console <vm> --state` | **L2** | Real console relay process |
| `console <vm> --kill` | **L2** | Real process kill |
| `console` on stopped VM | **L2** | Real process check |
|---|---|---|
| `logs <vm>` / `--os` / `--lines` / `--follow` | **L2** | Real Firecracker output |
| `logs` by IP | **L2** | Real IP resolution |
|---|---|---|
| `cp` host↔VM (file, dir, multi-source) | **L2** | Real SSH + tar pipe |
| `cp` VM→host (file, dir) | **L2** | Real SSH + tar pipe |
| `cp` VM→VM | **L2** | Real SSH + tar pipe |
| `cp` nonexistent source | **L2** | Real error handling |
| `cp --force` / without `--force` | **L2** | Real overwrite behavior |
|---|---|---|
| `host info` / `--json` / `--refresh` | **L2** | Real system probe |
| `host status` / `--json` | **L2** | Real system probe |
| `host init` | **L2** | Real sudo + system modification |
| `host clean` / `host reset` (with safety checks) | **L2** | Real sudo + system modification |
|---|---|---|
| `cache init` | **L2** | Real DB + file creation |
| `cache prune` (all variants) | **L2** | Real file/db deletion |
| `cache prune` dry-run | L1 | DB query + JSON |
| `cache clean --dry-run` | L1 | DB query |
| `cache clean --force` | **L2** | Real DB + file deletion |

### `mvm env`

| Scenario | Verdict | Why |
|----------|---------|-----|
| `env apply <spec>` (basic) | **L2** | Real resource creation |
| `env apply` re-apply | **L2** | Idempotency |
| `env apply` nonexistent spec | L1 | Error handling |
| `env apply` invalid YAML | L1 | Error handling |
| `env ls` (empty, after apply, after destroy) | L1 | DB query |
| `env diff` (no diff, drifted, new, removed) | **L2** | Real state comparison |
| `env destroy` (by spec, by ID) | **L2** | Real resource destruction |
| `env destroy` nonexistent | L1 | Error handling |
| Full lifecycle apply→diff→destroy | **L2** | End-to-end |

### `mvm run` (internal service subprocesses)

| Scenario | Verdict | Why |
|----------|---------|-----|
| `run nocloudnet serve --help` | L1 | Cobra output |
| `run console relay --help` | L1 | Cobra output |
| `run provision --help` | L1 | Cobra output |

### Invariants, Cross-Resource, and Consistency

| Scenario | Verdict | Why |
|----------|---------|-----|
| JSON field consistency (all domains) | L1 | Parse JSON from seeded DB |
| Default uniqueness (cross-domain) | L1 | Cross-domain DB queries |
| Cross-resource: volume vm_id matches VM | L1 | DB query |
| Cross-resource: VM inspect shows attached volumes | L1 | DB query |
| Cross-resource: network rejects rm with active VMs | L1 | DB constraint check |
| CLI flag naming consistency (`--force` vs `--overwrite`) | L1 | Cobra help text parse |
| Help output structure | L1 | Cobra output |
| Error message: `vm rm nonexistent` → "not found" | L1 | Handler output |
| Error message: `--vcpu 0` | L1 | Cobra validation |
| Error message: `network create` without `--subnet` | L1 | Cobra validation |

---

## How L2 Tests Run: The Runner VM

### The Execution Substrate

Every L2 test runs **inside a disposable Firecracker VM** with nested KVM, not directly on the host:

```
Host (no mvm state at all)
│
└── Runner VM (custom base image mvm-test-runner:<version>)
    ├── /usr/local/bin/mvm          ← built binary, baked in
    ├── /tests/system/              ← test suite, baked in
    ├── /mnt/                       ← shared RO asset volume
    │
    └── pytest runs inside the VM
        ├── mvm vm create ...       ← creates REAL VMs (nested)
        ├── mvm network create ...  ← creates REAL bridges
        ├── mvm ssh ... --cmd ...   ← SSHes into those VMs
        └── mvm cp ...              ← copies files to those VMs
```

### Runner VM Lifecycle

```
1. Build mvm binary on host
2. Create runner VM from custom base image (mvm-test-runner:<version>)
3. Provision: mount shared asset volume, run mvm init, pull cache hits
4. Execute pytest <test-file> directly inside the VM
5. Destroy runner VM when done
```

Runner VMs are created fresh per test session, not restored from snapshots. See `docs/system-test-architecture.md` for the complete architecture.

### Scoping Model

The runner VM is created ONCE per test session (session-scoped pytest fixture). All tests in the session share the same VM. This is safe because:

- The VM is **ephemeral** — created at session start, destroyed at session end. No state leaks between sessions.
- Within a session, tests are ORDERED by dependency (create → read → update → delete), not by "safe vs destructive." A test that runs `cache clean --force` may break subsequent tests in the same session — but that's a test ordering bug, not a global state corruption. The NEXT session starts clean.
- For PARALLEL execution, each worker gets its OWN runner VM. No state sharing across workers.
- The `zzz_destructive/` pattern is replaced by explicit markers. The runner VM makes ordering a convenience concern, not a correctness requirement.

### Asset Pre-Seeding

The runner VM snapshot includes pre-cached assets to eliminate network-dependent skips:

- Alpine image (for fast VM creation)
- Ubuntu 24.04 image (for nested virt tests)
- Firecracker binary v1.15+ (for hotplug tests)
- Firecracker kernel v1.15 (default)
- Official kernel 7.0.11 with `kvm,nftables,tuntap,btrfs` features (pre-built, cached)

---

## How to Write L2 Tests

### Fixture Pattern

All L2 tests use a session-scoped `runner_vm` fixture that reads the VM name from the orchestrator:

```python
# tests/system/conftest.py
@pytest.fixture(scope="session")
def runner_vm() -> str:
    """Return the test VM name, set by orchestrator via MVM_TEST_VM env var."""
    return os.environ.get("MVM_TEST_VM", "t1-base")
```

The orchestrator (`scripts/run-system-tests.py`) creates and provisions the VM before
running pytest, and destroys it after. The fixture just provides the VM name to
tests so they can reference it when calling `_run_mvm(runner_vm, ...)`.

### Test Timeout Policy: CLI flags vs subprocess timeouts

**These are two distinct concerns — do not conflate them.**

**CLI `--timeout` flags** (e.g., `mvm exec --timeout`, `mvm ssh --timeout`):

These are **connect/probe timeouts** only (per ADR-0013). They control how long the CLI waits for the target to establish a connection. Default: **5s**, absolute max: **10s**. Once connected, the operation runs unbounded. See `CONTEXT.md` for the full taxonomy.

**Subprocess timeouts in test infrastructure** (`timeout=` parameter of `_run_mvm()` / `_guest_run()`):

These control how long `subprocess.run()` waits for the `mvm` command to complete. They are a **safety net**, not a performance floor. The defaults in `conftest.py` are generous:

```python
def _run_mvm(
    vm_name: str, *args: str, check: bool = True, timeout: int = 60
) -> subprocess.CompletedProcess[str]:
    """Run an mvm command. vm_name is ignored — already inside test VM."""
    cmd = ["mvm", *args]
    result = subprocess.run(
        cmd, capture_output=True, text=True,
        timeout=timeout + 30,  # 90s total by default
        env={**os.environ, "NO_COLOR": "1"},
    )
    if check and result.returncode != 0:
        raise RuntimeError(...)
    return result
```

Individual slow operations use explicit higher timeouts:

| Operation | Subprocess timeout |
|---|---|
| `vm create` | 180s |
| `vm rm --force` | 120s |
| `image pull`, `kernel pull`, `bin pull` | 300s |
| `network rm --force` | 60s |

**When writing a test:** Do NOT add explicit `timeout=` to `_run_mvm()` calls unless the operation is known to be slow (pulls, VM create, rm). Let the default handle fast operations. The speed-first principle applies to the CLI `--timeout` flag, not to test subprocess timeouts.

### Test File Pattern

```python
"""Volume CRUD system tests — runs inside runner VM."""

from __future__ import annotations

import json
import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_volume]


class TestVolumeLifecycle:
    def test_volume_create(self, runner_vm):
        """Create a volume and verify size via --json."""
        result = _run_mvm(
            runner_vm, "volume", "create", "myvol", "1G", "--json", timeout=30
        )
        data = json.loads(result.stdout)
        assert data["name"] == "myvol"
        assert data["size_bytes"] == 1073741824
        assert data["status"] == "available"
    
    def test_volume_rm(self, runner_vm):
        """Remove a volume and verify it is gone from listing."""
        _run_mvm(runner_vm, "volume", "create", "myvol2", "512M", "--json")
        _run_mvm(runner_vm, "volume", "rm", "myvol2", "--force")
        
        result = _run_mvm(runner_vm, "volume", "ls", "--json")
        volumes = json.loads(result.stdout)
        assert not any(v["name"] == "myvol2" for v in volumes)
```

### What L2 Tests Assert

L2 tests assert on **real side effects** — what the user sees and what the system does:

- CLI output (JSON fields, return codes, error messages)
- Files on disk (key files, volume files, log files, config files)
- System state (bridges, iptables rules, processes)
- Guest state (SSH connectivity, block devices, CPU features)

L2 tests do NOT mock anything. The binary is real. The subprocesses are real. The infrastructure is real (inside the VM).

---

## How to Write L0/L1 Tests

L0 and L1 tests follow the patterns in [HOW_AGENTS_WRITE_UNIT_TESTS.md](HOW_AGENTS_WRITE_UNIT_TESTS.md):

- **L0:** Table-driven tests with `map[string]struct{...}` and `t.Run()`. No I/O. Pure function assertions.
- **L1:** Tests that use `t.TempDir()` for filesystem I/O, `testutil.NewInMemoryDB()` for DB operations, and `testutil.FakeRunner` for subprocess call verification.

Key resources:
- `testutil.NewInMemoryDB(t)` — opens file-based SQLite in t.TempDir(), runs all migrations, returns a handle
- `testutil.NewVMRepo()` / `testutil.NewNetworkRepo()` (zero arguments) — in-memory repository implementations (backed by `map`, not SQLite)
- `testutil.FakeRunner` — records subprocess calls for assertion (use for argument verification, NOT for behavior testing)
- `t.TempDir()` — temp directory for any file I/O

---

## The "Zero Blindside" Principle

The purpose of the system test suite is **confidence for shipping.** When all tests pass, the team must be able to say: *"Every public-facing CLI command and flag works correctly. No regressions. Ship it."*

This requires:

1. **Exhaustive coverage:** Every CLI command, every flag, every output format, every error path is tested. The quick-reference table above is the catalog. If a flag or command is not listed, coverage is incomplete.

2. **Deterministic execution:** Tests never skip, never flake, never depend on network availability or host hardware quirks. Runner VMs are created fresh from a custom base image with a shared read-only asset volume — no network-dependent pulls needed. If a test cannot pass deterministically, fix the test — do not add `pytest.skip()`.

3. **Isolated state:** No test can see or corrupt another test's state. The runner VM is disposable — destroyed and recreated between sessions. Parallel workers each get their own VM.

4. **Includable destructive tests:** Operations like `cache clean --force`, `host reset`, `volume rm --force` are tested the SAME WAY as create/read operations. The test verifies the removal worked. The VM absorbs the destruction.

---

## Migration Phases

The migration from the current architecture to the target architecture is incremental:

### Phase 0: Scaffold L0/L1 Infrastructure (Weeks 1-2)

Add the building blocks for fast pre-filter tests:
- `testutil.NewInMemoryDB()` — opens file-based SQLite in t.TempDir(), runs all migrations
- `testutil.NewVMRepo()` / `testutil.NewNetworkRepo()` (zero arguments) — in-memory repository implementations
- 3-5 representative L1 tests as patterns

### Phase 1: Add L0/L1 Pre-Filter Tests (Weeks 3-6)

Write L1 tests for scenarios that need NO subprocess calls. These run alongside the existing Python tests (which remain the ground truth). No Python test is deleted.

- Config roundtrip tests using temp dirs
- JSON output format tests using seeded in-memory SQLite
- Error path tests using handler error verification
- CLI structure tests (help, completion, version)

### Phase 2: Runner VM Substrate (Weeks 7-9)

Build the runner VM infrastructure:
- `tests/system/conftest.py` with session-scoped `runner_vm` fixture
- `_run_mvm` helper for direct subprocess calls (replaces old `_guest_run` vsock proxy)
- Asset pre-seeding (images, kernels, binaries in the snapshot)
- Migrate 2-3 representative test files to runner VM

### Phase 3: Migrate All Python Tests to Runner VM (Weeks 10-12)

Every Python test runs inside the runner VM:
- Migrate remaining 24 test files
- Remove all per-domain conftest.py files
- Remove `zzz_destructive/`

### Phase 4: Clean Up (Week 13) ✅ COMPLETE

Dead code deleted and docs consolidated:
- Deleted `scripts/run_tests.py` domain-looping script (replaced by `pytest tests/system/`)
- `COVERAGE_MATRIX.md` maintained in `tests/system/` as accountability document
- Deleted the old per-domain Python test scripts (replaced by unified L0/L1/L2 test architecture)
- Updated all related docs to reflect the three-level (L0/L1/L2) architecture

---

## What Agents Must Not Do

- ❌ Write an L2 test that mocks or stubs a subprocess call. If you can't run the real subprocess, the test belongs at L0/L1.
- ❌ Write only L0/L1 tests for a feature and claim it's tested. The feature is not tested until an L2 test proves the real binary works.
- ❌ Add `pytest.skip()` for network-dependent conditions. The runner VM has pre-seeded assets — if a pull fails, the environment is broken, not the test.
- ❌ Use `zzz_destructive/` naming. Mark destructive tests explicitly. The runner VM makes ordering a convenience concern, not a correctness requirement.
- ❌ Import from `mvmctl.*` in Python tests. L2 tests use the binary as a black box. L0/L1 tests are Go-only.
- ❌ Use the forbidden assertion pattern: `assert any(s in combined for s in [...])`.
- ❌ Forget that L0/L1 tests are fast pre-filters, not replacements. Every scenario must also have L2 coverage (either existing or planned).

---

## Before Submitting, Self-Check

```
[ ] Did I classify the scenario using the decision tree?
[ ] L0: No I/O, no DB, no subprocess — just a table-driven Go test?
[ ] L1: Temp dirs, in-memory SQLite, FakeRunner for subprocess calls?
[ ] L2: Real binary inside a runner VM, no mocking?
[ ] Does every L2 scenario also have an L0/L1 pre-filter test (or a plan to add one)?
[ ] Is the quick-reference table updated if I added a new flag or command?
[ ] L2 tests: Do they assert on real side effects (output, files, system state)?
[ ] L1/L0 tests: Do they follow HOW_AGENTS_WRITE_UNIT_TESTS.md patterns?
[ ] Is every destructive test explicitly marked (not ordered by filename)?
[ ] Does `go test ./...` pass? (checks L0/L1)
[ ] Does `pytest tests/system/` pass inside the runner VM? (checks L2)
[ ] Zero skipped tests? (skip = broken environment or incomplete test)
```
