"""Integration test fixtures — subprocess mocks + asset seeding."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from tests.helpers.paths import make_test_paths

# ======================================================================
# Smart subprocess mocks
# ======================================================================


class SmartSubprocessMock:
    """A stateful subprocess.run mock that handles real filesystem operations.

    Handles:
    - cp --reflink=auto --sparse=always  (image materialization)
    - dd if=... of=...                    (rootfs resize)
    - ip link show / ip -j link show      (network state queries)
    - ip addr add / ip link set           (bridge / TAP setup)
    - iptables                             (NAT / firewall rules)
    - genisoimage / cloud-localds          (cloud-init ISO creation)
    - sysctl / modprobe / lsmod           (host init operations)
    - Default: returncode=0 for anything else
    """

    def __init__(self) -> None:
        self.calls: list[list[str]] = []

    def __call__(
        self, *args: object, **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        cmd = kwargs.get("args", args[0] if args else [])
        if not isinstance(cmd, list):
            cmd = []

        self.calls.append(cmd)
        cmd_str = " ".join(str(c) for c in cmd) if cmd else ""

        # --- cp: image materialization ---
        if cmd and cmd[0] == "cp":
            # --sparse=always src dst (or --reflink=auto --sparse=always src dst)
            src_idx = None
            dst_idx = None
            for i, part in enumerate(cmd):
                if part == "--sparse=always" and i + 1 < len(cmd):
                    src_idx = i + 1
                    dst_idx = i + 2
                    break
            if src_idx is not None and dst_idx is not None:
                src = Path(str(cmd[src_idx]))
                dst = Path(str(cmd[dst_idx]))
                dst.parent.mkdir(parents=True, exist_ok=True)
                if src.exists():
                    dst.write_bytes(src.read_bytes())
                else:
                    dst.write_text("fake rootfs")
            return self._ok()

        # --- dd: rootfs resize ---
        if cmd and cmd[0] == "dd":
            of_path = None
            for part in cmd:
                if part.startswith("of="):
                    of_path = part[3:]
            if of_path:
                Path(of_path).parent.mkdir(parents=True, exist_ok=True)
                Path(of_path).write_text("fake resized rootfs")
            return self._ok()

        # --- genisoimage / cloud-localds: cloud-init ISO ---
        if cmd and (cmd[0] == "genisoimage" or "cloud-localds" in cmd_str):
            # Find output flag
            for i, part in enumerate(cmd):
                if part in ("-o", "--output") and i + 1 < len(cmd):
                    out_path = Path(str(cmd[i + 1]))
                    out_path.parent.mkdir(parents=True, exist_ok=True)
                    out_path.write_text("fake iso")
                    break
            return self._ok()

        # --- ip link show <iface>: interface existence check ---
        if cmd_str.startswith("ip link show") and len(cmd) >= 4:
            return self._fail()

        # --- ip -j link show <iface>: interface JSON query ---
        if (
            len(cmd) >= 5
            and cmd[0] == "ip"
            and cmd[1] == "-j"
            and cmd[2] == "link"
            and cmd[3] == "show"
        ):
            iface = str(cmd[-1])
            return self._ok(
                json.dumps([{"ifname": iface, "master": "mvm-br0"}])
            )

        # --- ip -o addr show <bridge>: bridge address query ---
        if (
            len(cmd) >= 5
            and cmd[0] == "ip"
            and cmd[1] == "-o"
            and cmd[2] == "addr"
            and cmd[3] == "show"
        ):
            return self._ok("inet 10.20.0.1/24 scope global mvm-br0\n")

        # --- ip -o -4 addr show <iface>: interface IPv4 address query ---
        if (
            len(cmd) >= 6
            and cmd[0] == "ip"
            and cmd[1] == "-o"
            and cmd[2] == "-4"
            and cmd[3] == "addr"
            and cmd[4] == "show"
        ):
            return self._ok("inet 10.0.0.2/24 scope global eth0\n")

        # --- ip route show default ---
        if "ip" in cmd_str and "route" in cmd_str and "default" in cmd_str:
            return self._ok("default via 192.168.1.1 dev eth0")

        # --- iptables rule existence check ---
        if cmd_str.startswith("iptables"):
            if "-C" in cmd_str or "-L" in cmd_str:
                return (
                    self._fail()
                )  # rule/chain does not exist → creation proceeds
            return self._ok()

        # --- sysctl read (ip_forward) ---
        if len(cmd) >= 3 and cmd[0] == "sysctl" and cmd[1] == "-n":
            return self._ok("0")

        # --- lsmod ---
        if cmd and cmd[0] == "lsmod":
            return self._ok("")

        # --- modprobe --dry-run ---
        if cmd and cmd[0] == "modprobe" and "--dry-run" in cmd:
            if "kvm_intel" in cmd_str or "kvm" in cmd_str:
                return self._ok()
            return self._fail()

        # --- Default: success ---
        return self._ok()

    def _ok(self, stdout: str = "") -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args=[], returncode=0, stdout=stdout, stderr=""
        )

    def _fail(self, stdout: str = "") -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(
            args=[], returncode=1, stdout=stdout, stderr=""
        )


class SmartPopenMock:
    """A stateful subprocess.Popen mock for long-running processes.

    Handles:
    - firecracker --api-sock ...     → pid=1000
    - console_relay ...              → pid=2000, writes pid/sock files
    - nocloud-net HTTP server        → pid=12345
    """

    def __init__(self) -> None:
        self.processes: list[MagicMock] = []

    def __call__(self, cmd: list[str], **kwargs: object) -> MagicMock:
        cmd_str = " ".join(str(c) for c in cmd) if isinstance(cmd, list) else ""
        proc = MagicMock()
        proc.pid = 3000  # default
        proc.poll.return_value = None
        proc.terminate.return_value = None
        proc.wait.return_value = 0

        if "firecracker" in cmd_str and "--api-sock" in cmd_str:
            proc.pid = 1000
            proc.poll.return_value = None
            # Create the API socket file so FirecrackerSpawner's poll
            # loop sees the socket appear. This mirrors what the real
            # Firecracker process does on startup.
            if isinstance(cmd, list):
                for i, arg in enumerate(cmd):
                    if arg == "--api-sock" and i + 1 < len(cmd):
                        sock_path = Path(str(cmd[i + 1]))
                        sock_path.parent.mkdir(parents=True, exist_ok=True)
                        sock_path.touch()
                        break
        elif "console_relay" in cmd_str:
            proc.pid = 2000
            # Write pid file and socket file if specified
            if isinstance(cmd, list):
                pid_file = None
                sock_file = None
                for i, arg in enumerate(cmd):
                    if arg == "--pid-file" and i + 1 < len(cmd):
                        pid_file = Path(str(cmd[i + 1]))
                    if arg == "--socket-path" and i + 1 < len(cmd):
                        sock_file = Path(str(cmd[i + 1]))
                if pid_file:
                    pid_file.parent.mkdir(parents=True, exist_ok=True)
                    pid_file.write_text("2000")
                if sock_file:
                    sock_file.parent.mkdir(parents=True, exist_ok=True)
                    sock_file.touch()
        elif "nocloud" in cmd_str.lower():
            proc.pid = 12345

        self.processes.append(proc)
        return proc


@pytest.fixture
def smart_subprocess() -> SmartSubprocessMock:
    """Create a SmartSubprocessMock for use in tests."""
    return SmartSubprocessMock()


@pytest.fixture
def smart_popen() -> SmartPopenMock:
    """Create a SmartPopenMock for use in tests."""
    return SmartPopenMock()


# ---- Override root conftest fixtures that reference non-existent code ----


@pytest.fixture(autouse=True)
def isolate_config_and_cache(request, monkeypatch: pytest.MonkeyPatch):
    """Ensure tests never write to real config or cache directories.

    Overrides the root conftest version which imports a non-existent
    _load_user_config_json from mvmctl.constants.
    """
    if request.node.get_closest_marker("system"):
        return

    import uuid

    short_id = uuid.uuid4().hex[:8]
    short_base = Path(f"/tmp/mvm_{short_id}")
    short_base.mkdir(parents=True, exist_ok=True)

    paths = make_test_paths(short_base)
    paths.config.mkdir(parents=True, exist_ok=True)
    paths.cache.mkdir(parents=True, exist_ok=True)
    paths.temp.mkdir(parents=True, exist_ok=True)
    monkeypatch.setenv("MVM_CONFIG_DIR", str(paths.config))
    monkeypatch.setenv("MVM_CACHE_DIR", str(paths.cache))
    monkeypatch.setenv("MVM_TEMP_DIR", str(paths.temp))

    # Work around core bug: CloudInitMode is only imported under TYPE_CHECKING
    # in _provisioner.py but used at runtime
    import mvmctl.core.cloudinit._provisioner as _ci_prov
    from mvmctl.models import CloudInitMode

    _ci_prov.CloudInitMode = CloudInitMode

    yield

    shutil.rmtree(short_base, ignore_errors=True)


# ======================================================================
# Host-level mocks
# ======================================================================


@pytest.fixture(autouse=True)
def _mock_root_uid(monkeypatch: pytest.MonkeyPatch) -> None:
    """Make the test process appear as root (uid=0)."""
    monkeypatch.setattr(os, "getuid", lambda: 0)
    monkeypatch.setattr(os, "getgid", lambda: 0)
    monkeypatch.setattr(os, "getegid", lambda: 0)
    monkeypatch.setattr(os, "getgroups", lambda: [0, 1000])
    monkeypatch.setattr(os, "access", lambda _path, _mode: True)


@pytest.fixture(autouse=True)
def _mock_shutil_which(monkeypatch: pytest.MonkeyPatch) -> None:
    """Make shutil.which return a valid path for any binary."""

    def _patched_which(cmd: str | None) -> str | None:
        # Prevent accidentally invoking the slow libguestfs appliance builder
        # in integration tests (it has a 150s timeout and hangs the suite).
        if cmd == "libguestfs-make-fixed-appliance":
            return None
        if cmd:
            return f"/usr/bin/{cmd}"
        return None

    monkeypatch.setattr("shutil.which", _patched_which)


@pytest.fixture(autouse=True)
def _mock_path_exists(monkeypatch: pytest.MonkeyPatch) -> None:
    """Make Path.exists return True for privileged device paths."""

    original_exists = Path.exists

    def _patched_exists(self: Path) -> bool:
        privileged = {
            "/dev/kvm",
            "/usr/sbin/ip",
            "/usr/sbin/iptables",
            "/usr/sbin/iptables-save",
            "/usr/sbin/sysctl",
            "/usr/sbin/modprobe",
            "/sys/class/net/eth0",
        }
        if str(self) in privileged:
            return True
        return original_exists(self)

    monkeypatch.setattr(Path, "exists", _patched_exists)


# ======================================================================
# DB and filesystem seeding
# ======================================================================


def _seed_test_image(repo, image_id: str = "b" * 64) -> None:
    """Seed a minimal test image into the repository."""
    from mvmctl.models.image import ImageItem
    from mvmctl.utils.common import CacheUtils

    repo.upsert(
        ImageItem(
            id=image_id,
            type="ubuntu-24.04",
            name="Ubuntu 24.04",
            arch="x86_64",
            path=str(CacheUtils.get_images_dir() / "ubuntu-24.04.ext4"),
            fs_type="ext4",
            minimum_rootfs_size_mib=10,
            original_size=10485760,
            is_default=True,
            is_present=True,
            pulled_at="2026-01-01T00:00:00+00:00",
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
            fs_uuid="12345678-1234-1234-1234-123456789abc",
        )
    )
    repo.set_default(image_id)


def _seed_test_kernel(repo, kernel_id: str = "a" * 64) -> None:
    """Seed a minimal test kernel into the repository."""
    from mvmctl.models.kernel import KernelItem
    from mvmctl.utils.common import CacheUtils

    repo.upsert(
        KernelItem(
            id=kernel_id,
            name="vmlinux",
            base_name="vmlinux",
            version="6.1.0",
            arch="x86_64",
            type="official",
            path=str(CacheUtils.get_kernels_dir() / "vmlinux"),
            is_default=True,
            is_present=True,
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )
    repo.set_default(kernel_id)


def _seed_test_network(repo, network_id: str = "c" * 64) -> None:
    """Seed a minimal test network into the repository."""
    from mvmctl.models.network import NetworkItem

    repo.upsert(
        NetworkItem(
            id=network_id,
            name="net",
            subnet="10.20.0.0/24",
            bridge="mvm-br0",
            ipv4_gateway="10.20.0.1",
            bridge_active=True,
            nat_enabled=True,
            nat_gateways="eth0",
            is_default=True,
            is_present=True,
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )
    repo.set_default(network_id)


def _seed_test_binary(repo, binary_id: str = "d" * 64) -> None:
    """Seed a minimal test firecracker binary into the repository."""
    from mvmctl.models.binary import BinaryItem
    from mvmctl.utils.common import CacheUtils

    repo.upsert(
        BinaryItem(
            id=binary_id,
            name="firecracker",
            version="1.15.0",
            full_version="v1.15.0",
            ci_version="v1.15",
            path=str(CacheUtils.get_bin_dir() / "firecracker"),
            is_default=True,
            is_present=True,
            created_at="2026-01-01T00:00:00+00:00",
            updated_at="2026-01-01T00:00:00+00:00",
        )
    )
    repo.set_default(binary_id, "1.15.0", "firecracker")


def _ensure_cache_files(cache_dir: Path) -> None:
    """Create minimal fake cache files needed by VM creation."""
    from mvmctl.utils.common import CacheUtils

    # Kernel file
    kernels_dir = CacheUtils.get_kernels_dir()
    kernels_dir.mkdir(parents=True, exist_ok=True)
    (kernels_dir / "vmlinux").write_text("fake kernel")

    # Image file
    images_dir = CacheUtils.get_images_dir()
    images_dir.mkdir(parents=True, exist_ok=True)
    (images_dir / "ubuntu-24.04.ext4").write_text("fake image")

    # Warm image cache (used by materialize_to)
    warm_dir = CacheUtils.get_warm_image_dir()
    warm_dir.mkdir(parents=True, exist_ok=True)
    (warm_dir / f"{'b' * 64}.ext4").write_bytes(b"\x00" * (10 * 1024 * 1024))

    # Binary file
    bin_dir = CacheUtils.get_bin_dir()
    bin_dir.mkdir(parents=True, exist_ok=True)
    fc_file = bin_dir / "firecracker"
    fc_file.write_text("fake firecracker")
    fc_file.chmod(0o755)
    # mvm-provision binary — needed by LoopMountManager.is_binary_available()
    # and HostService.validate_sudoers_binaries()
    prov_file = bin_dir / "mvm-provision"
    prov_file.write_text("fake mvm-provision")
    prov_file.chmod(0o755)


@pytest.fixture(autouse=True)
def _seed_full_test_fixtures(tmp_path: Path) -> None:
    """Seed all assets needed for VM creation — auto-applied for all integration tests.

    This ensures the DB and filesystem have the minimum required assets
    (image, kernel, network, binary + cache files) before every test.
    """
    from mvmctl.core._shared import Database
    from mvmctl.core.binary._repository import BinaryRepository
    from mvmctl.core.image._repository import ImageRepository
    from mvmctl.core.kernel._repository import KernelRepository
    from mvmctl.core.network._repository import NetworkRepository

    db = Database()
    db.migrate()

    _seed_test_image(ImageRepository(db))
    _seed_test_kernel(KernelRepository(db))
    _seed_test_network(NetworkRepository(db))
    _seed_test_binary(BinaryRepository(db))

    from mvmctl.utils.common import CacheUtils

    _ensure_cache_files(CacheUtils.get_cache_dir())
