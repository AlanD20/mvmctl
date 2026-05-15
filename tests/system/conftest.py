"""System test fixtures and utilities.

System tests are black-box integration tests that invoke mvm via subprocess.
NO imports from mvmctl.* — tests must work against the actual CLI.
"""

from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any, Generator

import pytest

# ============================================================================
# Session-scoped fixtures (shared by all domains)
# ============================================================================


@pytest.fixture(scope="session")
def mvm_binary() -> str:
    """Resolve MVM binary path from env var or default."""
    binary = os.environ.get("MVM_BINARY", "uv run mvm")
    result = subprocess.run(
        [*shlex.split(binary), "--version"],
        capture_output=True,
        text=True,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if result.returncode != 0:
        pytest.skip(f"MVM binary not functional: {binary}")
    return binary


@pytest.fixture(scope="session")
def check_system_prerequisites() -> None:
    """Verify system can run real VM tests."""
    if not Path("/dev/kvm").exists():
        pytest.skip("System tests require /dev/kvm (KVM not available)")
    import grp

    try:
        mvm_group = grp.getgrnam("mvm")
        if mvm_group.gr_gid not in os.getgroups() and os.getgid() != 0:
            pytest.skip("User not in 'mvm' group")
    except KeyError:
        pytest.skip("'mvm' group not found")
    if not (Path.home() / ".cache" / "mvmctl" / "mvmdb.db").exists():
        pytest.skip("mvmctl not initialized (run 'mvm host init')")


@pytest.fixture(scope="session")
def system_cache_dir() -> Path:
    return Path.home() / ".cache" / "mvmctl"


@pytest.fixture(scope="session")
def timing_targets() -> dict[str, float]:
    return {
        "alpine:3.21": 10.0,
        "ubuntu:24.04": 10.0,
        "ubuntu-24.04": 10.0,
        "archlinux": 10.0,
        "debian-bookworm": 10.0,
    }


# ============================================================================
# Internal asset helpers (not exported, used by fixtures within this file)
# ============================================================================


def _print_prep(msg: str) -> None:
    print(f"[prepare] {msg}", file=sys.stderr, flush=True)


def _cleanup_stale_processes() -> None:
    for name in ("firecracker", "jailer"):
        try:
            out = subprocess.run(
                ["pgrep", name], capture_output=True, text=True, timeout=5
            )
            if out.returncode == 0 and out.stdout.strip():
                subprocess.run(
                    ["kill", "-9", *out.stdout.strip().splitlines()],
                    capture_output=True,
                    timeout=5,
                )
        except FileNotFoundError:
            pass


def _run_cmd(
    binary: str, args: list[str], *, timeout: int = 60, check: bool = True
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [*shlex.split(binary), *args],
        capture_output=True,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )


def _pull_asset(binary: str, args: list[str], desc: str) -> None:
    _print_prep(f"Pulling {desc}...")
    r = _run_cmd(binary, args, timeout=300)
    if r.returncode != 0:
        pytest.skip(f"Failed to pull {desc}: {r.stderr or r.stdout}")


def _ensure_kernel(binary: str) -> None:
    r = _run_cmd(binary, ["kernel", "ls", "--json"], timeout=30)
    if r.returncode != 0:
        _pull_asset(
            binary,
            ["kernel", "pull", "--type", "firecracker", "--default"],
            "kernel",
        )
        return
    kernels = json.loads(r.stdout)
    if not any(k.get("is_default") and k.get("is_present") for k in kernels):
        present = [k for k in kernels if k.get("is_present")]
        if present:
            _run_cmd(
                binary, ["kernel", "default", present[0]["id"][:6]], timeout=30
            )
        else:
            _pull_asset(
                binary,
                ["kernel", "pull", "--type", "firecracker", "--default"],
                "kernel",
            )


def _ensure_image(binary: str, image: str = "alpine:3.21") -> None:
    """Ensure an image is cached, pulling if needed.

    The *image* parameter should be in ``type:version`` format
    (e.g. ``"alpine:3.21"``, ``"ubuntu:24.04"``).
    """
    img_type = image.split(":")[0] if ":" in image else image
    img_version = image.split(":")[1] if ":" in image else None
    r = _run_cmd(binary, ["image", "ls", "--json"], timeout=30)
    if r.returncode != 0:
        pull_args = ["image", "pull", img_type]
        if img_version:
            pull_args.extend(["--version", img_version])
        _pull_asset(binary, pull_args, f"image '{image}'")
        return
    cached = json.loads(r.stdout) if r.stdout else []
    matching = [
        i
        for i in cached
        if i.get("type", "").startswith(img_type) and i.get("is_present")
    ]
    if not matching:
        pull_args = ["image", "pull", img_type]
        if img_version:
            pull_args.extend(["--version", img_version])
        _pull_asset(binary, pull_args, f"image '{image}'")
        return

    # Verify the image file actually exists on disk (DB may be stale)
    inspect = _run_cmd(
        binary,
        ["image", "inspect", matching[0]["id"][:6], "--json"],
        timeout=30,
    )
    if inspect.returncode == 0 and inspect.stdout.strip():
        try:
            info = json.loads(inspect.stdout)
            img_rel_path = info.get("path", "")
            if img_rel_path:
                cache_dir = Path(
                    os.environ.get(
                        "MVM_CACHE_DIR", Path.home() / ".cache" / "mvmctl"
                    )
                )
                img_file = cache_dir / "images" / img_rel_path
                if not img_file.exists():
                    _print_prep(
                        f"Image file {img_file} missing — re-pulling {image}"
                    )
                    _pull_asset(
                        binary,
                        ["image", "pull", image, "--force"],
                        f"image '{image}'",
                    )
        except (json.JSONDecodeError, KeyError, TypeError):
            pass


def _ensure_binary(binary: str) -> None:
    r = _run_cmd(binary, ["bin", "ls", "--json"], timeout=30)
    has = False
    if r.returncode == 0:
        has = any(
            b.get("name") == "firecracker" and b.get("is_present")
            for b in json.loads(r.stdout)
        )
    if not has:
        _pull_asset(
            binary, ["bin", "pull", "1.15.1", "--default"], "firecracker binary"
        )


def _ensure_services_binary(binary: str) -> None:
    """Ensure mvm-services binary (mvm-provision) is registered in the DB.

    Service binaries (mvm-provision, mvm-console-relay, mvm-nocloud-server)
    can be removed by ``cache clean --force``. This function re-extracts the
    combined ``mvm-services`` binary from the build output and re-registers
    it in the DB so VM creation does not fail.
    """
    r = _run_cmd(binary, ["bin", "ls", "--json"], timeout=30)
    has_provision = False
    if r.returncode == 0:
        has_provision = any(
            b.get("name") == "mvm-provision" and b.get("is_present")
            for b in json.loads(r.stdout)
        )
    if has_provision:
        return

    # Binary missing — attempt to re-extract from build output
    services_src = Path("dist/services/mvm-services")
    if not services_src.exists():
        _print_prep(
            "WARNING: dist/services/mvm-services not found. "
            "Run 'python scripts/build_services.py' to build it."
        )
        return

    bin_dir = Path.home() / ".cache" / "mvmctl" / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    dest = bin_dir / "mvm-services"

    # Retry copy on ETXTBUSY (errno 26) — stale service processes (nocloud,
    # console-relay, mvm-provision) may hold the binary mmap'd even after
    # VM cleanup.  Kill them before retrying.
    import errno as _errno

    _copy_retries = 5
    for copy_attempt in range(_copy_retries):
        try:
            shutil.copy2(str(services_src), str(dest))
            break
        except OSError as _e:
            if _e.errno != _errno.ETXTBSY:
                raise
            if copy_attempt >= _copy_retries - 1:
                # Last retry — fall back: copy to a temp name, then rename
                # (rename does not require opening the destination file).
                _print_prep(
                    "Text file busy persists, trying rename fallback..."
                )
                import tempfile as _tempfile

                _tmp_dest = dest.with_suffix(".tmp.copy")
                shutil.copy2(str(services_src), str(_tmp_dest))
                _tmp_dest.chmod(0o755)
                _tmp_dest.rename(dest)
                _print_prep("Renamed temp copy over destination")
                break
            _print_prep(
                f"Text file busy on mvm-services, killing stale "
                f"service processes and retrying "
                f"({copy_attempt + 1}/{_copy_retries})..."
            )
            # Kill stale service processes that may hold the binary open
            for _name in (
                "mvm-console-relay",
                "mvm-nocloud-server",
                "mvm-provision",
            ):
                try:
                    subprocess.run(
                        ["killall", "-9", _name],
                        capture_output=True,
                        timeout=5,
                        check=False,
                    )
                except FileNotFoundError:
                    pass
            time.sleep(2.0)
    dest.chmod(0o755)

    # Create symlinks so the service binaries are findable by name
    for _link_name in ("mvm-provision", "mvm-console-relay", "mvm-nocloud-server"):
        _link_path = bin_dir / _link_name
        _link_path.unlink(missing_ok=True)
        _link_path.symlink_to(dest.name)
        _print_prep(f"Created symlink: {_link_name} -> mvm-services")
    _print_prep("Copied mvm-services binary to cache bin dir")

    # Register all service binaries directly in the DB since there is
    # no CLI command to do so.  This mirrors _restore_service_binaries()
    # in tests/system/cache/test_cache.py.
    import sqlite3 as _sqlite3
    import hashlib as _hashlib
    import datetime as _datetime

    db_path = Path.home() / ".cache" / "mvmctl" / "mvmdb.db"
    _service_entries = [
        ("mvm-console-relay", "0.1.0", "0.1.0"),
        ("mvm-nocloud-server", "0.1.0", "0.1.0"),
        ("mvm-provision", "0.1.0", "0.1.0"),
    ]
    _now = _datetime.datetime.now(_datetime.UTC).strftime("%Y-%m-%dT%H:%M:%S")
    _conn = _sqlite3.connect(str(db_path))
    try:
        for _sname, _sver, _sfullver in _service_entries:
            _cur = _conn.execute(
                "SELECT COUNT(*) FROM binaries WHERE name = ?", (_sname,)
            )
            if _cur.fetchone()[0] == 0:
                _uid = _hashlib.sha256(f"{_sname}:{_sver}".encode()).hexdigest()
                _conn.execute(
                    """INSERT INTO binaries
                       (id, name, version, full_version, path,
                        is_default, is_present, created_at, updated_at)
                       VALUES (?, ?, ?, ?, ?, 0, 1, ?, ?)""",
                    (_uid, _sname, _sver, _sfullver, _sname, _now, _now),
                )
            else:
                _conn.execute(
                    "UPDATE binaries SET is_present=1, updated_at=? WHERE name=?",
                    (_now, _sname),
                )
        _conn.commit()
    finally:
        _conn.close()
    _print_prep("Service binaries registered in DB")


def ensure_vm_deps(binary: str) -> None:
    """Ensure kernel, alpine image, and firecracker binary are available.

    Call this before inline ``vm create`` calls in tests that don't use
    the ``created_vm`` or ``minimal_vm`` fixtures (which call it internally).
    Guestfs is disabled by default (``settings.guestfs_enabled = False`` in
    ``constants.py``) — only loop-mount is used for provisioning.

    If a previous test enabled guestfs and failed to reset it, re-disable it
    here so loop-mount provisioning works reliably.
    """
    _run_cmd(
        binary,
        ["config", "set", "settings", "guestfs_enabled", "false"],
        timeout=10,
        check=False,
    )
    _ensure_kernel(binary)
    _ensure_image(binary)
    _ensure_binary(binary)
    _ensure_services_binary(binary)


# ============================================================================
# Autouse fixture: override root conftest env isolation
# ============================================================================


@pytest.fixture(autouse=True)
def _restore_real_dirs(monkeypatch, system_cache_dir) -> None:
    monkeypatch.setenv("MVM_CACHE_DIR", str(system_cache_dir))
    monkeypatch.setenv(
        "MVM_CONFIG_DIR", str(Path.home() / ".config" / "mvmctl")
    )
    monkeypatch.setenv("MVM_TEMP_DIR", str(Path("/tmp") / "mvmctl"))
    monkeypatch.setenv("NO_COLOR", "1")


# ============================================================================
# Per-test fixtures
# ============================================================================


@pytest.fixture
def unique_vm_name() -> str:
    return f"sys-vm-{uuid.uuid4().hex[:8]}"


@pytest.fixture
def unique_network_name() -> str:
    return f"sys-net-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def unique_key_name() -> str:
    return f"sys-key-{uuid.uuid4().hex[:6]}"


def _unique_subnet(network_name: str) -> str:
    import random

    rng = random.Random(network_name)
    return f"10.{rng.randint(1, 254)}.{rng.randint(0, 254)}.0/24"


# ============================================================================
# VM creation: two levels shared across domains
# ============================================================================


def _create_minimal_vm_core(
    binary: str,
    vm_name: str,
    net_name: str,
    *,
    ssh_key_name: str | None = None,
    extra_args: list[str] | None = None,
) -> dict[str, Any]:
    """Core VM creation — ensures kernel/image/binary, creates network, returns VM info.

    All VM fixtures (minimal_vm, created_vm, module_vm, lifecycle_vm) delegate
    to this function so that asset verification happens in one place.
    """
    ensure_vm_deps(binary)

    subnet = _unique_subnet(net_name)
    _run_mvm(
        binary,
        "network",
        "create",
        net_name,
        "--subnet",
        subnet,
        "--non-interactive",
    )

    cmd: list[str] = [
        "vm",
        "create",
        "--name",
        vm_name,
        "--image",
        "alpine:3.21",
        "--network",
        net_name,
        "--no-console",
    ]
    if ssh_key_name:
        cmd.extend(["--ssh-key", ssh_key_name])
    if extra_args:
        cmd.extend(extra_args)
    _run_mvm(binary, *cmd)

    vms = _parse_vm_list(_run_mvm(binary, "vm", "ls", "--json").stdout)
    vm_info = next((v for v in vms if v["name"] == vm_name), None)
    if not vm_info:
        raise RuntimeError(f"Failed to find created VM: {vm_name}")
    return vm_info


def _cleanup_vm_resources(
    binary: str, vm_name: str, net_name: str, key_name: str | None = None
) -> None:
    """Clean up VM, network, and optional key."""
    _run_mvm(binary, "vm", "rm", vm_name, "--force", check=False)
    _run_mvm(binary, "network", "rm", net_name, check=False)
    if key_name:
        _run_mvm(binary, "key", "rm", key_name, check=False)


# -- minimal_vm: bare VM, no SSH key, no extras ---------------------------


@pytest.fixture
def minimal_vm(
    mvm_binary, unique_vm_name, unique_network_name
) -> Generator[dict[str, Any], None, None]:
    """Bare VM: no SSH, no console. Fastest creation. Self-contained deps."""
    vm_name = unique_vm_name
    net_name = unique_network_name
    vm_info = _create_minimal_vm_core(mvm_binary, vm_name, net_name)
    try:
        yield vm_info
    finally:
        _cleanup_vm_resources(mvm_binary, vm_name, net_name)


# -- created_vm: VM with SSH key -------------------------------------------


@pytest.fixture
def created_vm(
    mvm_binary, unique_vm_name
) -> Generator[dict[str, Any], None, None]:
    """VM with SSH key injected. Self-contained deps. No console."""
    vm_name = unique_vm_name
    key_name = f"sys-vmkey-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-vm-net-{uuid.uuid4().hex[:6]}"

    _run_mvm(mvm_binary, "key", "create", key_name, "--algorithm", "ed25519")
    _run_mvm(mvm_binary, "key", "default", key_name, check=False)

    vm_info = _create_minimal_vm_core(
        mvm_binary, vm_name, net_name, ssh_key_name=key_name
    )
    try:
        yield vm_info
    finally:
        _cleanup_vm_resources(mvm_binary, vm_name, net_name, key_name)


# -- module_vm: module-scoped VM with SSH key (read-only tests) ------------


@pytest.fixture(scope="module")
def module_vm(mvm_binary) -> Generator[dict[str, Any], None, None]:
    """Module-scoped VM with SSH key. Shared across a module for read-only tests."""
    vm_name = f"sys-modvm-{uuid.uuid4().hex[:8]}"
    key_name = f"sys-modvm-key-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-modvm-net-{uuid.uuid4().hex[:6]}"

    _run_mvm(mvm_binary, "key", "create", key_name, "--algorithm", "ed25519")
    vm_info = _create_minimal_vm_core(
        mvm_binary, vm_name, net_name, ssh_key_name=key_name
    )
    try:
        yield vm_info
    finally:
        _cleanup_vm_resources(mvm_binary, vm_name, net_name, key_name)


# -- module_network: module-scoped network --------------------------------


@pytest.fixture(scope="module")
def module_network(mvm_binary) -> Generator[str, None, None]:
    """Module-scoped network for read-only tests."""
    name = f"sys-modnet-{uuid.uuid4().hex[:6]}"
    _run_mvm(
        mvm_binary,
        "network",
        "create",
        name,
        "--subnet",
        _unique_subnet(name),
        "--non-interactive",
    )
    try:
        yield name
    finally:
        _run_mvm(mvm_binary, "network", "rm", name, check=False)
        result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if result.returncode == 0:
            import json as _json

            nets = _json.loads(result.stdout)
            if not any(n.get("is_default") for n in nets) and nets:
                _run_mvm(
                    mvm_binary,
                    "network",
                    "default",
                    nets[0]["name"],
                    check=False,
                )


# ============================================================================
# Other shared fixtures
# ============================================================================


@pytest.fixture
def created_network(
    mvm_binary, unique_network_name
) -> Generator[str, None, None]:
    """Create a network and guarantee cleanup."""
    subnet = _unique_subnet(unique_network_name)
    _run_mvm(
        mvm_binary,
        "network",
        "create",
        unique_network_name,
        "--subnet",
        subnet,
        "--non-interactive",
    )
    try:
        yield unique_network_name
    finally:
        _run_mvm(mvm_binary, "network", "rm", unique_network_name, check=False)
        import json as _json

        result = _run_mvm(mvm_binary, "network", "ls", "--json", check=False)
        if result.returncode == 0:
            nets = _json.loads(result.stdout)
            if not any(n.get("is_default") for n in nets) and nets:
                _run_mvm(
                    mvm_binary,
                    "network",
                    "default",
                    nets[0]["name"],
                    check=False,
                )


@pytest.fixture
def created_key(mvm_binary, unique_key_name) -> Generator[str, None, None]:
    """Create an SSH key and guarantee cleanup."""
    _run_mvm(
        mvm_binary, "key", "create", unique_key_name, "--algorithm", "ed25519"
    )
    try:
        yield unique_key_name
    finally:
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


# ============================================================================
# Helper functions (used by test files)
# ============================================================================


def _run_mvm(
    binary: str, *args: str, check: bool = True, timeout: int = 60
) -> subprocess.CompletedProcess[str]:
    """Run mvm command via subprocess."""
    cmd = [*shlex.split(binary), *args]
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"mvm command failed: {' '.join(args)}\nstdout: {result.stdout}\nstderr: {result.stderr}"
        )
    return result


def _parse_vm_list(json_output: str) -> list[dict[str, Any]]:
    return json.loads(json_output) if json_output else []


def wait_for_ssh(binary: str, vm_name: str, user: str, timeout: float) -> bool:
    """Poll SSH via mvm ssh until available or timeout."""
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        try:
            result = subprocess.run(
                [
                    *shlex.split(binary),
                    "ssh",
                    vm_name,
                    "-u",
                    user,
                    "--cmd",
                    "exit",
                ],
                capture_output=True,
                timeout=5,
                env={**os.environ, "NO_COLOR": "1"},
            )
            if result.returncode == 0:
                return True
        except subprocess.TimeoutExpired:
            pass
        time.sleep(0.5)
    return False
