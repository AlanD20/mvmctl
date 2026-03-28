# Cloud-Init Model Refactor - Implementation Plan

**Date:** 2026-03-28  
**Scope:** Create dedicated cloud_init.py module, remove datasource_mode  
**Approach:** Test-Driven Development (TDD)

---

## Overview

This plan implements the cloud-init model refactor by:
1. Creating a dedicated `src/mvmctl/models/cloud_init.py` module
2. Moving cloud-init types from `vm.py`
3. Creating a proper `CloudInitConfig` dataclass
4. **Removing `datasource_mode` completely** (no backward compatibility)
5. Updating all imports and serialization

---

## Phase 1: Test Preparation (Red Phase)

### 1.1 Create Test File First
**File:** `tests/unit/models/test_cloud_init.py`

```python
"""Tests for cloud-init models."""

from pathlib import Path

import pytest

from mvmctl.models.cloud_init import (
    CloudInitConfig,
    CloudInitMode,
    CloudInitStatus,
)


class TestCloudInitMode:
    """Test CloudInitMode enum."""

    def test_enum_values(self):
        """Test that enum has expected values."""
        assert CloudInitMode.AUTO.value == "auto"
        assert CloudInitMode.CUSTOM.value == "custom"
        assert CloudInitMode.DISABLED.value == "disabled"
        assert CloudInitMode.NO_CLOUD_NET.value == "nocloud-net"

    def test_from_string(self):
        """Test creating enum from string values."""
        assert CloudInitMode("auto") == CloudInitMode.AUTO
        assert CloudInitMode("custom") == CloudInitMode.CUSTOM
        assert CloudInitMode("disabled") == CloudInitMode.DISABLED
        assert CloudInitMode("nocloud-net") == CloudInitMode.NO_CLOUD_NET


class TestCloudInitStatus:
    """Test CloudInitStatus enum."""

    def test_status_values(self):
        """Test that status enum has expected values."""
        # Values are auto() generated - just verify they exist and are distinct
        assert CloudInitStatus.PENDING != CloudInitStatus.RUNNING
        assert CloudInitStatus.RUNNING != CloudInitStatus.DONE
        assert CloudInitStatus.DONE != CloudInitStatus.ERROR


class TestCloudInitConfig:
    """Test CloudInitConfig dataclass."""

    def test_default_construction(self):
        """Test default construction."""
        config = CloudInitConfig()
        assert config.mode == CloudInitMode.AUTO
        assert config.iso_path is None
        assert config.keep_iso is False
        assert config.nocloud_net_url is None

    def test_custom_construction(self):
        """Test construction with custom values."""
        config = CloudInitConfig(
            mode=CloudInitMode.NO_CLOUD_NET,
            iso_path=Path("/custom/path.iso"),
            keep_iso=True,
            nocloud_net_url="http://10.0.0.1:8080/",
        )
        assert config.mode == CloudInitMode.NO_CLOUD_NET
        assert config.iso_path == Path("/custom/path.iso")
        assert config.keep_iso is True
        assert config.nocloud_net_url == "http://10.0.0.1:8080/"

    def test_to_dict_default(self):
        """Test serialization with defaults."""
        config = CloudInitConfig()
        data = config.to_dict()

        assert data["mode"] == "auto"
        assert data["iso_path"] is None
        assert data["keep_iso"] is False
        assert data["nocloud_net_url"] is None

    def test_to_dict_custom(self):
        """Test serialization with custom values."""
        config = CloudInitConfig(
            mode=CloudInitMode.NO_CLOUD_NET,
            iso_path=Path("/path/to/iso"),
            keep_iso=True,
            nocloud_net_url="http://example.com/",
        )
        data = config.to_dict()

        assert data["mode"] == "nocloud-net"
        assert data["iso_path"] == "/path/to/iso"
        assert data["keep_iso"] is True
        assert data["nocloud_net_url"] == "http://example.com/"

    def test_from_dict_default(self):
        """Test deserialization with defaults."""
        data = {}
        config = CloudInitConfig.from_dict(data)

        assert config.mode == CloudInitMode.AUTO
        assert config.iso_path is None
        assert config.keep_iso is False
        assert config.nocloud_net_url is None

    def test_from_dict_explicit(self):
        """Test deserialization with explicit values."""
        data = {
            "mode": "nocloud-net",
            "iso_path": "/path/to/iso",
            "keep_iso": True,
            "nocloud_net_url": "http://example.com/",
        }
        config = CloudInitConfig.from_dict(data)

        assert config.mode == CloudInitMode.NO_CLOUD_NET
        assert config.iso_path == Path("/path/to/iso")
        assert config.keep_iso is True
        assert config.nocloud_net_url == "http://example.com/"

    def test_from_dict_partial(self):
        """Test deserialization with partial data."""
        data = {"mode": "disabled"}
        config = CloudInitConfig.from_dict(data)

        assert config.mode == CloudInitMode.DISABLED
        assert config.iso_path is None
        assert config.keep_iso is False

    def test_roundtrip_serialization(self):
        """Test that to_dict/from_dict are inverse operations."""
        original = CloudInitConfig(
            mode=CloudInitMode.CUSTOM,
            iso_path=Path("/custom.iso"),
            keep_iso=True,
            nocloud_net_url="http://test/",
        )
        data = original.to_dict()
        restored = CloudInitConfig.from_dict(data)

        assert restored.mode == original.mode
        assert restored.iso_path == original.iso_path
        assert restored.keep_iso == original.keep_iso
        assert restored.nocloud_net_url == original.nocloud_net_url


class TestCloudInitModeBehavior:
    """Test mode-specific behavior validation."""

    def test_custom_mode_requires_iso_path(self):
        """Test that CUSTOM mode should have iso_path."""
        # This is a validation that should be in __post_init__
        config = CloudInitConfig(mode=CloudInitMode.CUSTOM)
        # Currently no validation - just document expected behavior
        assert config.mode == CloudInitMode.CUSTOM
        assert config.iso_path is None

    def test_nocloud_net_mode_with_url(self):
        """Test NO_CLOUD_NET mode with URL."""
        config = CloudInitConfig(
            mode=CloudInitMode.NO_CLOUD_NET,
            nocloud_net_url="http://10.0.0.1:8080/",
        )
        assert config.mode == CloudInitMode.NO_CLOUD_NET
        assert config.nocloud_net_url == "http://10.0.0.1:8080/"

    def test_disabled_mode_ignores_other_fields(self):
        """Test DISABLED mode behavior."""
        config = CloudInitConfig(
            mode=CloudInitMode.DISABLED,
            iso_path=Path("/ignored.iso"),
            nocloud_net_url="http://ignored/",
        )
        assert config.mode == CloudInitMode.DISABLED
        # Fields still exist but should be ignored by business logic
        assert config.iso_path == Path("/ignored.iso")
```

