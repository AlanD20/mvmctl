"""Tests for BinaryOperation — binary management orchestration."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.api.binary_operations import BinaryOperation
from mvmctl.exceptions import BinaryError
from mvmctl.models import BinaryItem
from mvmctl.models.result import BatchResult, OperationResult


def _make_bin(name="firecracker", version="1.15.0", bin_id="bin-001", **kw):
    defaults = dict(
        id=bin_id,
        name=name,
        version=version,
        full_version=version,
        ci_version=version,
        path=f"/cache/bin/{name}-{version}",
        is_default=False,
        is_present=True,
        created_at="2026-01-01T12:00:00+00:00",
        updated_at="2026-01-01T12:00:00+00:00",
    )
    defaults.update(kw)
    return BinaryItem(**defaults)


class TestBinaryFetch:
    """Tests for BinaryOperation.fetch()."""

    def test_fetch_downloads_when_not_exists(self, mocker):
        """fetch() downloads when version doesn't exist in DB."""
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name_and_version.return_value = None
        mock_repo.get_default.return_value = None
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository",
                     return_value=mock_repo)
        mock_request = MagicMock()
        resolved = MagicMock(version="1.15.0", set_as_default=False,
                             download_override=False, bin_dir=MagicMock())
        mock_request.resolve.return_value = resolved
        mocker.patch("mvmctl.api.binary_operations.BinaryFetchRequest",
                     return_value=mock_request)
        bins = [_make_bin(name="firecracker"), _make_bin(name="jailer")]
        mocker.patch("mvmctl.api.binary_operations.BinaryService.download_firecracker",
                     return_value=bins)
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        result = BinaryOperation.fetch(MagicMock(version="1.15.0"))
        assert len(result.item) == 2
        mock_repo.upsert.assert_called()

    def test_fetch_early_return_when_exists(self, mocker):
        """fetch() returns existing binaries without download."""
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_repo = MagicMock()
        fc = _make_bin(name="firecracker")
        jl = _make_bin(name="jailer")
        mock_repo.get_by_name_and_version.side_effect = lambda n, v: fc if n == "firecracker" else jl
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository",
                     return_value=mock_repo)
        mock_request = MagicMock()
        resolved = MagicMock(version="1.15.0", set_as_default=False,
                             download_override=False)
        mock_request.resolve.return_value = resolved
        mocker.patch("mvmctl.api.binary_operations.BinaryFetchRequest",
                     return_value=mock_request)
        mock_download = mocker.patch(
            "mvmctl.api.binary_operations.BinaryService.download_firecracker"
        )

        result = BinaryOperation.fetch(MagicMock(version="1.15.0"))
        assert len(result.item) == 2
        mock_download.assert_not_called()

    def test_fetch_sets_default_when_no_default_exists(self, mocker):
        """fetch() sets binary as default when no default exists."""
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name_and_version.return_value = None
        mock_repo.get_default.return_value = None  # no default
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository",
                     return_value=mock_repo)
        mock_request = MagicMock()
        resolved = MagicMock(version="1.15.0", set_as_default=False,
                             download_override=False, bin_dir=MagicMock())
        mock_request.resolve.return_value = resolved
        mocker.patch("mvmctl.api.binary_operations.BinaryFetchRequest",
                     return_value=mock_request)
        bins = [_make_bin(name="firecracker"), _make_bin(name="jailer")]
        mocker.patch("mvmctl.api.binary_operations.BinaryService.download_firecracker",
                     return_value=bins)
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        BinaryOperation.fetch(MagicMock(version="1.15.0"))
        # Should set is_default=True since no default exists
        calls = mock_repo.upsert.call_args_list
        for call in calls:
            assert call[0][0].is_default is True

    def test_fetch_download_override(self, mocker):
        """fetch() downloads even when exists if download_override=True."""
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_repo = MagicMock()
        mock_repo.get_by_name_and_version.return_value = _make_bin()
        mock_repo.get_default.return_value = _make_bin(is_default=True)
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository",
                     return_value=mock_repo)
        mock_request = MagicMock()
        resolved = MagicMock(version="1.15.0", set_as_default=False,
                             download_override=True, bin_dir=MagicMock())
        mock_request.resolve.return_value = resolved
        mocker.patch("mvmctl.api.binary_operations.BinaryFetchRequest",
                     return_value=mock_request)
        bins = [_make_bin(name="firecracker"), _make_bin(name="jailer")]
        mock_download = mocker.patch(
            "mvmctl.api.binary_operations.BinaryService.download_firecracker",
            return_value=bins,
        )
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        BinaryOperation.fetch(MagicMock(version="1.15.0"))
        mock_download.assert_called_once()


