"""Volume management system tests — CRUD and lifecycle."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_volume,
]


class TestVolumeLifecycle:
    """Test volume CRUD operations."""

    def test_volume_create(self, mvm_binary, unique_key_name):
        """Create a volume with name and size."""
        vol_name = f"sys-vol-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "512M",
            )
            assert result.returncode == 0
            assert vol_name in result.stdout
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_create_with_format_qcow2(self, mvm_binary, unique_key_name):
        """Create a volume with --format qcow2."""
        vol_name = f"sys-vol-qcow2-{unique_key_name}"
        try:
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "512M",
                "--format",
                "qcow2",
            )
            assert result.returncode == 0
            assert vol_name in result.stdout
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_duplicate_name_rejected(self, mvm_binary, unique_key_name):
        """Creating a volume with duplicate name should be rejected."""
        vol_name = f"sys-vol-dup-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")
            result = _run_mvm(
                mvm_binary,
                "volume",
                "create",
                vol_name,
                "256M",
                check=False,
            )
            assert result.returncode != 0
            assert "already exists" in result.stdout.lower()
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_list(self, mvm_binary, unique_key_name):
        """List volumes — should include newly created volume."""
        vol_name = f"sys-vol-list-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(mvm_binary, "volume", "ls")
            assert result.returncode == 0
            assert vol_name in result.stdout
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_list_json(self, mvm_binary, unique_key_name):
        """List volumes in JSON format."""
        vol_name = f"sys-vol-json-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            assert result.returncode == 0
            data = json.loads(result.stdout)
            assert isinstance(data, list)
            assert any(v["name"] == vol_name for v in data)
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_inspect(self, mvm_binary, unique_key_name):
        """Inspect a volume and verify fields."""
        vol_name = f"sys-vol-inspect-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(mvm_binary, "volume", "inspect", vol_name)
            assert result.returncode == 0
            assert vol_name in result.stdout
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_inspect_json(self, mvm_binary, unique_key_name):
        """Inspect a volume with --json and verify parsed fields."""
        vol_name = f"sys-vol-ijson-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(
                mvm_binary, "volume", "inspect", vol_name, "--json"
            )
            assert result.returncode == 0
            data = json.loads(result.stdout)
            assert "name" in data
            assert "size_bytes" in data
            assert "format" in data
            assert "status" in data
            assert "path" in data
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_remove(self, mvm_binary, unique_key_name):
        """Create and remove a volume, verify it's gone."""
        vol_name = f"sys-vol-rm-{unique_key_name}"
        _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

        result = _run_mvm(mvm_binary, "volume", "rm", vol_name)
        assert result.returncode == 0

        # Verify gone
        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        volumes = json.loads(result.stdout)
        assert not any(v["name"] == vol_name for v in volumes)

    def test_volume_remove_nonexistent(self, mvm_binary):
        """Removing a nonexistent volume should fail."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "rm",
            "nonexistent-volume-xyz",
            check=False,
        )
        assert result.returncode != 0
        assert (
            "not found" in result.stdout.lower()
            or "not found" in result.stderr.lower()
        )

    def test_volume_resize(self, mvm_binary, unique_key_name):
        """Create a volume and resize it."""
        vol_name = f"sys-vol-resize-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(mvm_binary, "volume", "resize", vol_name, "1G")
            assert result.returncode == 0

            # Verify new size in inspect
            inspect = _run_mvm(
                mvm_binary,
                "volume",
                "inspect",
                vol_name,
                "--json",
            )
            data = json.loads(inspect.stdout)
            assert data["size_bytes"] == 1024 * 1024 * 1024
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

    def test_volume_rm_with_force(self, mvm_binary, unique_key_name):
        """Remove a volume with --force."""
        vol_name = f"sys-vol-frc-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol_name, "512M")

            result = _run_mvm(mvm_binary, "volume", "rm", vol_name, "--force")
            assert result.returncode == 0
        except Exception:
            _run_mvm(mvm_binary, "volume", "rm", vol_name, check=False)

        # Verify gone
        result = _run_mvm(mvm_binary, "volume", "ls", "--json")
        volumes = json.loads(result.stdout)
        assert not any(v["name"] == vol_name for v in volumes)

    # ── Edge case tests ─────────────────────────────────────────────────────

    def test_volume_ls_empty(self, mvm_binary):
        """Listing volumes when none exist should succeed with empty output."""
        result = _run_mvm(mvm_binary, "volume", "ls")
        assert result.returncode == 0

    def test_volume_create_invalid_size_fails(self, mvm_binary):
        """Creating a volume with invalid size should be rejected."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "create",
            "invalid-size-vol",
            "abc",
            check=False,
        )
        assert result.returncode != 0

    def test_volume_create_invalid_format_fails(
        self, mvm_binary, unique_key_name
    ):
        """Creating a volume with invalid --format should be rejected."""
        vol_name = f"sys-vol-badfmt-{unique_key_name}"
        result = _run_mvm(
            mvm_binary,
            "volume",
            "create",
            vol_name,
            "512M",
            "--format",
            "vmdk",
            check=False,
        )
        assert result.returncode != 0

    def test_volume_inspect_nonexistent_fails(self, mvm_binary):
        """Inspecting a nonexistent volume should fail."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "inspect",
            "nonexistent-volume-abc",
            check=False,
        )
        assert result.returncode != 0

    def test_volume_resize_nonexistent_fails(self, mvm_binary):
        """Resizing a nonexistent volume should fail."""
        result = _run_mvm(
            mvm_binary,
            "volume",
            "resize",
            "nonexistent-volume-def",
            "1G",
            check=False,
        )
        assert result.returncode != 0

    def test_volume_remove_multiple(self, mvm_binary, unique_key_name):
        """Remove two volumes at once."""
        vol1 = f"sys-vol-mrm1-{unique_key_name}"
        vol2 = f"sys-vol-mrm2-{unique_key_name}"
        try:
            _run_mvm(mvm_binary, "volume", "create", vol1, "512M")
            _run_mvm(mvm_binary, "volume", "create", vol2, "256M")

            result = _run_mvm(mvm_binary, "volume", "rm", vol1, vol2)
            assert result.returncode == 0

            # Verify both gone
            result = _run_mvm(mvm_binary, "volume", "ls", "--json")
            volumes = json.loads(result.stdout)
            assert not any(v["name"] == vol1 for v in volumes)
            assert not any(v["name"] == vol2 for v in volumes)
        finally:
            _run_mvm(mvm_binary, "volume", "rm", vol1, vol2, check=False)