---

## Phase 2: Model Implementation (Green Phase)

### 2.1 Create Cloud Init Model Module
**File:** `src/mvmctl/models/cloud_init.py`

```python
"""Cloud-init configuration models for MicroVM Manager."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum, auto
from pathlib import Path
from typing import Any

from mvmctl.constants import DEFAULT_CLOUD_INIT_MODE


class CloudInitMode(StrEnum):
    """Cloud-init configuration mode.

    Attributes:
        AUTO: Generate cloud-init ISO from config files (default).
        CUSTOM: Use a pre-existing custom cloud-init ISO.
        DISABLED: Skip cloud-init entirely (no ISO mounted).
        NO_CLOUD_NET: Serve cloud-init files via HTTP (nocloud-net datasource).
    """

    AUTO = "auto"
    CUSTOM = "custom"
    DISABLED = "disabled"
    NO_CLOUD_NET = "nocloud-net"


class CloudInitStatus(StrEnum):
    """Cloud-init execution status based on console log detection."""

    PENDING = auto()  # Console log file doesn't exist yet
    RUNNING = auto()  # Console log exists but no "done" marker found
    DONE = auto()     # Final message marker detected in console log
    ERROR = auto()     # Error state (for future use)


@dataclass
class CloudInitConfig:
    """Cloud-init configuration for a VM.

    This dataclass represents the cloud-init configuration state for a VM,
    including the mode (ISO, nocloud-net, disabled), paths, and URLs.

    Attributes:
        mode: The cloud-init mode (auto, custom, disabled, nocloud-net).
        iso_path: Path to custom cloud-init ISO (for CUSTOM mode).
        keep_iso: Whether to keep the generated ISO after VM creation.
        nocloud_net_url: URL for nocloud-net HTTP server.
    """

    mode: CloudInitMode = DEFAULT_CLOUD_INIT_MODE
    iso_path: Path | None = None
    keep_iso: bool = False
    nocloud_net_url: str | None = None

    def to_dict(self) -> dict[str, Any]:
        """Serialize CloudInitConfig to a dictionary.

        Returns:
            Dictionary representation of the config.
        """
        return {
            "mode": self.mode.value,
            "iso_path": str(self.iso_path) if self.iso_path else None,
            "keep_iso": self.keep_iso,
            "nocloud_net_url": self.nocloud_net_url,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> CloudInitConfig:
        """Deserialize CloudInitConfig from a dictionary.

        Args:
            data: Dictionary containing cloud-init configuration.

        Returns:
            New CloudInitConfig instance.
        """
        mode_value = data.get("mode")
        mode = CloudInitMode(mode_value) if mode_value else DEFAULT_CLOUD_INIT_MODE

        iso_path_str = data.get("iso_path")
        iso_path = Path(iso_path_str) if iso_path_str else None

        return cls(
            mode=mode,
            iso_path=iso_path,
            keep_iso=data.get("keep_iso", False),
            nocloud_net_url=data.get("nocloud_net_url"),
        )
```

