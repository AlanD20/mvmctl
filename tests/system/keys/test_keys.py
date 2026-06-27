"""SSH key management system tests — CRUD, advanced options, edge cases, dependencies.

Migrated from tests/e2e/keys/test_keys.py.
Violations removed:
- NO pytest.skip() — all preconditions are fixed
- NO subprocess.run on host — ssh-keygen runs inside the test VM
- NO os.path.exists on host paths — checks done inside the VM via _guest_run
- NO tmp_path (host path) — uses /tmp/<unique-name> inside the test VM instead
"""

from __future__ import annotations

import json
import uuid

import pytest

from tests.system.conftest import _guest_run, _run_mvm, ensure_vm_deps

pytestmark = [pytest.mark.system, pytest.mark.domain_keys]


# ============================================================================
# Read-only classes (no resources created within tests)
# ============================================================================


class TestKeyInspectTree:
    """Test SSH key inspect with default output format."""

    def test_key_inspect_tree(self, runner_vm, created_key):
        """Inspect key with default output format."""
        # Rationale: Uses created_key fixture. Read-only — tests the
        # default output format for key inspect.
        result = _run_mvm(runner_vm, "key", "inspect", created_key)
        assert result.returncode == 0
        assert "├─ " in result.stdout or "└─ " in result.stdout


class TestKeyExportForce:
    """Test SSH key export with --force overwrite behavior."""

    def test_key_export_force(self, runner_vm, created_key):
        """Export key, reject overwrite, accept with --force.

        Uses /tmp/<uuid> inside the test VM instead of host tmp_path.
        """
        export_tag = f"key-export-{uuid.uuid4().hex[:6]}"
        export_dir = f"/tmp/{export_tag}"
        _guest_run(runner_vm, f"mkdir -p {export_dir}")

        result = _run_mvm(runner_vm, "key", "export", created_key, export_dir)
        assert result.returncode == 0
        file_check = _guest_run(
            runner_vm, f"ls -A {export_dir} 2>/dev/null | head -1", check=False
        )
        assert file_check.returncode == 0 and file_check.stdout.strip(), (
            f"Export directory {export_dir} should contain key files"
        )

        export_tag2 = f"key-export2-{uuid.uuid4().hex[:6]}"
        export_dir2 = f"/tmp/{export_tag2}"
        _guest_run(runner_vm, f"mkdir -p {export_dir2}")
        _run_mvm(runner_vm, "key", "export", created_key, export_dir2)

        result = _run_mvm(
            runner_vm, "key", "export", created_key, export_dir2, check=False
        )
        assert result.returncode != 0

        result = _run_mvm(
            runner_vm, "key", "export", created_key, export_dir2, "--force"
        )
        assert result.returncode == 0
        file_check = _guest_run(
            runner_vm, f"ls -A {export_dir2} 2>/dev/null | head -1", check=False
        )
        assert file_check.returncode == 0 and file_check.stdout.strip(), (
            f"Export directory {export_dir2} should contain key files after --force"
        )


# ============================================================================
# Mixed class: read-only tests first, then state-modifying, then destructive
# ============================================================================


