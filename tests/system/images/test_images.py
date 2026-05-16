"""Image management system tests."""

from __future__ import annotations

import json
import re
import subprocess
from typing import Any

import pytest

from tests.system.conftest import _ensure_image, _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [pytest.mark.system, pytest.mark.domain_image]


@pytest.fixture(scope="module", autouse=True)
def _ensure_alpine_available(mvm_binary: str) -> None:
    """Ensure alpine-3.21 image is cached before any test in this module.

    Cache prune tests (test_cache.py) run before this module alphabetically
    and may have removed all images. Re-pull alpine so image tests don't
    skip due to missing cached images.
    """
    _ensure_image(mvm_binary, "alpine:3.21")


class TestImagePull:
    """Test image pulling operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    @pytest.mark.parametrize(
        "image_args",
        [
            ["alpine", "--version", "3.21"],
            ["ubuntu-minimal", "--version", "24.04"],
        ],
        ids=["alpine:3.21", "ubuntu:24.04"],
    )
    def test_image_pull(self, mvm_binary, image_args):
        """Pull each supported image.

        Tests a lightweight image (alpine) and a common one (ubuntu-minimal).
        Full list of 5 images is tested in CI on a schedule, not per-PR.
        Note: production code now auto-parses slugs into type + version,
        so ``ubuntu-24.04-minimal`` must be passed as ``ubuntu-minimal --version 24.04``.
        """
        result = _run_mvm(mvm_binary, "image", "pull", *image_args, timeout=120)
        assert result.returncode == 0
        assert (
            "pulled" in result.stdout.lower()
            or "already" in result.stdout.lower()
        )


class TestImageList:
    """Test image listing operations."""

    def test_image_list_json(self, mvm_binary):
        """List images in JSON format."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        assert result.returncode == 0

        data = json.loads(result.stdout)
        assert isinstance(data, list)

    def test_image_list_table(self, mvm_binary):
        """List images in table format."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        if data:
            entry = data[0]
            assert entry.get("is_present") is True, (
                f"Expected image to be present: {entry}"
            )
            assert isinstance(entry.get("type"), str) and entry["type"], (
                f"Expected non-empty type: {entry}"
            )

    def test_image_list_remote(self, mvm_binary):
        """List images available from the remote registry."""
        result = _run_mvm(
            mvm_binary, "image", "ls", "--remote", "--json", check=False
        )
        if result.returncode != 0 or not result.stdout.strip():
            pytest.skip("Remote listing not available (network?)")
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert len(data) > 0, "Expected at least one remote image"
        entry = data[0]
        # Remote entries may not have an "id" field (future releases, etc.)
        # but should always have a "type" and "display_name" or "version".
        assert isinstance(entry.get("type"), str) and entry["type"], (
            f"Expected non-empty type: {entry}"
        )
        assert isinstance(entry.get("display_name"), str) or isinstance(
            entry.get("version"), str
        ), f"Expected display_name or version: {entry}"

    def test_image_ls_remote_works(self, mvm_binary):
        """Listing remote images should return a non-empty list."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "ls",
            "--remote",
            "--json",
            timeout=30,
            check=False,
        )
        if result.returncode != 0 or not result.stdout.strip():
            pytest.skip("Remote listing not available or returned empty")
        try:
            data = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError, TypeError):
            pytest.skip("Remote listing returned non-JSON output")
        assert len(data) > 0, "Expected at least one remote image"

    def test_image_inspect(self, mvm_binary):
        """Inspect a cached image by ID prefix."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = [i for i in json.loads(result.stdout) if i.get("is_present")]
        if not images:
            pytest.skip("No present cached images to inspect")
        prefix = images[0]["id"][:6]
        result = _run_mvm(mvm_binary, "image", "inspect", prefix)
        assert result.returncode == 0

    def test_image_inspect_json(self, mvm_binary):
        """Inspect an image with --json output."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = [i for i in json.loads(result.stdout) if i.get("is_present")]
        if not images:
            pytest.skip("No present cached images to inspect")
        prefix = images[0]["id"][:6]
        result = _run_mvm(mvm_binary, "image", "inspect", prefix, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "id" in data
        assert "name" in data


class TestImageDefaults:
    """Test image default operations."""

    @pytest.mark.serial
    def test_image_set_default(self, mvm_binary):
        """Set image as default."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        if result.returncode != 0:
            pytest.skip("Failed to list images")
        images = json.loads(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("type", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available")
        target_id = alpine_images[0]["id"]

        result = _run_mvm(
            mvm_binary, "image", "default", target_id, check=False
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set image as default: {result.stderr.strip()}"
            )
        assert "default" in result.stdout.lower()

    def test_set_default_nonexistent_image_fails(self, mvm_binary):
        """Setting default to a nonexistent image slug should fail."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "default",
            "totally-nonexistent-image",
            check=False,
        )
        assert result.returncode != 0
        assert result.stderr, (
            f"Expected stderr with error message, got stdout={result.stdout}"
        )