### 2.2 Update Constants (Add Default)
**File:** `src/mvmctl/constants.py` (add if not exists)

```python
from mvmctl.models.cloud_init import CloudInitMode

DEFAULT_CLOUD_INIT_MODE = CloudInitMode.AUTO
```

**Note:** Need to handle circular import - may need to define as string and resolve later, or define inline.

**Alternative:** Define directly in cloud_init.py to avoid circular import.

---

## Phase 3: Update Existing Models

### 3.1 Update vm.py - Remove CloudInit Types
**File:** `src/mvmctl/models/vm.py`

**Changes:**
1. Remove `CloudInitStatus` class definition (lines 36-42)
2. Remove `CloudInitMode` class definition (lines 45-58)
3. Remove `datasource_mode` field from VMConfig (line 107)
4. Remove `datasource_mode` from VMConfig.to_dict() (line 153)
5. Remove `datasource_mode` from VMConfig.from_dict() (lines 210-212)
6. Add import: `from mvmctl.models.cloud_init import CloudInitMode`
7. Update VMConfig to use CloudInitMode from new module

**VMConfig Field Changes:**
```python
@dataclass
class VMConfig:
    # ... other fields ...
    
    # REMOVE this field entirely:
    # datasource_mode: CloudInitMode = CloudInitMode.AUTO
    
    cloud_init_mode: CloudInitMode = CloudInitMode.AUTO
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False
    nocloud_net_url: str | None = None
```

**VMConfig Serialization Changes:**
```python
def to_dict(self) -> dict[str, Any]:
    return {
        # ... other fields ...
        "cloud_init_mode": self.cloud_init_mode.value,
        # REMOVE: "datasource_mode": self.datasource_mode.value,
        # ... rest ...
    }
```

**VMConfig Deserialization Changes:**
```python
@classmethod
def from_dict(cls, data: dict[str, Any]) -> VMConfig:
    return cls(
        # ... other fields ...
        cloud_init_mode=CloudInitMode(data["cloud_init_mode"])
            if data.get("cloud_init_mode")
            else CloudInitMode.AUTO,
        # REMOVE: datasource_mode=... (entire block)
        # ... rest ...
    )
```

### 3.2 VMInstance Updates
**File:** `src/mvmctl/models/vm.py` (same file, VMInstance class)

VMInstance already doesn't have `datasource_mode`, so only need to ensure import is correct.

---

## Phase 4: Update Module Exports

### 4.1 Update models/__init__.py
**File:** `src/mvmctl/models/__init__.py`

```python
"""Data models for MicroVM Manager."""

from mvmctl.models.cloud_init import CloudInitConfig, CloudInitMode, CloudInitStatus
from mvmctl.models.image import ImageSpec
from mvmctl.models.kernel import KernelSpec
from mvmctl.models.vm import VMConfig, VMInstance, VMState

__all__ = [
    "CloudInitConfig",  # NEW
    "CloudInitMode",
    "CloudInitStatus",  # NEW - now exported
    "ImageSpec",
    "KernelSpec",
    "VMConfig",
    "VMInstance",
    "VMState",
]
```

---

## Phase 5: Update All Import Sites

### 5.1 Files to Update

| File | Current Import | New Import |
|------|----------------|------------|
| `src/mvmctl/cli/vm.py` | `from mvmctl.models.vm import CloudInitMode` | `from mvmctl.models import CloudInitMode` |
| `src/mvmctl/api/vms.py` | `from mvmctl.models.vm import CloudInitMode` | `from mvmctl.models import CloudInitMode` |
| `src/mvmctl/core/cloud_init_status.py` | `from mvmctl.models.vm import CloudInitStatus` | `from mvmctl.models import CloudInitStatus` |
| `src/mvmctl/core/config_gen.py` | `from mvmctl.models.vm import CloudInitMode` | `from mvmctl.models import CloudInitMode` |
| `src/mvmctl/core/vm_lifecycle.py` | `from mvmctl.models.vm import CloudInitMode` | `from mvmctl.models import CloudInitMode` |

### 5.2 Test Files to Update

| File | Change |
|------|--------|
| `tests/unit/test_cloud_init_status.py` | Update import path |
| `tests/unit/test_config_gen.py` | Update import path |
| `tests/unit/test_vm_lifecycle.py` | Update import path, remove any datasource_mode references |
| `tests/integration/test_nocloud_net_lifecycle.py` | Update import path |

---

## Phase 6: Remove datasource_mode References

### 6.1 Files to Check for datasource_mode

Based on research, `datasource_mode` appears **only in models/vm.py**:
- Definition: line 107
- Serialization: line 153
- Deserialization: lines 210-212

