"""Unit tests for KernelService — stateless kernel operations.

Parse filename tests run against real business logic.
Fetch/build tests mock HTTP/subprocess calls but exercise real orchestration.
"""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

import pytest

from mvmctl.core._shared import Database
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.kernel._service import KernelService, ParsedKernelFilename
from mvmctl.exceptions import HttpDownloadError, KernelError
from mvmctl.models import KernelItem, KernelSpec

# ---------------------------------------------------------------------------
# parse_filename tests
# ---------------------------------------------------------------------------


class TestParseFilename:
    """Tests for KernelService.parse_filename()."""

    def test_firecracker_with_v_prefix(self) -> None:
        result = KernelService.parse_filename("vmlinux-fc-v1.15-x86_64")
        assert isinstance(result, ParsedKernelFilename)
        # New parser takes only the first dash-delimited component
        assert result.base_name == "vmlinux"
        assert result.version == "v1.15"
        assert result.arch == "x86_64"

    def test_firecracker_without_v_prefix(self) -> None:
        result = KernelService.parse_filename("vmlinux-fc-1.15-arm64")
        assert result.base_name == "vmlinux"
        assert result.version == "1.15"
        assert result.arch == "arm64"

    def test_official(self) -> None:
        result = KernelService.parse_filename("vmlinux-6.1.102")
        assert result.base_name == "vmlinux"
        assert result.version == "6.1.102"
        assert result.arch == "-"

    def test_plain_vmlinux(self) -> None:
        result = KernelService.parse_filename("vmlinux")
        assert result.base_name == "vmlinux"
        assert result.version == "-"
        assert result.arch == "-"

    def test_amd64_arch(self) -> None:
        result = KernelService.parse_filename("vmlinux-fc-1.12-amd64")
        assert result.base_name == "vmlinux"
        assert result.version == "1.12"
        assert result.arch == "amd64"

    def test_aarch64_arch(self) -> None:
        result = KernelService.parse_filename("vmlinux-6.1-aarch64")
        assert result.base_name == "vmlinux"
        assert result.version == "6.1"
        assert result.arch == "aarch64"

    def test_empty_string(self) -> None:
        result = KernelService.parse_filename("")
        assert result.base_name == ""
        assert result.version == "-"
        assert result.arch == "-"

    def test_only_arch_suffix(self) -> None:
        result = KernelService.parse_filename("vmlinux-x86_64")
        assert result.base_name == "vmlinux"
        assert result.arch == "x86_64"


# ---------------------------------------------------------------------------
# get_specs_for tests
# ---------------------------------------------------------------------------


class TestGetSpecsFor:
    """Tests for KernelService.get_specs_for()."""

    def test_by_kernel_type(self) -> None:
        """Filter firecracker-type specs."""
        specs = KernelService.get_specs_for(kernel_type="firecracker")
        assert len(specs) >= 1
        for s in specs:
            assert s.kernel_type == "firecracker"

    def test_by_official_type(self) -> None:
        """Filter official-type specs."""
        specs = KernelService.get_specs_for(kernel_type="official")
        assert len(specs) >= 1
        for s in specs:
            assert s.kernel_type == "official"

    def test_by_name(self) -> None:
        """Look up a single spec by name."""
        specs = KernelService.get_specs_for(names=["kernel-firecracker"])
        assert len(specs) == 1
        assert specs[0].name == "kernel-firecracker"

    def test_by_name_not_found(self) -> None:
        """Look up a non-existent spec name raises KernelError."""
        with pytest.raises(KernelError, match="Kernel spec.*not found"):
            KernelService.get_specs_for(names=["nonexistent-spec"])

    def test_by_version(self) -> None:
        """Filter specs by version."""
        specs = KernelService.get_specs_for(version="6.1")
        assert len(specs) >= 1
        for s in specs:
            assert s.version == "6.1"

    def test_by_type_and_version(self) -> None:
        """Filter by type and version together."""
        specs = KernelService.get_specs_for(
            kernel_type="firecracker", version="6.1"
        )
        assert len(specs) >= 1
        for s in specs:
            assert s.kernel_type == "firecracker"
            assert s.version == "6.1"

    def test_all_specs_loaded(self) -> None:
        """Ensure all bundled specs are loadable."""
        specs = KernelService._load_specs()
        assert isinstance(specs, dict)
        assert len(specs) >= 1
        for name, spec in specs.items():
            assert isinstance(spec, KernelSpec)
            assert spec.name == name


