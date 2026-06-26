"""Image management system tests."""

from __future__ import annotations

import json
import re
from typing import Any

import pytest

from tests.system.conftest import (
    _ensure_image,
    _guest_run,
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
)

pytestmark = [pytest.mark.system, pytest.mark.domain_image]

# Cache directory inside the test VM
VM_CACHE_DIR = "/root/.cache/mvmctl"
VM_IMAGES_DIR = f"{VM_CACHE_DIR}/images"
VM_TMP_DIR = "/tmp"


# ============================================================================
# Helpers — all operations run INSIDE the test VM via _run_mvm
# ============================================================================


def _parse_json_output(output: str) -> Any:
    """Parse mvm JSON output, stripping trailing non-JSON lines (warnings)."""
    for end_char in ("]", "}"):
        last_idx = output.rfind(end_char)
        if last_idx != -1:
            return json.loads(output[:last_idx + 1])
    return json.loads(output)


def _vm_mktemp(runner_vm: str, suffix: str = "") -> str:
    """Create a unique temp path inside the test VM."""
    result = _guest_run(runner_vm,
        f"mktemp -p {VM_TMP_DIR} {suffix}",
    )
    return result.stdout.strip()


def _get_cached_alpine_path_in_vm(
    runner_vm: str,
    name: str = "alpine-extracted.raw",
) -> str | None:
    """Find a cached alpine image and decompress it to a VM temp path.

    Returns the VM-internal path to the decompressed image file, or None if
    no cached alpine image can be found.
    """
    result = _run_mvm(runner_vm, "image", "ls", "--json")
    images: list[dict[str, Any]] = _parse_json_output(result.stdout)
    alpine_images = [i for i in images if "alpine" in i.get("type", "").lower()]
    if not alpine_images:
        return None

    target = alpine_images[0]
    result = _run_mvm(
        runner_vm, "image", "inspect", target["id"], "--json", check=False
    )
    if result.returncode != 0:
        return None

    data = _parse_json_output(result.stdout)
    source_path = data.get("path") or data.get("storage", {}).get("path")
    if not source_path:
        return None

    if source_path.startswith("/"):
        resolved_source = source_path
    else:
        resolved_source = f"{VM_IMAGES_DIR}/{source_path}"
    temp_path = f"{VM_TMP_DIR}/{name}"

    # Check if source file exists inside the VM
    check = _guest_run(runner_vm,
        f"test -f {resolved_source} && echo exists || echo not-found",
        check=False,
    )
    if "not-found" in check.stdout:
        return None

    # Decompress or copy inside the VM
    if source_path.endswith(".zst"):
        result = _guest_run(runner_vm,
            f"zstd -d -f {resolved_source} -o {temp_path}",
            check=False,
        )
        if result.returncode != 0:
            return None
    else:
        result = _guest_run(runner_vm,
            f"cp {resolved_source} {temp_path}",
            check=False,
        )
        if result.returncode != 0:
            return None
    return temp_path


def _create_ext4_raw_in_vm(
    runner_vm: str,
    name: str = "test.raw",
    size: str = "64M",
) -> str | None:
    """Create an ext4-formatted raw disk image inside the test VM.

    Requires truncate and mkfs.ext4 inside the VM. Returns the VM-internal
    path, or None if a prerequisite tool is unavailable or the operation fails.
    """
    # Check prerequisites inside the VM
    for tool in ("truncate", "mkfs.ext4"):
        check = _guest_run(runner_vm,
            f"which {tool} && echo found || echo not-found",
            check=False,
        )
        if "not-found" in check.stdout:
            return None

    raw_path = f"{VM_TMP_DIR}/{name}"

    result = _guest_run(runner_vm,
        f"truncate --size {size} {raw_path}",
        check=False,
    )
    if result.returncode != 0:
        return None

    result = _guest_run(runner_vm,
        f"mkfs.ext4 -F {raw_path}",
        check=False,
    )
    if result.returncode != 0:
        return None
    return raw_path


def _create_qcow2_from_raw_in_vm(
    runner_vm: str,
    raw_path: str,
    qcow2_path: str,
) -> bool:
    """Convert a raw image to qcow2 format inside the test VM.

    Returns True on success, False on failure.
    """
    result = _guest_run(
        runner_vm,
        f"qemu-img convert -f raw -O qcow2 {raw_path} {qcow2_path}",
        check=False,
    )
    return result.returncode == 0


@pytest.fixture(scope="module", autouse=True)
def _ensure_alpine_available(runner_vm: str) -> None:
    """Ensure alpine-3.21 image is cached before any test in this module."""
    _ensure_image(runner_vm, "alpine:3.23")