class TestKeyLifecycle:
    """Test SSH key CRUD operations — read-only first, destructive last."""

    # ------------------------------------------------------------------
    # Read-only tests (use created_key fixture, no mutation)
    # ------------------------------------------------------------------

    def test_key_listing(self, runner_vm, created_key):
        """List keys and verify created key appears (via --json)."""
        result = _run_mvm(runner_vm, "key", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert any(k["name"] == created_key for k in data)

    def test_duplicate_key_rejection(self, runner_vm, created_key):
        """Reject duplicate key name."""
        result = _run_mvm(
            runner_vm,
            "key",
            "create",
            created_key,
            "--algorithm",
            "ed25519",
            check=False,
        )
        assert result.returncode != 0
        assert "already exists" in (result.stdout + result.stderr).lower()

    def test_key_show(self, runner_vm, created_key):
        """Inspect key details (verify via --json)."""
        result = _run_mvm(runner_vm, "key", "inspect", created_key, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert data.get("key", {}).get("name") == created_key

    def test_key_remove_nonexistent(self, runner_vm):
        """Removing a non-existent key should fail and not create artifacts."""
        result = _run_mvm(
            runner_vm, "key", "rm", "nonexistent-key-name-xyz", check=False
        )
        assert result.returncode != 0
        assert "not found" in (result.stdout + result.stderr).lower()

        result = _run_mvm(runner_vm, "key", "ls", "--json", check=False)
        if result.returncode == 0:
            data = json.loads(result.stdout)
            assert not any(k["name"] == "nonexistent-key-name-xyz" for k in data)

    def test_key_inspect_json(self, runner_vm, created_key):
        """Inspect key with JSON output."""
        result = _run_mvm(runner_vm, "key", "inspect", created_key, "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert "name" in data.get("key", {})

    def test_key_export(self, runner_vm, created_key):
        """Export a key to a file and verify files exist on disk (inside VM)."""
        export_tag = f"key-export-{uuid.uuid4().hex[:6]}"
        export_dir = f"/tmp/{export_tag}"
        _guest_run(runner_vm, f"mkdir -p {export_dir}")
        result = _run_mvm(runner_vm, "key", "export", created_key, export_dir)
        assert result.returncode == 0
        file_check = _guest_run(
            runner_vm, f"ls -A {export_dir} 2>/dev/null | head -1", check=False
        )
        assert file_check.returncode == 0 and file_check.stdout.strip(), (
            f"Export directory {export_dir} should contain key files"
        )

    def test_key_list_json(self, runner_vm, created_key):
        """List keys in JSON format."""
        result = _run_mvm(runner_vm, "key", "ls", "--json")
        assert result.returncode == 0
        data = json.loads(result.stdout)
        assert isinstance(data, list)
        assert any(k["name"] == created_key for k in data)

    # ------------------------------------------------------------------
    # Serial state-modifying tests (modify shared default state)
    # ------------------------------------------------------------------

    def test_key_set_default(self, runner_vm, created_key):
        """Set key as default and verify is_default in ls --json."""
        result = _run_mvm(runner_vm, "key", "default", created_key)
        assert result.returncode == 0

        result = _run_mvm(runner_vm, "key", "ls", "--json")
        data = json.loads(result.stdout)
        key_entry = next((k for k in data if k["name"] == created_key), None)
        assert key_entry is not None
        assert key_entry.get("is_default")

    def test_key_set_default_clear(self, runner_vm, created_key):
        """Set key as default then clear the default."""
        _run_mvm(runner_vm, "key", "default", created_key)
        _run_mvm(runner_vm, "key", "default", "--clear")

        result = _run_mvm(runner_vm, "key", "ls", "--json")
        data = json.loads(result.stdout)
        key_entry = next((k for k in data if k["name"] == created_key), None)
        assert key_entry is not None
        assert not key_entry.get("is_default")

    # ------------------------------------------------------------------
    # Destructive tests (create resources that are cleaned up)
    # ------------------------------------------------------------------

    def test_key_create_ed25519(self, runner_vm, unique_key_name):
        """Create ed25519 SSH key and verify via ls --json."""
        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "ed25519",
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_create_rsa(self, runner_vm, unique_key_name):
        """Create RSA SSH key and verify via ls --json."""
        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "rsa",
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_create_ecdsa(self, runner_vm, unique_key_name):
        """Create ECDSA SSH key and verify via ls --json."""
        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "ecdsa",
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_add_existing(self, runner_vm, unique_key_name):
        """Import an existing SSH public key and verify via ls --json.

        ssh-keygen runs inside the test VM — no host subprocess.
        """
        key_tag = f"key-add-{uuid.uuid4().hex[:6]}"
        key_path = f"/tmp/{key_tag}"
        _guest_run(
            runner_vm,
            f"ssh-keygen -t ed25519 -f {key_path} -N '' -q",
        )
        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "import",
                unique_key_name,
                f"{key_path}.pub",
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_import_shows_fingerprint(self, runner_vm, unique_key_name):
        """Import an existing SSH public key and verify fingerprint appears in ls --json.

        ssh-keygen runs inside the test VM. Runs a key import, then checks
        the ls --json output for a non-empty fingerprint field on the imported key.
        """
        key_tag = f"key-fp-{uuid.uuid4().hex[:6]}"
        key_path = f"/tmp/{key_tag}"
        _guest_run(
            runner_vm,
            f"ssh-keygen -t ed25519 -f {key_path} -N '' -q",
        )
        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "import",
                unique_key_name,
                f"{key_path}.pub",
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            key_entry = next(
                (k for k in data if k["name"] == unique_key_name), None
            )
            assert key_entry is not None, (
                f"Imported key '{unique_key_name}' not found in listing"
            )
            assert "fingerprint" in key_entry, (
                f"Expected 'fingerprint' field in key entry, "
                f"got keys: {list(key_entry.keys())}"
            )
            assert key_entry["fingerprint"], (
                f"Expected non-empty fingerprint, got '{key_entry['fingerprint']}'"
            )
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_delete(self, runner_vm, unique_key_name):
        """Create and delete key, verify it's gone via ls --json."""
        _run_mvm(
            runner_vm,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
        )

        try:
            result = _run_mvm(runner_vm, "key", "rm", unique_key_name)
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert not any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_remove_default_key_when_only_key(self, runner_vm, unique_key_name):
        """Removing the default key when it's the only key should be allowed."""
        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "ed25519",
                "--default",
            )

            _run_mvm(runner_vm, "key", "rm", unique_key_name)

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            names = [k["name"] for k in data]
            assert unique_key_name not in names
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_rm_multiple(self, runner_vm, unique_key_name):
        """Remove two keys at once and verify both are gone via ls --json."""
        key1 = f"{unique_key_name}-multi1"
        key2 = f"{unique_key_name}-multi2"
        try:
            _run_mvm(
                runner_vm, "key", "create", key1, "--algorithm", "ed25519"
            )
            _run_mvm(
                runner_vm, "key", "create", key2, "--algorithm", "ed25519"
            )

            result = _run_mvm(runner_vm, "key", "rm", key1, key2)
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert not any(k["name"] == key1 for k in data)
            assert not any(k["name"] == key2 for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", key1, check=False)
            _run_mvm(runner_vm, "key", "rm", key2, check=False)


# ============================================================================
# Destructive classes (create/modify key resources)
# ============================================================================


class TestKeyCreateAdvanced:
    """Test SSH key creation with advanced options."""

    def test_key_create_with_bits(self, runner_vm, unique_key_name):
        """Create RSA key with custom bits and verify via ls --json."""
        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "rsa",
                "--bits",
                "2048",
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_comment(self, runner_vm, unique_key_name):
        """Create key with --comment and verify via inspect --json."""
        comment = "test-comment-for-key"
        result = _run_mvm(
            runner_vm,
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
                runner_vm,
                "key",
                "inspect",
                unique_key_name,
                "--json",
            )
            data = json.loads(result.stdout)
            assert data.get("key", {}).get("comment") == comment
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_out(self, runner_vm, unique_key_name):
        """Create key with --out pointing to a temp directory inside the VM."""
        out_tag = f"key-out-{uuid.uuid4().hex[:6]}"
        out_dir = f"/tmp/{out_tag}"
        _guest_run(runner_vm, f"mkdir -p {out_dir}")

        result = _run_mvm(
            runner_vm,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
            "--out",
            out_dir,
        )
        assert result.returncode == 0

        try:
            priv_check = _guest_run(
                runner_vm,
                f"test -f {out_dir}/{unique_key_name} && echo exists",
                check=False,
            )
            assert priv_check.returncode == 0, (
                f"Private key {out_dir}/{unique_key_name} not found inside VM"
            )
            pub_check = _guest_run(
                runner_vm,
                f"test -f {out_dir}/{unique_key_name}.pub && echo exists",
                check=False,
            )
            assert pub_check.returncode == 0, (
                f"Public key {out_dir}/{unique_key_name}.pub not found inside VM"
            )
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)

    def test_key_create_with_set_default(self, runner_vm, unique_key_name):
        """Create key with --default and verify is_default in ls --json."""
        result = _run_mvm(
            runner_vm,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
            "--default",
        )
        assert result.returncode == 0

        try:
            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            key_entry = next(
                (k for k in data if k["name"] == unique_key_name), None
            )
            assert key_entry is not None
            assert key_entry.get("is_default")
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)
            _run_mvm(runner_vm, "key", "default", "--clear", check=False)

    def test_key_create_force_overwrite(self, runner_vm, unique_key_name):
        """Force overwrite an existing key with --force."""
        _run_mvm(
            runner_vm,
            "key",
            "create",
            unique_key_name,
            "--algorithm",
            "ed25519",
        )

        try:
            result = _run_mvm(
                runner_vm,
                "key",
                "create",
                unique_key_name,
                "--algorithm",
                "ed25519",
                "--force",
                check=False,
            )
            assert result.returncode == 0

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            assert any(k["name"] == unique_key_name for k in data)
        finally:
            _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)