# ---------------------------------------------------------------------------
# fetch_firecracker_kernel tests
# ---------------------------------------------------------------------------


class TestFetchFirecrackerKernel:
    """Tests for KernelService.fetch_firecracker_kernel()."""

    def _make_firecracker_spec(self) -> KernelSpec:
        return KernelSpec(
            name="kernel-firecracker-test",
            kernel_type="firecracker",
            version="6.1",
            source="https://example.com/firecracker-ci",
            output_name="vmlinux-fc-test",
            build_dir="/tmp/build",
            list_url_template=(
                "http://example.com/?prefix=firecracker-ci/"
                "{ci_version}/{arch}/vmlinux-{version}"
            ),
            config_url_template=(
                "http://example.com/config-{major_minor}.config"
            ),
        )

    def test_success(self, tmp_path: Path) -> None:
        """Download the latest vmlinux from a CI listing."""
        spec = self._make_firecracker_spec()
        output_dir = tmp_path / "kernels"
        output_dir.mkdir(parents=True)

        xml_response = (
            b'<?xml version="1.0"?>'
            b"<ListBucketResult>"
            b"<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>"
            b"</ListBucketResult>"
        )

        def _fake_download(url: str, dest: Path, **kwargs: object) -> None:
            dest.write_bytes(b"\x7fELF\x00\x00\x00")

        with (
            patch(
                "mvmctl.core.kernel._service.HttpDownload.read_raw_content",
                return_value=xml_response.decode(),
            ),
            patch(
                "mvmctl.core.kernel._service.HttpDownload.download_file",
                side_effect=_fake_download,
            ),
        ):
            result = KernelService.fetch_firecracker_kernel(
                spec=spec,
                ci_version="1.12",
                arch="amd64",
                output_dir=output_dir,
            )

        assert result.path.exists()
        assert result.version == "6.1.9"
        assert result.arch == "amd64"
        assert result.kernel_type == "firecracker"
        assert result.path.name == "vmlinux-fc-test-6.1.9-amd64"

    def test_list_failure(self, tmp_path: Path) -> None:
        """When the listing URL fails, an error is raised."""
        spec = self._make_firecracker_spec()

        with (
            patch(
                "mvmctl.core.kernel._service.HttpDownload.read_raw_content",
                side_effect=HttpDownloadError("HTTP 500"),
            ),
            pytest.raises(KernelError, match="Failed to list CI kernels"),
        ):
            KernelService.fetch_firecracker_kernel(
                spec=spec,
                ci_version="1.12",
                arch="amd64",
                output_dir=tmp_path,
            )

    def test_no_vmlinux_keys(self, tmp_path: Path) -> None:
        """When the listing has no vmlinux keys, an error is raised."""
        spec = self._make_firecracker_spec()
        empty_xml = (
            b'<?xml version="1.0"?>'
            b"<ListBucketResult>"
            b"<IsTruncated>false</IsTruncated>"
            b"</ListBucketResult>"
        )

        with (
            patch(
                "mvmctl.core.kernel._service.HttpDownload.read_raw_content",
                return_value=empty_xml.decode(),
            ),
            pytest.raises(
                KernelError, match="No vmlinux found for Firecracker CI"
            ),
        ):
            KernelService.fetch_firecracker_kernel(
                spec=spec,
                ci_version="1.12",
                arch="amd64",
                output_dir=tmp_path,
            )

    def test_already_cached(self, tmp_path: Path) -> None:
        """When the kernel file already exists, return it without download."""
        spec = self._make_firecracker_spec()
        output_dir = tmp_path / "kernels"
        output_dir.mkdir(parents=True)

        cached_path = output_dir / "vmlinux-fc-test-6.1.9-amd64"
        cached_path.write_text("cached kernel content")

        xml_response = (
            b'<?xml version="1.0"?>'
            b"<ListBucketResult>"
            b"<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>"
            b"</ListBucketResult>"
        )

        with (
            patch(
                "mvmctl.core.kernel._service.HttpDownload.read_raw_content",
                return_value=xml_response.decode(),
            ),
        ):
            result = KernelService.fetch_firecracker_kernel(
                spec=spec,
                ci_version="1.12",
                arch="amd64",
                output_dir=output_dir,
            )

        assert result.path == cached_path
        assert result.path.exists()
        assert result.version == "6.1.9"

    def test_checksum_required_when_spec_has_sha256_url(
        self, tmp_path: Path
    ) -> None:
        """When sha256_url is set but the sidecar fetch fails, error is raised."""
        spec_with_sha = self._make_firecracker_spec()
        spec_with_sha.sha256_url = (
            "https://example.com/{ci_version}/{arch}/vmlinux-{version}.sha256"
        )

        output_dir = tmp_path / "kernels"
        output_dir.mkdir(parents=True)

        xml_response = (
            b'<?xml version="1.0"?>'
            b"<ListBucketResult>"
            b"<Key>firecracker-ci/1.12/amd64/vmlinux-6.1.9</Key>"
            b"</ListBucketResult>"
        )

        # First call returns listing, second call (sha256 fetch) fails
        list_resp = xml_response.decode()

        with (
            patch(
                "mvmctl.core.kernel._service.HttpDownload.read_raw_content",
                side_effect=[
                    list_resp,
                    HttpDownloadError("sha256 fetch failed"),
                ],
            ),
            pytest.raises(KernelError, match="Checksum required"),
        ):
            KernelService.fetch_firecracker_kernel(
                spec=spec_with_sha,
                ci_version="1.12",
                arch="amd64",
                output_dir=output_dir,
            )


