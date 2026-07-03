"""Env management system tests — Tier 3 host-direct tests for env workflow.

All tests run directly on the host via _run_mvm_host (NOT inside a runner VM).
Tests use only lightweight resources (network + key steps — no VM creation).
Every test that creates resources cleans them up in a finally block.
"""
from __future__ import annotations

import json
import os
import random
import uuid

import pytest
from tests.system.conftest import _run_mvm_host


pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_env,
    pytest.mark.tier3,
]


# ============================================================================
# Helpers
# ============================================================================


def _unique_subnet(name: str) -> str:
    """Deterministically generate a unique /24 subnet from a resource name."""
    rng = random.Random(name)
    return f"10.{rng.randint(1, 254)}.{rng.randint(0, 254)}.0/24"


def _make_spec(tmp_path, *, networks=0, keys=0):
    """Write an env spec YAML file with randomly-named resources.

    Returns:
        dict with keys: path (str), net_names (list[str]), key_names (list[str])
    """
    suffix = uuid.uuid4().hex[:8]
    spec_path = os.path.join(os.fspath(tmp_path), f"env-spec-{suffix}.yaml")
    lines = ['version: "1"']
    net_names = []
    if networks:
        lines.append("network:")
        for _ in range(networks):
            name = f"sys-env-net-{uuid.uuid4().hex[:6]}"
            net_names.append(name)
            lines.append(f"  - name: {name}")
            lines.append(f"    subnet: {_unique_subnet(name)}")
    key_names = []
    if keys:
        lines.append("key:")
        for _ in range(keys):
            name = f"sys-env-key-{uuid.uuid4().hex[:6]}"
            key_names.append(name)
            lines.append(f"  - name: {name}")
            lines.append("    algorithm: ed25519")
    with open(spec_path, "w") as f:
        f.write("\n".join(lines) + "\n")
    return {"path": spec_path, "net_names": net_names, "key_names": key_names}


def _write_spec(tmp_path, name, content):
    """Write literal YAML content to a spec file and return absolute path."""
    spec_path = os.path.join(os.fspath(tmp_path), name)
    with open(spec_path, "w") as f:
        f.write(content)
    return spec_path


def _parse_wfid(ls_stdout: str) -> str | None:
    """Extract the first workflow ID from ``mvm env ls`` table output."""
    for line in ls_stdout.splitlines():
        s = line.strip()
        if not s:
            continue
        # Skip header and separator lines.
        if s.startswith("WORKFLOW"):
            continue
        if set(s.replace(" ", "")) <= {"-"}:
            continue
        parts = s.split()
        if len(parts) >= 2:
            return parts[0]
    return None


def _cleanup_resources(net_names, key_names):
    """Best-effort removal of dangling test resources."""
    for name in net_names:
        _run_mvm_host("network", "rm", name, "--force", check=False, timeout=30)
    for name in key_names:
        _run_mvm_host("key", "rm", name, check=False, timeout=30)


# ============================================================================
# TestEnvApply
# ============================================================================


class TestEnvApply:
    """Test ``mvm env apply`` — resource provisioning from YAML specs."""

    def test_apply_basic_spec(self, tmp_path):
        """Apply a spec with one network and one key; verify resources exist."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            result = _run_mvm_host("env", "apply", spec_path, timeout=60)
            assert result.returncode == 0
            assert "successfully" in result.stdout

            # Verify resources via JSON output.
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            assert net_out.returncode == 0
            nets = json.loads(net_out.stdout) if net_out.stdout.strip() else []
            found_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            assert len(found_nets) == 1, f"Expected 1 network, got {found_nets}"

            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            assert key_out.returncode == 0
            keys = json.loads(key_out.stdout) if key_out.stdout.strip() else []
            found_keys = [k["name"] for k in keys if k["name"] in spec["key_names"]]
            assert len(found_keys) == 1, f"Expected 1 key, got {found_keys}"
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_apply_reapply(self, tmp_path):
        """Applying the same spec twice must succeed both times (idempotent)."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            result1 = _run_mvm_host("env", "apply", spec_path, timeout=60)
            assert result1.returncode == 0

            result2 = _run_mvm_host("env", "apply", spec_path, timeout=60)
            assert result2.returncode == 0
            assert "successfully" in result2.stdout

            # Verify both resources still exist.
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            nets = json.loads(net_out.stdout) if net_out.stdout.strip() else []
            found_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            assert len(found_nets) == 1

            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = json.loads(key_out.stdout) if key_out.stdout.strip() else []
            found_keys = [k["name"] for k in keys if k["name"] in spec["key_names"]]
            assert len(found_keys) == 1
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_apply_nonexistent_spec(self, tmp_path):
        """Apply on a nonexistent spec path must exit non-zero with 'not found'."""
        result = _run_mvm_host("env", "apply", "/nonexistent/path.yaml", check=False, timeout=30)
        assert result.returncode != 0
        assert "not found" in result.stderr.lower() or "not a directory" in result.stderr.lower()

    def test_apply_invalid_yaml(self, tmp_path):
        """Apply on a spec with broken YAML must exit non-zero."""
        spec_path = os.path.join(os.fspath(tmp_path), "bad.yaml")
        with open(spec_path, "w") as f:
            f.write('version: "1"\nnetwork:\n  - invalid_yaml: [\n')
        result = _run_mvm_host("env", "apply", spec_path, check=False, timeout=30)
        assert result.returncode != 0
        assert "yaml" in result.stderr.lower() or "invalid" in result.stderr.lower()

    def test_apply_empty_spec(self, tmp_path):
        """Apply a spec with version but no resources must fail with 'no resources'."""
        spec_path = os.path.join(os.fspath(tmp_path), "empty.yaml")
        with open(spec_path, "w") as f:
            f.write('version: "1"\n')
        result = _run_mvm_host("env", "apply", spec_path, check=False, timeout=30)
        assert result.returncode != 0
        assert "no resources" in result.stderr.lower()


