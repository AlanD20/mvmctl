"""SSH key management system tests."""

from __future__ import annotations

import json
import os
import subprocess

import pytest

from tests.system.conftest import _run_mvm

pytestmark = pytest.mark.system


class TestKeyLifecycle:
    """Test SSH key CRUD operations."""

    def test_key_create_ed25519(self, mvm_binary, unique_key_name):
        """Create ed25519 SSH key."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
        )
        assert result.returncode == 0
        assert unique_key_name in result.stdout

        # Cleanup
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_rsa(self, mvm_binary, unique_key_name):
        """Create RSA SSH key."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "rsa",
        )
        assert result.returncode == 0
        assert unique_key_name in result.stdout

        # Cleanup
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_listing(self, mvm_binary, created_key):
        """List keys and verify created key appears."""
        result = _run_mvm(mvm_binary, "key", "ls")
        assert result.returncode == 0
        assert created_key in result.stdout

    @pytest.mark.serial
    def test_key_set_default(self, mvm_binary, created_key):
        """Set key as default."""
        result = _run_mvm(mvm_binary, "key", "set-default", created_key)
        assert result.returncode == 0

    def test_key_delete(self, mvm_binary, unique_key_name):
        """Create and delete key."""
        _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
        )

        try:
            result = _run_mvm(mvm_binary, "key", "rm", unique_key_name)
            assert result.returncode == 0
        finally:
            # Ensure cleanup even if assertion fails
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_duplicate_key_rejection(self, mvm_binary, created_key):
        """Reject duplicate key name."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            created_key,
            "--algorithm",
            "ed25519",
            check=False,
        )
        assert result.returncode != 0
        assert "already exists" in (result.stdout + result.stderr).lower()

    def test_key_show(self, mvm_binary, created_key):
        """Inspect key details (table output)."""
        result = _run_mvm(mvm_binary, "key", "inspect", created_key)
        assert result.returncode == 0
        assert created_key in result.stdout

    def test_key_add_existing(self, mvm_binary, unique_key_name, tmp_path):
        """Import an existing SSH public key."""
        key_path = tmp_path / "test_key_temp"
        subprocess.run(
            [
                "ssh-keygen",
                "-t",
                "ed25519",
                "-f",
                str(key_path),
                "-N",
                "",
                "-q",
            ],
            check=True,
        )
        result = _run_mvm(
            mvm_binary, "key", "add", unique_key_name, str(key_path) + ".pub"
        )
        assert result.returncode == 0
        assert unique_key_name in result.stdout

        # Cleanup
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_remove_nonexistent(self, mvm_binary):
        """Removing a non-existent key should fail."""
        result = _run_mvm(
            mvm_binary, "key", "rm", "nonexistent-key-name-xyz", check=False
        )
        assert result.returncode != 0
        assert "not found" in (result.stdout + result.stderr).lower()

    def test_key_inspect_json(self, mvm_binary, created_key):
        """Inspect key with JSON output."""
        result = _run_mvm(mvm_binary, "key", "inspect", created_key, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "name" in data

    @pytest.mark.serial
    def test_key_set_default_clear(self, mvm_binary, created_key):
        """Set key as default then clear the default."""
        result = _run_mvm(mvm_binary, "key", "set-default", created_key)
        assert result.returncode == 0
        result = _run_mvm(mvm_binary, "key", "set-default", "--clear")
        assert result.returncode == 0

    def test_key_export(self, mvm_binary, created_key, tmp_path):
        """Export a key to a file."""
        result = _run_mvm(
            mvm_binary, "key", "export", created_key, "--out", str(tmp_path)
        )
        assert result.returncode == 0

    def test_key_list_json(self, mvm_binary, created_key):
        """List keys in JSON format."""
        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(k["name"] == created_key for k in data)

    def test_key_create_ecdsa(self, mvm_binary, unique_key_name):
        """Create ECDSA SSH key."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ecdsa",
        )
        assert result.returncode == 0
        assert unique_key_name in result.stdout

        # Cleanup
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


