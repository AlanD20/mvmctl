# How Agents Run System Tests

## Purpose

This is the **authoritative execution plan** for running the mvmctl system test
suite and qualifying a release. An AI agent can start from a fresh clone and
follow this guide linearly without cross-referencing other docs.

**Do NOT deviate** from the commands below without approval. All paths,
environment variables, and flags are intentional.

---

## Table of Contents

1. [Prerequisite Check](#1-prerequisite-check)
2. [Build the Binary](#2-build-the-binary)
3. [Initialize mvmctl](#3-initialize-mvmctl)
4. [Seed the Asset Mirror](#4-seed-the-asset-mirror)
5. [Run System Tests (Per-Domain)](#5-run-system-tests-per-domain)
6. [Interpret Results](#6-interpret-results)
7. [Collect Release Evidence](#7-collect-release-evidence)
8. [Reference](#8-reference)

---

## 1. Prerequisite Check

Run ALL checks below. If any fails, stop and report.

### 1.1 Hardware

```bash
# KVM
test -c /dev/kvm && echo "KVM: OK" || echo "KVM: MISSING"

# Virtualization extensions
egrep -c '(vmx|svm)' /proc/cpuinfo
# Expected: > 0

# Memory (total)
free -g | awk '/Mem:/{print $2 " GB"}'
# Expected: >= 8 (16 recommended)

# Disk (free)
df -h ~/.cache/mvmctl 2>/dev/null || echo "~/.cache/mvmctl does not exist yet"
# Expected: >= 20 GB free
```

### 1.2 Software

```bash
# Go
go version
# Expected: 1.26+ (match the project's go.mod)

# Python + pytest
python3 -c "import pytest; print(f'pytest: OK ({pytest.__version__})')"  \
  || echo "pytest: MISSING — install with: pip3 install pytest"
```

### 1.3 Groups

```bash
groups
# Expected: kvm mvm disk (all three)

# Verify each explicitly:
getent group kvm >/dev/null && echo "kvm group: OK" || echo "kvm group: MISSING"
getent group mvm >/dev/null && echo "mvm group: OK" || echo "mvm group: MISSING"
getent group disk >/dev/null && echo "disk group: OK" || echo "disk group: MISSING"
```

If missing, add yourself (`sudo usermod -aG kvm $USER`, etc.) then **log out
and back in** for the group change to take effect.

### 1.4 System Tools

```bash
for tool in qemu-img mkfs.ext4 truncate zstd genisoimage ssh-keygen ip nft; do
  which "$tool" >/dev/null 2>&1 && echo "$tool: OK" || echo "$tool: MISSING"
done
```

If any tool is missing, install it:
- Debian/Ubuntu: `sudo apt-get install -y <package>`
- Arch: `sudo pacman -S --needed <package>`

See `docs/development/SYSTEM_TEST_SETUP.md` §2 for the package list.

### 1.5 Sudo Configuration

```bash
sudo -n true 2>/dev/null && echo "passwordless sudo: OK" || echo "passwordless sudo: MISSING — check /etc/sudoers.d/mvm"
```

The mvm group must have passwordless sudo:
```bash
grep '%mvm' /etc/sudoers.d/mvm 2>/dev/null || echo "sudoers not configured — run: echo '%mvm ALL=(ALL) NOPASSWD: ALL' | sudo tee /etc/sudoers.d/mvm"
```

---

## 2. Build the Binary

```bash
# Build the release binary
./scripts/build.sh release

# Verify it exists and is executable
test -x dist/mvm && echo "binary: OK ($(dist/mvm --version 2>/dev/null))"

# Copy to ~/.local/bin (for sudo operations — this path has sudoers access)
cp dist/mvm ~/.local/bin/mvm
```

**IMPORTANT**: The binary at `~/.local/bin/mvm` is what system tests use for
sudo operations. The runner script finds `dist/mvm` via auto-detection.

---

## 3. Initialize mvmctl

```bash
# First-time init (creates DB, chains, directories)
sudo ~/.local/bin/mvm host init

# Verify
~/.local/bin/mvm host status --json
# Expected: shows kvm_accessible: true, etc.
```

---

## 4. Seed the Asset Mirror

Pre-seed the asset mirror to avoid re-downloading during tests:

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mkdir -p "$MVM_ASSET_MIRROR"

# Seed assets (one-time download — takes 30-60s each)
# Use ~/.local/bin/mvm — this is the canonical runtime binary path
# with passwordless sudo access.
# Kernel:
~/.local/bin/mvm kernel pull --type firecracker --default

# Images:
~/.local/bin/mvm image pull alpine --version 3.21
~/.local/bin/mvm image pull ubuntu-minimal --version 24.04

# Firecracker binary:
~/.local/bin/mvm bin pull 1.15.1 --default

# Verify mirror is populated
ls -la "$MVM_ASSET_MIRROR"
```

The runner script (`scripts/run_tests.py`) auto-seeds the mirror if empty,
but pre-seeding here avoids duplicate downloads across domains.

---

## 5. Run System Tests (Per-Domain)

### 5.1 The Rule

System tests are **stateful**. Running all domains as a single batch causes
cross-file state pollution. **Always run per-domain** using the unified runner:

```bash
MVM_BINARY=dist/mvm MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run_tests.py --domain <domain>
```

### 5.2 All Domains (Release Gate)

Run each domain exactly once, in any order. Each takes 1-10 minutes:

```bash
for domain in \
  bin cache cli config console cp host \
  images init invariants kernel keys logs \
  network ssh vm volume zzz_destructive; do
  echo "=== Domain: $domain ==="
  MVM_BINARY=dist/mvm MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
    python3 scripts/run_tests.py --domain "$domain"
  echo ""
done
```

**Tip:** Add `--pytest-extra "-x"` to stop on first failure for faster
iteration, but remove `-x` for the final release gate run (you need the
complete failure count).

### 5.3 Single Domain

```bash
MVM_BINARY=dist/mvm MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run_tests.py --domain vm
```

### 5.4 Single File

```bash
MVM_BINARY=dist/mvm MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run_tests.py --test tests/system/network/test_network.py
```

### 5.5 Marker Filters

```bash
# Exclude kernel build tests (slow, requires gcc/make)
MVM_BINARY=dist/mvm ... python3 scripts/run_tests.py --domain kernel \
  --pytest-extra "-m 'not kernel_build'"

# Exclude host reset tests (destructive, modifies real state)
MVM_BINARY=dist/mvm ... python3 scripts/run_tests.py --domain host \
  --pytest-extra "-m 'not host_reset'"
```

### 5.6 Re-run Only Failures

```bash
MVM_BINARY=dist/mvm MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
  python3 scripts/run_tests.py --failed-only
```

Reads `.reports/system-test-results-latest.txt` and re-runs only the files
that previously failed.

---

## 6. Interpret Results

The runner prints a summary at the end:
```
  12 passed  0 failed  0 skipped  (3m12s)
```

Each domain's results are saved to `.reports/system-test-results-latest.txt`:

```
test_network.py: PASS
test_nftables.py: PASS
```

### 6.1 Pass / Fail / Skip Rules

| Status | Meaning | Release Gate |
|--------|---------|--------------|
| **PASS** | All tests in file passed | ✓ |
| **FAIL** | At least one test failed | ✗ — must fix before release |
| **SKIP** | All tests skipped (prerequisite missing) | ⚠ — investigate why |

### 6.2 What a Skip Means

A skipped test file means ALL tests in that file were skipped, typically
because a prerequisite check failed. Investigate:
- Is `/dev/kvm` accessible?
- Is the asset mirror reachable?
- Is the `mvm` group set up?

A skip on a required test (e.g., `test_vm_lifecycle.py` skipped because no
KVM) is a **release blocker** — fix the environment, not the code.

### 6.3 Common Failure Patterns

| Symptom | Likely Cause | Fix |
|---------|-------------|-----|
| `iptables` errors | `mvm host init` not run | Run `sudo ~/.local/bin/mvm host init` |
| VM creation hangs | Binary not built or missing | Re-run `./scripts/build.sh release` |
| `bridge already exists` | Stale bridges from previous run | Clean up with `mvm network rm --force` |
| `Text file busy` on service binary | Stale service processes | `killall -9 mvm-console-relay mvm-nocloud-server mvm-provision` |
| All tests skip with "KVM not available" | KVM not accessible | Check `/dev/kvm` permissions, user groups |
| High skip ratio (>10%) | Missing dependencies | Run the prerequisite check (section 1) |

---

## 7. Collect Release Evidence

Before signing off a release, collect and archive evidence:

```bash
mkdir -p release-evidence/vX.Y.Z

# Build + unit gates
go build ./... > release-evidence/vX.Y.Z/build.log 2>&1
go vet ./... > release-evidence/vX.Y.Z/vet.log 2>&1
go test ./... > release-evidence/vX.Y.Z/test.log 2>&1

# System test results (per-domain)
for domain in bin cache cli config console cp host \
  images init invariants kernel keys logs \
  network ssh vm volume zzz_destructive; do
  MVM_BINARY=dist/mvm MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror \
    python3 scripts/run_tests.py --domain "$domain" \
    > release-evidence/vX.Y.Z/system-${domain}.log 2>&1
done

# Binary verification
~/.local/bin/mvm --version > release-evidence/vX.Y.Z/version.txt 2>&1
~/.local/bin/mvm --help > release-evidence/vX.Y.Z/help.txt 2>&1
sha256sum dist/mvm > release-evidence/vX.Y.Z/checksum.sha256

# Coverage matrix snapshot
cp tests/system/COVERAGE_MATRIX.md release-evidence/vX.Y.Z/
```

Each `.log` file must show zero failures. Any failure blocks the release.

---

## 8. Reference

- `docs/RC_QA.md` — Release gates and checklist (human-facing)
- `docs/RELEASE.md` — Full release process (tagging, CI, AUR)
- `docs/development/SYSTEM_TEST_SETUP.md` — Detailed environment setup
- `tests/system/COVERAGE_MATRIX.md` — Per-command coverage tracking
- `.opencode/agent/qa-engineer.md` — QA agent instructions
