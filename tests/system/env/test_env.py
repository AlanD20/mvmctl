"""Environment workflows through the installed qualification-host candidate."""
from __future__ import annotations

import json
import os
import random
import time
import uuid

import pytest
from tests.system.conftest import _json_success_data, _run_mvm


def _run_mvm_host(*args, **kwargs):
    """Use the installed candidate in the selected qualification VM."""
    return _run_mvm(os.environ.get("MVM_TEST_VM", ""), *args, **kwargs)


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
            lines.append(f"  {name}:")
            lines.append(f"    subnet: {_unique_subnet(name)}")
    key_names = []
    if keys:
        lines.append("key:")
        for _ in range(keys):
            name = f"sys-env-key-{uuid.uuid4().hex[:6]}"
            key_names.append(name)
            lines.append(f"  {name}:")
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


def _parse_wfid(ls_stdout: str, spec_path: str) -> str | None:
    """Extract the first workflow ID from ``mvm env ls`` table output."""
    for line in ls_stdout.splitlines():
        if spec_path not in line:
            continue
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
            nets = _json_success_data(net_out.stdout, list)
            found_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            assert len(found_nets) == 1, f"Expected 1 network, got {found_nets}"

            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            assert key_out.returncode == 0
            keys = _json_success_data(key_out.stdout, list)
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
            nets = _json_success_data(net_out.stdout, list)
            found_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            assert len(found_nets) == 1

            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = _json_success_data(key_out.stdout, list)
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
            f"  {net_name}:\n"
            f"    subnet: 10.0.1.0/24\n"
            f"key:\n"
            f"  {key_name_a}:\n"
            f"    algorithm: ed25519\n"
        )
        spec_path = _write_spec(tmp_path, "diff-new.yaml", orig)
        created_nets = [net_name]
        created_keys = [key_name_a]
        try:
            _run_mvm_host("env", "apply", spec_path, timeout=60)

            # Add a second key to the spec.
            key_name_b = f"diff-key-b-{uuid.uuid4().hex[:6]}"
            modified = orig + f"  {key_name_b}:\n    algorithm: ed25519\n"
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
            f"  {net_name}:\n"
            f"    subnet: 10.0.2.0/24\n"
            f"key:\n"
            f"  {key_name}:\n"
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
                f"  {net_name}:\n"
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
            nets = _json_success_data(net_out.stdout, list)
            created_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = _json_success_data(key_out.stdout, list)
            created_keys = [k["name"] for k in keys if k["name"] in spec["key_names"]]

            result = _run_mvm_host("env", "destroy", spec_path, timeout=60)
            assert result.returncode == 0
            assert "destroyed" in result.stdout.lower()

            # Verify resources are gone.
            net_after = _run_mvm_host("network", "ls", "--json", timeout=30)
            remaining_nets = _json_success_data(net_after.stdout, list)
            assert not any(n["name"] in created_nets for n in remaining_nets)

            key_after = _run_mvm_host("key", "ls", "--json", timeout=30)
            remaining_keys = _json_success_data(key_after.stdout, list)
            assert not any(k["name"] in created_keys for k in remaining_keys)

            # State should be gone — env ls shows empty.
            ls_result = _run_mvm_host("env", "ls", timeout=30)
            assert spec_path not in ls_result.stdout

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
            wf_id = _parse_wfid(ls_result.stdout, spec_path)
            assert wf_id is not None, f"Could not parse workflow ID from:\n{ls_result.stdout}"
            assert len(wf_id) > 0

            # Capture resource names before destroy.
            net_out = _run_mvm_host("network", "ls", "--json", timeout=30)
            nets = _json_success_data(net_out.stdout, list)
            created_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = _json_success_data(key_out.stdout, list)
            created_keys = [k["name"] for k in keys if k["name"] in spec["key_names"]]

            result = _run_mvm_host("env", "destroy", wf_id, timeout=60)
            assert result.returncode == 0
            assert "destroyed" in result.stdout.lower()

            # Verify resources are gone.
            net_after = _run_mvm_host("network", "ls", "--json", timeout=30)
            remaining = _json_success_data(net_after.stdout, list)
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
            nets = _json_success_data(net_out.stdout, list)
            created_nets = [n["name"] for n in nets if n["name"] in spec["net_names"]]
            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = _json_success_data(key_out.stdout, list)
            created_keys = [k["name"] for k in keys if k["name"] in spec["key_names"]]

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

    def test_ls_json(self, tmp_path):
        """Listing does not require deletion of unrelated environments."""
        result = _run_mvm_host("env", "ls", "--json", timeout=30)
        _json_success_data(result.stdout, list)

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
            wf_id = _parse_wfid(result.stdout, spec_path)
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
            assert spec_path not in ls_after.stdout
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
            nets = _json_success_data(net_out.stdout, list)
            net_name = next(
                (n["name"] for n in nets if n["name"] in spec["net_names"]), None
            )
            assert net_name is not None, "Network not found after apply"

            key_out = _run_mvm_host("key", "ls", "--json", timeout=30)
            keys = _json_success_data(key_out.stdout, list)
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
            remaining_nets = _json_success_data(net_after.stdout, list)
            assert not any(
                n["name"] in spec["net_names"] for n in remaining_nets
            )

            key_after = _run_mvm_host("key", "ls", "--json", timeout=30)
            remaining_keys = _json_success_data(key_after.stdout, list)
            assert not any(
                k["name"] in spec["key_names"] for k in remaining_keys
            )

            # --- env ls shows empty ---
            ls_result = _run_mvm_host("env", "ls", timeout=30)
            assert spec_path not in ls_result.stdout
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
        result = _run_mvm_host("env", "apply", "--help", timeout=30)
        assert "Usage:" in result.stdout

    def test_env_destroy_help(self, tmp_path):
        result = _run_mvm_host("env", "destroy", "--help", timeout=30)
        assert "Usage:" in result.stdout

    def test_env_diff_help(self, tmp_path):
        result = _run_mvm_host("env", "diff", "--help", timeout=30)
        assert "Usage:" in result.stdout