class TestKeyCreateAdvanced:
    """Test SSH key creation with advanced options."""

    def test_key_create_with_bits(self, mvm_binary, unique_key_name):
        """Create RSA key with custom bits (--bits 2048)."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "rsa",
            "--bits",
            "2048",
        )
        assert result.returncode == 0

        # Cleanup
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_comment(self, mvm_binary, unique_key_name):
        """Create key with --comment and verify via inspect --json."""
        comment = "test-comment-for-key"
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
            "--comment",
            comment,
        )
        assert result.returncode == 0

        try:
            result = _run_mvm(
                mvm_binary, "key", "inspect", unique_key_name, "--json"
            )
            data = json.loads(result.stdout)
            assert data.get("comment") == comment
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_out(self, mvm_binary, unique_key_name, tmp_path):
        """Create key with --out pointing to a temp directory."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
            "--out",
            str(tmp_path),
        )
        assert result.returncode == 0

        try:
            assert os.path.exists(tmp_path / unique_key_name)
            assert os.path.exists(tmp_path / f"{unique_key_name}.pub")
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_set_default(self, mvm_binary, unique_key_name):
        """Create key with --set-default and verify it's the default."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
            "--set-default",
        )
        assert result.returncode == 0

        try:
            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            key_entry = next(
                (k for k in data if k["name"] == unique_key_name), None
            )
            assert key_entry is not None
            assert key_entry.get("is_default")
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)
            _run_mvm(mvm_binary, "key", "set-default", "--clear", check=False)

    @pytest.mark.serial
    def test_key_create_force_overwrite(self, mvm_binary, unique_key_name):
        """Force overwrite an existing key with --force."""
        _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
        )

        try:
            result = _run_mvm(
                mvm_binary,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "ed25519",
                "--force",
                check=False,
            )
            assert result.returncode == 0
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


class TestKeyInspectTree:
    """Test SSH key inspect with tree output format."""

    def test_key_inspect_tree(self, mvm_binary, created_key):
        """Inspect key with tree-style output."""
        result = _run_mvm(mvm_binary, "key", "inspect", created_key, "--tree")
        assert result.returncode == 0
        assert "├──" in result.stdout or "└──" in result.stdout


class TestKeyAddOverwrite:
    """Test SSH key add with overwrite behavior."""

    def test_key_add_overwrite(self, mvm_binary, unique_key_name, tmp_path):
        """Add key, reject duplicate, accept with --overwrite."""
        key_path = tmp_path / "test_add_overwrite_key"
        subprocess.run(
            [
                "ssh-keygen",
                "-t",
                "ed25519",
                "-f",
                str(key_path),
                "-N",
                "",
                "-q",
            ],
            check=True,
        )
        pub_key_path = str(key_path) + ".pub"
        key_name = f"test-add-overwrite-{unique_key_name}"

        # First add should succeed
        result = _run_mvm(mvm_binary, "key", "add", key_name, pub_key_path)
        assert result.returncode == 0
        assert key_name in result.stdout

        # Second add without overwrite should fail
        result = _run_mvm(
            mvm_binary, "key", "add", key_name, pub_key_path, check=False
        )
        assert result.returncode != 0
        assert "already exists" in (result.stdout + result.stderr).lower()

        # Third add with --overwrite should succeed
        result = _run_mvm(
            mvm_binary, "key", "add", key_name, pub_key_path, "--overwrite"
        )
        assert result.returncode == 0

        # Cleanup
        _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestKeyExportForce:
    """Test SSH key export with --force overwrite behavior."""

    def test_key_export_force(self, mvm_binary, created_key, tmp_path):
        """Export key, reject overwrite, accept with --force."""
        # First export should succeed
        result = _run_mvm(
            mvm_binary, "key", "export", created_key, "--out", str(tmp_path)
        )
        assert result.returncode == 0

        # Second export without --force should fail (file already exists)
        result = _run_mvm(
            mvm_binary,
            "key",
            "export",
            created_key,
            "--out",
            str(tmp_path),
            check=False,
        )
        assert result.returncode != 0

        # Third export with --force should succeed
        result = _run_mvm(
            mvm_binary,
            "key",
            "export",
            created_key,
            "--out",
            str(tmp_path),
            "--force",
        )
        assert result.returncode == 0