class TestBinaryRemove:
    def test_remove_delegates(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(
            binaries=[_make_bin(name="firecracker")]
        )
        mocker.patch("mvmctl.api.binary_operations.BinaryRequest",
                     return_value=mock_request)
        mock_service = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        result = BinaryOperation.remove(MagicMock(identifiers=["bin-001"]))
        assert isinstance(result, BatchResult)
        assert len(result.items) > 0

    def test_remove_force(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(
            binaries=[_make_bin()]
        )
        mocker.patch("mvmctl.api.binary_operations.BinaryRequest",
                     return_value=mock_request)
        mock_service = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        result = BinaryOperation.remove(MagicMock(identifiers=["bin-001"]), force=True)
        assert isinstance(result, BatchResult)


class TestBinaryRemoveByVersion:
    def test_removes_both_binaries(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_repo = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository",
                     return_value=mock_repo)
        mock_resolver = MagicMock()
        mock_resolver.by_name_version.side_effect = [
            _make_bin(name="firecracker", version="1.15.0"),
            _make_bin(name="jailer", version="1.15.0"),
        ]
        mocker.patch("mvmctl.api.binary_operations.BinaryResolver",
                     return_value=mock_resolver)
        mock_service = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        BinaryOperation.remove_by_version("1.15.0")
        args, kwargs = mock_service.remove_many.call_args
        assert len(args[0]) == 2

    def test_remove_by_version_partial(self, mocker):
        """remove_by_version() handles when one binary not found."""
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_resolver = MagicMock()
        mock_resolver.by_name_version.side_effect = [
            _make_bin(name="firecracker"),
            BinaryError("not found"),  # jailer not found
        ]
        mocker.patch("mvmctl.api.binary_operations.BinaryResolver",
                     return_value=mock_resolver)
        mock_service = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        mocker.patch("mvmctl.api.binary_operations.AuditLog")
        from mvmctl.exceptions import BinaryNotFoundError
        mock_resolver.by_name_version.side_effect = [
            _make_bin(name="firecracker"),
            BinaryNotFoundError("not found"),
        ]

        BinaryOperation.remove_by_version("1.15.0")
        # Should still remove firecracker even if jailer not found
        assert mock_service.remove_many.call_count == 1

    def test_remove_by_version_skips_if_none_found(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        from mvmctl.exceptions import BinaryNotFoundError
        mock_resolver = MagicMock()
        mock_resolver.by_name_version.side_effect = [
            BinaryNotFoundError("not found"),
            BinaryNotFoundError("not found"),
        ]
        mocker.patch("mvmctl.api.binary_operations.BinaryResolver",
                     return_value=mock_resolver)
        mock_service = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)

        BinaryOperation.remove_by_version("1.14.0")
        mock_service.remove_many.assert_not_called()


class TestBinaryGet:
    def test_get_returns_binaries(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(
            binaries=[_make_bin()]
        )
        mocker.patch("mvmctl.api.binary_operations.BinaryRequest",
                     return_value=mock_request)
        result = BinaryOperation.get(MagicMock(identifiers=["bin-001"]))
        assert len(result) == 1


class TestBinaryListLocal:
    def test_list_local(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_service = MagicMock()
        mock_service.list_local.return_value = [_make_bin()]
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        assert len(BinaryOperation.list_local()) == 1

    def test_list_local_empty(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_service = MagicMock()
        mock_service.list_local.return_value = []
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        assert BinaryOperation.list_local() == []


class TestBinaryListRemote:
    def test_list_remote_with_limit(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.BinaryService.list_remote",
                     return_value=["1.15.0", "1.14.0"])
        result = BinaryOperation.list_remote(limit=2)
        assert len(result) == 2

    def test_list_remote_default_limit(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.SettingsService.resolve",
                     return_value="10")
        mocker.patch("mvmctl.api.binary_operations.BinaryService.list_remote",
                     return_value=[f"1.{i}.0" for i in range(10)])
        result = BinaryOperation.list_remote()
        assert len(result) == 10


class TestBinarySetDefault:
    def test_set_default_success(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(
            binaries=[_make_bin()]
        )
        mocker.patch("mvmctl.api.binary_operations.BinaryRequest",
                     return_value=mock_request)
        mocker.patch("mvmctl.api.binary_operations.BinaryController")
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        BinaryOperation.set_default(MagicMock(identifiers=["bin-001"]))

    def test_set_default_raises_on_ambiguous(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_request = MagicMock()
        mock_request.resolve.return_value = MagicMock(
            binaries=[_make_bin(), _make_bin(bin_id="bin-002")]
        )
        mocker.patch("mvmctl.api.binary_operations.BinaryRequest",
                     return_value=mock_request)
        result = BinaryOperation.set_default(MagicMock(identifiers=["bin"]))
        assert result.status == "error"


class TestBinaryEnsureDefault:
    def test_ensure_default_returns_none_when_no_local(self, mocker):
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_service = MagicMock()
        mock_service.list_local.return_value = []
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        result = BinaryOperation.ensure_default()
        assert result.status == "skipped" or result.item is None

    def test_ensure_default_returns_existing(self, mocker):
        default = _make_bin(is_default=True)
        mocker.patch("mvmctl.api.binary_operations.Database")
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository")
        mock_service = MagicMock()
        mock_service.list_local.return_value = [_make_bin(), default]
        mock_service.get_default_firecracker.return_value = default
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        result = BinaryOperation.ensure_default()
        assert result.item.is_default is True

    def test_ensure_default_sets_latest(self, mocker):
        """ensure_default() sets latest version when no default."""
        mocker.patch("mvmctl.api.binary_operations.Database")
        mock_repo = MagicMock()
        mocker.patch("mvmctl.api.binary_operations.BinaryRepository",
                     return_value=mock_repo)
        mock_service = MagicMock()
        old = _make_bin(version="1.14.0", name="firecracker")
        new = _make_bin(version="1.15.0", name="firecracker")
        mock_service.list_local.return_value = [old, new]
        mock_service.get_default_firecracker.return_value = None
        mocker.patch("mvmctl.api.binary_operations.BinaryService",
                     return_value=mock_service)
        mocker.patch("mvmctl.api.binary_operations.BinaryController")
        mocker.patch("mvmctl.api.binary_operations.AuditLog")

        result = BinaryOperation.ensure_default()
        assert result.item.version == "1.15.0"