# ============================================================================
# TestEnvDiff
# ============================================================================


class TestEnvDiff:
    """Test ``mvm env diff`` — compare spec against saved workflow state."""

    def test_diff_after_apply(self, tmp_path):
        """Diff against an applied spec must show 'No differences'."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)
            result = _run_mvm_host("env", "diff", spec_path, timeout=30)
            assert result.returncode == 0
            assert "No differences" in result.stdout
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_diff_drifted(self, tmp_path):
        """Change a spec field after apply; diff must show the step as Drifted."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Alter the subnet in-place so the spec hash changes.
            with open(spec_path) as f:
                content = f.read()
            content = content.replace("/24", "/25")
            with open(spec_path, "w") as f:
                f.write(content)

            result = _run_mvm_host("env", "diff", spec_path, timeout=30)
            assert result.returncode == 0
            assert "Drifted" in result.stderr
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_diff_new_resource(self, tmp_path):
        """Add a resource to the spec; diff shows it as New and existing as Existing."""
        net_name = f"diff-net-{uuid.uuid4().hex[:6]}"
        key_name_a = f"diff-key-a-{uuid.uuid4().hex[:6]}"
        orig = (
            f'version: "1"\n'
            f"network:\n"
            f"  - name: {net_name}\n"
            f"    subnet: 10.0.1.0/24\n"
            f"key:\n"
            f"  - name: {key_name_a}\n"
            f"    algorithm: ed25519\n"
        )
        spec_path = _write_spec(tmp_path, "diff-new.yaml", orig)
        created_nets = [net_name]
        created_keys = [key_name_a]
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Add a second key to the spec.
            key_name_b = f"diff-key-b-{uuid.uuid4().hex[:6]}"
            modified = orig + f"  - name: {key_name_b}\n    algorithm: ed25519\n"
            with open(spec_path, "w") as f:
                f.write(modified)

            result = _run_mvm_host("env", "diff", spec_path, timeout=30)
            assert result.returncode == 0
            assert "New" in result.stderr
            assert "Existing" in result.stderr
            created_keys = [key_name_a, key_name_b]
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_diff_removed_resource(self, tmp_path):
        """Remove a resource from the spec; diff shows it as Removed."""
        net_name = f"diff-net-{uuid.uuid4().hex[:6]}"
        key_name = f"diff-key-{uuid.uuid4().hex[:6]}"
        orig = (
            f'version: "1"\n'
            f"network:\n"
            f"  - name: {net_name}\n"
            f"    subnet: 10.0.2.0/24\n"
            f"key:\n"
            f"  - name: {key_name}\n"
            f"    algorithm: ed25519\n"
        )
        spec_path = _write_spec(tmp_path, "diff-rem.yaml", orig)
        created_nets = [net_name]
        created_keys = [key_name]
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Write a spec without the key (network only).
            reduced = (
                f'version: "1"\n'
                f"network:\n"
                f"  - name: {net_name}\n"
                f"    subnet: 10.0.2.0/24\n"
            )
            with open(spec_path, "w") as f:
                f.write(reduced)

            result = _run_mvm_host("env", "diff", spec_path, timeout=30)
            assert result.returncode == 0
            assert "Removed" in result.stderr
            assert "Existing" in result.stderr
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_diff_nonexistent_spec(self, tmp_path):
        """Diff on a nonexistent spec path must exit non-zero."""
        result = _run_mvm_host("env", "diff", "/nonexistent/spec.yaml", check=False, timeout=30)
        assert result.returncode != 0
        # The error mentions either "not found" or the wrapped "failed".
        assert "not found" in result.stderr.lower() or "not a directory" in result.stderr.lower()