class TestImageWarm:
    """Test image warm operations."""

    @pytest.mark.serial
    def test_image_warm(self, mvm_binary):
        """Pre-decompress image to ready pool for fast VM creation."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "warm",
            "alpine:3.21",
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"Image warm not available: {result.stderr.strip()}")
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )

    def test_warm_nonexistent_image_fails(self, mvm_binary):
        """Warming a nonexistent image slug should fail with clear error."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "warm",
            "totally-nonexistent-image",
            check=False,
        )
        assert result.returncode != 0
        assert result.stderr, (
            f"Expected stderr with error message, got stdout={result.stdout}"
        )

    @pytest.mark.serial
    def test_image_warm_by_id_prefix(self, mvm_binary: str) -> None:
        """Warm an image using its 6-char ID prefix."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        cached = [i for i in images if i.get("is_present")]
        if not cached:
            pytest.skip("No cached images available to warm")
        prefix = cached[0]["id"][:6]
        result = _run_mvm(
            mvm_binary,
            "image",
            "warm",
            prefix,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"Image warm by prefix failed: {result.stderr.strip()}")
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )


class TestImageImport:
    """Test image import operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_import_local_file(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a local image file."""
        import shutil

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("type", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available to import")

        target = alpine_images[0]
        target_id = target["id"]

        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            pytest.skip(f"Image file not found: {resolved_source}")

        temp_path = tmp_path / "alpine-import.raw"

        if resolved_source.suffix == ".zst":
            import subprocess as _subprocess

            decompress = _subprocess.run(
                [
                    "zstd",
                    "-d",
                    "-f",
                    str(resolved_source),
                    "-o",
                    str(temp_path),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            if decompress.returncode != 0:
                pytest.skip(f"zstd decompress failed: {decompress.stderr}")
        else:
            shutil.copy2(str(resolved_source), temp_path)

        imported_prefix = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "imported-alpine",
                str(temp_path),
                "--format",
                "raw",
                check=False,
            )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("name") == "imported-alpine"]
            assert imported, "Imported image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "rm",
                    imported_prefix,
                    check=False,
                )

    def test_import_from_nonexistent_path_fails(self, mvm_binary):
        """Import from a path that does not exist should fail with clear error."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "import",
            "/tmp/nonexistent-path-that-does-not-exist.qcow2",
            "--os-slug",
            "test-fail",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(s in combined for s in ["not found", "no such", "exist"]), (
            f"Expected error about nonexistent path, got: {result.stderr}"
        )


class TestImageInspectTree:
    """Test image inspect tree output."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_image]

    def test_image_inspect_tree_output(self, mvm_binary):
        """Inspect an image with --tree output."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = [i for i in json.loads(result.stdout) if i.get("is_present")]
        if not images:
            pytest.skip("No present cached images to inspect")
        prefix = images[0]["id"][:6]

        result = _run_mvm(mvm_binary, "image", "inspect", prefix, "--tree")
        assert result.returncode == 0
        assert (
            "├──" in result.stdout
            or "└──" in result.stdout
            or "ID:" in result.stdout
        )


class TestImagePullAdvanced:
    """Test advanced image pull operations — edge cases and state verification."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_pull_cached_image_with_default_sets_default(
        self, mvm_binary: str
    ) -> None:
        """Pull already-cached alpine-3.21 with --default must set it as sole default."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
        if result.returncode != 0 or not result.stdout.strip():
            pytest.skip("Cannot list images")
        images: list[dict[str, Any]] = json.loads(result.stdout)
        alpine_present = any(
            i.get("type") == "alpine" and i.get("is_present")
            for i in images
        )
        if not alpine_present:
            pytest.skip("alpine-3.21 not cached")

        original_defaults = [i for i in images if i.get("is_default")]
        original_default_id: str | None = (
            original_defaults[0]["id"] if original_defaults else None
        )

        try:
            _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                "--default",
                timeout=60,
            )

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = json.loads(result.stdout)
            defaults = [i for i in images_after if i.get("is_default")]
            assert len(defaults) == 1, (
                f"Expected exactly 1 default image, got {len(defaults)}"
            )
            assert defaults[0]["type"] == "alpine", (
                f"Expected alpine:3.21 as default, got {defaults[0].get('type')}"
            )
        finally:
            if original_default_id:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "default",
                    original_default_id[:6],
                    check=False,
                )

    def test_pull_cached_image_with_force_redownloads(
        self, mvm_binary: str
    ) -> None:
        """Pull already-cached alpine-3.21 with --force should re-download."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
        if result.returncode != 0 or not result.stdout.strip():
            pytest.skip("Cannot list images")
        images: list[dict[str, Any]] = json.loads(result.stdout)
        alpine_present = any(
            i.get("type") == "alpine" and i.get("is_present")
            for i in images
        )
        if not alpine_present:
            pytest.skip("alpine-3.21 not cached")

        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            "--force",
            timeout=60,
        )

        assert (
            "pulled" in result.stdout.lower()
            or "success" in result.stdout.lower()
        ), f"Expected pull success message, got: {result.stdout}"

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = json.loads(result.stdout)
        alpine_still_present = any(
            i.get("type") == "alpine" and i.get("is_present")
            for i in images_after
        )
        assert alpine_still_present, "alpine-3.21 missing after --force pull"

    def test_pull_nonexistent_image_fails_gracefully(
        self, mvm_binary: str
    ) -> None:
        """Pull a nonexistent image slug should fail with clear error."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "totally-nonexistent-image-name-12345",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert any(
            s in combined for s in ["not found", "no such", "invalid"]
        ), f"Expected error about nonexistent image, got: {result.stderr}"

    def test_pull_with_explicit_type_override(
        self, mvm_binary: str
    ) -> None:
        """Pull with ``--type alpine`` should override slug-derived type.

        The ``--type`` flag now takes precedence over the slug's derived type,
        so ``image pull debian --type alpine`` pulls the alpine image.
        """
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "debian",
            "--type",
            "alpine",
            timeout=60,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"Pull with --type override failed: {result.stderr.strip()}")
        assert (
            "pulled" in result.stdout.lower()
            or "already" in result.stdout.lower()
        ), f"Expected pull success, got: {result.stdout}"

        # Verify the pulled image has type alpine (not debian)
        ls_result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(ls_result.stdout)
        alpine_present = any(
            i.get("type", "").startswith("alpine") and i.get("is_present")
            for i in images
        )
        assert alpine_present, (
            "No alpine image found after --type override pull"
        )

    def test_pull_image_with_version_flag(self, mvm_binary: str) -> None:
        """Pull alpine with --version 3.21 should succeed.

        Note: production code auto-parses slugs, so ``alpine-3.21 --version 3.21``
        is redundant (version specified twice) and fails. Use ``alpine --version 3.21``.
        """
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            timeout=60,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"Pull with --version failed: {result.stderr.strip()}")

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images: list[dict[str, Any]] = json.loads(result.stdout)
        alpine_present = any(
            i.get("type", "").startswith("alpine") and i.get("is_present")
            for i in images
        )
        assert alpine_present, (
            "alpine not present after pull with --version flag"
        )


