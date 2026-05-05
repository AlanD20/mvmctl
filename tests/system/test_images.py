"""Image management system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.slow]


class TestImagePull:
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
        result = _run_mvm(mvm_binary, "image", "pull", image_id, timeout=300)
        assert result.returncode == 0
        assert "ready" in result.stdout.lower()


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
        # Get first cached image
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        if not images:
            pytest.skip("No cached images to inspect")
        prefix = images[0]["id"][:6]
        result = _run_mvm(mvm_binary, "image", "inspect", prefix)
        assert result.returncode == 0

    def test_image_inspect_json(self, mvm_binary):
        """Inspect an image with --json output."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        if not images:
            pytest.skip("No cached images to inspect")
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

        result = _run_mvm(mvm_binary, "image", "set-default", "alpine-3.21")
        assert result.returncode == 0
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
        assert result.returncode == 0
        assert (
            "warmed" in result.stdout.lower()
            or "ready" in result.stdout.lower()
        )


class TestImageRemove:
    """Test image removal operations."""

    @pytest.mark.serial
    def test_image_remove_with_fixture(self, mvm_binary):
        """Remove a cached image by ID prefix and verify it's gone."""

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        before = json.loads(result.stdout)
        alpine_images = [
            i for i in before if "alpine" in i.get("os_slug", "").lower()
        ]
        if not alpine_images:
            pytest.skip("No alpine image available to test removal")

        target_id = alpine_images[0]["id"]

        # Remove the image
        result = _run_mvm(
            mvm_binary,
            "image",
            "rm",
            target_id[:6],
            check=False,
        )
        assert result.returncode == 0

        # Verify gone (filter by is_present to account for soft-delete)
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        after = [
            i for i in json.loads(result.stdout) if i.get("is_present", True)
        ]
        assert not any(i["id"] == target_id for i in after)

        # Re-pull so other tests aren't broken
        repull = _run_mvm(
            mvm_binary, "image", "pull", "alpine-3.21", check=False
        )
        assert repull.returncode == 0, f"Re-pull failed: {repull.stderr}"

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

    pytestmark = [pytest.mark.system, pytest.mark.slow]

    @pytest.mark.serial
    def test_image_import_local_file(self, mvm_binary, tmp_path):
        """Import a local image file."""
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

        prefix = alpine_images[0]["id"][:6]

        # Inspect to get path
        result = _run_mvm(mvm_binary, "image", "inspect", prefix, "--json")
        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        # Copy to temp location
        temp_path = tmp_path / "alpine-import.raw"
        shutil.copy2(source_path, temp_path)

        imported_prefix = None
        try:
            # Import the copied image
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

            # Verify imported image appears
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("os_slug") == "imported-alpine"
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

    pytestmark = [pytest.mark.system]

    def test_image_inspect_tree_output(self, mvm_binary):
        """Inspect an image with --tree output."""
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        if not images:
            pytest.skip("No cached images to inspect")
        prefix = images[0]["id"][:6]

        result = _run_mvm(mvm_binary, "image", "inspect", prefix, "--tree")
        assert result.returncode == 0
        assert (
            "├──" in result.stdout
            or "└──" in result.stdout
            or "ID:" in result.stdout
        )