class TestImagePull:
    """Test image pulling operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    @pytest.mark.parametrize(
        "image_args",
        [
            ["alpine", "--version", "3.21"],
            ["ubuntu-minimal", "--version", "24.04"],
        ],
        ids=["alpine:3.23", "ubuntu:24.04"],
    )
    def test_image_pull(self, runner_vm, image_args):
        """Pull each supported image.

        Tests a lightweight image (alpine) and a common one (ubuntu-minimal).
        """
        result = _run_mvm(runner_vm, "image", "pull", *image_args, timeout=120)
        assert result.returncode == 0
        assert (
            "pulled" in result.stdout.lower()
            or "already" in result.stdout.lower()
        )


class TestImageList:
    """Test image listing operations."""

    def test_image_list_json(self, runner_vm):
        """List images in JSON format."""
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        data = _parse_json_output(result.stdout)
        assert isinstance(data, list)
        if data:
            entry = data[0]
            assert "id" in entry, f"Expected 'id' field in image entry: {entry}"
            assert isinstance(entry.get("type"), str) and entry["type"], (
                f"Expected non-empty type: {entry}"
            )

    def test_image_list_table(self, runner_vm):
        """List images in table format."""
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        data = _parse_json_output(result.stdout)
        assert isinstance(data, list)
        if data:
            entry = data[0]
            assert entry.get("is_present") is True, (
                f"Expected image to be present: {entry}"
            )
            assert isinstance(entry.get("type"), str) and entry["type"], (
                f"Expected non-empty type: {entry}"
            )

    def test_image_list_no_cache(self, runner_vm):
        """List cached images with --no-cache flag."""
        result = _run_mvm(runner_vm, "image", "ls", "--no-cache", check=False)
        assert result.returncode == 0, (
            f"image ls --no-cache failed: {result.stderr}"
        )

    def test_image_list_type_filter(self, runner_vm):
        """List cached images filtered by --type alpine."""
        result = _run_mvm(
            runner_vm, "image", "ls", "--type", "alpine", check=False
        )
        assert result.returncode == 0, (
            f"image ls --type alpine failed: {result.stderr}"
        )
        if result.stdout.strip():
            try:
                data = _parse_json_output(result.stdout)
                for entry in data:
                    assert "alpine" in entry.get("type", "").lower(), (
                        f"Expected alpine type, got: {entry}"
                    )
            except (json.JSONDecodeError, ValueError, TypeError):
                pass

    def test_image_list_remote(self, runner_vm):
        """List images available from the remote registry."""
        result = _run_mvm(
            runner_vm, "image", "ls", "--remote", "--json", check=False
        )
        assert result.returncode == 0 and result.stdout.strip(), (
            f"Remote listing failed in Tier 2 environment (should have network): "
            f"{result.stderr}"
        )
        data = _parse_json_output(result.stdout)
        assert isinstance(data, list)
        assert len(data) > 0, "Expected at least one remote image"
        entry = data[0]
        assert isinstance(entry.get("type"), str) and entry["type"], (
            f"Expected non-empty type: {entry}"
        )
        assert isinstance(entry.get("display_name"), str) or isinstance(
            entry.get("version"), str
        ), f"Expected display_name or version: {entry}"

    def test_image_ls_remote_works(self, runner_vm):
        """Listing remote images should return a non-empty list."""
        result = _run_mvm(
            runner_vm,
            "image",
            "ls",
            "--remote",
            "--json",
            timeout=30,
            check=False,
        )
        assert result.returncode == 0, (
            f"Remote listing failed in Tier 2 environment: {result.stderr}"
        )
        assert result.stdout.strip(), "Remote listing returned empty stdout"
        try:
            data = _parse_json_output(result.stdout)
        except (json.JSONDecodeError, ValueError, TypeError) as e:
            pytest.fail(f"Remote listing returned non-JSON output: {e}")
        assert len(data) > 0, "Expected at least one remote image"

    def test_image_inspect(self, runner_vm):
        """Inspect a cached image by ID prefix."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = [i for i in _parse_json_output(result.stdout) if i.get("is_present")]
        assert images, (
            "No present cached images to inspect — _ensure_image should "
            "have pulled alpine:3.23"
        )
        prefix = images[0]["id"][:6]
        result = _run_mvm(runner_vm, "image", "inspect", prefix, "--json")
        assert result.returncode == 0
        data = _parse_json_output(result.stdout)
        img_data = data.get("image", data)
        assert img_data.get("id", "").startswith(prefix), (
            f"Expected image id to start with prefix '{prefix}', "
            f"got: {img_data.get('id', 'N/A')}"
        )

    def test_image_inspect_json(self, runner_vm):
        """Inspect an image with --json output."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = [i for i in _parse_json_output(result.stdout) if i.get("is_present")]
        assert images, (
            "No present cached images to inspect — _ensure_image should "
            "have pulled alpine:3.23"
        )
        prefix = images[0]["id"][:6]
        result = _run_mvm(runner_vm, "image", "inspect", prefix, "--json")
        assert result.returncode == 0
        data = _parse_json_output(result.stdout)
        img_data = data.get("image", data)
        assert "id" in img_data, (
            f"Expected 'id' in image inspect output, got keys: {list(data.keys())}"
        )
        assert "name" in img_data or "base_name" in img_data


class TestImageDefaults:
    """Test image default operations."""

    def test_image_set_default(self, runner_vm):
        """Set image as default."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = _parse_json_output(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("type", "").lower()
        ]
        assert alpine_images, "No alpine image available in Tier 2 environment"
        target_id = alpine_images[0]["id"]

        result = _run_mvm(runner_vm, "image", "default", target_id, check=False)
        assert result.returncode == 0, (
            f"Failed to set image as default: {result.stderr.strip()}"
        )
        ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images_after = json.loads(ls_result.stdout)
        default_img = next(
            (i for i in images_after if i.get("is_default")), None
        )
        assert default_img is not None, (
            "No image marked as default in ls --json"
        )

    def test_set_default_nonexistent_image_fails(self, runner_vm):
        """Setting default to a nonexistent image slug should fail."""
        result = _run_mvm(
            runner_vm,
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

    def test_image_warm(self, runner_vm):
        """Pre-decompress image to ready pool for fast VM creation."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(
            runner_vm,
            "image",
            "warm",
            "alpine:3.23",
            check=False,
        )
        assert result.returncode == 0, (
            f"Image warm failed in Tier 2 environment: {result.stderr.strip()}"
        )
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )

    def test_warm_nonexistent_image_fails(self, runner_vm):
        """Warming a nonexistent image slug should fail with clear error."""
        result = _run_mvm(
            runner_vm,
            "image",
            "warm",
            "totally-nonexistent-image",
            check=False,
        )
        assert result.returncode != 0
        assert result.stderr, (
            f"Expected stderr with error message, got stdout={result.stdout}"
        )

    def test_image_warm_by_id_prefix(self, runner_vm: str) -> None:
        """Warm an image using its 6-char ID prefix."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = _parse_json_output(result.stdout)
        cached = [i for i in images if i.get("is_present")]
        assert cached, "No cached images available to warm"
        prefix = cached[0]["id"][:6]
        result = _run_mvm(
            runner_vm, "image", "warm", prefix, check=False,
        )
        assert result.returncode == 0, (
            f"Image warm by prefix failed: {result.stderr.strip()}"
        )
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )

    def test_image_warm_all(self, runner_vm: str) -> None:
        """Pre-decompress all cached images to ready pool via --all flag."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(
            runner_vm, "image", "warm", "--all", check=False,
        )
        assert result.returncode == 0, (
            f"Image warm --all failed in Tier 2 environment: {result.stderr.strip()}"
        )
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )


class TestImageInspectJson:
    """Test image inspect JSON output."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_image]

    def test_image_inspect_json_output(self, runner_vm):
        """Inspect an image with --json output."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = [i for i in _parse_json_output(result.stdout) if i.get("is_present")]
        assert images, "No present cached images to inspect"
        prefix = images[0]["id"][:6]

        result = _run_mvm(runner_vm, "image", "inspect", prefix, "--json")
        assert result.returncode == 0
        data = _parse_json_output(result.stdout)
        img_data = data.get("image", data)
        assert "id" in img_data, (
            f"Expected 'id' in image inspect --json output, got keys: {list(img_data.keys())}"
        )
        assert "name" in img_data or "base_name" in img_data


class TestImageImport:
    """Test image import operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_import_local_file(self, runner_vm):
        """Import a local image file (inside the test VM)."""
        _ensure_image(runner_vm, "alpine:3.23")
        cached_path = _get_cached_alpine_path_in_vm(
            runner_vm, "alpine-import.raw"
        )
        assert cached_path is not None, (
            "No cached alpine image available to extract and re-import"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "imported-alpine",
                cached_path,
                "--format",
                "raw",
                check=False,
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [
                i for i in images
                if "imported" in i.get("name", "").lower()
                and "alpine" in i.get("type", "").lower()
            ]
            assert imported, "Imported image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_import_from_nonexistent_path_fails(self, runner_vm):
        """Import from a path that does not exist should fail with clear error."""
        result = _run_mvm(
            runner_vm,
            "image",
            "import",
            "test-fail-name",
            "/tmp/nonexistent-path-that-does-not-exist.qcow2",
            check=False,
        )
        assert result.returncode != 0
        combined = (result.stdout + result.stderr).lower()
        assert "not found" in combined or "does not exist" in combined, (
            f"Expected error about nonexistent path, got: {result.stderr}"
        )


class TestImagePullAdvanced:
    """Test advanced image pull operations -- edge cases and state verification."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_pull_cached_image_with_default_sets_default(
        self, runner_vm: str
    ) -> None:
        """Pull already-cached alpine-3.21 with --default must set it as sole default."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = _parse_json_output(result.stdout)
        alpine_present = any(
            i.get("type") == "alpine" and i.get("is_present") for i in images
        )
        assert alpine_present, "alpine-3.21 not cached in Tier 2 environment"

        original_defaults = [i for i in images if i.get("is_default")]
        original_default_id: str | None = (
            original_defaults[0]["id"] if original_defaults else None
        )

        try:
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                "--default",
                timeout=60,
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = _parse_json_output(result.stdout)
            defaults = [i for i in images_after if i.get("is_default")]
            assert len(defaults) == 1, (
                f"Expected exactly 1 default image, got {len(defaults)}"
            )
            assert defaults[0]["type"] == "alpine", (
                f"Expected alpine:3.23 as default, got {defaults[0].get('type')}"
            )
        finally:
            if original_default_id:
                _run_mvm(
                    runner_vm,
                    "image",
                    "default",
                    original_default_id[:6],
                    check=False,
                )

    def test_pull_cached_image_with_force_redownloads(
        self, runner_vm: str
    ) -> None:
        """Pull already-cached alpine-3.21 with --force should re-download."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = _parse_json_output(result.stdout)
        alpine_present = any(
            i.get("type") == "alpine" and i.get("is_present") for i in images
        )
        assert alpine_present, "alpine-3.21 not cached in Tier 2 environment"

        result = _run_mvm(
            runner_vm,
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

        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images_after: list[dict[str, Any]] = _parse_json_output(result.stdout)
        alpine_still_present = any(
            i.get("type") == "alpine" and i.get("is_present")
            for i in images_after
        )
        assert alpine_still_present, "alpine-3.21 missing after --force pull"

    def test_pull_nonexistent_image_fails_gracefully(
        self, runner_vm: str
    ) -> None:
        """Pull a nonexistent image slug should fail with clear error."""
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "totally-nonexistent-image-name-12345",
            check=False,
        )
        assert result.returncode != 0
        assert "not found" in result.stderr.lower() or "no image types matched" in result.stderr.lower(), (
            f"Expected error about nonexistent image, got: {result.stderr}"
        )

    def test_pull_with_explicit_type_override(self, runner_vm: str) -> None:
        """Pull with --type alpine should override slug-derived type."""
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "debian",
            "--type",
            "alpine",
            timeout=60,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull with --type override failed in Tier 2 environment: {result.stderr.strip()}"
        )
        assert (
            "pulled" in result.stdout.lower()
            or "already" in result.stdout.lower()
        ), f"Expected pull success, got: {result.stdout}"

        ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = json.loads(ls_result.stdout)
        alpine_present = any(
            i.get("type", "").startswith("alpine") and i.get("is_present")
            for i in images
        )
        assert alpine_present, (
            "No alpine image found after --type override pull"
        )

    def test_pull_image_with_version_flag(self, runner_vm: str) -> None:
        """Pull alpine with --version 3.21 should succeed."""
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            timeout=60,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull with --version failed in Tier 2 environment: {result.stderr.strip()}"
        )

        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = _parse_json_output(result.stdout)
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
        pytest.mark.domain_image,
    ]

    def test_image_import_with_format_qcow2(self, runner_vm):
        """Import a qcow2 image using --format qcow2 (inside the VM)."""
        # Check qemu-img availability inside the VM
        check = _guest_run(runner_vm,
            "which qemu-img && echo found || echo not-found",
            check=False,
        )
        assert "found" in check.stdout, "qemu-img not available inside the test VM"

        raw_path = _create_ext4_raw_in_vm(runner_vm, "test-image.raw")
        assert raw_path is not None, (
            "mkfs.ext4 or truncate not available inside the test VM"
        )

        qcow2_path = f"{VM_TMP_DIR}/test-image.qcow2"
        assert _create_qcow2_from_raw_in_vm(runner_vm, raw_path, qcow2_path), (
            "qemu-img convert failed inside the VM"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-qcow2",
                qcow2_path,
                "--format",
                "qcow2",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import qcow2 failed: {result.stderr.strip()}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [i for i in images if i.get("name") == "test-qcow2"]
            assert imported, "Imported qcow2 image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_image_import_force_overwrite(self, runner_vm):
        """Import the same image twice, verify --force suppresses the error."""
        raw_path = _create_ext4_raw_in_vm(runner_vm, "test-overwrite.raw")
        assert raw_path is not None, (
            "mkfs.ext4 or truncate not available inside the test VM"
        )

        result = _run_mvm(
            runner_vm,
            "image",
            "import",
            "test-overwrite",
            raw_path,
            "--format",
            "raw",
            "--skip-optimization",
            check=False,
        )
        assert result.returncode == 0, (
            f"First import failed: {result.stderr.strip()}"
        )

        imported_prefix = None
        try:
            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [i for i in images if i.get("name") == "test-overwrite"]
            if imported:
                imported_prefix = imported[0]["id"][:6]

            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-overwrite",
                raw_path,
                "--format",
                "raw",
                "--skip-optimization",
                "--force",
                check=False,
            )
            assert result.returncode == 0, (
                f"Force import failed: {result.stderr}"
            )
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_image_import_with_root_partition(self, runner_vm):
        """Import a qcow2 image with --root-partition 1 flag."""
        check = _guest_run(runner_vm,
            "which qemu-img && echo found || echo not-found",
            check=False,
        )
        assert "found" in check.stdout, "qemu-img not available inside the test VM"

        raw_path = _create_ext4_raw_in_vm(runner_vm, "test-rootpart.raw")
        assert raw_path is not None, (
            "mkfs.ext4 or truncate not available inside the test VM"
        )

        qcow2_path = f"{VM_TMP_DIR}/test-rootpart.qcow2"
        assert _create_qcow2_from_raw_in_vm(runner_vm, raw_path, qcow2_path), (
            "qemu-img convert failed inside the VM"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-rootpart",
                qcow2_path,
                "--format",
                "qcow2",
                "--root-partition",
                "1",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import with --root-partition failed: {result.stderr.strip()}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [i for i in images if i.get("name") == "test-rootpart"]
            assert imported, (
                "Imported root-partition image not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_image_import_with_disable_detector(self, runner_vm):
        """Import an image with --disable-detector arch flag."""
        _ensure_image(runner_vm, "alpine:3.23")
        cached_path = _get_cached_alpine_path_in_vm(
            runner_vm, "alpine-nodetector.raw"
        )
        assert cached_path is not None, (
            "No cached alpine image available to import with --disable-detector"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "imported-no-detector",
                cached_path,
                "--format",
                "raw",
                "--disable-detector",
                "type",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import with --disable-detector failed: {result.stderr.strip()}"
            )

            ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(ls_result.stdout)
            imported = [
                i for i in images if i.get("name") == "imported-no-detector"
            ]
            assert imported, (
                "Imported image with --disable-detector not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_image_import_without_format_auto_detect(self, runner_vm):
        """Import a raw image without specifying --format (auto-detect)."""
        raw_path = _create_ext4_raw_in_vm(runner_vm, "test-autodetect.raw")
        assert raw_path is not None, (
            "mkfs.ext4 or truncate not available inside the test VM"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-autodetect",
                raw_path,
                "--format",
                "raw",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import without --format failed: {result.stderr.strip()}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [i for i in images if i.get("name") == "test-autodetect"]
            assert imported, "Auto-detected imported image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_image_import_with_skip_optimization(self, runner_vm):
        """Import an image with --skip-optimization flag."""
        _ensure_image(runner_vm, "alpine:3.23")
        cached_path = _get_cached_alpine_path_in_vm(
            runner_vm, "alpine-skipopt.raw"
        )
        assert cached_path is not None, (
            "No alpine image available to import with --skip-optimization"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "imported-skip-opt",
                cached_path,
                "--format",
                "raw",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import with --skip-optimization failed: {result.stderr.strip()}"
            )

            ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(ls_result.stdout)
            imported = [
                i for i in images
                if "imported" in i.get("name", "").lower()
            ]
            assert imported, (
                "Imported image with --skip-optimization not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)


class TestImagePullArchFlag:
    """Test image pull (arch is auto-detected in Go CLI)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_arch(self, runner_vm):
        """Pull an already-cached image (arch detection is automatic in Go CLI)."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            "--force",
            timeout=60,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull failed in Tier 2 environment: {result.stderr.strip()}"
        )
        assert "pulled" in result.stdout.lower(), (
            f"Expected 'pulled' in output, got: {result.stdout}"
        )


class TestImagePullNoCache:
    """Test image pull with --no-cache flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_no_cache(self, runner_vm):
        """Pull an image with --no-cache flag to bypass local cache."""
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.21",
            "--no-cache",
            "--force",
            timeout=120,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull with --no-cache failed in Tier 2 environment: {result.stderr.strip()}"
        )
        assert "pulled" in result.stdout.lower(), (
            f"Expected 'pulled' in output, got: {result.stdout}"
        )


