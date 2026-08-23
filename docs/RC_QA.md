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
| Tidy | `go mod tidy && git diff --exit-code` | Module files unchanged |
| Format | `test -z "$(gofmt -l .)"` | Entire Go tree formatted |
| Line length | `golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/` | No listed files |
| Generate | `go generate ./internal/service/agent/...` | Generated assets current |
| Vet | `go vet ./...` | Zero warnings |
| Go tests | `go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic` | All pass |
| System install | `sudo ./dist/mvm host install-system && sudo /usr/local/bin/mvm host init` | Canonical root-owned install succeeds |
| System tests | `MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm python3 scripts/run-system-tests.py --release-qualification --host-direct --rebuild --candidate-version X.Y.Z --all` | Identity-qualified T1/T2 runners and clean-host T3 all pass |
| Version check | `/usr/local/bin/mvm --version` | Returns correct version (not `0.0.0-dev`) |
| Smoke test | `/usr/local/bin/mvm --help` | Shows all commands |

**No gate is optional.** A single failure blocks the release.

The system-install, version, and smoke gates run on a dedicated clean qualification host or disposable outer VM. Do not
replace a developer workstation's active controller to satisfy them. During iterative QA, the existing outer controller
creates disposable nested runners and the candidate is installed only inside those runners.

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
asset mirror, running the orchestrator (`--prepare`, `--all`, `--tier`, `--host-direct`,
`--release-qualification`, `--push`),
interpreting results, and troubleshooting common failures.

The orchestrator script flags are documented there. For the architecture overview
(three tiers, base image, shared volume), see
[docs/system-test-architecture.md](system-test-architecture.md).

---

## 3. Execution Strategy

### 3.1 System Test Execution

T1/T2 system tests run inside disposable Firecracker VMs with nested KVM. The
orchestrator creates one VM per T1/T2 domain, runs tests via `mvm exec`, and
destroys the VM after. Tier 3 is host-direct and has the separate restrictions
below.

For T1/T2, release qualification keeps the outer controller (`MVM_BINARY`) separate from `./dist/mvm`
(`MVM_CANDIDATE_BINARY`). The controller manages disposable test resources. The candidate-qualified base image is built
by staging the candidate, invoking `host install-system`, and initializing through the resulting exact
`/usr/local/bin/mvm`. T1/T2 runners clone that image and execute the installed path; the focused `system_install` domain
replays and verifies the administrator install contract. Directly copying onto the canonical path, relying on PATH
lookup, or aliasing the candidate to the controller does not qualify the installer.

The builder's unprivileged initialization pins Firecracker 1.16.0 through
`mvm init --non-interactive --binary-version 1.16.0`; it must not resolve a moving latest release during qualification.

Tier 3 is different: those tests run directly on the outer host and may create or destroy host resources. The runner
rejects Tier 3 and `--all` before any work unless `--host-direct` acknowledges that mutation. For release evidence,
`--release-qualification` additionally requires an unfiltered `--all`, an explicit-version rebuild, exact canonical
root-owned `/usr/local/bin/mvm`, safe ancestors, and matching candidate/controller SHA-256 and strict version before
resource preparation.

These gates still do not prove that the host started empty or that every Tier 3 resource belongs to the current run.
Until the remaining Task 17 inventory and cleanup gates land, run full qualification only on a dedicated clean host or
disposable outer VM. On a development workstation, use focused T1/T2 domains or `--tier 1,2`; `--host-direct` is consent,
not isolation or an ownership-safe cleanup guarantee.

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
| Tidy output | `go mod tidy && git diff --exit-code` | Module files unchanged |
| Format output | `test -z "$(gofmt -l .)"` | Entire Go tree formatted |
| Generate output | `go generate ./internal/service/agent/...` | Generated assets current |
| Go vet output | `go vet ./... 2>&1` | Zero static analysis warnings |
| Go test output | `go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic` | All Go tests pass |
| System test results | `MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm python3 scripts/run-system-tests.py --release-qualification --host-direct --rebuild --candidate-version X.Y.Z --all` | Identity-qualified full matrix passes on the clean host |
| Version output | `/usr/local/bin/mvm --version` | Correct version string |
| Help output | `/usr/local/bin/mvm --help` | All commands listed |
| Benchmark results | `benchmarks/results.json` | Performance within thresholds |
| Binary checksum | `sha256sum dist/mvm` | Reproducibility |

### 4.1 Evidence Archive

```bash
set -euo pipefail

version=X.Y.Z
evidence_dir="release-evidence/v${version}"
mkdir -p "$evidence_dir"
exec > >(tee -a "$evidence_dir/qualification.log") 2>&1

# Collect evidence
./scripts/build.sh release --version "$version" --output dist/mvm
go mod tidy
git diff --exit-code
test -z "$(gofmt -l .)"
test -z "$(golines --max-len=120 --no-reformat-tags --list-files \
  ./internal/ ./pkg/ ./cmd/)"
go generate ./internal/service/agent/...
git diff --exit-code
go vet ./... > "$evidence_dir/vet.log" 2>&1
go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic \
  > "$evidence_dir/test.log" 2>&1

# The following gates mutate the outer host. Run them only on the dedicated
# clean qualification host or disposable outer VM.
sudo ./dist/mvm host install-system
sudo /usr/local/bin/mvm host init
test "$(/usr/local/bin/mvm --version)" = "mvm ${version}"
/usr/local/bin/mvm --version > "$evidence_dir/version.txt" 2>&1
/usr/local/bin/mvm --help > "$evidence_dir/help.txt" 2>&1
sha256sum dist/mvm > "$evidence_dir/checksum.sha256"

# System test results (full suite with orchestrator)
MVM_BINARY=/usr/local/bin/mvm MVM_CANDIDATE_BINARY=./dist/mvm \
  MVM_ASSET_MIRROR="$HOME/.cache/mvm-asset-mirror" \
  python3 scripts/run-system-tests.py \
  --release-qualification --host-direct --rebuild \
  --candidate-version "$version" --all \
  2>&1 | tee "$evidence_dir/system-tests.log"
```

Until the orchestrator records skip/xfail counts in its final summary, QA must inspect every archived per-domain pytest
summary and record that it contains zero skipped, xfailed, or xpassed cases. A domain-level `PASS` alone is not sufficient
release evidence.

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
| **New test exposing an old bug** | Not classified as a regression, but the revealed defect still blocks the release until fixed or the requirement is explicitly removed |
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
- [ ] `go mod tidy && git diff --exit-code` — module files and generated tree unchanged
- [ ] `test -z "$(gofmt -l .)"` — entire Go tree formatted
- [ ] `golines --max-len=120 --no-reformat-tags --list-files ./internal/ ./pkg/ ./cmd/` — no files listed
- [ ] `go generate ./internal/service/agent/... && git diff --exit-code` — generated assets current
- [ ] `go vet ./...` — zero warnings
- [ ] `go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic` — all pass
- [ ] `./scripts/build.sh release --version X.Y.Z --output dist/mvm` — exact candidate built
- [ ] Dedicated clean qualification host/outer VM — candidate installed at exact `/usr/local/bin/mvm`
- [ ] System tests — every required T1/T2/T3 case passes; archived summaries show zero skips/xfails/xpasses
- [ ] `/usr/local/bin/mvm --version` — returns exact `mvm X.Y.Z`
- [ ] `/usr/local/bin/mvm --help` — all commands present

### Evidence
- [ ] Build log archived
- [ ] Tidy, format, line-length, generate, vet, and Go-test logs archived
- [ ] System test log archived
- [ ] Per-domain skip/xfail audit archived
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