**No other files reference datasource_mode** - it's dead code.

### 6.2 Test Updates

Search and remove any test fixtures or test data that includes `datasource_mode`:

```bash
grep -r "datasource_mode" tests/
```

If found:
- Remove from test fixtures
- Remove from expected JSON structures
- Remove from test assertions

---

## Phase 7: Verification Commands

After all changes, run:

```bash
# 1. Run new cloud-init model tests
uv run pytest tests/unit/models/test_cloud_init.py -v

# 2. Run all unit tests
uv run pytest tests/unit/ -x -q

# 3. Run integration tests  
uv run pytest tests/integration/ -x -q

# 4. Lint check
uv run ruff check src/ tests/

# 5. Type check
uv run mypy src/

# 6. Format check
uv run ruff format --check src/ tests/

# 7. Full test suite with coverage
uv run pytest tests/ -q --cov=src/mvmctl --cov-report=term-missing
```

---

## Phase 8: Behavior Preservation Checklist

### 8.1 Nocloud-Net Default Mode

**Verify:** AUTO mode still defaults to nocloud-net in vm_lifecycle.py

```python
# In core/vm_lifecycle.py around line 434-438
if cloud_init_mode == CloudInitMode.AUTO:
    effective_mode = CloudInitMode.NO_CLOUD_NET
else:
    effective_mode = cloud_init_mode
```

**Check:** Import statement updated correctly.

### 8.2 Explicit ISO Mode

**Verify:** CUSTOM mode still works for custom ISO paths

```python
# In core/vm_lifecycle.py around line 533
if cloud_init_mode == CloudInitMode.CUSTOM:
    # Handle custom ISO
```

### 8.3 VM State Serialization

**Verify:** VMConfig and VMInstance serialization still works

- Test with existing VM state files (backward compat for state files)
- Ensure no `datasource_mode` in new state files
- Ensure old state files without `datasource_mode` load correctly

### 8.4 VM State Deserialization

**Verify:** Existing VM state files load correctly

Old state files may have `datasource_mode` - we should:
- Option A: Ignore the field during deserialization (safest)
- Option B: Fail if field present (strict)

**Recommendation:** Option A - ignore unknown fields for forward compatibility.

---

## Atomic Commit Strategy

Each commit should be a logical, working state:

### Commit 1: Create Cloud Init Model (Tests + Implementation)
```
Create cloud-init model module with CloudInitConfig

- Add CloudInitMode, CloudInitStatus, CloudInitConfig to cloud_init.py
- Add comprehensive unit tests
- All tests pass
```

### Commit 2: Update vm.py - Remove Types and datasource_mode
```
Remove cloud-init types from vm.py and datasource_mode field

- Import CloudInitMode from cloud_init module
- Remove CloudInitMode and CloudInitStatus class definitions
- Remove datasource_mode field from VMConfig
- Remove datasource_mode serialization
- Remove datasource_mode deserialization
- Update tests
```

### Commit 3: Update Module Exports and Imports
```
Update model exports and all import sites

- Update models/__init__.py exports
- Update all production import statements
- Update all test import statements
- All tests pass
```

### Commit 4: Final Verification
```
Final verification - all quality gates pass

- Full test suite passes
- Lint clean
- Type check clean
- Format check clean
- Coverage maintained
```

---

## Risk Mitigation

| Risk | Mitigation |
|------|------------|
| Circular imports | Define CloudInitMode before importing in constants.py |
| Missing import updates | Use grep to find all CloudInitMode/CloudInitStatus imports |
| Test failures | Run tests after each commit |
| State file incompatibility | Ensure deserialization handles missing/unknown fields |
| Coverage drop | Add tests for new CloudInitConfig class |

---

## Success Criteria

- [ ] `src/mvmctl/models/cloud_init.py` exists with CloudInitConfig
- [ ] `CloudInitMode` and `CloudInitStatus` moved to cloud_init.py
- [ ] `datasource_mode` completely removed from codebase
- [ ] All imports updated to use new module paths
- [ ] All tests pass (unit + integration)
- [ ] Lint clean (`ruff check`)
- [ ] Type check clean (`mypy`)
- [ ] Format clean (`ruff format --check`)
- [ ] Coverage maintained at 80%+
- [ ] No behavioral changes to nocloud-net or ISO modes

---

## Estimated Effort

| Phase | Time Estimate |
|-------|---------------|
| Phase 1: Test Preparation | 15 min |
| Phase 2: Model Implementation | 20 min |
| Phase 3: Update Existing Models | 15 min |
| Phase 4: Update Module Exports | 5 min |
| Phase 5: Update Import Sites | 15 min |
| Phase 6: Remove datasource_mode | 10 min |
| Phase 7: Verification | 10 min |
| **Total** | **~90 min** |
