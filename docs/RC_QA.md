# QA Process — Release Qualification

**Purpose:** Define the exact evidence required before releasing a new version of mvmctl to production. No release ships without passing every gate below.

---

## Table of Contents

1. [Release Gate](#1-release-gate)
2. [Test Environment](#2-test-environment)
3. [Execution Strategy](#3-execution-strategy)
4. [Evidence Collection](#4-evidence-collection)
5. [Regression Criteria](#5-regression-criteria)
6. [Release Checklist](#6-release-checklist)

---

## 1. Release Gate

A release is **blocked** until ALL of the following pass:

| Gate | Command | Must Pass |
|------|---------|-----------|
| Compile | `go build ./...` | Zero errors |
| Vet | `go vet ./...` | Zero warnings |
| Unit tests | `go test ./...` | All pass |
| System tests | `python3 scripts/run-system-tests.py --all` | All pass, zero skips on required tests |
| Version check | `./scripts/build.sh release && ./dist/mvm --version` | Returns correct version (not `0.0.0-dev`) |
| Smoke test | `./dist/mvm --help` | Shows all commands |

**No gate is optional.** A single failure blocks the release.

---

## 2. Test Environment

### 2.1 Why Nested VM

System tests run inside a Firecracker VM with nested KVM enabled. This provides:

- **Isolation** — tests don't pollute the host
- **Reproducibility** — clean snapshot before each run
- **Real hardware simulation** — nested KVM exercises the same code paths as bare metal
- **Unprivileged user** — tests run as a normal user, not root, matching real-world usage

### 2.2 Running System Tests

See [docs/development/HOW_TO_RUN_SYSTEM_TESTS.md](development/HOW_TO_RUN_SYSTEM_TESTS.md)
for the full walkthrough: host prerequisites, building the binary, setting up the
asset mirror, running the orchestrator (`--prepare`, `--all`, `--tier`, `--push`),
interpreting results, and troubleshooting common failures.

The orchestrator script flags are documented there. For the architecture overview
(three tiers, base image, shared volume), see
[docs/system-test-architecture.md](system-test-architecture.md).

---

## 3. Execution Strategy

### 3.1 System Test Execution

System tests run inside disposable Firecracker VMs with nested KVM, providing
full isolation. The orchestrator creates one VM per domain, runs tests via
`mvm exec`, and destroys the VM after.

Tests are organized into three tiers:

- **Tier 1** — Host-level CLI operations (no nested virt needed). Each domain
  gets a VM from the custom base image with the shared volume attached.
- **Tier 2** — VM creation and interaction (nested virt required). Same VM
  model as T1 but with additional asset pre-registration.
- **Tier 3** — Runs directly on the host. Includes nested virt tests, kernel
  builds, snapshot operations, and environment validation.

Tiers execute in order (T1 → T2 → T3). A failure in an earlier tier does not
block later tiers.

See [system-test-architecture.md](system-test-architecture.md) for the full
architecture overview, file layout, and per-domain classification.

For manual ad-hoc testing inside a runner VM:

```bash
mvm exec <runner-vm> --user runner --timeout 600 -- \
  "cd / && MVM_ASSET_MIRROR=/mnt python3 -m pytest \
   /tests/system/network/test_network.py --tb=short -q"
```

### 3.2 Marker Filtering

```bash
# Exclude kernel build tests (slow, optional)
pytest tests/system/ -m "not kernel_build"

# Exclude host reset tests (destructive, requires sudo)
pytest tests/system/ -m "not host_reset"

# Run only destructive tests (run last, serial)
pytest tests/system/ -m destructive
```

### 3.3 Non-Destructive Before Destructive

Each test file runs non-destructive tests (read-only) first, then destructive tests (remove, clean, force-delete) at the end. Every destructive test restores removed state in a `finally` block.

---

## 4. Evidence Collection

For every release, collect and archive:

| Evidence | How to Collect | Purpose |
|----------|----------------|---------|
| Go build output | `go build ./... 2>&1` | Zero compilation errors |
| Go vet output | `go vet ./... 2>&1` | Zero static analysis warnings |
| Go test output | `go test ./... 2>&1` | All unit tests pass |
| System test results | `python3 scripts/run-system-tests.py --all` | All system tests pass |
| Version output | `./dist/mvm --version` | Correct version string |
| Help output | `./dist/mvm --help` | All commands listed |
| Benchmark results | `benchmarks/results.json` | Performance within thresholds |
| Binary checksum | `sha256sum dist/mvm` | Reproducibility |

### 4.1 Evidence Archive

```bash
# Create evidence directory
mkdir -p release-evidence/vX.Y.Z

# Collect evidence
go build ./... > release-evidence/vX.Y.Z/build.log 2>&1
go vet ./... > release-evidence/vX.Y.Z/vet.log 2>&1
go test ./... > release-evidence/vX.Y.Z/test.log 2>&1
./dist/mvm --version > release-evidence/vX.Y.Z/version.txt 2>&1
./dist/mvm --help > release-evidence/vX.Y.Z/help.txt 2>&1
sha256sum dist/mvm > release-evidence/vX.Y.Z/checksum.sha256

# System test results (full suite with orchestrator)
python3 scripts/run-system-tests.py --all 2>&1 | tee release-evidence/vX.Y.Z/system-tests.log
```

---

## 5. Regression Criteria

### 5.1 What Is a Regression

A regression is **any** of the following:

| Category | Definition | Example |
|----------|-----------|---------|
| **Test failure** | A previously passing test now fails | `test_vm_create` was passing, now returns exit code 1 |
| **Behavior change** | A command produces different output for the same input | `mvm vm ls --json` returns different JSON structure |
| **Performance regression** | A benchmark exceeds the 6s threshold that previously passed | Alpine create_s was 0.9s, now 3.5s |
| **New error** | A command that previously succeeded now returns an error | `mvm image pull ubuntu:24.04` fails with a new error code |
| **Missing output** | A command that previously produced output now produces nothing | `mvm vm ls --json` returns empty instead of VM list |

### 5.2 What Is NOT a Regression

| Category | Definition |
|----------|-----------|
| **Expected behavior change** | A feature was intentionally changed (documented in CHANGELOG.md) |
| **New test failure** | A new test was added that reveals a pre-existing bug (file a bug, don't block release) |
| **Performance improvement** | A benchmark is faster than before |
| **Test environment issue** | Missing dependency, wrong permissions, stale state (fix environment, not code) |

### 5.3 Regression Response

1. **Identify** — which test, which domain, which commit introduced it
2. **Reproduce** — run the failing test in isolation to confirm
3. **Bisect** — `git bisect` to find the offending commit
4. **Fix** — revert or fix the commit
5. **Re-verify** — run the full domain suite again
6. **Document** — add to CHANGELOG.md if user-facing

---

## 6. Release Checklist

```markdown
## Release vX.Y.Z — QA Sign-off

### Gates
- [ ] `go build ./...` — zero errors
- [ ] `go vet ./...` — zero warnings
- [ ] `go test ./...` — all pass
- [ ] System tests — all pass inside runner VM (see evidence archive)
- [ ] `./dist/mvm --version` — returns vX.Y.Z
- [ ] `./dist/mvm --help` — all commands present

### Evidence
- [ ] Build log archived
- [ ] System test log archived
- [ ] Binary checksum archived
- [ ] Benchmark results within thresholds

### Regression check
- [ ] No previously passing tests now fail
- [ ] No behavior changes not documented in CHANGELOG.md
- [ ] No performance regressions beyond 6s threshold

### Sign-off
- [ ] QA engineer: _________________ Date: _________
```

---

## Related Documents

- [development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md](development/HOW_AGENTS_WRITE_SYSTEM_TESTS.md) — three-level test architecture (L0/L1/L2)
- [development/HOW_AGENTS_WRITE_UNIT_TESTS.md](development/HOW_AGENTS_WRITE_UNIT_TESTS.md) — L0/L1 unit test patterns
- [system-test-architecture.md](system-test-architecture.md) — L2 test runner VM architecture
- [CONTEXT.md](../CONTEXT.md) — domain language, architecture rules, test types
- [RELEASE.md](RELEASE.md) — release process and checklist
