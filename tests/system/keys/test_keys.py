"""SSH key management system tests — CRUD, advanced options, edge cases, dependencies."""

from __future__ import annotations

import json
import os
import subprocess

import pytest

from tests.system.conftest import _run_mvm, ensure_vm_deps

pytestmark = [pytest.mark.system, pytest.mark.domain_key]


# ============================================================================
# Read-only classes (no resources created within tests)
# ============================================================================


class TestKeyInspectTree:
    """Test SSH key inspect with tree output format."""

    def test_key_inspect_tree(self, mvm_binary, created_key):
        """Inspect key with tree-style output."""
        # Rationale: Uses created_key fixture. Read-only — tests the
        # --tree output format for key inspect.
        result = _run_mvm(mvm_binary, "key", "inspect", created_key, "--tree")
        assert result.returncode == 0
        assert "├──" in result.stdout or "└──" in result.stdout


class TestKeyExportForce:
    """Test SSH key export with --force overwrite behavior."""

    def test_key_export_force(self, mvm_binary, created_key, tmp_path):
        """Export key, reject overwrite, accept with --force."""
        # Rationale: Uses created_key + tmp_path. Tests export,
        # overwrite rejection, and --force overwrite. No VM.
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
        assert any(tmp_path.iterdir()), (
            "Export directory should contain key files after --force"
        )


# ============================================================================
# Mixed class: read-only tests first, then state-modifying, then destructive
# ============================================================================