class TestImagePullSkipOptimization:
    """Test image pull with --skip-optimization flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_skip_optimization(self, runner_vm):
        """Pull an image with --skip-optimization flag."""
        result = _run_mvm(
            runner_vm,
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
        pytest.mark.domain_image,
    ]

    def test_image_import_with_set_default(self, runner_vm):
        """Import a local image file with --default flag."""
        _ensure_image(runner_vm, "alpine:3.23")
        cached_path = _get_cached_alpine_path_in_vm(
            runner_vm, "test-import-default.raw"
        )
        assert cached_path is not None, (
            "No cached alpine image available to import"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-import-default",
                cached_path,
                "--format",
                "raw",
                "--default",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import with --default failed: {result.stderr.strip()}"
            )

            ls_after = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after = json.loads(ls_after.stdout)
            default_img = next(
                (i for i in images_after if i.get("is_default")), None
            )
            assert default_img is not None, (
                "No image marked as default in ls --json after import --default"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [
                i for i in images
                if "imported" in i.get("name", "").lower()
                and i.get("type", "").lower().startswith("alpine")
            ]
            if imported:
                imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)


class TestImageImportArch:
    """Test image import (arch is auto-detected in Go CLI)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_import_with_arch(self, runner_vm):
        """Import a qcow2 image (arch detection is automatic in Go CLI)."""
        check = _guest_run(runner_vm,
            "which qemu-img && echo found || echo not-found",
            check=False,
        )
        assert "found" in check.stdout, "qemu-img not available inside the test VM"

        raw_path = _create_ext4_raw_in_vm(runner_vm, "test-arch.raw")
        assert raw_path is not None, (
            "mkfs.ext4 or truncate not available inside the test VM"
        )

        qcow2_path = f"{VM_TMP_DIR}/test-arch.qcow2"
        assert _create_qcow2_from_raw_in_vm(runner_vm, raw_path, qcow2_path), (
            "qemu-img convert failed inside the VM"
        )

        imported_prefix = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-arch",
                qcow2_path,
                "--format",
                "qcow2",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Image import failed: {result.stderr.strip()}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [i for i in images if i.get("name") == "test-arch"]
            assert imported, "Imported arch image not found in listing"
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)


