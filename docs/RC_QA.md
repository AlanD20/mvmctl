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
| System tests | `pytest tests/system/` (inside runner VM) | All pass, zero skips on required tests |
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

### 2.2 Host Requirements

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| CPU | x86_64 with VMX/SVM | 8+ cores |
| RAM | 16 GB | 32 GB |
| Disk | 40 GB free | 80 GB free |
| KVM | `/dev/kvm` accessible | Nested virt enabled |
| Network | Outbound HTTP/HTTPS | Asset mirror pre-seeded |

### 2.3 Nested VM Setup

```bash
# 1. Enable nested virtualization on the host (Intel)
sudo modprobe -r kvm_intel
sudo modprobe kvm_intel nested=1
cat /sys/module/kvm_intel/parameters/nested   # should print Y

# For AMD:
# sudo modprobe -r kvm_amd
# sudo modprobe kvm_amd nested=1
# cat /sys/module/kvm_amd/parameters/nested

# 2. Build the mvm binary
./scripts/build.sh release
cp dist/mvm ~/.local/bin/mvm

# 3. Initialize mvmctl on the host
sudo ~/.local/bin/mvm host init

# 4. Create a network for the test runner VM
mvm network create testrunner-net --subnet 10.77.0.0/24

# 5. Create the test runner VM with nested virt
mvm vm create testrunner \
  --image ubuntu:24.04 \
  --network testrunner-net \
  --vcpu 4 \
  --mem 4096 \
  --disk-size 20G \
  --nested-virt

# 6. Wait for SSH
mvm logs testrunner --follow
mvm ssh testrunner
```

### 2.4 Unprivileged User Setup (Inside Guest)

```bash
# Inside the guest VM:

# Create an unprivileged test user
sudo useradd -m -s /bin/bash testrunner
sudo usermod -aG kvm testrunner

# Ensure /dev/kvm is accessible
sudo chmod 666 /dev/kvm

# Install system packages (as root)
sudo apt-get update
sudo apt-get install -y \
  iproute2 iptables nftables qemu-utils e2fsprogs util-linux \
  procps kmod openssh-client tar sudo passwd python3 python3-pip

# Install pytest
pip3 install pytest pytest-timeout

# Copy the pre-built release binary from the host (no Go needed in the guest)
# From the host:
mvm cp dist/mvm testrunner:~/.local/bin/mvm
mvm ssh testrunner --cmd "chmod +x ~/.local/bin/mvm"

# Clone test scripts (only tests/system/ and scripts/ needed)
# From the host:
mvm cp ./scripts testrunner:~/mvmctl/scripts
mvm cp ./tests testrunner:~/mvmctl/tests

# Initialize mvmctl (requires sudo for host init only)
mvm ssh testrunner --cmd "sudo ~/.local/bin/mvm host init"

# Verify
mvm ssh testrunner --cmd "mvm host status --json"
mvm ssh testrunner --cmd "test -c /dev/kvm && echo KVM available"
```

### 2.5 Asset Mirror (Inside Guest)

Pre-seed the asset mirror to avoid re-downloading on every run:

```bash
# On the host, copy assets to a shared volume
# Or set MVM_ASSET_MIRROR to a host-mounted path
export MVM_ASSET_MIRROR=/mnt/shared/mvm-asset-mirror
mkdir -p "$MVM_ASSET_MIRROR"
```

---

## 3. Execution Strategy

### 3.1 System Test Execution

System tests run inside a disposable Firecracker VM with nested KVM, providing
full isolation. The orchestrator creates one VM per domain, runs tests via
`mvm exec`, and destroys the VM after.

```bash
# Run the full suite (T1 + T2 + T3) using the orchestrator
python3 scripts/run-system-tests.py --all

# Run specific domains
python3 scripts/run-system-tests.py cli network

# Run specific tiers
python3 scripts/run-system-tests.py --tier 1,2

# Run a single file manually inside an existing runner VM
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
- [ ] E2E tests — all pass inside runner VM (see evidence archive)
- [ ] `./dist/mvm --version` — returns vX.Y.Z
- [ ] `./dist/mvm --help` — all commands present

### Evidence
- [ ] Build log archived
- [ ] E2E test log archived
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