class TestKeyLifecycle:
    """Test SSH key CRUD operations — read-only first, destructive last."""

    # ------------------------------------------------------------------
    # Read-only tests (use created_key fixture, no mutation)
    # ------------------------------------------------------------------

    def test_key_listing(self, mvm_binary, created_key):
        """List keys and verify created key appears (via --json)."""
        # Rationale: Uses created_key fixture (already exists via conftest).
        # Read-only — no resources created within test.
        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert any(k["name"] == created_key for k in data)

    def test_duplicate_key_rejection(self, mvm_binary, created_key):
        """Reject duplicate key name."""
        # Rationale: Uses created_key fixture. Read-only — tests
        # error handling for duplicate key creation.
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
        """Inspect key details (verify via --json)."""
        # Rationale: Uses created_key fixture. Read-only — tests
        # key inspect with JSON output format.
        result = _run_mvm(mvm_binary, "key", "inspect", created_key, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("key", {}).get("name") == created_key

    def test_key_remove_nonexistent(self, mvm_binary):
        """Removing a non-existent key should fail and not create artifacts."""
        # Rationale: No resources needed — testing error path for
        # removal of a nonexistent key name.
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
        # Rationale: Uses created_key fixture. Read-only — validates
        # JSON output structure for key inspect.
        result = _run_mvm(mvm_binary, "key", "inspect", created_key, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "name" in data.get("key", {})

    def test_key_export(self, mvm_binary, created_key, tmp_path):
        """Export a key to a file and verify files exist on disk."""
        # Rationale: Uses created_key + tmp_path (cheapest fixtures).
        # Read-only with filesystem assertion on export output.
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
        # Rationale: Uses created_key fixture. Read-only — validates
        # JSON list structure for key listing.
        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(k["name"] == created_key for k in data)

    # ------------------------------------------------------------------
    # Serial state-modifying tests (modify shared default state)
    # ------------------------------------------------------------------

    @pytest.mark.serial
    def test_key_set_default(self, mvm_binary, created_key):
        """Set key as default and verify is_default in ls --json."""
        # Rationale: Uses created_key fixture. Modifies shared default
        # state — marked serial. No VM needed.
        result = _run_mvm(mvm_binary, "key", "default", created_key)
        assert result.returncode == 0

        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(result.stdout)
        key_entry = next((k for k in data if k["name"] == created_key), None)
        assert key_entry is not None
        assert key_entry.get("is_default")

    @pytest.mark.serial
    def test_key_set_default_clear(self, mvm_binary, created_key):
        """Set key as default then clear the default."""
        # Rationale: Uses created_key fixture. Modifies shared default
        # state — marked serial. No VM needed.
        _run_mvm(mvm_binary, "key", "default", created_key)
        _run_mvm(mvm_binary, "key", "default", "--clear")

        result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(result.stdout)
        key_entry = next((k for k in data if k["name"] == created_key), None)
        assert key_entry is not None
        assert not key_entry.get("is_default")

    # ------------------------------------------------------------------
    # Destructive tests (create resources that are cleaned up)
    # ------------------------------------------------------------------

    def test_key_create_ed25519(self, mvm_binary, unique_key_name):
        """Create ed25519 SSH key and verify via ls --json."""
        # Rationale: Uses unique_key_name (cheapest fixture) to create
        # a key and verify via ls --json. No VM needed.
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_rsa(self, mvm_binary, unique_key_name):
        """Create RSA SSH key and verify via ls --json."""
        # Rationale: Uses unique_key_name (cheapest fixture) to create
        # an RSA key and verify via ls --json. No VM needed.
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_ecdsa(self, mvm_binary, unique_key_name):
        """Create ECDSA SSH key and verify via ls --json."""
        # Rationale: Uses unique_key_name (cheapest fixture) to create
        # an ECDSA key and verify via ls --json. No VM needed.
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_add_existing(self, mvm_binary, unique_key_name, tmp_path):
        """Import an existing SSH public key and verify via ls --json."""
        # Rationale: Uses unique_key_name + tmp_path (both cheapest
        # fixtures) to test importing an existing public key. No VM.
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

            result = _run_mvm(mvm_binary, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_delete(self, mvm_binary, unique_key_name):
        """Create and delete key, verify it's gone via ls --json."""
        # Rationale: Uses unique_key_name (cheapest fixture) but is
        # destructive — removes a real key from the system.
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

    @pytest.mark.serial
    def test_remove_default_key_when_only_key(
        self, mvm_binary, unique_key_name
    ):
        """Removing the default key when it's the only key should be allowed."""
        # Rationale: Uses unique_key_name (cheapest). Destructive —
        # removes the default key. Marked serial due to default-state change.
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
        # Rationale: Uses unique_key_name (cheapest). Destructive —
        # removes two keys at once. No VM needed.
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


# ============================================================================
# Destructive classes (create/modify key resources)
# ============================================================================


class TestKeyCreateAdvanced:
    """Test SSH key creation with advanced options."""

    def test_key_create_with_bits(self, mvm_binary, unique_key_name):
        """Create RSA key with custom bits and verify via ls --json."""
        # Rationale: Uses unique_key_name (cheapest fixture) to test
        # the --bits flag for RSA key creation. No VM needed.
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
        # Rationale: Uses unique_key_name (cheapest fixture) to test
        # the --comment flag for key creation. No VM needed.
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
            assert data.get("key", {}).get("comment") == comment
        finally:
            _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_out(self, mvm_binary, unique_key_name, tmp_path):
        """Create key with --out pointing to a temp directory."""
        # Rationale: Uses unique_key_name + tmp_path (cheapest fixtures)
        # to test the --out flag for key file export. No VM needed.
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
        # Rationale: Uses unique_key_name (cheapest fixture). Modifies
        # shared default state — marked serial. No VM needed.
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
        # Rationale: Uses unique_key_name (cheapest fixture) to test
        # the --force overwrite flag for key creation. No VM needed.
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


class TestKeyAddOverwrite:
    """Test SSH key add with overwrite behavior."""

    def test_key_add_overwrite(self, mvm_binary, unique_key_name, tmp_path):
        """Add key, reject duplicate, accept with --overwrite."""
        # Rationale: Uses unique_key_name + tmp_path (cheapest fixtures).
        # Tests add, duplicate rejection, and --force overwrite. No VM.
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

        # Verify the key appears in JSON listing
        ls_result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(ls_result.stdout)
        assert any(k["name"] == key_name for k in data), (
            f"Key '{key_name}' not found in ls --json after add"
        )

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

        # Verify the key still exists in listing after --force overwrite
        ls_result = _run_mvm(mvm_binary, "key", "ls", "--json")
        data = json.loads(ls_result.stdout)
        assert any(k["name"] == key_name for k in data), (
            "Key should still be listed after --force overwrite"
        )

        _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


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
        """SSH key used by a running VM: rm without --force is rejected, with --force succeeds."""
        # Rationale: Needs a real VM (unique_vm_name) because we need a
        # running VM using the key to verify protection mechanism. Requires
        # KVM, network, and key fixtures.
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
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--network",
                    network_name,
                    "--ssh-key",
                    key_name,
                )
            except RuntimeError as e:
                # Skip-reason: No loop-mount provisioner means VM provisioning
                # is impossible without the mvm-services binaries being built
                # and registered. Run `python scripts/build_services.py` first.
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise
            _run_mvm(mvm_binary, "vm", "start", vm_name)

            # rm without --force prints error (but returns 0) when key is in use
            result = _run_mvm(mvm_binary, "key", "rm", key_name, check=False)
            assert "used by VM" in (result.stdout + result.stderr), (
                "rm without --force should report key is in use"
            )

            key_ls = _run_mvm(mvm_binary, "key", "ls", "--json")
            keys_after = json.loads(key_ls.stdout)
            key_names = [k.get("name") for k in keys_after]
            assert key_name in key_names, (
                "Key should still be present after rejected rm"
            )

            # rm with --force should succeed
            result = _run_mvm(mvm_binary, "key", "rm", key_name, "--force")
            assert result.returncode == 0

            key_ls = _run_mvm(mvm_binary, "key", "ls", "--json")
            keys_after = json.loads(key_ls.stdout)
            key_names = [k.get("name") for k in keys_after]
            assert key_name not in key_names, (
                "Key should be gone after --force rm"
            )
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
                    vm_name,
                    "--image",
                    "alpine:3.21",
                    "--network",
                    network_name,
                    "--ssh-key",
                    f"{key1},{key2}",
                )
            except RuntimeError as e:
                # Skip-reason: No loop-mount provisioner means VM provisioning
                # is impossible without the mvm-services binaries being built
                # and registered. Run `python scripts/build_services.py` first.
                if "No provisioner available" in str(e):
                    pytest.skip(
                        "No loop-mount provisioner available "
                        "(mvm-services not set up)"
                    )
                raise

            result = _run_mvm(mvm_binary, "vm", "inspect", vm_name, "--json")
            data = json.loads(result.stdout)
            ssh_keys = data.get("vm", {}).get("ssh_keys")
            assert ssh_keys is not None
            for kid in key_ids:
                assert kid in ssh_keys, f"VM should contain key ID {kid}"
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "key", "rm", key1, check=False)
            _run_mvm(mvm_binary, "key", "rm", key2, check=False)