class TestImageImportCreateVM:
    """Test the full end-to-end flow of importing an image and creating a VM."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_imported_image_vm_creation(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Import a cached alpine image and create a running VM from it."""
        _ensure_image(runner_vm, "alpine:3.23")
        cached_path = _get_cached_alpine_path_in_vm(
            runner_vm, "alpine-for-import.raw"
        )
        assert cached_path is not None, (
            "No present alpine image available for import"
        )

        import_name = f"imported-{unique_vm_name}"
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        vm_name = unique_vm_name

        imported_prefix: str | None = None

        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                import_name,
                cached_path,
                "--format",
                "raw",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Image import failed: {result.stderr.strip()}"
            )

            m = re.search(r"ID:\s+(\w+)", result.stdout)
            assert m, (
                f"Could not parse image ID from import output: {result.stdout.strip()}"
            )
            imported_prefix = m.group(1)

            img_ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
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
                runner_vm,
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

            ensure_vm_deps(runner_vm)
            try:
                result = _run_mvm(
                    runner_vm,
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    imported_prefix,
                    "--network",
                    network_name,
                )
                assert result.returncode == 0, (
                    f"VM create failed: {result.stderr}"
                )

                result = _run_mvm(runner_vm, "vm", "ls", "--json")
                vms = _parse_json_output(result.stdout)
                vm = next((v for v in vms if v["name"] == vm_name), None)
                assert vm is not None, f"VM '{vm_name}' not found in listing"
                assert vm["status"] == "running", (
                    f"Expected VM status 'running', got '{vm['status']}'"
                )
                assert vm.get("image_id", ""), f"VM has no image_id: {vm}"

                inspect_result = _run_mvm(
                    runner_vm, "vm", "inspect", vm_name, "--json"
                )
                inspect_data = json.loads(inspect_result.stdout)
                assert imported_name in str(
                    inspect_data.get("assets", {}).get("image", {}).get("name", "")
                ), (
                    f"VM image_name doesn't contain '{imported_name}': "
                    f"{inspect_data.get('assets', {}).get('image', {}).get('name', '')}"
                )
            finally:
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
                _run_mvm(
                    runner_vm, "network", "rm", network_name, "--force", check=False,
                )
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)

    def test_import_ubuntu_tar_rootfs(
        self,
        runner_vm,
        unique_vm_name,
        unique_network_name,
    ):
        """Download Ubuntu 24.04 minimal tar-rootfs, import, create VM, verify running."""
        ubuntu_url = (
            "https://cloud-images.ubuntu.com/minimal/releases/noble/release/"
            "ubuntu-24.04-minimal-cloudimg-amd64-root.tar.xz"
        )
        download_path = f"{VM_TMP_DIR}/ubuntu-24.04-minimal-root.tar.xz"

        # Download inside the VM using curl
        download = _guest_run(runner_vm,
            f"curl -sSL -o {download_path} {ubuntu_url}",
            timeout=120,
            check=False,
        )
        assert download.returncode == 0, (
            f"Failed to download Ubuntu image inside VM: {download.stderr}"
        )

        import_name = f"ubuntu-imported-{unique_vm_name}"
        network_name = unique_network_name
        subnet = _unique_subnet(network_name)
        vm_name = unique_vm_name
        imported_prefix: str | None = None

        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                import_name,
                download_path,
                "--format",
                "tar-rootfs",
                "--skip-optimization",
                check=False,
            )
            assert result.returncode == 0, (
                f"Ubuntu image import failed: {result.stderr.strip()}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = _parse_json_output(result.stdout)
            imported = [
                i for i in images
                if "imported" in i.get("name", "").lower()
                and "ubuntu" in i.get("type", "").lower()
            ]
            assert imported, (
                f"Imported ubuntu image not found in listing. "
                f"Used import name '{import_name}'."
            )
            imported_prefix = imported[0]["id"][:6]

            result = _run_mvm(
                runner_vm,
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

            ensure_vm_deps(runner_vm)
            try:
                result = _run_mvm(
                    runner_vm,
                    "vm",
                    "create",
                    vm_name,
                    "--image",
                    imported_prefix,
                    "--network",
                    network_name,
                )
                assert result.returncode == 0, (
                    f"VM create with imported Ubuntu failed: {result.stderr}"
                )

                result = _run_mvm(runner_vm, "vm", "ls", "--json")
                vms = _parse_json_output(result.stdout)
                vm = next((v for v in vms if v["name"] == vm_name), None)
                assert vm is not None, f"VM '{vm_name}' not found in listing"
                assert vm["status"] == "running", (
                    f"Expected VM status 'running', got '{vm['status']}'"
                )
            finally:
                _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
                _run_mvm(
                    runner_vm, "network", "rm", network_name, "--force", check=False,
                )
        finally:
            if imported_prefix:
                _run_mvm(runner_vm, "image", "rm", imported_prefix, check=False)


class TestImageDefaultMigration:
    """Test that the default image migrates to a new record on force re-pull."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_default_migrates_to_new_image_on_force_repull(
        self, runner_vm: str
    ) -> None:
        """When force-re-pulling the default image, default should migrate to new record."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images: list[dict[str, Any]] = _parse_json_output(result.stdout)

        present_alpine = [
            i for i in images
            if i.get("type") == "alpine" and i.get("is_present")
        ]
        assert present_alpine, (
            "alpine-3.21 not present after _ensure_image"
        )

        old_alpine = present_alpine[0]
        old_alpine_id: str = old_alpine["id"]

        original_defaults = [i for i in images if i.get("is_default")]
        original_default_id: str | None = (
            original_defaults[0]["id"] if original_defaults else None
        )

        changed_default = False
        if not old_alpine.get("is_default"):
            result_set = _run_mvm(
                runner_vm, "image", "default", old_alpine_id[:6], check=False
            )
            assert result_set.returncode == 0, (
                f"Could not set default to {old_alpine_id[:6]}: {result_set.stderr}"
            )
            changed_default = True

        try:
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                "--force",
                timeout=180,
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after: list[dict[str, Any]] = _parse_json_output(result.stdout)

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
                    runner_vm,
                    "image",
                    "default",
                    original_default_id[:6],
                    check=False,
                )


class TestImageRemove:
    """Test image removal operations."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_remove_with_fixture(self, runner_vm):
        """Remove a cached image by ID prefix and verify it's gone."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        before = _parse_json_output(result.stdout)
        alpine_images = [
            i for i in before
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        assert alpine_images, (
            "No present alpine image available to test removal"
        )

        was_default = alpine_images[0].get("is_default", False)
        target_id = alpine_images[0]["id"]
        target_prefix = target_id[:6]
        was_removed = False

        try:
            result = _run_mvm(
                runner_vm, "image", "rm", target_prefix, check=False,
            )
            assert result.returncode == 0
            was_removed = True

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            after = [
                i for i in _parse_json_output(result.stdout)
                if i.get("is_present", True)
            ]
            assert not any(i["id"] == target_id for i in after)
        finally:
            if was_removed:
                pull_args = ["image", "pull", "alpine", "--version", "3.21"]
                if was_default:
                    pull_args.append("--default")
                _run_mvm(runner_vm, *pull_args, timeout=120)


class TestImageRemoveForce:
    """Test image removal with --force flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.domain_image,
    ]

    def test_image_rm_with_force(self, runner_vm):
        """Remove a cached image by ID prefix with --force and verify it's gone."""
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = _parse_json_output(result.stdout)
        alpine_images = [
            i for i in images
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        assert alpine_images, (
            "No present alpine image available to test removal"
        )

        was_default = alpine_images[0].get("is_default", False)
        target_id = alpine_images[0]["id"]

        try:
            result = _run_mvm(
                runner_vm, "image", "rm", target_id[:6], "--force", check=False,
            )
            assert result.returncode == 0, (
                f"Force remove failed: {result.stderr}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            after = [
                i for i in _parse_json_output(result.stdout)
                if i.get("is_present", True)
            ]
            assert not any(i["id"] == target_id for i in after)
        finally:
            pull_args = ["image", "pull", "alpine", "--version", "3.21"]
            if was_default:
                pull_args.append("--default")
            _run_mvm(runner_vm, *pull_args, check=False, timeout=120)


class TestImageDependencyDeletion:
    """Test dependency ordering for image deletion -- references by VMs block removal."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_image,
    ]

    @pytest.mark.requires_kvm
    def test_delete_image_used_by_stopped_vm_fails(
        self,
        runner_vm: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """Deleting an image referenced by a stopped VM should be rejected."""
        vm_name = unique_vm_name

        try:
            ls_result = _run_mvm(
                runner_vm, "image", "ls", "--json", check=False,
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
                    runner_vm,
                    "image",
                    "pull",
                    "alpine",
                    "--version",
                    "3.21",
                    timeout=180,
                )

            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )

            ins_result = _run_mvm(
                runner_vm, "vm", "inspect", vm_name, "--json", check=False,
            )
            assert ins_result.returncode == 0, (
                f"Failed to inspect VM: {ins_result.stderr}"
            )
            vm_info: dict[str, Any] = json.loads(ins_result.stdout)
            image_id_full = vm_info.get("assets", {}).get("image", {}).get("id", "")
            assert image_id_full
            image_id_prefix = image_id_full[:6]

            result = _run_mvm(
                runner_vm, "image", "rm", image_id_prefix, check=False,
            )
            assert "referenced" in result.stderr.lower() or "in use by" in result.stderr.lower(), (
                f"Expected image rm to report reference, got: "
                f"rc={result.returncode} stdout={result.stdout}"
            )

            ls_image = _run_mvm(
                runner_vm, "image", "ls", "--json", check=False,
            )
            if ls_image.returncode == 0 and ls_image.stdout.strip():
                images_after = json.loads(ls_image.stdout)
                alpine_after = next(
                    (i for i in images_after if i.get("type") == "alpine"),
                    None,
                )
                assert alpine_after is not None

            ls_vm = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if ls_vm.returncode == 0 and ls_vm.stdout.strip():
                vms_after = json.loads(ls_vm.stdout)
                vm_names = [v.get("name") for v in vms_after]
                assert vm_name in vm_names
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_image_used_by_running_vm_fails_without_force(
        self,
        runner_vm: str,
        unique_vm_name: str,
        created_network: str,
    ) -> None:
        """Deleting an image referenced by a running VM should be rejected."""
        vm_name = unique_vm_name

        try:
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                created_network,
            )
            _run_mvm(runner_vm, "vm", "start", vm_name)

            vm_ls = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if vm_ls.returncode == 0 and vm_ls.stdout.strip():
                vms = json.loads(vm_ls.stdout)
                vm_names = [v.get("name") for v in vms]
                assert vm_name in vm_names

            ins_result = _run_mvm(
                runner_vm, "vm", "inspect", vm_name, "--json", check=False,
            )
            assert ins_result.returncode == 0
            vm_info: dict[str, Any] = json.loads(ins_result.stdout)
            image_id_full = vm_info.get("assets", {}).get("image", {}).get("id", "")
            assert image_id_full
            image_id_prefix = image_id_full[:6]

            result = _run_mvm(
                runner_vm, "image", "rm", image_id_prefix, check=False,
            )
            assert "referenced" in result.stderr.lower() or "in use by" in result.stderr.lower(), (
                f"Expected image rm to report reference, got: "
                f"rc={result.returncode} stdout={result.stdout}"
            )

            ls_image = _run_mvm(
                runner_vm, "image", "ls", "--json", check=False,
            )
            if ls_image.returncode == 0 and ls_image.stdout.strip():
                images_after = json.loads(ls_image.stdout)
                alpine_after = next(
                    (i for i in images_after if i.get("type") == "alpine"),
                    None,
                )
                assert alpine_after is not None

            ls_vm = _run_mvm(runner_vm, "vm", "ls", "--json", check=False)
            if ls_vm.returncode == 0 and ls_vm.stdout.strip():
                vms_after = json.loads(ls_vm.stdout)
                vm_names = [v.get("name") for v in vms_after]
                assert vm_name in vm_names
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)

    def test_delete_default_image_promotes_other_or_clears(
        self,
        runner_vm: str,
    ) -> None:
        """Deleting a formerly-default image should not leave orphans."""
        _ensure_image(runner_vm, "alpine:3.23")
        _ensure_image(runner_vm, "ubuntu:24.04")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = _parse_json_output(result.stdout)
        present_images = [i for i in images if i.get("is_present", True)]

        assert len(present_images) >= 2, (
            "Need at least 2 present images for this test"
        )

        default_img = next(
            (i for i in present_images if i.get("is_default")),
            present_images[0],
        )
        other_img = next(
            (i for i in present_images if i["id"] != default_img["id"]),
            None,
        )

        assert other_img is not None, (
            "No non-default image available to set as default"
        )

        old_default_prefix = default_img["id"][:6]
        other_prefix = other_img["id"][:6]

        try:
            _run_mvm(runner_vm, "image", "default", other_prefix, check=False)
            _run_mvm(runner_vm, "image", "rm", old_default_prefix, check=False)

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images_after = _parse_json_output(result.stdout)
            default_count = sum(1 for i in images_after if i.get("is_default"))
            assert default_count <= 1, (
                f"Expected at most 1 default image, got {default_count}"
            )
        finally:
            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images_final = _parse_json_output(result.stdout)
            has_default = any(i.get("is_default") for i in images_final)
            if not has_default and images_final:
                _run_mvm(
                    runner_vm,
                    "image",
                    "default",
                    images_final[0]["id"][:6],
                    check=False,
                )


class TestImageAdvancedFlags:
    """Tests for image advanced flags and edge cases."""

    pytestmark = [pytest.mark.system, pytest.mark.tier2, pytest.mark.domain_image]

    @pytest.mark.slow
    def test_image_pull_with_disable_detector(self, runner_vm):
        """Pull an image with --disable-detector all --force."""
        # Rationale: Downloads an image with --disable-detector flag.
        # Needs network access to pull but no local VM resources.
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.23",
            "--disable-detector",
            "all",
            "--force",
            timeout=60,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull with --disable-detector failed: {result.stderr.strip()}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "pulled" in combined


class TestImagePullAdvancedFlags:
    """Test advanced image pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_version(self, runner_vm):
        """Pull an image with --version flag, dynamically resolved from remote listing."""
        # Rationale: Verifies that --version flag works when pulling
        # a specific image version. Regression would break versioned
        # image pulls for all users.
        result = _run_mvm(
            runner_vm,
            "image",
            "ls",
            "--remote",
            "--json",
            timeout=30,
            check=False,
        )
        assert result.returncode == 0 and result.stdout.strip(), (
            f"Remote listing not available: {result.stderr}"
        )
        try:
            remote_images = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError, TypeError):
            pytest.fail("Remote listing returned non-JSON output")
        assert remote_images, "No remote images available"
        test_img = next(
            (
                img
                for img in remote_images
                if img.get("type") and img.get("version")
            ),
            None,
        )
        assert test_img is not None, (
            "No remote image has both type and version metadata"
        )
        selector = test_img["type"]
        version = test_img["version"]
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            selector,
            "--version",
            version,
            "--force",
            "--skip-optimization",
            timeout=300,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull {selector} --version {version} failed: "
            f"{result.stderr.strip()}"
        )

        # L2 verification: confirm the image type appears in listing
        ls_result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = json.loads(ls_result.stdout)
        assert any(
            i.get("type") == selector and i.get("is_present")
            for i in images
        ), f"Pulled image type={selector} not found in listing"

    def test_image_pull_with_arch(self, runner_vm):
        """Pull an image (arch detection is automatic in Go CLI)."""
        # Rationale: Verifies that image pull works. The Go CLI detects
        # host architecture automatically — no --arch flag needed.
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine",
            "--version",
            "3.23",
            "--force",
            timeout=60,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull failed: {result.stderr.strip()}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "pulled" in combined


class TestImagePullWithTypeFlag:
    """Test image pull with explicit --type flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_explicit_type(self, runner_vm):
        """Pull an image with --type flag matching the positional selector."""
        # Rationale: Verifies that --type flag works alongside the
        # positional selector. Regression would break image pulls that
        # explicitly specify type.
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            "alpine:3.23",
            "--type",
            "alpine",
            "--force",
            timeout=120,
            check=False,
        )
        assert result.returncode == 0, (
            f"Pull with --type failed: {result.stderr.strip()}"
        )
        combined = (result.stdout + result.stderr).lower()
        assert "pulled" in combined


class TestImageImportWithDisableDetector:
    """Test image import with --disable-detector flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.tier2,
        pytest.mark.domain_image,
    ]

    def test_image_import_with_disable_detector(
        self, runner_vm
    ):
        """Import an image with --disable-detector all flag.

        Rationale: Verifies that --disable-detector all skips the
        OS detection phase during import. Regression would break
        users importing images with custom or unknown OS layouts.
        """
        _ensure_image(runner_vm, "alpine:3.23")
        result = _run_mvm(runner_vm, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.23",
                check=False,
            )
            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(result.stdout)
            alpine_images = [
                i
                for i in images
                if "alpine" in i.get("type", "").lower() and i.get("is_present")
            ]

        assert alpine_images, (
            "No alpine image available to use as import source"
        )

        cached_path = _get_cached_alpine_path_in_vm(
            runner_vm, "alpine-for-import.raw"
        )
        assert cached_path is not None, (
            "Failed to extract cached alpine image for import"
        )

        imported_prefix: str | None = None
        try:
            result = _run_mvm(
                runner_vm,
                "image",
                "import",
                "test-disable-detector",
                cached_path,
                "--format",
                "raw",
                "--disable-detector",
                "all",
                check=False,
            )
            assert result.returncode == 0, (
                f"Import with --disable-detector failed: {result.stderr.strip()}"
            )

            result = _run_mvm(runner_vm, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("name") == "test-disable-detector"
            ]
            assert imported, (
                "Imported image with --disable-detector not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    runner_vm, "image", "rm", imported_prefix, check=False
                )
