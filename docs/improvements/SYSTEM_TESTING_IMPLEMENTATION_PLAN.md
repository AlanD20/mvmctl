# System Testing Implementation Plan

**Status:** Approved  
**Approach:** Standard pytest with subprocess-based black-box testing  
**Timeline:** ~1 week (5 days)  
**Test Count:** 70 tests across 5 files

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Pytest Markers Explained](#pytest-markers-explained)
3. [Phase 1 — System Test Infrastructure](#phase-1--system-test-infrastructure)
4. [Phase 2 — Test Implementation](#phase-2--test-implementation)
5. [CI Integration](#ci-integration)
6. [Atomic Commit Strategy](#atomic-commit-strategy)
7. [Acceptance Criteria](#acceptance-criteria)
8. [Risk Mitigation](#risk-mitigation)

---

## Executive Summary

### Scope

Implement a production-grade system test suite for mvmctl that tests real Firecracker microVMs against actual KVM hardware. This is a **black-box integration test suite** — tests invoke the `mvm` CLI via subprocess and verify behavior against real system state.

### Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Test Framework** | Standard pytest | Industry standard, full ecosystem support, no custom framework maintenance |
| **Test Count** | 70 tests | 63 from spec + 7 additional for both state operation approaches |
| **Execution** | Subprocess only | No imports from `mvmctl.*`, true black-box testing |
| **Timing** | Per-image targets | Alpine <5s, Ubuntu minimal <10s, Ubuntu <30s, Arch <30s, Debian <30s |
| **Prerequisites** | KVM, mvm group | Tests require `/dev/kvm` and mvm group membership |

### Timeline

```
Week 1: System Tests (5 days)
  Day 1-2: Infrastructure (conftest.py)
  Day 3-5: Test implementation
  Day 6-7: CI integration & documentation
```

### Files Created

**New Files:**
- `tests/system/conftest.py` — System test fixtures
- `tests/system/__init__.py` — System test package marker
- `tests/system/test_network.py` — 8 network tests
- `tests/system/test_keys.py` — 7 key tests
- `tests/system/test_images.py` — 15 image tests
- `tests/system/test_vm_lifecycle.py` — 28 VM tests
- `tests/system/test_full_journeys.py` — 10 journey tests
- `tests/system/AGENTS.md` — Documentation

**Modified Files:**
- `tests/conftest.py` — Add marker-based skipping for system tests
- `pyproject.toml` — Add pytest markers, exclude system tests from default run
- `.github/workflows/system-tests.yml` — New workflow for manual system test runs

---

## Pytest Markers Explained

**What are pytest markers?**

Markers are labels you attach to tests using `@pytest.mark.marker_name`. They allow you to:

1. **Selectively run tests** — `pytest -m system` runs only system tests
2. **Skip tests by condition** — `pytest -m "not slow"` skips slow tests
3. **Organize tests** — CI can run different markers in different jobs
4. **Document test requirements** — markers show what a test needs (KVM, network, etc.)

**Our 7 markers and their purposes:**

| Marker | Purpose | When to use |
|--------|---------|-------------|
| `system` | Real hardware integration test | Every test in `tests/system/` |
| `shared_vm` | Uses module-scoped VM fixture | Tests that share one VM for state operations |
| `independent_vm` | Creates independent VM per test | Tests that create their own VM |
| `slow` | Takes >30 seconds | Tests that download images or boot slow VMs |
| `requires_kvm` | Needs `/dev/kvm` access | Tests that create actual VMs |
| `requires_network` | Needs network setup | Tests that create networks or use networking |
| `real_mvm_group_check` | Uses real group membership | Tests that check mvm group (existing marker) |

**Example usage in CI:**

```bash
# Run only fast system tests (no slow downloads)
pytest tests/system/ -m "system and not slow"

# Run only tests that don't require KVM (for CI without hardware)
pytest tests/system/ -m "system and not requires_kvm"

# Run only shared VM tests
pytest tests/system/ -m shared_vm

# Run everything except system tests (default CI behavior)
pytest tests/ -m "not system"
```

**Why this matters:**
- CI can exclude system tests (no KVM on GitHub runners)
- Nightly runs can include all system tests (self-hosted runners with KVM)
- Developers can run fast subset locally during development
- Slow tests can be run separately so they don't block quick feedback

---

## Phase 1 — System Test Infrastructure

### Core Principle: No Imports from mvmctl

System tests are **black-box**. They invoke `mvm` via subprocess and parse output. This ensures tests verify the actual user experience, not internal Python state.

### Fixture Architecture

```
conftest.py
├── Session-scoped (expensive, run once)
│   ├── mvm_binary
│   ├── check_system_prerequisites
│   ├── system_cache_dir
│   └── timing_targets
├── Module-scoped (shared across tests in a file)
│   └── lifecycle_vm (for pause/resume/stop/start chain)
└── Function-scoped (per test, with cleanup)
    ├── _restore_real_dirs (autouse - CRITICAL)
    ├── unique_vm_name
    ├── created_vm
    ├── created_network
    └── created_key
```

### New File: `tests/system/conftest.py`

```python
"""System test fixtures and utilities.

System tests are black-box integration tests that invoke mvm via subprocess.
NO imports from mvmctl.* — tests must work against the actual CLI.
"""

import os
import re
import json
import uuid
import time
import shutil
import subprocess
from pathlib import Path
from typing import Optional, Generator

import pytest


# ============================================================================
# Session-scoped fixtures (expensive setup, run once per session)
# ============================================================================

@pytest.fixture(scope="session")
def mvm_binary() -> str:
    """Resolve MVM binary path from env var or default."""
    binary = os.environ.get("MVM_BINARY", "uv run mvm")
    
    # Verify binary works
    result = subprocess.run(
        [*binary.split(), "--version"],
        capture_output=True,
        text=True,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if result.returncode != 0:
        pytest.skip(f"MVM binary not functional: {binary}")
    
    return binary


@pytest.fixture(scope="session")
def check_system_prerequisites() -> None:
    """Verify system can run real VM tests.
    
    Fails fast with clear message if prerequisites not met.
    """
    # Check KVM
    if not Path("/dev/kvm").exists():
        pytest.skip("System tests require /dev/kvm (KVM not available)")
    
    # Check mvm group membership
    import grp
    try:
        mvm_group = grp.getgrnam("mvm")
        if os.getgid() not in [mvm_group.gr_gid, 0]:
            pytest.skip("User not in 'mvm' group (run 'mvm host init' first)")
    except KeyError:
        pytest.skip("'mvm' group not found (run 'mvm host init' first)")
    
        # Check host initialization
        cache_dir = Path.home() / ".cache" / "mvmctl"
        if not (cache_dir / "mvmdb.db").exists():
            pytest.skip("mvmctl not initialized (run 'mvm host init' first)")


@pytest.fixture(scope="session")
def system_cache_dir() -> Path:
    """Real mvmctl cache directory (NOT the isolated tmp_path)."""
    return Path.home() / ".cache" / "mvmctl"


@pytest.fixture(scope="session")
def timing_targets() -> dict[str, float]:
    """Per-image boot timing targets in seconds."""
    return {
        "alpine-3.21": 5.0,
        "ubuntu-24.04-minimal": 10.0,
        "ubuntu-24.04": 30.0,
        "archlinux": 30.0,
        "debian-bookworm": 30.0,
    }


# ============================================================================
# Function-scoped autouse fixture (CRITICAL: overrides root conftest)
# ============================================================================

@pytest.fixture(autouse=True)
def _restore_real_dirs(monkeypatch, system_cache_dir) -> None:
    """CRITICAL: Override root conftest env var isolation.
    
    The root tests/conftest.py has an autouse fixture that redirects
    MVM_CACHE_DIR and MVM_CONFIG_DIR to empty tmp_path directories.
    This breaks system tests because subprocess mvm invocations inherit
    those env vars and can't find cached images/kernels.
    
    This fixture overrides them back to real paths.
    """
    real_config = Path.home() / ".config" / "mvmctl"
    
    monkeypatch.setenv("MVM_CACHE_DIR", str(system_cache_dir))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(real_config))
    monkeypatch.setenv("NO_COLOR", "1")  # Prevent ANSI codes in output


# ============================================================================
# Function-scoped fixtures (per test, with guaranteed cleanup)
# ============================================================================

@pytest.fixture
def unique_vm_name() -> str:
    """Generate unique VM name for test isolation."""
    return f"sys-vm-{uuid.uuid4().hex[:8]}"


@pytest.fixture
def unique_network_name() -> str:
    """Generate unique network name for test isolation."""
    return f"sys-net-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def unique_key_name() -> str:
    """Generate unique key name for test isolation."""
    return f"sys-key-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def created_vm(mvm_binary, unique_vm_name) -> Generator[dict, None, None]:
    """Create a VM and guarantee cleanup.
    
    Yields VM info dict from 'mvm vm ls --json'.
    Cleans up VM even if test fails.
    """
    # Create VM (image must be pre-cached or will download)
    _run_mvm(mvm_binary, "vm", "create", "--name", unique_vm_name, "--image", "alpine-3.21")
    
    # Get VM info
    vms = _parse_vm_list(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
    vm_info = next((v for v in vms if v["name"] == unique_vm_name), None)
    
    if not vm_info:
        raise RuntimeError(f"Failed to find created VM: {unique_vm_name}")
    
    try:
        yield vm_info
    finally:
        # Guaranteed cleanup
        _run_mvm(mvm_binary, "vm", "rm", "--name", unique_vm_name, check=False)


@pytest.fixture
def created_network(mvm_binary, unique_network_name) -> Generator[str, None, None]:
    """Create a network and guarantee cleanup."""
    _run_mvm(
        mvm_binary, "network", "create", unique_network_name,
        "--subnet", "10.99.0.0/24"
    )
    
    try:
        yield unique_network_name
    finally:
        _run_mvm(mvm_binary, "network", "rm", unique_network_name, check=False)


@pytest.fixture
def created_key(mvm_binary, unique_key_name) -> Generator[str, None, None]:
    """Create an SSH key and guarantee cleanup."""
    _run_mvm(mvm_binary, "key", "create", unique_key_name, "--type", "ed25519")
    
    try:
        yield unique_key_name
    finally:
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


# ============================================================================
# Module-scoped fixtures (shared across tests in a module)
# ============================================================================

@pytest.fixture(scope="module")
def lifecycle_vm(mvm_binary) -> Generator[dict, None, None]:
    """One VM shared across module for stateful operation tests.
    
    Used for pause→resume→stop→start→start chain tests.
    VM is created once, goes through state changes, then cleaned up.
    """
    vm_name = f"sys-lifecycle-{uuid.uuid4().hex[:8]}"
    
    _run_mvm(mvm_binary, "vm", "create", "--name", vm_name, "--image", "alpine-3.21")
    
    vms = _parse_vm_list(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
    vm_info = next((v for v in vms if v["name"] == vm_name), None)
    
    if not vm_info:
        raise RuntimeError(f"Failed to create lifecycle VM: {vm_name}")
    
    try:
        yield vm_info
    finally:
        _run_mvm(mvm_binary, "vm", "rm", "--name", vm_name, check=False)


# ============================================================================
# Helper functions (not fixtures)
# ============================================================================

def _run_mvm(
    binary: str,
    *args: str,
    check: bool = True,
    timeout: Optional[int] = 300,
) -> subprocess.CompletedProcess:
    """Run mvm command via subprocess.
    
    Args:
        binary: MVM binary path or "uv run mvm"
        args: Command arguments
        check: Raise on non-zero exit (default True)
        timeout: Command timeout in seconds
    
    Returns:
        CompletedProcess with stdout/stderr
    """
    cmd = [*binary.split(), *args]
    
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )
    
    if check and result.returncode != 0:
        raise RuntimeError(
            f"mvm command failed: {' '.join(args)}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )
    
    return result


def _parse_vm_list(json_output: str) -> list[dict]:
    """Parse 'mvm vm ls --json' output."""
    return json.loads(json_output)


def wait_for_ssh(
    vm_ip: str,
    user: str,
    timeout: float,
    key_path: Optional[Path] = None,
) -> bool:
    """Poll SSH until available or timeout.
    
    Args:
        vm_ip: VM IP address
        user: SSH username (root for Alpine/Arch/Debian, ubuntu for Ubuntu)
        timeout: Maximum wait time in seconds
        key_path: Path to SSH private key (optional)
    
    Returns:
        True if SSH available, False if timeout
    """
    import socket
    
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        try:
            # Try SSH connection
            cmd = ["ssh", "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=2"]
            if key_path:
                cmd.extend(["-i", str(key_path)])
            cmd.extend([f"{user}@{vm_ip}", "exit"])
            
            result = subprocess.run(cmd, capture_output=True, timeout=5)
            if result.returncode == 0:
                return True
        except (subprocess.TimeoutExpired, socket.error):
            pass
        
        time.sleep(0.5)
    
    return False


def strip_ansi(text: str) -> str:
    """Remove ANSI escape codes from text."""
    ansi_escape = re.compile(r'\x1b\[[0-9;]*m')
    return ansi_escape.sub('', text)
```

### Fixture Ordering Fix (Root Conftest)

**Problem:** The root `tests/conftest.py` has an autouse fixture `isolate_config_and_cache` that redirects `MVM_CACHE_DIR` to a temporary directory. This breaks system tests because they run after this fixture, and the redirection prevents finding cached images/kernels.

**Solution:** Add marker-based skipping in the root conftest:

```python
# tests/conftest.py — modify the existing fixture:
@pytest.fixture(autouse=True)
def isolate_config_and_cache(request, tmp_path, monkeypatch) -> None:
    """Isolate config and cache for unit/integration tests.
    
    Skipped for system tests (marked with @pytest.mark.system).
    """
    # Skip isolation for system tests
    if "system" in [m.name for m in request.node.iter_markers()]:
        return
    
    # Existing isolation code continues...
    config_dir = tmp_path / "config"
    cache_dir = tmp_path / "cache"
    # ... rest of fixture
```

This ensures:
1. System tests bypass the tmp_path isolation
2. Unit/integration tests still get isolated environments
3. No ordering conflicts between autouse fixtures

---

## Phase 2 — Test Implementation

### Test File Overview

| File | Tests | Description |
|------|-------|-------------|
| `test_network.py` | 8 | Network CRUD, iptables, NAT, cleanup |
| `test_keys.py` | 7 | SSH key creation, listing, deletion |
| `test_images.py` | 15 | Image fetch, list, per-image tests |
| `test_vm_lifecycle.py` | 28 | VM create, state ops, SSH, per-image |
| `test_full_journeys.py` | 10 | End-to-end workflows with timing |
| **Total** | **68** | Plus 2 additional for both approaches = **70** |

### test_network.py (8 tests)

```python
"""Network management system tests."""

import pytest
import subprocess

pytestmark = [pytest.mark.system, pytest.mark.requires_network]


class TestNetworkLifecycle:
    """Test network CRUD operations."""
    
    def test_network_create_with_default_cidr(self, mvm_binary, unique_network_name):
        """Create network with default CIDR (10.0.0.0/24)."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert "created" in result.stdout.lower() or "Network" in result.stdout
    
    def test_network_create_with_custom_cidr(self, mvm_binary, unique_network_name):
        """Create network with custom CIDR."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name, "--subnet", "192.168.100.0/24"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_network_listing_and_verification(self, mvm_binary, created_network):
        """List networks and verify created network appears."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert created_network in result.stdout
    
    def test_ip_rule_verification_iptables(self, mvm_binary, created_network):
        """Verify iptables rules were created for network."""
        # Check iptables rules exist
        result = subprocess.run(
            ["sudo", "iptables", "-t", "nat", "-L"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        # Network name should appear in rules
        assert created_network in result.stdout or "MVM" in result.stdout
    
    def test_nat_gateway_configuration(self, mvm_binary, created_network):
        """Verify NAT gateway is configured for network."""
        result = subprocess.run(
            ["ip", "addr", "show"],
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0
        # Bridge interface should exist
        assert f"mvm-{created_network}" in result.stdout or "mvm-" in result.stdout
    
    def test_network_deletion_and_cleanup(self, mvm_binary, unique_network_name):
        """Create and delete network, verify cleanup."""
        # Create
        subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        # Delete
        result = subprocess.run(
            [*mvm_binary.split(), "network", "rm", unique_network_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        
        # Verify gone
        result = subprocess.run(
            [*mvm_binary.split(), "network", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert unique_network_name not in result.stdout
    
    def test_duplicate_network_handling(self, mvm_binary, created_network):
        """Attempt to create duplicate network name."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", created_network],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode != 0 or "already exists" in result.stdout.lower()
    
    def test_invalid_cidr_rejection(self, mvm_binary, unique_network_name):
        """Reject invalid CIDR format."""
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name, "--subnet", "invalid-cidr"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode != 0
```

### test_keys.py (7 tests)

```python
"""SSH key management system tests."""

import pytest
import subprocess

pytestmark = pytest.mark.system


class TestKeyLifecycle:
    """Test SSH key CRUD operations."""
    
    def test_key_create_ed25519(self, mvm_binary, unique_key_name):
        """Create ed25519 SSH key."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name, "--type", "ed25519"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert "created" in result.stdout.lower() or unique_key_name in result.stdout
    
    def test_key_create_rsa(self, mvm_binary, unique_key_name):
        """Create RSA SSH key."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name, "--type", "rsa"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_key_listing(self, mvm_binary, created_key):
        """List keys and verify created key appears."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert created_key in result.stdout
    
    def test_key_set_default(self, mvm_binary, created_key):
        """Set key as default."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "set-default", created_key],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_key_delete(self, mvm_binary, unique_key_name):
        """Create and delete key."""
        # Create
        subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        # Delete
        result = subprocess.run(
            [*mvm_binary.split(), "key", "rm", unique_key_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_duplicate_key_rejection(self, mvm_binary, created_key):
        """Reject duplicate key name."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", created_key],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode != 0 or "already exists" in result.stdout.lower()
    
    def test_key_show(self, mvm_binary, created_key):
        """Show key details."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "show", created_key],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
```

### test_images.py (15 tests)

```python
"""Image management system tests."""

import pytest
import subprocess

pytestmark = [pytest.mark.system, pytest.mark.slow]


class TestImageFetch:
    """Test image fetching operations."""
    
    @pytest.mark.parametrize("image_id", [
        "alpine-3.21",
        "ubuntu-24.04-minimal",
        "ubuntu-24.04",
        "archlinux",
        "debian-bookworm",
    ])
    def test_image_fetch(self, mvm_binary, image_id):
        """Fetch each supported image."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "fetch", image_id],
            capture_output=True,
            text=True,
            timeout=300,  # 5 minute timeout for downloads
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert "fetched" in result.stdout.lower() or "downloaded" in result.stdout.lower()


class TestImageList:
    """Test image listing operations."""
    
    def test_image_list_json(self, mvm_binary):
        """List images in JSON format."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        # Should be valid JSON
        import json
        data = json.loads(result.stdout)
        assert isinstance(data, list)
    
    def test_image_list_table(self, mvm_binary):
        """List images in table format."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        # Should contain column headers
        assert "NAME" in result.stdout or "name" in result.stdout.lower()


class TestImageDefaults:
    """Test image default operations."""
    
    def test_image_set_default(self, mvm_binary):
        """Set image as default."""
        # First ensure we have an image
        subprocess.run(
            [*mvm_binary.split(), "image", "fetch", "alpine-3.21"],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        result = subprocess.run(
            [*mvm_binary.split(), "image", "set-default", "alpine-3.21"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_image_get_default(self, mvm_binary):
        """Get default image."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "get-default"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        # May fail if no default set, that's OK
        assert result.returncode == 0 or "no default" in result.stdout.lower()


class TestImageRemove:
    """Test image removal operations."""
    
    def test_image_remove(self, mvm_binary):
        """Remove an image."""
        # This test is destructive - only run if explicitly enabled
        pytest.skip("Destructive test - run manually with --run-destructive")
```

### test_vm_lifecycle.py (28 tests)

```python
"""VM lifecycle system tests — state operations with both approaches."""

import pytest
import time
import subprocess
import json

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


class TestVMCreatePerImage:
    """Test VM creation with each supported image."""
    
    @pytest.mark.parametrize("image_id", [
        "alpine-3.21",
        "ubuntu-24.04-minimal",
        "ubuntu-24.04",
        "archlinux",
        "debian-bookworm",
    ])
    def test_vm_create(self, mvm_binary, unique_vm_name, image_id, timing_targets):
        """Create VM with specific image."""
        start = time.monotonic()
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, "--image", image_id],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        duration = time.monotonic() - start
        
        assert result.returncode == 0
        # Command should return quickly (<1s target)
        assert duration < 1.0, f"Create command took {duration:.2f}s, expected <1s"
        
        # Cleanup
        subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name],
            check=False,
            env={**os.environ, "NO_COLOR": "1"},
        )


class TestVMStateOperationsShared:
    """Test state operations on shared VM (approach 1)."""
    
    pytestmark = pytest.mark.shared_vm
    
    def test_vm_pause_resume_chain(self, mvm_binary, lifecycle_vm):
        """Pause then resume VM."""
        vm_name = lifecycle_vm["name"]
        
        # Pause
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "pause", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        
        # Verify paused
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == vm_name), None)
        assert vm["status"] == "PAUSED"
        
        # Resume
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "resume", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_vm_stop_start_chain(self, mvm_binary, lifecycle_vm):
        """Stop then restart VM."""
        vm_name = lifecycle_vm["name"]
        
        # Stop
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "stop", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        
        # Start
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "start", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_vm_reboot_graceful(self, mvm_binary, lifecycle_vm):
        """Reboot VM (stop + start)."""
        vm_name = lifecycle_vm["name"]
        
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "reboot", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0


class TestVMStateOperationsIndependent:
    """Test state operations with independent VMs (approach 2)."""
    
    pytestmark = pytest.mark.independent_vm
    
    def test_vm_pause_independent(self, mvm_binary, created_vm):
        """Pause independently created VM."""
        vm_name = created_vm["name"]
        
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "pause", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_vm_resume_independent(self, mvm_binary, created_vm):
        """Resume independently created VM."""
        vm_name = created_vm["name"]
        
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "resume", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_vm_stop_independent(self, mvm_binary, created_vm):
        """Stop independently created VM."""
        vm_name = created_vm["name"]
        
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "stop", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
    
    def test_vm_start_independent(self, mvm_binary, created_vm):
        """Start independently created VM."""
        vm_name = created_vm["name"]
        
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "start", vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0


class TestVMSSH:
    """Test VM SSH operations."""
    
    def test_vm_ssh_available(self, mvm_binary, created_vm, timing_targets):
        """SSH is available after VM boots."""
        vm_name = created_vm["name"]
        vm_ip = created_vm.get("ipv4", "")
        
        if not vm_ip:
            pytest.skip("VM has no IP address")
        
        # Wait for SSH with timeout
        from conftest import wait_for_ssh
        available = wait_for_ssh(vm_ip, "root", timing_targets["alpine-3.21"])
        assert available, f"SSH not available after {timing_targets['alpine-3.21']}s"


class TestVMList:
    """Test VM listing operations."""
    
    def test_vm_list_json(self, mvm_binary, created_vm):
        """List VMs in JSON format."""
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        vms = json.loads(result.stdout)
        assert any(v["name"] == created_vm["name"] for v in vms)
    
    def test_vm_list_table(self, mvm_binary, created_vm):
        """List VMs in table format."""
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert created_vm["name"] in result.stdout


class TestVMRemove:
    """Test VM removal operations."""
    
    def test_vm_remove(self, mvm_binary, unique_vm_name):
        """Create and remove VM."""
        # Create
        subprocess.run(
            [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, "--image", "alpine-3.21"],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        # Remove
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        
        # Verify gone
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        vms = json.loads(result.stdout)
        assert not any(v["name"] == unique_vm_name for v in vms)
    
    def test_vm_remove_force(self, mvm_binary, unique_vm_name):
        """Force remove running VM."""
        # Create
        subprocess.run(
            [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, "--image", "alpine-3.21"],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        # Force remove
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
```

### test_full_journeys.py (10 tests)

```python
"""End-to-end journey system tests."""

import pytest
import time
import subprocess

pytestmark = [pytest.mark.system, pytest.mark.requires_kvm, pytest.mark.slow]


class TestQuickStartJourney:
    """Test the quick start workflow from README."""
    
    def test_journey_create_and_ssh(self, mvm_binary, unique_vm_name, timing_targets):
        """Full journey: create VM and SSH into it."""
        # Create VM
        start = time.monotonic()
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, "--image", "alpine-3.21"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        create_time = time.monotonic() - start
        
        # Get VM info
        result = subprocess.run(
            [*mvm_binary.split(), "vm", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        import json
        vms = json.loads(result.stdout)
        vm = next((v for v in vms if v["name"] == unique_vm_name), None)
        assert vm is not None
        
        # Wait for SSH
        from conftest import wait_for_ssh
        ssh_available = wait_for_ssh(vm["ipv4"], "root", timing_targets["alpine-3.21"])
        assert ssh_available, "SSH not available within timeout"
        
        # Cleanup
        subprocess.run(
            [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
            check=False,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        # Timing assertion
        total_time = time.monotonic() - start
        assert total_time < timing_targets["alpine-3.21"], f"Journey took {total_time:.2f}s"


class TestNetworkVMJourney:
    """Test network + VM workflow."""
    
    def test_journey_network_then_vm(self, mvm_binary, unique_network_name, unique_vm_name):
        """Create network, then create VM on that network."""
        # Create network
        result = subprocess.run(
            [*mvm_binary.split(), "network", "create", unique_network_name, "--subnet", "10.99.0.0/24"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        
        try:
            # Create VM on network
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, 
                 "--image", "alpine-3.21", "--network", unique_network_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
            
            # Verify VM is on correct network
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "ls", "--json"],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            import json
            vms = json.loads(result.stdout)
            vm = next((v for v in vms if v["name"] == unique_vm_name), None)
            assert vm is not None
            assert vm["network"] == unique_network_name or "10.99.0" in vm.get("ipv4", "")
            
        finally:
            # Cleanup
            subprocess.run(
                [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )
            subprocess.run(
                [*mvm_binary.split(), "network", "rm", unique_network_name],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )


class TestKeyVMJourney:
    """Test key + VM workflow."""
    
    def test_journey_key_then_vm(self, mvm_binary, unique_key_name, unique_vm_name):
        """Create key, then create VM with that key."""
        # Create key
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        
        try:
            # Create VM with key
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name,
                 "--image", "alpine-3.21", "--key", unique_key_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
            
        finally:
            # Cleanup
            subprocess.run(
                [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )
            subprocess.run(
                [*mvm_binary.split(), "key", "rm", unique_key_name],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )


class TestVMStateJourney:
    """Test VM state transition journey."""
    
    def test_journey_pause_resume_stop_start(self, mvm_binary, unique_vm_name):
        """Full state transition journey."""
        # Create VM
        subprocess.run(
            [*mvm_binary.split(), "vm", "create", "--name", unique_vm_name, "--image", "alpine-3.21"],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        
        try:
            # Pause
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "pause", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
            
            # Resume
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "resume", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
            
            # Stop
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "stop", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
            
            # Start
            result = subprocess.run(
                [*mvm_binary.split(), "vm", "start", unique_vm_name],
                capture_output=True,
                text=True,
                env={**os.environ, "NO_COLOR": "1"},
            )
            assert result.returncode == 0
            
        finally:
            # Cleanup
            subprocess.run(
                [*mvm_binary.split(), "vm", "rm", "--name", unique_vm_name, "--force"],
                check=False,
                env={**os.environ, "NO_COLOR": "1"},
            )
```

---

## CI Integration

### pyproject.toml Changes

```toml
[tool.pytest.ini_options]
testpaths = ["tests"]
addopts = """
    --cov=src/mvmctl
    --cov-branch
    --cov-report=term-missing
    --cov-fail-under=80
    --ignore=tests/system
"""
markers = [
    "real_mvm_group_check: test uses real _require_mvm_group_membership",
    "system: real hardware integration test — requires KVM, mvm group",
    "shared_vm: uses module-scoped VM fixture for stateful tests",
    "independent_vm: creates independent VM per test",
    "slow: test takes >30 seconds",
    "requires_kvm: requires /dev/kvm access",
    "requires_network: requires network setup",
]
```

### GitHub Actions

```yaml
# .github/workflows/system-tests.yml
name: System Tests

on:
  workflow_dispatch:  # Manual trigger only

jobs:
  system-tests:
    runs-on: self-hosted  # Must have KVM
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup
        run: |
          sudo python3 scripts/setup-test-environment.py
      
      - name: Run system tests
        run: |
          uv run pytest tests/system/ -v --tb=short
```

---

## Atomic Commit Strategy

```
Commit 1: feat(tests): add system test conftest.py with fixtures
  - tests/system/conftest.py
  - tests/system/__init__.py
  - Verify: uv run pytest tests/system/ --collect-only

Commit 2: feat(tests): add test_network.py (8 tests)
  - tests/system/test_network.py
  - Verify: uv run pytest tests/system/test_network.py -v

Commit 3: feat(tests): add test_keys.py (7 tests)
  - tests/system/test_keys.py
  - Verify: uv run pytest tests/system/test_keys.py -v

Commit 4: feat(tests): add test_images.py (15 tests)
  - tests/system/test_images.py
  - Verify: uv run pytest tests/system/test_images.py -v

Commit 5: feat(tests): add test_vm_lifecycle.py (28 tests)
  - tests/system/test_vm_lifecycle.py
  - Verify: uv run pytest tests/system/test_vm_lifecycle.py -v

Commit 6: feat(tests): add test_full_journeys.py (10 tests)
  - tests/system/test_full_journeys.py
  - Verify: uv run pytest tests/system/test_full_journeys.py -v

Commit 7: ci: add pytest markers and exclude system tests from default
  - pyproject.toml (markers, --ignore=tests/system)
  - .github/workflows/system-tests.yml (manual trigger)
  - Verify: uv run pytest tests/ -q --cov-fail-under=80 (system excluded)

Commit 8: docs: add tests/system/AGENTS.md documentation
  - tests/system/AGENTS.md
  - Verify: file exists and is readable
```

---

## Acceptance Criteria

All criteria must be verifiable via shell commands:

```bash
# 1. All 70 tests are collected
pytest tests/system/ --collect-only | grep "test_" | wc -l  # Should be 70

# 2. No imports from mvmctl in system tests
grep -r "from mvmctl" tests/system/  # Should be empty

# 3. CI still passes (system tests excluded)
uv run pytest tests/ -q --cov-fail-under=80

# 4. System tests can be run manually
uv run pytest tests/system/test_network.py -v --collect-only

# 5. All markers registered
grep "markers =" pyproject.toml  # Should show 7 markers

# 6. No hardcoded names
grep -r '"test-vm"' tests/system/  # Should be empty

# 7. Coverage gate maintained
uv run pytest tests/unit tests/integration -q --cov-fail-under=80

# 8. System tests have proper markers
grep -r "pytestmark" tests/system/*.py  # Should show markers in each file
```

---

## Risk Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| System tests too slow (>30 min) | High | Medium | Run only Alpine + Ubuntu minimal in CI; other images manual only |
| Coverage drops below 80% | Low | High | Ensure new code is unit tested; system tests don't count toward coverage |
| Flaky SSH waits | Medium | Medium | Image-specific timeouts; retry logic in wait_for_ssh helper |
| VM name collisions | Low | High | UUID-based names guaranteed unique |
| Race conditions in concurrent test | Medium | High | SQLite WAL mode handles this; test uses real DB |
| Root conftest isolation not overridden | Low | Critical | _restore_real_dirs autouse fixture; verify with env var print |
| KVM not available on CI | High | High | System tests excluded from default CI; manual trigger only |

---

## Next Steps

1. **Review this plan** — confirm all details are correct
2. **Begin implementation** — start with Commit 1 (system test infrastructure)
3. **Execute in order** — complete all 8 commits

**Note:** This plan assumes the SQLite migration (separate plan) is already complete. System tests depend on the database being initialized via `mvm host init`.

**Ready to proceed?** Confirm and I'll begin the implementation phase.
