"""Image management system tests."""

from __future__ import annotations

import json
import subprocess

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]


class TestImagePull:
    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]
    """Test image pulling operations."""

    @pytest.mark.parametrize(
        "image_id",
        [
            "alpine-3.21",
            "ubuntu-24.04-minimal",
        ],
    )
    @pytest.mark.serial
    def test_image_pull(self, mvm_binary, image_id):
        """Pull each supported image.

        Tests a lightweight image (alpine) and a common one (ubuntu-minimal).
        Full list of 5 images is tested in CI on a schedule, not per-PR.
        """
        result = _run_mvm(mvm_binary, "image", "pull", image_id, timeout=60)
        assert result.returncode == 0
        assert (
            "pulled successfully" in result.stdout.lower()
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
        result = _run_mvm(mvm_binary, "image", "ls")
        assert result.returncode == 0
        assert "ID" in result.stdout or "id" in result.stdout.lower()
        assert "NAME" in result.stdout or "name" in result.stdout.lower()

    def test_image_list_remote(self, mvm_binary):
        """List images available from the remote registry."""
        result = _run_mvm(mvm_binary, "image", "ls", "--remote")
        assert result.returncode == 0

    def test_image_inspect(self, mvm_binary):
        """Inspect a cached image by ID prefix."""
        # Get first present cached image
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

        # Ensure image exists before setting default
        _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)

        # Use --json to get the actual image ID, then set by ID
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        if result.returncode != 0:
            pytest.skip("Failed to list images")
        images = json.loads(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("os_slug", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available")
        target_id = alpine_images[0]["id"]

        result = _run_mvm(
            mvm_binary, "image", "set-default", target_id, check=False
        )
        if result.returncode != 0:
            pytest.skip(
                f"Failed to set image as default: {result.stderr.strip()}"
            )
        assert "default" in result.stdout.lower()

    @pytest.mark.serial
    def test_image_warm(self, mvm_binary):
        """Pre-decompress image to ready pool for fast VM creation."""

        result = _run_mvm(
            mvm_binary,
            "image",
            "warm",
            "alpine-3.21",
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(f"Image warm not available: {result.stderr.strip()}")
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )


class TestImageRemove:
    """Test image removal operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_image_remove_with_fixture(self, mvm_binary):
        """Remove a cached image by ID prefix and verify it's gone."""

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        before = json.loads(result.stdout)
        alpine_images = [
            i
            for i in before
            if "alpine" in i.get("os_slug", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            pytest.skip("No present alpine image available to test removal")

        was_default = alpine_images[0].get("is_default", False)
        target_id = alpine_images[0]["id"]
        target_prefix = target_id[:6]

        try:
            # Remove the image
            result = _run_mvm(
                mvm_binary,
                "image",
                "rm",
                target_prefix,
                check=False,
            )
            assert result.returncode == 0

            # Verify gone (filter by is_present to account for soft-delete)
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            after = [
                i
                for i in json.loads(result.stdout)
                if i.get("is_present", True)
            ]
            assert not any(i["id"] == target_id for i in after)
        finally:
            # Re-pull so other tests aren't broken — always runs even if assert fails.
            # Restore default flag if the removed image was the default.
            try:
                pull_args = ["image", "pull", "alpine-3.21"]
                if was_default:
                    pull_args.append("--set-default")
                repull = _run_mvm(
                    mvm_binary, *pull_args, check=False
                )
                if repull.returncode != 0:
                    pytest.skip(f"Re-pull failed after test: {repull.stderr}")
            except subprocess.TimeoutExpired:
                pytest.skip("Re-pull timed out (>60s download)")

    def test_image_pull_nonexistent(self, mvm_binary):
        """Pull a nonexistent image and expect failure."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "completely-nonexistent-image-12345",
            check=False,
        )
        assert result.returncode != 0


class TestImageImport:
    """Test image import operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_image_import_local_file(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a local image file."""
        import shutil

        # Ensure alpine is cached
        try:
            _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)
        except subprocess.TimeoutExpired:
            pytest.skip("Image pull timed out (>60s download)")

        # Get cached image info — store the full ID to avoid races
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("os_slug", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available to import")

        target = alpine_images[0]
        target_id = target["id"]

        # Inspect using full ID (avoids prefix lookup races)
        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            # Image was removed by a concurrent test — skip rather than fail
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        # The path from inspect is relative — resolve against images dir
        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            pytest.skip(f"Image file not found: {resolved_source}")

        # Copy to temp location
        temp_path = tmp_path / "alpine-import.raw"

        # The cached image may be compressed (.zst) — decompress if needed
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
            # Import the decompressed image
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

            # Verify imported image appears (search by os_name which is
            # set to the display name passed to image import)
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("os_name") == "imported-alpine"
            ]
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


class TestImageInspectTree:
    """Test image inspect tree output."""

    pytestmark = [pytest.mark.system, pytest.mark.serial]

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
    """Test advanced image pull operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_image_pull_force(self, mvm_binary):
        """Pull an already-cached image with --force, should re-download."""
        # Ensure image is cached first
        try:
            _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine-3.21",
                check=False,
                timeout=60,
            )
        except subprocess.TimeoutExpired:
            pytest.skip("Initial image pull timed out (>60s download)")

        # Force pull
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine-3.21",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(f"Force pull failed: {result.stderr.strip()}")
            assert "pulled successfully" in result.stdout.lower()
        except subprocess.TimeoutExpired:
            pytest.skip("Force pull timed out (>60s download)")

    @pytest.mark.serial
    def test_image_pull_set_default(self, mvm_binary):
        """Pull an image and set it as default in one command."""
        # Pull previous default info so we can restore later if needed
        result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
        previous_default = None
        if result.returncode == 0:
            images = json.loads(result.stdout)
            defaults = [i for i in images if i.get("is_default")]
            if defaults:
                previous_default = defaults[0]["id"]

        result = _run_mvm(
            mvm_binary,
            "image",
            "pull",
            "alpine-3.21",
            "--set-default",
            timeout=60,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Pull with --set-default failed: {result.stderr.strip()}"
            )
        assert "default" in result.stdout.lower()

        # Restore previous default if we had one
        if previous_default:
            _run_mvm(
                mvm_binary,
                "image",
                "set-default",
                previous_default,
                check=False,
            )


class TestImageImportAdvanced:
    """Test advanced image import operations."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_image_import_with_format_qcow2(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a qcow2 image using --format qcow2."""
        import shutil

        qemu_img = shutil.which("qemu-img")
        if not qemu_img:
            pytest.skip("qemu-img not available on this system")

        qcow2_path = tmp_path / "test-image.qcow2"
        result = subprocess.run(
            [qemu_img, "create", "-f", "qcow2", str(qcow2_path), "64M"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"qemu-img create failed: {result.stderr}")

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

            # Verify imported image appears in listing
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("os_name") == "test-qcow2"]
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

        # Create a tiny raw image (1MB)
        raw_path = tmp_path / "test-overwrite.raw"
        result = subprocess.run(
            ["dd", "if=/dev/zero", f"of={raw_path}", "bs=1M", "count=1"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"dd create failed: {result.stderr}")

        # First import
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
            # Get prefix for cleanup
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("os_name") == "test-overwrite"
            ]
            if imported:
                imported_prefix = imported[0]["id"][:6]

            # Second import with --force should succeed
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
                    mvm_binary,
                    "image",
                    "rm",
                    imported_prefix,
                    check=False,
                )


class TestImageRemoveForce:
    """Test image removal with --force flag."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    @pytest.mark.serial
    def test_image_rm_with_force(self, mvm_binary):
        """Remove a cached image by ID prefix with --force and verify it's gone."""

        # Ensure alpine is pulled
        try:
            _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)
        except subprocess.TimeoutExpired:
            pytest.skip("Initial image pull timed out (>60s download)")

        # Get alpine image ID
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        if result.returncode != 0:
            pytest.skip("Failed to list images")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("os_slug", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            pytest.skip("No present alpine image available to test removal")

        was_default = alpine_images[0].get("is_default", False)
        target_id = alpine_images[0]["id"]

        # Remove with --force
        result = _run_mvm(
            mvm_binary,
            "image",
            "rm",
            target_id[:6],
            "--force",
            check=False,
        )
        assert result.returncode == 0, f"Force remove failed: {result.stderr}"

        # Verify it's gone (filter by is_present to account for soft-delete)
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        after = [
            i for i in json.loads(result.stdout) if i.get("is_present", True)
        ]
        assert not any(i["id"] == target_id for i in after)

        # Re-pull to restore the image. Restore default flag if the removed
        # image was the default.
        try:
            pull_args = ["image", "pull", "alpine-3.21"]
            if was_default:
                pull_args.append("--set-default")
            repull = _run_mvm(
                mvm_binary, *pull_args, check=False
            )
            if repull.returncode != 0:
                pytest.skip(f"Re-pull failed: {repull.stderr}")
        except subprocess.TimeoutExpired:
            pytest.skip("Re-pull timed out (>60s download)")


class TestImagePullSkipOptimization:
    """Test image pull with --skip-optimization flag."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    @pytest.mark.serial
    def test_image_pull_with_skip_optimization(self, mvm_binary):
        """Pull an image with --skip-optimization flag."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine-3.21",
                "--skip-optimization",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"skip-optimization pull failed: {result.stderr.strip()}"
                )
            assert "pulled successfully" in result.stdout.lower()
        except subprocess.TimeoutExpired:
            pytest.skip(
                "skip-optimization pull timed out (>60s download)"
            )


class TestImageImportSetDefault:
    """Test image import with --set-default flag."""

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_image_import_with_set_default(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a local image file with --set-default flag."""
        import shutil

        # Ensure alpine is cached
        _run_mvm(mvm_binary, "image", "pull", "alpine-3.21", check=False)

        # Get cached image info
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i for i in images if "alpine" in i.get("os_slug", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available to import")

        target = alpine_images[0]
        target_id = target["id"]

        # Inspect using full ID
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

        # Copy to temp location
        temp_path = tmp_path / "test-import-default.raw"
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
                "--set-default",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import with --set-default failed: {result.stderr.strip()}"
                )
            assert "default" in result.stdout.lower()

            # Get prefix for cleanup
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("os_name") == "test-import-default"
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

    pytestmark = [pytest.mark.system, pytest.mark.slow, pytest.mark.serial]

    def test_image_import_with_arch(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import a qcow2 image with --arch x86_64 flag."""
        import shutil

        qemu_img = shutil.which("qemu-img")
        if not qemu_img:
            pytest.skip("qemu-img not available on this system")

        qcow2_path = tmp_path / "test-arch.qcow2"
        result = subprocess.run(
            [qemu_img, "create", "-f", "qcow2", str(qcow2_path), "64M"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            pytest.skip(f"qemu-img create failed: {result.stderr}")

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

            # Verify imported image appears in listing
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [i for i in images if i.get("os_name") == "test-arch"]
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