# ============================================================================
# TestEnvDestroy
# ============================================================================


class TestEnvDestroy:
    """Test ``mvm env destroy`` — tear down provisioned resources."""

    def test_destroy_by_spec_path(self, tmp_path):
        """Destroy by spec path must remove resources and workflow state."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Capture resource names before destroy.
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            nets = json.loads(net_out.stdout) if net_out.stdout.strip() else []
            created_nets = [n["name"] for n in nets if "sys-env-net" in n["name"]]
            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = json.loads(key_out.stdout) if key_out.stdout.strip() else []
            created_keys = [k["name"] for k in keys if "sys-env-key" in k["name"]]

            result = _run_mvm_host("env", "destroy", spec_path, timeout=60)
            assert result.returncode == 0
            assert "destroyed" in result.stdout.lower()

            # Verify resources are gone.
            net_after = _run_mvm_host("network", "ls", "--json", timeout=30)
            if net_after.stdout.strip():
                remaining_nets = json.loads(net_after.stdout)
                assert not any(n["name"] in created_nets for n in remaining_nets)

            key_after = _run_mvm_host("key", "ls", "--json", timeout=30)
            if key_after.stdout.strip():
                remaining_keys = json.loads(key_after.stdout)
                assert not any(k["name"] in created_keys for k in remaining_keys)

            # State should be gone — env ls shows empty.
            ls_result = _run_mvm_host("env", "ls", timeout=30)
            assert "No saved environments found" in ls_result.stdout

            created_nets = []
            created_keys = []
        finally:
            _cleanup_resources(created_nets, created_keys)

    def test_destroy_by_workflow_id(self, tmp_path):
        """Destroy by workflow ID (from env ls) must remove resources."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Parse workflow ID from env ls.
            ls_result = _run_mvm_host("env", "ls", timeout=30)
            wf_id = _parse_wfid(ls_result.stdout)
            assert wf_id is not None, f"Could not parse workflow ID from:\n{ls_result.stdout}"
            assert len(wf_id) > 0

            # Capture resource names before destroy.
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            nets = json.loads(net_out.stdout) if net_out.stdout.strip() else []
            created_nets = [n["name"] for n in nets if "sys-env-net" in n["name"]]
            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = json.loads(key_out.stdout) if key_out.stdout.strip() else []
            created_keys = [k["name"] for k in keys if "sys-env-key" in k["name"]]

            result = _run_mvm_host("env", "destroy", wf_id, timeout=60)
            assert result.returncode == 0
            assert "destroyed" in result.stdout.lower()

            # Verify resources are gone.
            net_after = _run_mvm_host("network", "ls", "--json", timeout=30)
            if net_after.stdout.strip():
                remaining = json.loads(net_after.stdout)
                assert not any(n["name"] in created_nets for n in remaining)

            created_nets = []
            created_keys = []
        finally:
            _cleanup_resources(created_nets, created_keys)

    def test_destroy_nonexistent(self, tmp_path):
        """Destroy on a nonexistent workflow ID must fail."""
        result = _run_mvm_host("env", "destroy", "nonexistent-id", check=False, timeout=30)
        assert result.returncode != 0
        assert "no saved workflow state found" in result.stderr.lower()

    def test_destroy_twice(self, tmp_path):
        """First destroy succeeds; second destroy on same spec fails."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Capture resource names before first destroy.
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            nets = json.loads(net_out.stdout) if net_out.stdout.strip() else []
            created_nets = [n["name"] for n in nets if "sys-env-net" in n["name"]]
            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = json.loads(key_out.stdout) if key_out.stdout.strip() else []
            created_keys = [k["name"] for k in keys if "sys-env-key" in k["name"]]

            # First destroy must succeed.
            r1 = _run_mvm_host("env", "destroy", spec_path, timeout=60)
            assert r1.returncode == 0
            assert "destroyed" in r1.stdout.lower()

            created_nets = []
            created_keys = []

            # Second destroy must fail — state already gone.
            r2 = _run_mvm_host("env", "destroy", spec_path, check=False, timeout=30)
            assert r2.returncode != 0
            assert "no saved workflow state found" in r2.stderr.lower()
        finally:
            _cleanup_resources(created_nets, created_keys)


# ============================================================================
# TestEnvLs
# ============================================================================


class TestEnvLs:
    """Test ``mvm env ls`` — list saved workflow states."""

    def test_ls_empty(self, tmp_path):
        """With no workflows, env ls must show 'No saved environments found'."""
        result = _run_mvm_host("env", "ls", timeout=30)
        assert result.returncode == 0
        assert "No saved environments found" in result.stdout

    def test_ls_after_apply(self, tmp_path):
        """After apply, env ls must list the workflow with spec path visible."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            result = _run_mvm_host("env", "ls", timeout=30)
            assert result.returncode == 0
            assert "No saved environments found" not in result.stdout
            assert spec_path in result.stdout

            # Parse workflow ID to verify it's non-empty.
            wf_id = _parse_wfid(result.stdout)
            assert wf_id is not None, "Expected a workflow ID in env ls output"
            assert len(wf_id) >= 6
        finally:
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)

    def test_ls_after_destroy(self, tmp_path):
        """Apply → ls shows entry → destroy → ls is empty again."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            ls_before = _run_mvm_host("env", "ls", timeout=30)
            assert spec_path in ls_before.stdout

            _run_mvm_host("env", "destroy", spec_path, timeout=60)

            created_nets = []
            created_keys = []

            ls_after = _run_mvm_host("env", "ls", timeout=30)
            assert "No saved environments found" in ls_after.stdout
        finally:
            _cleanup_resources(created_nets, created_keys)


# ============================================================================
# TestEnvLifecycle
# ============================================================================


class TestEnvLifecycle:
    """Full lifecycle: apply → verify → diff → destroy → verify → ls."""

    def test_full_lifecycle(self, tmp_path):
        """Execute a complete env workflow lifecycle end-to-end."""
        spec = _make_spec(tmp_path, networks=1, keys=1)
        spec_path = spec["path"]
        created_nets = list(spec["net_names"])
        created_keys = list(spec["key_names"])
        net_name = None
        key_name = None
        try:
            # --- Apply ---
            result = _run_mvm_host("env", "apply", spec_path, timeout=60)
            assert result.returncode == 0
            assert "successfully" in result.stdout

            # --- Verify both resources exist via JSON ---
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            nets = json.loads(net_out.stdout) if net_out.stdout.strip() else []
            net_name = next(
                (n["name"] for n in nets if n["name"] in spec["net_names"]), None
            )
            assert net_name is not None, "Network not found after apply"

            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = json.loads(key_out.stdout) if key_out.stdout.strip() else []
            key_name = next(
                (k["name"] for k in keys if k["name"] in spec["key_names"]), None
            )
            assert key_name is not None, "Key not found after apply"

            # --- Diff shows no differences ---
            diff_result = _run_mvm_host("env", "diff", spec_path, timeout=30)
            assert diff_result.returncode == 0
            assert "No differences" in diff_result.stdout

            # --- Destroy ---
            destroy_result = _run_mvm_host("env", "destroy", spec_path, timeout=60)
            assert destroy_result.returncode == 0
            assert "destroyed" in destroy_result.stdout.lower()

            net_name = None
            key_name = None

            # --- Verify resources are gone ---
            net_after = _run_mvm_host("network", "ls", "--json", timeout=30)
            if net_after.stdout.strip():
                remaining_nets = json.loads(net_after.stdout)
                assert not any(
                    n["name"] in spec["net_names"] for n in remaining_nets
                )

            key_after = _run_mvm_host("key", "ls", "--json", timeout=30)
            if key_after.stdout.strip():
                remaining_keys = json.loads(key_after.stdout)
                assert not any(
                    k["name"] in spec["key_names"] for k in remaining_keys
                )

            # --- env ls shows empty ---
            ls_result = _run_mvm_host("env", "ls", timeout=30)
            assert "No saved environments found" in ls_result.stdout
        finally:
            if net_name:
                _run_mvm_host("network", "rm", net_name, "--force", check=False, timeout=30)
            if key_name:
                _run_mvm_host("key", "rm", key_name, check=False, timeout=30)
            _run_mvm_host("env", "destroy", spec_path, check=False, timeout=60)
            _cleanup_resources(created_nets, created_keys)


# ============================================================================
# TestEnvHelp
# ============================================================================


class TestEnvHelp:
    """Test help output for env and its subcommands."""

    def test_env_help(self, tmp_path):
        """``mvm env --help`` must show Usage, apply, ls, diff, destroy."""
        result = _run_mvm_host("env", "--help", timeout=30)
        assert "Usage:" in result.stdout
        assert "apply" in result.stdout
        assert "ls" in result.stdout
        assert "diff" in result.stdout
        assert "destroy" in result.stdout

    def test_env_apply_help(self, tmp_path):
        """``mvm env apply --help`` must show Usage."""
        result = _run_mvm_host("env", "apply", "--help", timeout=30)
        assert "Usage:" in result.stdout

    def test_env_destroy_help(self, tmp_path):
        """``mvm env destroy --help`` must show Usage."""
        result = _run_mvm_host("env", "destroy", "--help", timeout=30)
        assert "Usage:" in result.stdout

    def test_env_diff_help(self, tmp_path):
        """``mvm env diff --help`` must show Usage."""
        result = _run_mvm_host("env", "diff", "--help", timeout=30)
        assert "Usage:" in result.stdout