# ---------------------------------------------------------------------------
# list_all / service-level orchestration
# ---------------------------------------------------------------------------


class TestServiceListAll:
    """Tests for KernelService.list_all()."""

    def test_list_all_empty(self) -> None:
        repo = KernelRepository(Database())
        service = KernelService(repo)
        assert service.list_all() == []

    def test_list_all_verify_updates_present(self, tmp_path: Path) -> None:
        """When verify=True and a kernel path is missing, is_present is cleared."""
        from mvmctl.utils.common import CacheUtils

        repo = KernelRepository(Database())
        service = KernelService(repo)

        # Insert a kernel whose file does not exist on disk
        kernels_dir = CacheUtils.get_kernels_dir()
        kernels_dir.mkdir(parents=True, exist_ok=True)

        repo.upsert(
            KernelItem(
                id="a" * 64,
                name="missing",
                base_name="missing",
                version="1.0",
                arch="x86_64",
                type="official",
                path="missing-kernel",
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        # With verify=True, the missing kernel should have is_present=False
        kernels = service.list_all(verify=True)
        assert len(kernels) == 1
        assert kernels[0].name == "missing"
        assert kernels[0].is_present == 0

    def test_list_all_no_verify_returns_as_is(self) -> None:
        """When verify=False, is_present is not updated."""
        repo = KernelRepository(Database())
        service = KernelService(repo)

        repo.upsert(
            KernelItem(
                id="b" * 64,
                name="ghost",
                base_name="ghost",
                version="1.0",
                arch="x86_64",
                type="official",
                path="nonexistent-file",
                is_default=False,
                is_present=True,
                created_at="2026-01-01T00:00:00+00:00",
                updated_at="2026-01-01T00:00:00+00:00",
            )
        )

        kernels = service.list_all(verify=False)
        assert len(kernels) == 1
        assert kernels[0].is_present == 1


# ---------------------------------------------------------------------------
# remove / service-level orchestration
# ---------------------------------------------------------------------------


class TestServiceRemove:
    """Tests for KernelService.remove() -- delegates to controller."""

    def test_remove_existing(self) -> None:
        """Remove a kernel that exists."""
        from mvmctl.utils.common import CacheUtils

        repo = KernelRepository(Database())
        service = KernelService(repo)

        kernels_dir = CacheUtils.get_kernels_dir()
        kernels_dir.mkdir(parents=True, exist_ok=True)
        kernel_file = kernels_dir / "test-kernel"
        kernel_file.write_text("kernel payload")

        k = KernelItem(
            id="c" * 64,
            name="test-kernel",
            base_name="test-kernel",
            version="1.0",
            arch="x86_64",
            type="official",
            path="test-kernel",
            is_default=False,
            is_present=True,
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
        repo.upsert(k)

        service.remove(k)

        # Kernel should be hard-deleted from DB
        assert repo.get(k.id) is None
        # File should be removed
        assert not kernel_file.exists()
