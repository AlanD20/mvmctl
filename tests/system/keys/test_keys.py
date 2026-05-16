"""SSH key management system tests — CRUD, advanced options, edge cases, dependencies."""

from __future__ import annotations

import json
import os
import subprocess

import pytest

from tests.system.conftest import _run_mvm, ensure_vm_deps

pytestmark = [pytest.mark.system, pytest.mark.domain_key]


class TestKeyLifecycle:
    """Test SSH key CRUD operations."""

    def test_key_create_ed25519(self, mvm_binary, unique_key_name):
        """Create ed25519 SSH key and verify via ls --json."""
        try:
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_rsa(self, mvm_binary, unique_key_name):
        """Create RSA SSH key and verify via ls --json."""
        try:
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_listing(self, mvm_binary, created_key):
        """List keys and verify created key appears."""
        result = _run_mvm(mvm_binary, "key", "ls")
        assert result.returncode == 0
        assert created_key in result.stdout

        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(result.stdout)
        assert any(k["name"] == created_key for k in data)

    @pytest.mark.serial
    def test_key_set_default(self, mvm_binary, created_key):
        """Set key as default and verify is_default in ls --json."""
        result = _run_mvm(mvm_binary, "key", "default", created_key)
        assert result.returncode == 0

        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(result.stdout)
        key_entry = next((k for k in data if k["name"] == created_key), None)
        assert key_entry is not None
        assert key_entry.get("is_default")

    def test_key_delete(self, mvm_binary, unique_key_name):
        """Create and delete key, verify it's gone via ls --json."""
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert not any(k["name"] == unique_key_name for k in data)
        finally:
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
        """Import an existing SSH public key and verify via ls --json."""
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
        try:
            result = _run_mvm(
                mvm_binary,
                "key",
                "add",
                unique_key_name,
                str(key_path) + ".pub",
            )
            assert result.returncode == 0
            assert unique_key_name in result.stdout

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_remove_nonexistent(self, mvm_binary):
        """Removing a non-existent key should fail and not create artifacts."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "rm",
            "nonexistent-key-name-xyz",
            check=False,
        )
        assert result.returncode != 0
        assert "not found" in (result.stdout + result.stderr).lower()

        result = _run_mvm(mvm_binary, "key", "ls", "--json", check=False)
        if result.returncode == 0:
            data = json.loads(result.stdout)
            assert not any(
                k["name"] == "nonexistent-key-name-xyz" for k in data
            )

    def test_key_inspect_json(self, mvm_binary, created_key):
        """Inspect key with JSON output."""
        result = _run_mvm(mvm_binary, "key", "inspect", created_key, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "name" in data

    @pytest.mark.serial
    def test_key_set_default_clear(self, mvm_binary, created_key):
        """Set key as default then clear the default."""
        _run_mvm(mvm_binary, "key", "default", created_key)
        _run_mvm(mvm_binary, "key", "default", "--clear")

        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(result.stdout)
        key_entry = next((k for k in data if k["name"] == created_key), None)
        assert key_entry is not None
        assert not key_entry.get("is_default")

    def test_key_export(self, mvm_binary, created_key, tmp_path):
        """Export a key to a file and verify files exist on disk."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "export",
            created_key,
            "--out",
            str(tmp_path),
        )
        assert result.returncode == 0
        assert any(tmp_path.iterdir()), (
            "Export directory should contain key files"
        )

    def test_key_list_json(self, mvm_binary, created_key):
        """List keys in JSON format."""
        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(k["name"] == created_key for k in data)

    def test_key_create_ecdsa(self, mvm_binary, unique_key_name):
        """Create ECDSA SSH key and verify via ls --json."""
        try:
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    @pytest.mark.serial
    def test_remove_default_key_when_only_key(
        self, mvm_binary, unique_key_name
    ):
        """Removing the default key when it's the only key should be allowed."""
        try:
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "ed25519",
                "--default",
            )

            _run_mvm(mvm_binary, "key", "rm", unique_key_name)

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            names = [k["name"] for k in data]
            assert unique_key_name not in names
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_rm_multiple(self, mvm_binary, unique_key_name):
        """Remove two keys at once and verify both are gone via ls --json."""
        key1 = f"{unique_key_name}-multi1"
        key2 = f"{unique_key_name}-multi2"
        try:
            _run_mvm(
                mvm_binary, "key", "create", key1, "--algorithm", "ed25519"
            )
            _run_mvm(
                mvm_binary, "key", "create", key2, "--algorithm", "ed25519"
            )

            result = _run_mvm(mvm_binary, "key", "rm", key1, key2)
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert not any(k["name"] == key1 for k in data)
            assert not any(k["name"] == key2 for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", key1, check=False)
            _run_mvm(mvm_binary, "key", "rm", key2, check=False)


class TestKeyCreateAdvanced:
    """Test SSH key creation with advanced options."""

    def test_key_create_with_bits(self, mvm_binary, unique_key_name):
        """Create RSA key with custom bits and verify via ls --json."""
        try:
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
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
                mvm_binary,
                "key",
                "inspect",
                unique_key_name,
                "--json",
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

    @pytest.mark.serial
    def test_key_create_with_set_default(self, mvm_binary, unique_key_name):
        """Create key with --default and verify is_default in ls --json."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
            "--default",
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
            _run_mvm(mvm_binary, "key", "default", "--clear", check=False)

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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
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

        result = _run_mvm(mvm_binary, "key", "add", key_name, pub_key_path)
        assert result.returncode == 0
        assert key_name in result.stdout

        result = _run_mvm(
            mvm_binary, "key", "add", key_name, pub_key_path, check=False
        )
        assert result.returncode != 0
        assert "already exists" in (result.stdout + result.stderr).lower()

        result = _run_mvm(
            mvm_binary,
            "key",
            "add",
            key_name,
            pub_key_path,
            "--force",
        )
        assert result.returncode == 0

        _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestKeyExportForce:
    """Test SSH key export with --force overwrite behavior."""

    def test_key_export_force(self, mvm_binary, created_key, tmp_path):
        """Export key, reject overwrite, accept with --force."""
        result = _run_mvm(
            mvm_binary,
            "key",
            "export",
            created_key,
            "--out",
            str(tmp_path),
        )
        assert result.returncode == 0

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


class TestKeyRunningVMDependency:
    """SSH key dependency behavior with running VMs."""

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_delete_ssh_key_used_by_running_vm(
        self,
        mvm_binary,
        unique_vm_name,
        unique_key_name,
        created_network,
    ):
        """SSH key used by a running VM — documents current behavior."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = created_network

        try:
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            try:
                ensure_vm_deps(mvm_binary)
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    "--name",
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--network",
                    network_name,
                    "--ssh-key",
                    key_name,
                )
            except RuntimeError as e:
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)

            key_ls = _run_mvm(mvm_binary, "key", "ls", "--json", check=False)
            if key_ls.returncode == 0 and key_ls.stdout.strip():
                keys_after = json.loads(key_ls.stdout)
                key_names = [k.get("name") for k in keys_after]
                assert key_name not in key_names
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