def _env_data(*args, kind=list):
    return _json_success_data(_run_mvm_host(*args, "--json").stdout, kind)


@pytest.fixture
def env_specs(tmp_path):
    """Own only specs written by this test; preserve failed teardown state."""
    paths = []

    def write(name, spec):
        path = tmp_path / name
        path.write_text(json.dumps(spec))
        if str(path) not in paths:
            paths.append(str(path))
        return str(path)

    yield write
    errors = []
    for path in reversed(paths):
        try:
            if path in _run_mvm_host("env", "ls").stdout:
                _run_mvm_host("env", "destroy", path, timeout=180)
        except Exception as error:
            errors.append(f"{path}: {error}")
    assert not errors, "\n".join(errors)


class TestEnvNativeSteps:
    """Own workflow composition; policy-domain journeys own packet isolation."""

    @pytest.mark.parametrize("invalid", [
        {"unknown_section": {"bad": {}}},
        {"vm": {"bad": {"count": 2}}},
        {"vm": {"bad": {"volumes": ["@volume:missing"]}}},
        {"ready": {"bad": {"target": "missing", "cmd": "true", "timeout": 0}}},
    ], ids=["unknown-section", "vm-count", "missing-reference", "ready-deadline"])
    def test_strict_refusal_before_effects(self, env_specs, invalid):
        name = f"env-admission-{uuid.uuid4().hex[:10]}"
        spec = {"version": "1", "volume": {name: {"size": "16M", "retain": False}}, **invalid}
        path = env_specs("invalid.yaml", spec)
        result = _run_mvm_host("env", "apply", path, check=False)
        assert result.returncode != 0, result.stdout
        assert "resolve env spec" in result.stderr, result.stderr
        assert not any(item["name"] == name for item in _env_data("volume", "ls"))
        assert path not in _run_mvm_host("env", "ls").stdout

    def test_reapply_refuses_changed_owned_volume(self, env_specs):
        name = f"env-identity-{uuid.uuid4().hex[:10]}"
        spec = {"version": "1", "volume": {"data": {"name": name, "size": "16M", "retain": False}}}
        path = env_specs("identity.yaml", spec)
        _run_mvm_host("env", "apply", path)
        original = _env_data("volume", "inspect", name, kind=dict)
        _run_mvm_host("env", "apply", path)
        assert _env_data("volume", "inspect", name, kind=dict) == original
        spec["volume"]["data"]["size"] = "32M"
        env_specs("identity.yaml", spec)
        refused = _run_mvm_host("env", "apply", path, check=False)
        assert refused.returncode != 0
        assert "declaration changed" in refused.stderr, refused.stderr
        assert _env_data("volume", "inspect", name, kind=dict) == original
        _run_mvm_host("env", "destroy", path)
        assert not any(item["name"] == name for item in _env_data("volume", "ls"))

    def test_vm_volume_policy_readiness_lifecycle(self, env_specs):
        suffix = uuid.uuid4().hex[:10]
        names = {key: f"env-{key}-{suffix}" for key in ("net", "source", "destination", "data")}
        # Use preseeded assets, with the source-session kernel already qualified by
        # the exec domain. This test performs no downloads or default changes.
        from tests.system.exec.test_vm_to_vm_exec import _require_source_kernel

        kernel = _require_source_kernel(os.environ["MVM_TEST_VM"])
        spec = {
            "version": "1",
            "network": {"net": {"name": names["net"], "subnet": _unique_subnet(names["net"]) }},
            "volume": {"data": {"name": names["data"], "size": "16M", "retain": False}},
            "vm": {}, "ready": {},
            "traffic_policy": {
                "network-access": {"source_network": "@network:net", "destination_vm": "@vm:destination",
                    "protocol": "tcp", "port": 18080, "depends_on": ["@vm:destination"]},
                "vm-access": {"source_vm": "@vm:source", "destination_vm": "@vm:destination",
                    "protocol": "udp", "port": 18081, "depends_on": ["@vm:source", "@vm:destination"]},
            },
            "exec_policy": {"forward": {"source_vm": "@vm:source", "destination_vm": "@vm:destination",
                "target_user": "runner", "depends_on": ["@vm:source", "@vm:destination"]}},
            "exec": {"consumer": {"target": "@vm:destination", "user": "runner",
                "cmd": "printf '%s\\n' '@literal:preserved' >> /tmp/env-consumer",
                "depends_on": ["@ready:destination", "@traffic_policy:network-access",
                    "@traffic_policy:vm-access", "@exec_policy:forward"]}},
        }
        for role in ("source", "destination"):
            spec["vm"][role] = {"name": names[role], "image": "alpine:3.23", "kernel": kernel,
                "network": "@network:net", "user": "runner", "vcpu": 1, "mem": "256M",
                "cloud_init_mode": "off", "skip_deblob": True, "depends_on": ["@network:net"]}
            spec["ready"][role] = {"target": f"@vm:{role}", "user": "runner", "cmd": "test $(id -un) = runner",
                "timeout": 60, "interval": 1, "depends_on": [f"@vm:{role}"]}
        spec["vm"]["destination"].update(volumes=["@volume:data"], depends_on=["@network:net", "@volume:data"])
        path = env_specs("lifecycle.yaml", spec)
        _run_mvm_host("env", "apply", path, timeout=360)
        vms = {v["name"]: v for v in _env_data("vm", "ls") if v["name"] in names.values()}
        assert set(vms) == {names["source"], names["destination"]}
        source, destination = (vms[names[role]]["id"] for role in ("source", "destination"))
        volume = next(v for v in _env_data("volume", "ls") if v["name"] == names["data"])
        assert volume["vm_id"] == destination, volume
        traffic = _env_data("policy", "traffic", "ls", kind=dict)
        network_id = next(n["id"] for n in _env_data("network", "ls") if n["name"] == names["net"])
        net_grants = [p for p in traffic["network_to_vm"] if p["destination_vm_id"] == destination]
        vm_grants = [p for p in traffic["vm_to_vm"] if p["destination_vm_id"] == destination]
        assert [(p["source_network_id"], p["protocol"], p["destination_port_start"]) for p in net_grants] == [(network_id, "tcp", 18080)]
        assert [(p["source_vm_id"], p["protocol"], p["destination_port_start"]) for p in vm_grants] == [(source, "udp", 18081)]
        grants = [p for p in _env_data("policy", "exec", "ls") if p["source_vm_id"] == source]
        assert [(p["destination_vm_id"], p["target_user"]) for p in grants] == [(destination, "runner")]
        for origin, target, allowed in ((source, destination, True), (destination, source, False)):
            remote = _run_mvm_host("exec", origin, "--user", "runner", "--allow-vm-exec", "--",
                f"/usr/bin/mvm-agent remote --user runner {target} -- id -un", check=False)
            assert (remote.returncode == 0) is allowed, (remote.stdout, remote.stderr)
            if allowed:
                assert remote.stdout.strip() == "runner"
        _run_mvm_host("env", "apply", path, timeout=180)
        current = {v["name"]: v["id"] for v in _env_data("vm", "ls") if v["name"] in vms}
        assert current == {name: vm["id"] for name, vm in vms.items()}
        lines = _run_mvm_host("exec", destination, "--user", "runner", "--", "cat /tmp/env-consumer").stdout.splitlines()
        assert lines == ["@literal:preserved", "@literal:preserved"]

        # A running check must obey the total deadline and block its consumer.
        timeout_spec = {"version": "1", "vm": {"selected": {"existing": destination}},
            "ready": {"never": {"target": "@vm:selected", "user": "runner", "cmd": "sleep 30",
                "timeout": 2, "interval": 1, "depends_on": ["@vm:selected"]}},
            "exec": {"blocked": {"target": "@vm:selected", "user": "runner",
                "cmd": "touch /tmp/env-should-not-run", "depends_on": ["@ready:never"]}}}
        deadline_path = env_specs("deadline.yaml", timeout_spec)
        started = time.monotonic()
        failed = _run_mvm_host("env", "apply", deadline_path, check=False)
        assert failed.returncode != 0
        assert "readiness did not complete" in failed.stderr, failed.stderr
        assert time.monotonic() - started < 15
        _run_mvm_host("exec", destination, "--user", "runner", "--", "test ! -e /tmp/env-should-not-run")
        _run_mvm_host("env", "destroy", deadline_path)
        assert any(v["id"] == destination for v in _env_data("vm", "ls"))
        _run_mvm_host("env", "destroy", path, timeout=180)
        assert not any(v["id"] in (source, destination) for v in _env_data("vm", "ls"))
        assert not any(v["id"] == volume["id"] for v in _env_data("volume", "ls"))
        assert not any(n["id"] == network_id for n in _env_data("network", "ls"))
        assert not any(p["id"] == grants[0]["id"] for p in _env_data("policy", "exec", "ls"))
        remaining = _env_data("policy", "traffic", "ls", kind=dict)
        assert not any(p["id"] in {g["id"] for g in net_grants + vm_grants}
            for group in remaining.values() for p in group)

    def test_default_retention_and_existing_volume_preservation(self, env_specs):
        name = f"env-retained-{uuid.uuid4().hex[:10]}"
        owned_path = env_specs("retained.yaml", {"version": "1", "volume": {"data": {"name": name, "size": "16M"}}})
        volume_id = None
        try:
            _run_mvm_host("env", "apply", owned_path)
            original = _env_data("volume", "inspect", name, kind=dict)
            volume_id = original["volume"]["id"]
            existing_path = env_specs("existing.yaml", {"version": "1", "volume": {"selected": {"existing": volume_id}}})
            _run_mvm_host("env", "apply", existing_path)
            _run_mvm_host("env", "destroy", existing_path)
            _run_mvm_host("env", "destroy", owned_path)
            assert _env_data("volume", "inspect", volume_id, kind=dict) == original
            assert owned_path in _run_mvm_host("env", "ls").stdout
            assert existing_path not in _run_mvm_host("env", "ls").stdout
            _run_mvm_host("env", "apply", owned_path)
            assert _env_data("volume", "inspect", volume_id, kind=dict) == original
        finally:
            if volume_id is not None:
                _run_mvm_host("volume", "rm", volume_id, "--force")
                _run_mvm_host("env", "destroy", owned_path)
                assert not any(v["id"] == volume_id for v in _env_data("volume", "ls"))
                assert owned_path not in _run_mvm_host("env", "ls").stdout

