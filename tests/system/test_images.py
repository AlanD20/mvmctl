"""Image management system tests."""

import os
import pytest
import subprocess

pytestmark = [pytest.mark.system, pytest.mark.slow]


class TestImageFetch:
    """Test image fetching operations."""

    @pytest.mark.parametrize(
        "image_id",
        [
            "alpine-3.21",
            "ubuntu-24.04-minimal",
            "ubuntu-24.04",
            "archlinux",
            "debian-bookworm",
        ],
    )
    def test_image_fetch(self, mvm_binary, image_id):
        """Fetch each supported image."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "fetch", image_id],
            capture_output=True,
            text=True,
            timeout=300,  # 5 minute timeout for downloads
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert "fetched" in result.stdout.lower() or "downloaded" in result.stdout.lower()


class TestImageList:
    """Test image listing operations."""

    def test_image_list_json(self, mvm_binary):
        """List images in JSON format."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "ls", "--json"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        # Should be valid JSON
        import json

        data = json.loads(result.stdout)
        assert isinstance(data, list)

    def test_image_list_table(self, mvm_binary):
        """List images in table format."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        # Should contain column headers
        assert "NAME" in result.stdout or "name" in result.stdout.lower()


class TestImageDefaults:
    """Test image default operations."""

    def test_image_set_default(self, mvm_binary):
        """Set image as default."""
        # First ensure we have an image
        subprocess.run(
            [*mvm_binary.split(), "image", "fetch", "alpine-3.21"],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )

        result = subprocess.run(
            [*mvm_binary.split(), "image", "set-default", "alpine-3.21"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_image_get_default(self, mvm_binary):
        """Get default image."""
        result = subprocess.run(
            [*mvm_binary.split(), "image", "get-default"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        # May fail if no default set, that's OK
        assert result.returncode == 0 or "no default" in result.stdout.lower()


class TestImageRemove:
    """Test image removal operations."""

    def test_image_remove(self, mvm_binary):
        """Remove an image."""
        # This test is destructive - only run if explicitly enabled
        pytest.skip("Destructive test - run manually with --run-destructive")
