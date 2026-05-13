"""Integration tests for Kernel API operations.

Tests exercise the complete kernel orchestration flow:
  pull → list → get → inspect → set_default → remove

Only subprocess and HTTP download operations are mocked. ALL orchestration
logic in api/ and core/ runs unmocked.
"""

from __future__ import annotations

import pytest

from mvmctl.api import KernelInput, KernelOperation, KernelPullInput
from mvmctl.exceptions import KernelNotFoundError
from mvmctl.models import (
    KernelItem,
    KernelPullResult,
    VMInstanceItem,
    VMStatus,
)
from mvmctl.models.result import BatchResult, OperationResult
from mvmctl.utils.common import CacheUtils

# ======================================================================
# Helpers
# ======================================================================


def _mock_pull_firecracker_kernel(
    cls: type,  # noqa: ARG001
    spec: object,  # noqa: ARG001
    ci_version: str,  # noqa: ARG001
    arch: str,
    output_dir: object,  # noqa: ARG001
    **kwargs: object,  # noqa: ARG001
) -> KernelPullResult:
    """Return a fake firecracker kernel result pointing to a real file."""
    kernels_dir = CacheUtils.get_kernels_dir()
    fake_path = kernels_dir / "vmlinux-firecracker-6.1.0-x86_64"
    fake_path.write_text("fake firecracker kernel")
    return KernelPullResult(
        path=fake_path,
        version="6.1.0",
        arch=arch,
        kernel_type="firecracker",
    )


# ======================================================================
# Kernel pull tests
# ======================================================================