class TestKeyImportOverwrite:
    """Test SSH key import with overwrite behavior."""

    def test_key_import_overwrite(self, runner_vm, unique_key_name):
        """Import key, reject duplicate, accept with --force.

        ssh-keygen runs inside the test VM.
        """
        key_tag = f"key-import-{uuid.uuid4().hex[:6]}"
        key_path = f"/tmp/{key_tag}"
        _guest_run(
            runner_vm,
            f"ssh-keygen -t ed25519 -f {key_path} -N '' -q",
        )
        pub_key_path = f"{key_path}.pub"
        key_name = f"test-add-overwrite-{unique_key_name}"

        result = _run_mvm(runner_vm, "key", "import", key_name, pub_key_path)
        assert result.returncode == 0

        ls_result = _run_mvm(runner_vm, "key", "ls", "--json")
        data = json.loads(ls_result.stdout)
        assert any(k["name"] == key_name for k in data), (
            f"Key '{key_name}' not found in ls --json after import"
        )

        result = _run_mvm(
            runner_vm, "key", "import", key_name, pub_key_path, check=False
        )
        assert result.returncode != 0
        assert "already exists" in (result.stdout + result.stderr).lower()

        result = _run_mvm(
            runner_vm,
            "key",
            "import",
            key_name,
            pub_key_path,
            "--force",
        )
        assert result.returncode == 0

        ls_result = _run_mvm(runner_vm, "key", "ls", "--json")
        data = json.loads(ls_result.stdout)
        assert any(k["name"] == key_name for k in data), (
            "Key should still be listed after --force overwrite"
        )

        _run_mvm(runner_vm, "key", "rm", key_name, check=False)


