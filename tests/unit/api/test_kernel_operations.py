"""Tests for KernelOperation — kernel management orchestration."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from mvmctl.api.kernel_operations import KernelOperation
from mvmctl.exceptions import KernelError
from mvmctl.models import KernelItem
from mvmctl.models.result import OperationResult


def _make_kernel(name="vmlinux-5.10", kernel_id="kern-001", **kw):
    defaults = dict(
        id=kernel_id,
        name=name,
        base_name="vmlinux",
        version="5.10.0",
        arch="x86_64",
        type="firecracker",
        path="/cache/kernels/vmlinux-5.10",
        is_default=False,
        is_present=True,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )
    defaults.update(kw)
    return KernelItem(**defaults)


class TestKernelPull:
    """Tests for KernelOperation.pull()."""

    def test_pull_firecracker_type(self, mocker):
        """pull() downloads a firecracker kernel."""
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_request = MagicMock()
        resolved = MagicMock(
            kernel_type="firecracker",
            version="5.10",
            arch="x86_64",
            output_dir=Path("/out"),
            set_default=True,
            kernel_config=None,
            jobs=None,
            keep_build_dir=False,
            clean_build=False,
        )
        mock_request.resolve.return_value = resolved
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelPullRequest",
            return_value=mock_request,
        )

        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService.get_specs_for",
            return_value=[MagicMock()],
        )
        mock_fetch_result = MagicMock(
            path=Path("/kernels/vmlinux-5.10"), version="5.10.0"
        )
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService.fetch_firecracker_kernel",
            return_value=mock_fetch_result,
        )
        mocker.patch("mvmctl.api.kernel_operations.BinaryService")
        mocker.patch("mvmctl.api.kernel_operations.BinaryRepository")
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService.parse_filename",
            return_value=MagicMock(base_name="vmlinux"),
        )
        mocker.patch(
            "mvmctl.api.kernel_operations.HashGenerator.kernel",
            return_value="kern-hash",
        )
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelRepository",
            return_value=mock_repo,
        )
        mocker.patch("mvmctl.api.kernel_operations.AuditLog")

        result = KernelOperation.pull(
            MagicMock(kernel_type="firecracker", version="5.10")
        )
        assert isinstance(result, OperationResult)
        assert result.item.id == "kern-hash"

    def test_pull_returns_existing(self, mocker):
        """pull() returns existing kernel if file is on disk."""
        existing = _make_kernel(path="/existing/vmlinux")
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_request = MagicMock()
        resolved = MagicMock(kernel_type="firecracker", version="5.10")
        mock_request.resolve.return_value = resolved
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelPullRequest",
            return_value=mock_request,
        )
        mock_repo = MagicMock()
        mock_repo.get_by_type.return_value = existing
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelRepository",
            return_value=mock_repo,
        )
        mocker.patch(
            "mvmctl.core.kernel._repository.KernelRepository.get_by_type",
            return_value=existing,
        )

        mocker.patch.object(Path, "exists", return_value=True)

        result = KernelOperation.pull(
            MagicMock(kernel_type="firecracker", version="5.10")
        )
        assert result.item.id == existing.id

    def test_pull_raises_on_bad_spec_count(self, mocker):
        """pull() raises KernelError when spec count != 1."""
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_request = MagicMock()
        resolved = MagicMock(kernel_type="firecracker", version="5.10")
        mock_request.resolve.return_value = resolved
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelPullRequest",
            return_value=mock_request,
        )
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService.get_specs_for",
            return_value=[],
        )
        result = KernelOperation.pull(MagicMock(kernel_type="firecracker"))
        assert result.status == "error"


class TestKernelRemove:
    """Tests for KernelOperation.remove()."""

    def test_remove_delegates_to_service(self, mocker):
        """remove() calls service.remove_many()."""
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_request = MagicMock()
        resolved = MagicMock(kernels=[_make_kernel()])
        mock_request.resolve.return_value = resolved
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        mock_service_instance = MagicMock()
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService",
            return_value=mock_service_instance,
        )
        mocker.patch("mvmctl.api.kernel_operations.AuditLog")

        KernelOperation.remove(MagicMock(identifiers=["kern-001"]))
        mock_service_instance.remove.assert_called_once()

    def test_remove_force_flag(self, mocker):
        """remove() passes force flag to service."""
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[_make_kernel()])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        mock_service_instance = MagicMock()
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService",
            return_value=mock_service_instance,
        )
        mocker.patch("mvmctl.api.kernel_operations.AuditLog")

        KernelOperation.remove(MagicMock(identifiers=["kern-001"]), force=True)
        args, kwargs = mock_service_instance.remove.call_args
        assert kwargs.get("force") is True

    def test_remove_logs_audit(self, mocker):
        """remove() logs audit for each kernel."""
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_request = MagicMock()
        resolved = MagicMock(
            kernels=[
                _make_kernel(name="k1", kernel_id="id1"),
                _make_kernel(name="k2", kernel_id="id2"),
            ]
        )
        mock_request.resolve.return_value = resolved
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.kernel_operations.KernelService")
        mock_audit = mocker.patch("mvmctl.api.kernel_operations.AuditLog")

        KernelOperation.remove(MagicMock(identifiers=["id1", "id2"]))
        assert mock_audit.log.call_count == 2


class TestKernelListAll:
    def test_list_all(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_service = MagicMock()
        mock_service.list_all.return_value = [_make_kernel()]
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService",
            return_value=mock_service,
        )
        result = KernelOperation.list_all()
        assert len(result) == 1

    def test_list_all_empty(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mocker.patch("mvmctl.api.kernel_operations.KernelRepository")
        mock_service = MagicMock()
        mock_service.list_all.return_value = []
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelService",
            return_value=mock_service,
        )
        assert KernelOperation.list_all() == []


class TestKernelGet:
    def test_get_single(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        kern = _make_kernel()
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[kern])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        result = KernelOperation.get(MagicMock(identifiers=["kern-001"]))
        assert result.id == "kern-001"

    def test_get_raises_on_multiple(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(
            kernels=[_make_kernel(), _make_kernel(kernel_id="kern-002")]
        )
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        with pytest.raises(KernelError, match="Expected exactly one"):
            KernelOperation.get(MagicMock(identifiers=["kern"]))

    def test_get_raises_on_zero(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        with pytest.raises(KernelError, match="Expected exactly one"):
            KernelOperation.get(MagicMock(identifiers=["nonexistent"]))


class TestKernelInspect:
    def test_inspect_returns_item(self, mocker):
        kern = _make_kernel()
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[kern])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        result = KernelOperation.inspect(MagicMock(identifiers=["kern-001"]))
        assert isinstance(result, KernelItem)

    def test_inspect_json(self, mocker):
        kern = _make_kernel()
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[kern])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        result = KernelOperation.inspect(
            MagicMock(identifiers=["kern-001"]), is_json=True
        )
        assert isinstance(result, dict)
        assert result["id"] == "kern-001"


class TestKernelSetDefault:
    def test_set_default_success(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelRepository",
            return_value=mock_repo,
        )
        kern = _make_kernel()
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[kern])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.kernel_operations.KernelController")
        mocker.patch("mvmctl.api.kernel_operations.AuditLog")

        KernelOperation.set_default(MagicMock(identifiers=["kern-001"]))

    def test_set_default_success(self, mocker):
        mocker.patch("mvmctl.api.kernel_operations.Database")
        mock_repo = MagicMock()
        mocker.patch(
            "mvmctl.api.kernel_operations.KernelRepository",
            return_value=mock_repo,
        )
        kern = _make_kernel()
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(kernels=[kern])
        mocker.patch(
            "mvmctl.api.inputs._kernel_input.KernelRequest",
            return_value=mock_request,
        )
        mocker.patch("mvmctl.api.kernel_operations.KernelController")
        mocker.patch("mvmctl.api.kernel_operations.AuditLog")
        KernelOperation.set_default(MagicMock(identifiers=["kern-001"]))