class TestKeyDefaults:
    """Test SSH key default behavior — multiple defaults are supported."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_key,
    ]

    @pytest.mark.requires_kvm
    @pytest.mark.requires_network
    @pytest.mark.slow
    def test_multiple_default_ssh_keys(
        self, mvm_binary, unique_vm_name, unique_key_name, created_network
    ) -> None:
        # Rationale: Needs a real VM to verify both default keys appear in
        # the VM's ssh_keys list. The SSH key domain uniquely allows
        # multiple defaults; image, kernel, network, binary do not.
        key1 = f"{unique_key_name}-1"
        key2 = f"{unique_key_name}-2"
        vm_name = unique_vm_name
        network_name = created_network

        try:
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key1,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                mvm_binary,
                "key",
                "create",
                key2,
                "--algorithm",
                "ed25519",
            )

            _run_mvm(mvm_binary, "key", "default", key1, key2)

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            k1 = next(k for k in data if k["name"] == key1)
            k2 = next(k for k in data if k["name"] == key2)
            assert k1.get("is_default"), f"'{key1}' should be default"
            assert k2.get("is_default"), f"'{key2}' should be default"
            key_ids = [k1["id"], k2["id"]]

            try:
                ensure_vm_deps(mvm_binary)
                _run_mvm(
                    mvm_binary,
                    "vm",
                    "create",
                    "--name",
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--network",
                    network_name,
                    "--ssh-key",
                    f"{key1},{key2}",
                )
            except RuntimeError as e:
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise

            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            data = json.loads(result.stdout)
            assert data.get("ssh_keys") is not None
            for kid in key_ids:
                assert kid in data["ssh_keys"], (
                    f"VM should contain key ID {kid}"
                )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key1, check=False)
            _run_mvm(mvm_binary, "key", "rm", key2, check=False)