class TestKeyRunningVMDependency:
    """SSH key dependency behavior with running VMs."""

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_delete_ssh_key_used_by_running_vm(
        self,
        runner_vm,
        unique_vm_name,
        unique_key_name,
        created_network,
    ):
        """SSH key used by a running VM: rm succeeds even when key is in use."""
        vm_name = unique_vm_name
        key_name = unique_key_name
        network_name = created_network

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key_name,
                "--algorithm",
                "ed25519",
            )
            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                key_name,
            )
            _run_mvm(runner_vm, "vm", "start", vm_name)

            result = _run_mvm(runner_vm, "key", "rm", key_name, check=False)
            assert result.returncode == 0

            key_ls = _run_mvm(runner_vm, "key", "ls", "--json")
            keys_after = json.loads(key_ls.stdout)
            key_names = [k.get("name") for k in keys_after]
            assert key_name not in key_names, (
                "Key should be removed after rm"
            )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key_name, check=False)


class TestKeyDefaults:
    """Test SSH key default behavior — multiple defaults are supported."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_keys,
    ]

    @pytest.mark.needs_kvm
    @pytest.mark.needs_network
    @pytest.mark.slow
    def test_multiple_default_ssh_keys(
        self, runner_vm, unique_vm_name, unique_key_name, created_network
    ) -> None:
        key1 = f"{unique_key_name}-1"
        key2 = f"{unique_key_name}-2"
        vm_name = unique_vm_name
        network_name = created_network

        try:
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key1,
                "--algorithm",
                "ed25519",
            )
            _run_mvm(
                runner_vm,
                "key",
                "create",
                key2,
                "--algorithm",
                "ed25519",
            )

            _run_mvm(runner_vm, "key", "default", key1, key2)

            result = _run_mvm(runner_vm, "key", "ls", "--json")
            data = json.loads(result.stdout)
            k1 = next(k for k in data if k["name"] == key1)
            k2 = next(k for k in data if k["name"] == key2)
            assert k1.get("is_default"), f"'{key1}' should be default"
            assert k2.get("is_default"), f"'{key2}' should be default"

            ensure_vm_deps(runner_vm)
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.23",
                "--network",
                network_name,
                "--ssh-key",
                f"{key1},{key2}",
            )

            result = _run_mvm(runner_vm, "vm", "inspect", vm_name, "--json")
            data = json.loads(result.stdout)
            ssh_keys = data.get("vm", {}).get("ssh_keys")
            assert ssh_keys is not None
            for key_name in [key1, key2]:
                assert key_name in ssh_keys, (
                    f"VM should contain key name {key_name}"
                )
        finally:
            _run_mvm(runner_vm, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(runner_vm, "key", "rm", key1, check=False)
            _run_mvm(runner_vm, "key", "rm", key2, check=False)
