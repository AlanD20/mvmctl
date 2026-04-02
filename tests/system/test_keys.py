"""SSH key management system tests."""

import os
import pytest
import subprocess

pytestmark = pytest.mark.system


class TestKeyLifecycle:
    """Test SSH key CRUD operations."""

    def test_key_create_ed25519(self, mvm_binary, unique_key_name):
        """Create ed25519 SSH key."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name, "--type", "ed25519"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert "created" in result.stdout.lower() or unique_key_name in result.stdout

    def test_key_create_rsa(self, mvm_binary, unique_key_name):
        """Create RSA SSH key."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name, "--type", "rsa"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_key_listing(self, mvm_binary, created_key):
        """List keys and verify created key appears."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "ls"],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
        assert created_key in result.stdout

    def test_key_set_default(self, mvm_binary, created_key):
        """Set key as default."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "set-default", created_key],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_key_delete(self, mvm_binary, unique_key_name):
        """Create and delete key."""
        # Create
        subprocess.run(
            [*mvm_binary.split(), "key", "create", unique_key_name],
            check=True,
            env={**os.environ, "NO_COLOR": "1"},
        )

        # Delete
        result = subprocess.run(
            [*mvm_binary.split(), "key", "rm", unique_key_name],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0

    def test_duplicate_key_rejection(self, mvm_binary, created_key):
        """Reject duplicate key name."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "create", created_key],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode != 0 or "already exists" in result.stdout.lower()

    def test_key_show(self, mvm_binary, created_key):
        """Show key details."""
        result = subprocess.run(
            [*mvm_binary.split(), "key", "show", created_key],
            capture_output=True,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        assert result.returncode == 0