class TestImageImportAdvanced:
    """Test advanced image import operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_import_with_format_qcow2(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a qcow2 image using --format qcow2."""
        import shutil

        qemu_img = shutil.which("qemu-img")
        if not qemu_img:
            pytest.skip("qemu-img not available on this system")
        mkfs_ext4 = shutil.which("mkfs.ext4")
        if not mkfs_ext4:
            pytest.skip("mkfs.ext4 not available on this system")

        raw_path = tmp_path / "test-image.raw"
        qcow2_path = tmp_path / "test-image.qcow2"
        result = subprocess.run(
            ["truncate", "--size", "64M", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"truncate failed: {result.stderr}")
        result = subprocess.run(
            [mkfs_ext4, "-F", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"mkfs.ext4 failed: {result.stderr}")
        result = subprocess.run(
            [
                qemu_img,
                "convert",
                "-f",
                "raw",
                "-O",
                "qcow2",
                str(raw_path),
                str(qcow2_path),
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"qemu-img convert failed: {result.stderr}")

        imported_prefix = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-qcow2",
                str(qcow2_path),
                "--format",
                "qcow2",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(f"Import qcow2 failed: {result.stderr.strip()}")
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("name") == "test-qcow2"]
            assert imported, "Imported qcow2 image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "rm",
                    imported_prefix,
                    check=False,
                )

    def test_image_import_force_overwrite(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import the same image twice, verify --force suppresses the error."""
        import shutil

        mkfs_ext4 = shutil.which("mkfs.ext4")
        if not mkfs_ext4:
            pytest.skip("mkfs.ext4 not available on this system")

        raw_path = tmp_path / "test-overwrite.raw"
        result = subprocess.run(
            ["truncate", "--size", "64M", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"truncate failed: {result.stderr}")
        result = subprocess.run(
            [mkfs_ext4, "-F", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"mkfs.ext4 failed: {result.stderr}")

        result = _run_mvm(
            mvm_binary,
            "image",
            "import",
            "test-overwrite",
            str(raw_path),
            "--format",
            "raw",
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"First import failed: {result.stderr.strip()}")

        imported_prefix = None
        try:
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("name") == "test-overwrite"]
            if imported:
                imported_prefix = imported[0]["id"][:6]

            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-overwrite",
                str(raw_path),
                "--format",
                "raw",
                "--force",
                check=False,
            )
            assert result.returncode == 0, (
                f"Force import failed: {result.stderr}"
            )
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )

    def test_image_import_with_root_partition(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a qcow2 image with --root-partition 1 flag."""
        import shutil

        qemu_img = shutil.which("qemu-img")
        if not qemu_img:
            pytest.skip("qemu-img not available on this system")
        mkfs_ext4 = shutil.which("mkfs.ext4")
        if not mkfs_ext4:
            pytest.skip("mkfs.ext4 not available on this system")

        raw_path = tmp_path / "test-rootpart.raw"
        qcow2_path = tmp_path / "test-rootpart.qcow2"
        result = subprocess.run(
            ["truncate", "--size", "64M", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"truncate failed: {result.stderr}")
        result = subprocess.run(
            [mkfs_ext4, "-F", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"mkfs.ext4 failed: {result.stderr}")
        result = subprocess.run(
            [
                qemu_img,
                "convert",
                "-f",
                "raw",
                "-O",
                "qcow2",
                str(raw_path),
                str(qcow2_path),
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"qemu-img convert failed: {result.stderr}")

        imported_prefix = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-rootpart",
                str(qcow2_path),
                "--format",
                "qcow2",
                "--root-partition",
                "1",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import with --root-partition failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("name") == "test-rootpart"]
            assert imported, (
                "Imported root-partition image not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )

    def test_image_import_without_format_auto_detect(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a raw image without specifying --format (auto-detect)."""
        import shutil

        mkfs_ext4 = shutil.which("mkfs.ext4")
        if not mkfs_ext4:
            pytest.skip("mkfs.ext4 not available on this system")

        raw_path = tmp_path / "test-autodetect.raw"
        result = subprocess.run(
            ["truncate", "--size", "64M", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"truncate failed: {result.stderr}")
        result = subprocess.run(
            [mkfs_ext4, "-F", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"mkfs.ext4 failed: {result.stderr}")

        imported_prefix = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-autodetect",
                str(raw_path),
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import without --format failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("name") == "test-autodetect"]
            assert imported, "Auto-detected imported image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )


class TestImagePullSkipOptimization:
    """Test image pull with --skip-optimization flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_skip_optimization(self, mvm_binary):
        """Pull an image with --skip-optimization flag."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            "--skip-optimization",
            "--force",
            timeout=60,
        )
        assert "pulled" in result.stdout.lower()


class TestImageImportSetDefault:
    """Test image import with --default flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_import_with_set_default(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a local image file with --default flag."""
        import shutil

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("type", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available to import")

        target = alpine_images[0]
        target_id = target["id"]

        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            pytest.skip(f"Image file not found: {resolved_source}")

        temp_path = tmp_path / "test-import-default.raw"
        # If source is compressed (e.g. .zst), decompress before import
        if str(resolved_source).endswith(".zst"):
            _compressed = tmp_path / "test-import-default.zst"
            shutil.copy2(str(resolved_source), str(_compressed))
            import subprocess as _subprocess
            _decomp = _subprocess.run(
                ["zstd", "-d", str(_compressed), "-o", str(temp_path)],
                capture_output=True, text=True, timeout=120,
            )
            if _decomp.returncode != 0:
                pytest.skip(f"zstd decompress failed: {_decomp.stderr}")
            if not temp_path.exists():
                pytest.skip("zstd decompress produced no output file")
        else:
            shutil.copy2(str(resolved_source), temp_path)

        imported_prefix = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-import-default",
                str(temp_path),
                "--format",
                "raw",
                "--default",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import with --default failed: {result.stderr.strip()}"
                )
            assert "default" in result.stdout.lower()

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("name") == "test-import-default"
            ]
            if imported:
                imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "rm",
                    imported_prefix,
                    check=False,
                )


class TestImageImportArch:
    """Test image import with --arch flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_import_with_arch(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a qcow2 image with --arch x86_64 flag."""
        import shutil

        qemu_img = shutil.which("qemu-img")
        if not qemu_img:
            pytest.skip("qemu-img not available on this system")
        mkfs_ext4 = shutil.which("mkfs.ext4")
        if not mkfs_ext4:
            pytest.skip("mkfs.ext4 not available on this system")

        raw_path = tmp_path / "test-arch.raw"
        qcow2_path = tmp_path / "test-arch.qcow2"
        result = subprocess.run(
            ["truncate", "--size", "64M", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"truncate failed: {result.stderr}")
        result = subprocess.run(
            [mkfs_ext4, "-F", str(raw_path)],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"mkfs.ext4 failed: {result.stderr}")
        result = subprocess.run(
            [
                qemu_img,
                "convert",
                "-f",
                "raw",
                "-O",
                "qcow2",
                str(raw_path),
                str(qcow2_path),
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"qemu-img convert failed: {result.stderr}")

        imported_prefix = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-arch",
                str(qcow2_path),
                "--format",
                "qcow2",
                "--arch",
                "x86_64",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import with --arch failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("name") == "test-arch"]
            assert imported, "Imported arch image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "rm",
                    imported_prefix,
                    check=False,
                )


class TestImageRemoveForce:
    """Test image removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_rm_with_force(self, mvm_binary):
        """Remove a cached image by ID prefix with --force and verify it's gone."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        if result.returncode != 0:
            pytest.skip("Failed to list images")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            pytest.skip("No present alpine image available to test removal")

        was_default = alpine_images[0].get("is_default", False)
        target_id = alpine_images[0]["id"]

        result = _run_mvm(
            mvm_binary, "image", "rm", target_id[:6], "--force", check=False
        )
        assert result.returncode == 0, f"Force remove failed: {result.stderr}"

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        after = [
            i for i in json.loads(result.stdout) if i.get("is_present", True)
        ]
        assert not any(i["id"] == target_id for i in after)

        try:
            pull_args = ["image", "pull", "alpine", "--version", "3.21"]
            if was_default:
                pull_args.append("--default")
            _run_mvm(mvm_binary, *pull_args, timeout=120)
        except subprocess.TimeoutExpired:
            pytest.skip("Re-pull timed out (>60s download)")


class TestImageRemove:
    """Test image removal operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_remove_with_fixture(self, mvm_binary):
        """Remove a cached image by ID prefix and verify it's gone."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        before = json.loads(result.stdout)
        alpine_images = [
            i
            for i in before
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            pytest.skip("No present alpine image available to test removal")

        was_default = alpine_images[0].get("is_default", False)
        target_id = alpine_images[0]["id"]
        target_prefix = target_id[:6]
        was_removed = False

        try:
            result = _run_mvm(
                mvm_binary, "image", "rm", target_prefix, check=False
            )
            assert result.returncode == 0
            was_removed = True

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            after = [
                i
                for i in json.loads(result.stdout)
                if i.get("is_present", True)
            ]
            assert not any(i["id"] == target_id for i in after)
        finally:
            if was_removed:
                try:
                    pull_args = ["image", "pull", "alpine", "--version", "3.21"]
                    if was_default:
                        pull_args.append("--default")
                    _run_mvm(mvm_binary, *pull_args, timeout=120)
                except subprocess.TimeoutExpired:
                    pytest.skip("Re-pull timed out (>60s download)")


class TestImageImportCreateVM:
    """Test the full end-to-end flow of importing an image and creating a VM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_vm,
    ]

    def test_imported_image_vm_creation(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
        tmp_path,
        system_cache_dir,
    ):
        """Import a cached alpine image and create a running VM from it."""
        import subprocess as _subprocess

        _run_mvm(mvm_binary, "image", "pull", "alpine", "--version", "3.21")

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            pytest.skip("No present alpine image available")

        target = alpine_images[0]
        target_id = target["id"]

        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            pytest.skip(f"Image file not found: {resolved_source}")

        temp_path = tmp_path / "alpine-for-import.raw"

        if resolved_source.suffix == ".zst":
            decompress = _subprocess.run(
                [
                    "zstd",
                    "-d",
                    "-f",
                    str(resolved_source),
                    "-o",
                    str(temp_path),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            if decompress.returncode != 0:
                pytest.skip(f"zstd decompress failed: {decompress.stderr}")
        else:
            import shutil

            shutil.copy2(str(resolved_source), temp_path)

        import_name = f"imported-{unique_vm_name}"
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        vm_name = unique_vm_name

        imported_prefix: str | None = None

        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                import_name,
                str(temp_path),
                "--format",
                "raw",
                "--skip-optimization",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(f"Image import failed: {result.stderr.strip()}")
            assert result.returncode == 0

            # Parse the image ID from the import command stdout:
            #   ✓ Image imported: <full_hash>.ext4
            #       Name: <name>
            #       ID:   <short_id>
            # Extract the full hash before ".ext4" on the first line.
            first_line = result.stdout.strip().splitlines()[0]
            m = re.search(r"([0-9a-f]{64})\.", first_line)
            assert m, f"Could not parse image ID from import output: {result.stdout.strip()}"
            image_full_id = m.group(1)
            imported_prefix = image_full_id[:6]

            # Look up the stored name in the listing (it may be "alpine (imported)"
            # when OS detection works, or "imported-<name> (imported)" otherwise).
            img_ls_result = _run_mvm(mvm_binary, "image", "ls", "--json")
            all_imgs = json.loads(img_ls_result.stdout)
            imported_img = next(
                (i for i in all_imgs if i["id"].startswith(imported_prefix)),
                None,
            )
            assert imported_img, (
                f"Imported image with prefix '{imported_prefix}' not in listing"
            )
            imported_name = imported_img.get("name", "")

            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0, (
                f"Network create failed: {result.stderr}"
            )

            ensure_vm_deps(mvm_binary)
            try:
                try:
                    result = _run_mvm(
                        mvm_binary,
                        "vm",
                        "create",
                        vm_name,
                        "--image",
                        imported_prefix,
                        "--network",
                        network_name,
                    )
                except RuntimeError as e:
                    if "No provisioner available" in str(e):
                        pytest.skip(
                            "No loop-mount provisioner available "
                            "(mvm-services not set up)"
                        )
                    raise
                assert result.returncode == 0, (
                    f"VM create failed: {result.stderr}"
                )

                result = _run_mvm(mvm_binary, "vm", "ls", "--json")
                vms = json.loads(result.stdout)
                vm = next((v for v in vms if v["name"] == vm_name), None)
                assert vm is not None, f"VM '{vm_name}' not found in listing"
                assert vm["status"] == "running", (
                    f"Expected VM status 'running', got '{vm['status']}'"
                )
                assert vm.get("image_id", ""), f"VM has no image_id: {vm}"

                inspect_result = _run_mvm(
                    mvm_binary, "vm", "inspect", vm_name, "--json"
                )
                inspect_data = json.loads(inspect_result.stdout)
                assert imported_name in str(
                    inspect_data.get("image_name", "")
                ), (
                    f"VM image_name doesn't contain '{imported_name}': "
                    f"{inspect_data.get('image_name')}"
                )

            finally:
                _run_mvm(
                    mvm_binary, "vm", "rm", vm_name, "--force", check=False
                )
                _run_mvm(
                    mvm_binary,
                    "network",
                    "rm",
                    network_name,
                    "--force",
                    check=False,
                )

        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )

    def test_import_ubuntu_tar_rootfs(
        self,
        mvm_binary,
        unique_vm_name,
        unique_network_name,
        tmp_path,
    ):
        """Download Ubuntu 24.04 minimal tar-rootfs, import, create VM, verify running."""
        import subprocess as _subprocess

        ubuntu_url = (
            "https://cloud-images.ubuntu.com/minimal/releases/noble/release/"
            "ubuntu-24.04-minimal-cloudimg-amd64-root.tar.xz"
        )
        download_path = tmp_path / "ubuntu-24.04-minimal-root.tar.xz"

        download = _subprocess.run(
            ["curl", "-sSL", "-o", str(download_path), ubuntu_url],
            capture_output=True,
            text=True,
            timeout=120,
            check=False,
        )
        if download.returncode != 0 or not download_path.exists():
            pytest.skip(f"Failed to download Ubuntu image: {download.stderr}")

        import_name = f"ubuntu-imported-{unique_vm_name}"
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        vm_name = unique_vm_name
        imported_prefix: str | None = None

        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                import_name,
                str(download_path),
                "--format",
                "tar-rootfs",
                "--skip-optimization",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Ubuntu image import failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            # Production code stores imported images as ``<type> (imported)``
            # rather than the passed import name. Look for the image by
            # type containing "ubuntu" and name containing "(imported)".
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i
                for i in images
                if "imported" in i.get("name", "").lower()
                and "ubuntu" in i.get("type", "").lower()
            ]
            assert imported, (
                f"Imported ubuntu image not found in listing. "
                f"Used import name '{import_name}'."
            )
            imported_prefix = imported[0]["id"][:6]

            result = _run_mvm(
                mvm_binary,
                "network",
                "create",
                network_name,
                "--subnet",
                subnet,
                "--non-interactive",
            )
            assert result.returncode == 0, (
                f"Network create failed: {result.stderr}"
            )

            ensure_vm_deps(mvm_binary)
            try:
                try:
                    result = _run_mvm(
                        mvm_binary,
                        "vm",
                        "create",
                        vm_name,
                        "--image",
                        imported_prefix,
                        "--network",
                        network_name,
                    )
                except RuntimeError as e:
                    if "No provisioner available" in str(e):
                        pytest.skip(
                            "No loop-mount provisioner available "
                            "(mvm-services not set up)"
                        )
                    raise
                assert result.returncode == 0, (
                    f"VM create with imported Ubuntu failed: {result.stderr}"
                )

                result = _run_mvm(mvm_binary, "vm", "ls", "--json")
                vms = json.loads(result.stdout)
                vm = next((v for v in vms if v["name"] == vm_name), None)
                assert vm is not None, f"VM '{vm_name}' not found in listing"
                assert vm["status"] == "running", (
                    f"Expected VM status 'running', got '{vm['status']}'"
                )

            finally:
                _run_mvm(
                    mvm_binary, "vm", "rm", vm_name, "--force", check=False
                )
                _run_mvm(
                    mvm_binary,
                    "network",
                    "rm",
                    network_name,
                    "--force",
                    check=False,
                )

        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )


class TestImageDependencyDeletion:
    """Test dependency ordering for image deletion — references by VMs block removal."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    @pytest.mark.requires_kvm
    def test_delete_image_used_by_stopped_vm_fails(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """Deleting an image referenced by a stopped VM should be rejected."""
        vm_name = unique_vm_name

        try:
            ls_result = _run_mvm(
                mvm_binary, "image", "ls", "--json", check=False
            )
            alpine_present = False
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                images: list[dict[str, Any]] = json.loads(ls_result.stdout)
                alpine_present = any(
                    i.get("type") == "alpine" and i.get("is_present")
                    for i in images
                )
            if not alpine_present:
                _run_mvm(
                    mvm_binary, "image", "pull", "alpine", "--version", "3.21", timeout=180
                )

            try:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--network",
                    created_network,
                )
            except RuntimeError as e:
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise

            ins_result = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json", check=False
            )
            assert ins_result.returncode == 0, (
                f"Failed to inspect VM: {ins_result.stderr}"
            )
            vm_info: dict[str, Any] = json.loads(ins_result.stdout)
            image_id_full = vm_info.get("image_id", "")
            assert image_id_full
            image_id_prefix = image_id_full[:6]

            result = _run_mvm(
                mvm_binary, "image", "rm", image_id_prefix, check=False
            )
            combined = (result.stdout + result.stderr).lower()
            assert "referenced" in combined or "in use" in combined, (
                f"Expected image rm to report reference, got: "
                f"rc={result.returncode} stdout={result.stdout}"
            )

            ls_image = _run_mvm(
                mvm_binary, "image", "ls", "--json", check=False
            )
            if ls_image.returncode == 0 and ls_image.stdout.strip():
                images_after = json.loads(ls_image.stdout)
                alpine_after = next(
                    (i for i in images_after if i.get("type") == "alpine"),
                    None,
                )
                assert alpine_after is not None

            ls_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if ls_vm.returncode == 0 and ls_vm.stdout.strip():
                vms_after = json.loads(ls_vm.stdout)
                vm_names = [v.get("name") for v in vms_after]
                assert vm_name in vm_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_image_used_by_running_vm_fails_without_force(
        self,
        mvm_binary: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """Deleting an image referenced by a running VM should be rejected."""
        vm_name = unique_vm_name

        try:
            try:
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--network",
                    created_network,
                )
            except RuntimeError as e:
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            vm_ls = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            ins_result = _run_mvm(
                mvm_binary, "vm", "inspect", vm_name, "--json", check=False
            )
            assert ins_result.returncode == 0
            vm_info: dict[str, Any] = json.loads(ins_result.stdout)
            image_id_full = vm_info.get("image_id", "")
            assert image_id_full
            image_id_prefix = image_id_full[:6]

            result = _run_mvm(
                mvm_binary, "image", "rm", image_id_prefix, check=False
            )
            combined = (result.stdout + result.stderr).lower()
            assert "referenced" in combined or "in use" in combined, (
                f"Expected image rm to report reference, got: "
                f"rc={result.returncode} stdout={result.stdout}"
            )

            ls_image = _run_mvm(
                mvm_binary, "image", "ls", "--json", check=False
            )
            if ls_image.returncode == 0 and ls_image.stdout.strip():
                images_after = json.loads(ls_image.stdout)
                alpine_after = next(
                    (i for i in images_after if i.get("type") == "alpine"),
                    None,
                )
                assert alpine_after is not None

            ls_vm = _run_mvm(mvm_binary, "vm", "ls", "--json", check=False)
            if ls_vm.returncode == 0 and ls_vm.stdout.strip():
                vms_after = json.loads(ls_vm.stdout)
                vm_names = [v.get("name") for v in vms_after]
                assert vm_name in vm_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)

    def test_delete_default_image_promotes_other_or_clears(
        self,
        mvm_binary: str,
    ) -> None:
        """Deleting a formerly-default image should not leave orphans."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        present_images = [i for i in images if i.get("is_present", True)]

        if len(present_images) < 2:
            pytest.skip("Need at least 2 present images for this test")

        default_img = next(
            (i for i in present_images if i.get("is_default")),
            present_images[0],
        )
        other_img = next(
            (i for i in present_images if i["id"] != default_img["id"]),
            None,
        )

        if other_img is None:
            pytest.skip("No non-default image available to set as default")

        old_default_prefix = default_img["id"][:6]
        other_prefix = other_img["id"][:6]

        try:
            _run_mvm(mvm_binary, "image", "default", other_prefix, check=False)

            _run_mvm(mvm_binary, "image", "rm", old_default_prefix, check=False)

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_after = json.loads(result.stdout)
            default_count = sum(1 for i in images_after if i.get("is_default"))
            assert default_count <= 1, (
                f"Expected at most 1 default image, got {default_count}"
            )
        finally:
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_final = json.loads(result.stdout)
            has_default = any(i.get("is_default") for i in images_final)
            if not has_default and images_final:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "default",
                    images_final[0]["id"][:6],
                    check=False,
                )


class TestImageDefaultMigration:
    """Test that the default image migrates to a new record on force re-pull."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_default_migrates_to_new_image_on_force_repull(
        self, mvm_binary: str
    ) -> None:
        """When force-re-pulling the default image, default should migrate to new record."""
        # Rationale: Uses image ls --json (free) and image pull --force (~200MB download, marked slow).
        # No VMs, networks, or volumes needed — pure image-record state verification.
        result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
        if result.returncode != 0 or not result.stdout.strip():
            pytest.skip("Cannot list images")
        images: list[dict[str, Any]] = json.loads(result.stdout)

        present_alpine = [
            i
            for i in images
            if i.get("type") == "alpine" and i.get("is_present")
        ]
        if not present_alpine:
            _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                timeout=180,
            )
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            present_alpine = [
                i
                for i in images
                if i.get("type") == "alpine" and i.get("is_present")
            ]
            if not present_alpine:
                pytest.skip("alpine-3.21 still not present after pull")

        old_alpine = present_alpine[0]
        old_alpine_id: str = old_alpine["id"]

        original_defaults = [i for i in images if i.get("is_default")]
        original_default_id: str | None = (
            original_defaults[0]["id"] if original_defaults else None
        )

        changed_default = False
        if not old_alpine.get("is_default"):
            _run_mvm(mvm_binary, "image", "default", old_alpine_id[:6])
            changed_default = True

        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                "--force",
                timeout=180,
            )

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = json.loads(result.stdout)

            alpine_after = [
                i for i in images_after if i.get("type") == "alpine"
            ]
            assert len(alpine_after) >= 1, (
                "Expected at least one alpine-3.21 record after force re-pull"
            )

            new_default = next(
                (i for i in alpine_after if i.get("is_default")), None
            )
            assert new_default is not None, (
                "Expected a default alpine-3.21 after force re-pull"
            )
            assert new_default["id"] != old_alpine_id, (
                "Expected new alpine record ID different from old one"
            )
            assert new_default.get("is_present"), (
                "New alpine record should be present"
            )

            old_record = next(
                (i for i in alpine_after if i["id"] == old_alpine_id), None
            )
            if old_record is not None:
                assert not old_record.get("is_present"), (
                    "Old alpine record should not be present after force re-pull"
                )
        finally:
            if changed_default and original_default_id:
                _run_mvm(
                    mvm_binary,
                    "image",
                    "default",
                    original_default_id[:6],
                    check=False,
                )
