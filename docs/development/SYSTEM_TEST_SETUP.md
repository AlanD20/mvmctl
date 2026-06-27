# System Test Setup — Runner VM Architecture

**Purpose:** Define how to set up the host machine and the runner VM for executing L2 (E2E) system tests.

> **⚠️ Deprecation notice:**
> This document describes the legacy **snapshot-based** approach using `rc-env.yaml` + `mvm snapshot`.
> The current recommended approach uses the orchestrator-based system with per-domain VMs,
> documented in [system-test-architecture.md](../system-test-architecture.md).
> The `scripts/run-system-tests.py` orchestrator creates a custom base image, provisions
> per-domain VMs from it, and runs tests in parallel. The `rc-env.yaml` path still works
> for single-VM sessions but is no longer the primary execution method.

> **See also:** [ADR-0012](../adr/0012-unified-test-architecture.md) for the architectural decisions.
>
> **See also:** [HOW_AGENTS_WRITE_SYSTEM_TESTS.md](HOW_AGENTS_WRITE_SYSTEM_TESTS.md) for the test writing guide.
>
> **See also:** [system-test-architecture.md](../system-test-architecture.md) for the orchestrator-based approach.
>
> **See also:** [RC_QA.md](../RC_QA.md) for release qualification gates.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Host Requirements](#2-host-requirements)
3. [Runner VM Specification](#3-runner-vm-specification)
4. [One-Time Setup](#4-one-time-setup)
5. [Create the Runner VM (via rc-env.yaml)](#5-create-the-runner-vm-via-rc-envyaml)
6. [Post-Provisioning: Wire Up Caches + DB](#6-post-provisioning-wire-up-caches--db)
7. [Snapshot the Runner VM](#7-snapshot-the-runner-vm)
8. [Running L2 Tests](#8-running-l2-tests)
9. [Running L2 Tests in Parallel](#9-running-l2-tests-in-parallel)
10. [Troubleshooting](#10-troubleshooting)
11. [Verification Checklist](#11-verification-checklist)

---

## 1. Architecture Overview

L2 tests run inside a **disposable Firecracker VM** (the "runner VM") with nested KVM. The host manages the VM lifecycle via `mvm`. Tests create their own VMs inside the runner VM (triple nesting: host → runner VM → test VM).

```
Host (Go + KVM only)
  │
  ├── rc-env.yaml creates + provisions the runner VM
  ├── Additional steps wire up caches + DB
  ├── Snapshot for reuse
  │
  └── Tests run via: mvm exec rc-vm -- "pytest tests/system/..."
```

---

## 2. Host Requirements

| Component | Requirement | Check |
|-----------|-------------|-------|
| CPU | x86_64 with VMX/SVM | `grep -c '(vmx|svm)' /proc/cpuinfo` > 0 |
| RAM | 8 GB min (16 GB rec.) | `free -g` |
| Disk | 5 GB free for caches | `df -h ~/.cache/mvm-asset-mirror` |
| KVM | `/dev/kvm` accessible | `test -c /dev/kvm && echo OK` |
| Nested virt | `nested=Y` | `cat /sys/module/kvm_intel/parameters/nested` |
| Go | 1.26+ | `go version` |
| Network | Outbound HTTP/HTTPS (first run only) | `curl -sI https://example.com` |

```bash
# Host only needs Go
sudo apt-get install -y golang
```

---

## 3. Runner VM Specification

The runner VM (named `rc-vm` per `rc-env.yaml`) is configured as:

| Resource | Value |
|----------|-------|
| Image | `ubuntu:noble` |
| Kernel | `official:7.0.11` with `kvm,nftables,tuntap` |
| vCPU | 6 |
| Memory | 4 GB |
| Disk | 25 GB |
| Nested virt | Enabled |
| User | `runner` (has passwordless sudo) |

---

## 4. One-Time Setup

These steps run ONCE on the host to populate the asset caches.

### 4.1 Build the binary

```bash
./scripts/build.sh release
cp dist/mvm ~/.local/bin/mvm
```

### 4.2 Ensure caches are populated

If not already done, build the official kernel and pull assets:

```bash
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
mkdir -p "$MVM_ASSET_MIRROR"

# Build official kernel (30-120 min first time, cached in /tmp/mvmctl/ for subsequent runs)
mvm kernel pull official:7.0.11 --features nftables,tuntap,kvm

# Pull remaining assets (cached in MVM_ASSET_MIRROR for subsequent runs)
mvm image pull ubuntu:noble
mvm kernel pull --type firecracker --version v1.15 --default
mvm bin pull firecracker --version 1.16.0 --default
```

After this, verify:

```bash
ls /tmp/mvmctl/vmlinux-7.0.11*   # compiled kernel binary
ls /tmp/mvmctl/*.marker           # build marker
ls ~/.cache/mvm-asset-mirror/     # downloaded assets
```

---

## 5. Create the Runner VM (via rc-env.yaml)

The [`rc-env.yaml`](../../rc-env.yaml) file handles everything: network, key, image, binary, kernel, VM creation, file copies, and package installation.

```bash
mvm env apply rc-env.yaml
```

This creates the VM `rc-vm` with:
- Ubuntu Noble, official kernel 7.0.11 with features, 6 vCPU, 4 GB RAM, 25 GB disk, nested virt
- `mvm` binary at `/usr/bin/mvm`
- Test suite at `/home/runner/tests/`
- Asset files (kernel `.vmlinux`, images, firecracker tarball) at `/mnt/`
- System packages installed (qemu-utils, python3, pytest, build tools, etc.)
- User `runner` with passwordless sudo + kvm group

---

## 6. Post-Provisioning: Import Assets + Init DB

`rc-env.yaml` copies all assets into `/mnt/` inside the VM. The `import cached assets` exec step points `MVM_ASSET_MIRROR` and `MVM_TEMP_DIR` to `/mnt/`, runs `mvm kernel pull`/`image pull`/`bin pull`/`init`, then sets env vars in `.bashrc`. Everything is wired up by `mvm env apply rc-env.yaml`.

---

## 7. Snapshot the Runner VM

Once provisioned, snapshot so subsequent test sessions restore instantly.

```bash
# Create snapshot
mvm snapshot create rc-vm --name rc-vm-snap
```

### Restore from snapshot (for each test run)

```bash
# Restore — replaces current rc-vm with snapshot copy, resumes it
mvm snapshot restore rc-vm-snap rc-vm --resume
```

---

## 8. Running L2 Tests

```bash
# One file
mvm exec rc-vm --user runner --timeout 600 -- \
  "cd ~ && python3 -m pytest tests/system/volume/test_volume.py -xvs"

# All L2 tests
mvm exec rc-vm --user runner --timeout 600 -- \
  "cd ~ && python3 -m pytest tests/system/ -x --junitxml=results.xml"

# Collect results
mvm cp rc-vm:/home/runner/tests/results.xml ./results.xml

# L0/L1 fast pre-filter (on host, no VM needed)
go test ./... -count=1
```

---

## 9. Running L2 Tests in Parallel

Multiple runner VMs from the same snapshot run different suites:

```bash
mvm exec rc-vm-1 --user runner --timeout 600 -- \
  "cd ~ && python3 -m pytest tests/system/volume/ tests/system/network/ -x"
mvm exec rc-vm-2 --user runner --timeout 600 -- \
  "cd ~ && python3 -m pytest tests/system/vm/ -x"
mvm exec rc-vm-3 --user runner --timeout 600 -- \
  "cd ~ && python3 -m pytest tests/system/ --ignore=tests/system/volume/ --ignore=tests/system/network/ --ignore=tests/system/vm/ -x"
```

---

## 10. Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `mvm env apply` fails | `mvmctl` not initialized | Run `mvm init --non-interactive --skip-host` first |
| SSH not available after creation | Cloud-init still running | Check `mvm logs rc-vm --lines 50` |
| Nested KVM missing inside VM | Host lacks `nested=1` | `cat /sys/module/kvm_intel/parameters/nested` → must be `Y` |
| `kernel pull` downloads instead of cache hit | `.vmlinux` or `.marker` not in `/mnt/` | Re-run `mvm env apply` to re-copy assets |
| `image pull` downloads instead of cache hit | Mirror not set to `/mnt/` | `MVM_ASSET_MIRROR=/mnt` must be set in the exec step |
| Snapshot restore fails | Files corrupted | Rebuild: re-run `mvm env apply`, re-snapshot |
| Test VM creation fails inside runner VM | Out of resources | Increase vcpu/mem/disk in `rc-env.yaml` |

---

## 11. Verification Checklist

```bash
# Host
echo "KVM:   $(test -c /dev/kvm && echo OK)"
echo "Nest:  $(cat /sys/module/kvm_intel/parameters/nested 2>/dev/null)"
echo "mvm:   $(~/.local/bin/mvm --version 2>/dev/null)"
echo "Krnl:  $(ls /tmp/mvmctl/vmlinux-7.0.11* 2>/dev/null)"
echo "Mrkr:  $(ls /tmp/mvmctl/*.marker 2>/dev/null)"

# Runner VM
echo "Alive: $(mvm exec rc-vm --user runner --timeout 10 -- 'echo OK' 2>/dev/null || echo DEAD)"
echo "KVM:   $(mvm exec rc-vm --user runner --timeout 10 -- 'test -c /dev/kvm && echo OK' 2>/dev/null)"
echo "Bin:   $(mvm exec rc-vm --user runner --timeout 10 -- 'mvm --version' 2>/dev/null)"
echo "Py:    $(mvm exec rc-vm --user runner --timeout 10 -- 'python3 -m pytest --version' 2>/dev/null | head -1)"
echo "Imgs:  $(mvm exec rc-vm --user runner --timeout 30 -- 'mvm image ls --json | python3 -c \"import sys,json; print(len([i for i in json.load(sys.stdin) if i.get(\\\"is_present\\\")]))\"' 2>/dev/null)"
echo "Krnls: $(mvm exec rc-vm --user runner --timeout 30 -- 'mvm kernel ls --json | python3 -c \"import sys,json; print(len([k for k in json.load(sys.stdin) if k.get(\\\"is_present\\\")]))\"' 2>/dev/null)"
```