class TestKernelPull:
    """Test kernel pull through the real API."""

    def test_pull_firecracker_kernel(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Pull a firecracker kernel and verify the DB record."""
        monkeypatch.setattr(
            "mvmctl.core.kernel._service.KernelService.fetch_firecracker_kernel",
            classmethod(_mock_pull_firecracker_kernel),
        )

        result = KernelOperation.pull(
            KernelPullInput(kernel_type="firecracker", version="6.1.0")
        )

        assert isinstance(result, OperationResult)
        assert result.status == "success"
        assert isinstance(result.item, KernelItem)
        assert result.item.type == "firecracker"
        assert result.item.version == "6.1.0"
        assert result.item.arch == "x86_64"
        assert result.item.name == "vmlinux-firecracker-6.1.0-x86_64"
        assert result.item.base_name == "vmlinux"
        assert result.item.is_present is True
        assert result.item.resolved_path.exists()

    def test_pull_invalid_kernel_type(self) -> None:
        """Pulling with an unsupported kernel type returns error."""
        result = KernelOperation.pull(
            KernelPullInput(kernel_type="invalid", version="6.1.0")
        )
        assert result.status == "error"

    def test_pull_invalid_version_format(self) -> None:
        """Pulling with an invalid version format returns error."""
        result = KernelOperation.pull(
            KernelPullInput(kernel_type="official", version="not-a-version")
        )
        assert result.status == "error"


# ======================================================================
# Kernel list and get tests
# ======================================================================


class TestKernelListAndGet:
    """Test kernel listing and retrieval through the real API."""

    def test_list_all_contains_seeded_kernel(self) -> None:
        """list_all() returns the seeded test kernel."""
        kernels = KernelOperation.list_all()
        names = [k.name for k in kernels]
        assert "vmlinux" in names

    def test_get_by_id_prefix(self) -> None:
        """Get a kernel by its ID prefix (first 6 chars of seeded ID)."""
        # Seeded kernel ID is "a" repeated 64 times
        kernel = KernelOperation.get(KernelInput(name=["aaaaaa"]))
        assert kernel.name == "vmlinux"
        assert kernel.version == "6.1.0"


# ======================================================================
# Kernel inspect tests
# ======================================================================


class TestKernelInspect:
    """Test kernel inspection through the real API."""

    def test_inspect_returns_kernel_item(self) -> None:
        """inspect() returns a KernelItem by default."""
        result = KernelOperation.inspect(KernelInput(id=["a" * 64]))
        assert isinstance(result, KernelItem)
        assert result.name == "vmlinux"
        assert result.version == "6.1.0"
        assert result.is_default

    def test_inspect_returns_dict_when_json(self) -> None:
        """inspect(is_json=True) returns a dict representation."""
        result = KernelOperation.inspect(
            KernelInput(id=["a" * 64]), is_json=True
        )
        assert isinstance(result, dict)
        assert result["name"] == "vmlinux"
        assert result["version"] == "6.1.0"
        assert result["is_default"]
        assert "id" in result
        assert "base_name" in result
        assert "arch" in result
        assert "type" in result


# ======================================================================
# Kernel default tests
# ======================================================================


class TestKernelDefault:
    """Test kernel default management through the real API."""

    def test_set_default(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Set a pulled kernel as default and verify is_default=True."""
        monkeypatch.setattr(
            "mvmctl.core.kernel._service.KernelService.fetch_firecracker_kernel",
            classmethod(_mock_pull_firecracker_kernel),
        )

        fetched = KernelOperation.pull(
            KernelPullInput(kernel_type="firecracker", version="6.1.0")
        )

        # Initially the seeded kernel is default, not the pulled one
        assert not fetched.item.is_default

        KernelOperation.set_default(KernelInput(id=[fetched.item.id]))

        # Verify the pulled kernel is now default
        kernel = KernelOperation.get(KernelInput(id=[fetched.item.id]))
        assert kernel.is_default

        # Verify the old default is no longer default
        old = KernelOperation.get(KernelInput(id=["a" * 64]))
        assert not old.is_default


# ======================================================================
# Kernel remove tests
# ======================================================================


class TestKernelRemove:
    """Test kernel removal through the real API."""

    def test_remove_kernel(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Pull a kernel, verify it exists, then remove it."""
        monkeypatch.setattr(
            "mvmctl.core.kernel._service.KernelService.fetch_firecracker_kernel",
            classmethod(_mock_pull_firecracker_kernel),
        )

        fetched = KernelOperation.pull(
            KernelPullInput(kernel_type="firecracker", version="6.1.0")
        )

        # Verify it exists in list
        kernels_before = KernelOperation.list_all()
        assert any(k.name == fetched.item.name for k in kernels_before)

        KernelOperation.remove(KernelInput(id=[fetched.item.id]))

        # Verify it's gone from list_all
        kernels_after = KernelOperation.list_all()
        assert not any(k.name == fetched.item.name for k in kernels_after)

        # Verify file is removed from disk
        assert not fetched.item.resolved_path.exists()

    def test_remove_nonexistent_kernel(self) -> None:
        """Removing a non-existent kernel raises KernelNotFoundError."""
        from mvmctl.exceptions import KernelNotFoundError

        with pytest.raises(KernelNotFoundError):
            KernelOperation.remove(KernelInput(name=["nonexistent-kernel"]))

    def test_remove_kernel_referenced_by_vm(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Removing a kernel referenced by a VM raises KernelError without force."""
        from mvmctl.core._shared import Database
        from mvmctl.core.vm._repository import VMRepository

        db = Database()
        vm_repo = VMRepository(db)
        kernel = KernelOperation.get(KernelInput(id=["a" * 64]))

        # Seed a VM that references the default kernel
        vm_repo.upsert(
            VMInstanceItem(
                id="x" * 64,
                name="test-vm-kernel-ref",
                status=VMStatus.STOPPED,
                pid=0,
                ipv4="10.20.0.10",
                mac="02:FC:00:00:00:01",
                network_id="c" * 64,
                tap_device="tap0",
                image_id="b" * 64,
                kernel_id=kernel.id,
                binary_id="d" * 64,
                api_socket_path="/tmp/test.sock",
                config_path="/tmp/test.json",
                cloud_init_mode="off",
                vcpu_count=2,
                mem_size_mib=256,
                disk_size_mib=1024,
                rootfs_path="/tmp/test.ext4",
                rootfs_suffix="ext4",
                enable_pci=False,
                enable_logging=False,
                enable_metrics=False,
                enable_console=False,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        # Without force, removal should fail because a VM references the kernel
        result = KernelOperation.remove(KernelInput(id=[kernel.id]))
        assert isinstance(result, BatchResult)
        assert result.has_any_error

        # With force=True it should succeed (soft delete since VM references it)
        KernelOperation.remove(KernelInput(id=[kernel.id]), force=True)

        # After forced removal, kernel should be soft-deleted (not in list_all)
        kernels = KernelOperation.list_all()
        assert not any(k.name == "vmlinux" for k in kernels)

        # File should still be removed from disk even for soft-delete
        assert not kernel.resolved_path.exists()


# ======================================================================
# Edge cases
# ======================================================================


class TestKernelEdgeCases:
    """Test edge cases and error handling in kernel operations."""

    def test_get_nonexistent_kernel(self) -> None:
        """Getting a non-existent kernel raises KernelNotFoundError."""
        with pytest.raises(KernelNotFoundError):
            KernelOperation.get(KernelInput(name=["no-such-kernel"]))

    def test_list_all_returns_list(self) -> None:
        """list_all() always returns a list containing the seeded kernel."""
        kernels = KernelOperation.list_all()
        assert isinstance(kernels, list)
        assert len(kernels) >= 1
        assert any(k.name == "vmlinux" for k in kernels)
