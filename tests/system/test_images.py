"""Image management system tests."""

from __future__ import annotations

import json
import os
import subprocess

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.slow]


class TestImageFetch:
    """Test image fetching operations."""

    @pytest.mark.parametrize(
        "image_id",
        [
            "alpine-3.21",
            "ubuntu-24.04-minimal",
        ],
    )
    def test_image_fetch(self, mvm_binary, image_id):
        """Fetch each supported image.

        Tests a lightweight image (alpine) and a common one (ubuntu-minimal).
        Full list of 5 images is tested in CI on a schedule, not per-PR.
        """
        result = subprocess.run(
            [*mvm_binary.split(), "image", "fetch", image_id],
            capture_output=True,
            text=True,
            timeout=300,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert image_id in result.stdout.lower()


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

    def test_image_set_default(self, mvm_binary):
        """Set image as default."""
        # Ensure image exists before setting default
        _run_mvm(mvm_binary, "image", "fetch", "alpine-3.21", check=False)

        result = _run_mvm(mvm_binary, "image", "set-default", "alpine-3.21")
        assert result.returncode == 0
        assert "default" in result.stdout.lower()

    def test_image_get_default(self, mvm_binary):
        """Get default image."""
        result = _run_mvm(mvm_binary, "image", "get-default", check=False)
        # Either succeeds (has a default) or gracefully reports no default
        if result.returncode != 0:
            assert "no default" in result.stdout.lower()

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

    def test_image_remove_with_fixture(self, mvm_binary, unique_vm_name):
        """Fetch a unique image (using a newly fetched small image) and remove it."""
        # The test fetches a specific image (already pre-cached), verifies it exists,
        # removes it, and verifies it's gone.
        # We use alpine-3.21 since it's the smallest and likely pre-cached.
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        import json as _json

        before = _json.loads(result.stdout)
        alpine_images = [
            i for i in before if "alpine" in i.get("name", "").lower()
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

        # Verify gone
        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        after = _json.loads(result.stdout)
        assert not any(i["id"] == target_id for i in after)

        # Re-fetch so other tests aren't broken
        _run_mvm(mvm_binary, "image", "fetch", "alpine-3.21", check=False)

    def test_image_fetch_nonexistent(self, mvm_binary):
        """Fetch a nonexistent image and expect failure."""
        result = _run_mvm(
            mvm_binary,
            "image",
            "fetch",
            "completely-nonexistent-image-12345",
            check=False,
        )
        assert result.returncode != 0
