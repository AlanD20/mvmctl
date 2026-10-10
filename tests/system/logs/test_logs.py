"""Installed-CLI log retrieval with bounded, function-owned Alpine guests."""

from __future__ import annotations

import base64
import json
import os
import re
import shlex
import stat
import subprocess
import tempfile
import time
import uuid
from collections.abc import Callable, Generator
from pathlib import Path
from typing import Any

import pytest

from tests.system.conftest import _json_success_data, _run_mvm, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.requires_kvm,
    pytest.mark.slow,
    pytest.mark.domain_logs,
]

_OS_LINE = re.compile(r"\[[^\]\n]+:(?:DEBUG|INFO|WARN|ERROR):[^\]\n]+\]")
_BOOT_ARGS = (
    "console=ttyS0 reboot=k panic=1 net.ifnames=0 rw rootwait loglevel=7 "
    "no_timer_check clocksource=kvm-clock systemd.show_status=false"
)


def _data(runner_vm: str, *args: str, kind: type = dict, timeout: int = 60) -> Any:
    return _json_success_data(
        _run_mvm(runner_vm, *args, "--json", timeout=timeout).stdout, kind
    )


def _inventory(runner_vm: str, domain: str) -> list[dict[str, Any]]:
    rows = _data(runner_vm, domain, "ls", kind=list)
    if domain == "kernel":
        # Listing missing recorded kernels refreshes this one observational field.
        rows = [
            {key: value for key, value in row.items() if key != "updated_at"}
            for row in rows
        ]
    return sorted(rows, key=lambda row: json.dumps(row, sort_keys=True))


class _OwnedLogsCase:
    """Adopt partial publication only for this case's exact attempted names."""

    def __init__(self, runner_vm: str) -> None:
        self.runner_vm = runner_vm
        self.before = {
            domain: _inventory(runner_vm, domain)
            for domain in ("vm", "network", "key", "volume", "image", "kernel", "bin")
        }
        self.config = _data(runner_vm, "config", "ls")
        self.attempted: dict[str, dict[str, str | None]] = {
            domain: {} for domain in ("vm", "network", "key")
        }
        self.receipts: dict[str, dict[str, dict[str, Any]]] = {
            domain: {} for domain in self.attempted
        }
        self.key_backing: dict[str, dict[Path, tuple[int, int]]] = {}

    def attempt(self, domain: str) -> str:
        name = f"logs-{domain}-{uuid.uuid4().hex[:16]}"
        assert all(row["name"] != name for row in self.before[domain])
        assert all(row["name"] != name for row in _inventory(self.runner_vm, domain))
        self.attempted[domain][name] = None
        return name

    def _identity(self, domain: str, resource: dict[str, Any]) -> dict[str, Any]:
        resource_id = resource["id"]
        assert isinstance(resource_id, str), resource
        fields = ("id", "name", "created_at")
        if domain == "vm":
            assert re.fullmatch(r"[0-9a-f]{32}", resource_id), resource
        elif domain == "network":
            assert re.fullmatch(r"[0-9a-f]{64}", resource_id), resource
        elif domain == "key":
            assert resource_id == resource["fingerprint"], resource
            assert re.fullmatch(r"SHA256:[A-Za-z0-9+/]{43}", resource_id), resource
            digest = base64.b64decode(resource_id[7:] + "=", validate=True)
            assert len(digest) == 32
            assert base64.b64encode(digest).decode().rstrip("=") == resource_id[7:]
            assert resource["algorithm"] == "ssh-ed25519", resource
            fields += (
                "fingerprint",
                "algorithm",
                "public_key_path",
                "private_key_path",
            )
        else:
            raise AssertionError(f"unsupported owned domain: {domain}")
        assert isinstance(resource["created_at"], str) and resource["created_at"], (
            resource
        )
        return {field: resource[field] for field in fields}

    def _key_files(self, name: str, resource: dict[str, Any]) -> None:
        backing = {}
        baseline_paths = {
            raw
            for row in self.before["key"]
            for raw in (row["public_key_path"], row["private_key_path"])
            if raw is not None
        }
        for raw in (resource["public_key_path"], resource["private_key_path"]):
            assert isinstance(raw, str) and raw and raw not in baseline_paths, resource
            path = Path(raw)
            metadata = path.lstat()
            assert stat.S_ISREG(metadata.st_mode) and metadata.st_uid == os.geteuid(), (
                path
            )
            backing[path] = metadata.st_dev, metadata.st_ino
        assert len(backing) == 2, resource
        remembered = self.key_backing.get(name)
        assert remembered is None or remembered == backing, (name, remembered, backing)
        self.key_backing[name] = backing

    def remember_receipt(
        self, domain: str, name: str, resource: dict[str, Any]
    ) -> None:
        assert name in self.attempted[domain], (domain, name)
        identity = self._identity(domain, resource)
        assert identity["name"] == name, (name, identity)
        assert all(row["id"] != identity["id"] for row in self.before[domain]), identity
        remembered = self.attempted[domain][name]
        assert remembered is None or remembered == identity["id"], (
            remembered,
            identity,
        )
        previous = self.receipts[domain].get(name)
        assert previous is None or previous == identity, (previous, identity)
        self.attempted[domain][name] = identity["id"]
        self.receipts[domain][name] = identity
        if domain == "key":
            self._key_files(name, resource)

    def adopt(self, domain: str, name: str) -> dict[str, Any] | None:
        assert name in self.attempted[domain], (domain, name)
        rows = [
            row for row in _inventory(self.runner_vm, domain) if row["name"] == name
        ]
        assert len(rows) <= 1, rows
        if not rows:
            return None
        resource = rows[0]
        resource_id = resource["id"]
        identity = self._identity(domain, resource)
        assert all(row["id"] != resource_id for row in self.before[domain]), resource
        remembered = self.attempted[domain][name]
        assert remembered is None or remembered == resource_id, (
            domain,
            name,
            remembered,
            resource,
        )
        receipt = self.receipts[domain].get(name)
        assert receipt is None or receipt == identity, (domain, name, receipt, identity)
        # A failed create may publish a record without returning a success receipt.
        # Only the exact attempted, baseline-absent name can enter this branch.
        self.receipts[domain].setdefault(name, identity)
        self.attempted[domain][name] = resource_id
        if domain == "key":
            self._key_files(name, resource)
        return resource

    def cleanup(self) -> None:
        failures = []
        for domain in ("vm", "key", "network"):
            for name in self.attempted[domain]:
                try:
                    resource = self.adopt(domain, name)
                    if resource is not None:
                        flags = ("--force",) if domain in ("vm", "network") else ()
                        _run_mvm(
                            self.runner_vm,
                            domain,
                            "rm",
                            resource["id"],
                            *flags,
                            timeout=120 if domain == "vm" else 60,
                        )
                    resource_id = self.attempted[domain][name]
                    assert all(
                        row["name"] != name and row["id"] != resource_id
                        for row in _inventory(self.runner_vm, domain)
                    ), (domain, name, resource_id)
                    if domain == "key":
                        for path in self.key_backing.get(name, {}):
                            assert not os.path.lexists(path), (
                                "key backing retained",
                                path,
                            )
                except Exception as error:
                    failures.append(f"{domain}/{name}: {error}")
        for domain, baseline in self.before.items():
            try:
                assert _inventory(self.runner_vm, domain) == baseline, domain
            except Exception as error:
                failures.append(f"baseline {domain}: {error}")
        try:
            assert _data(self.runner_vm, "config", "ls") == self.config, (
                "configuration changed"
            )
        except Exception as error:
            failures.append(f"baseline config: {error}")
        assert not failures, "\n".join(failures)


@pytest.fixture(autouse=True)
def logs_case(runner_vm: str) -> Generator[_OwnedLogsCase, None, None]:
    case = _OwnedLogsCase(runner_vm)
    try:
        yield case
    finally:
        case.cleanup()


def _require_assets(runner_vm: str) -> dict[str, Any]:
    assert os.geteuid() != 0, (
        "exercise the installed CLI as the non-root runner operator"
    )
    mirror = os.environ.get("MVM_ASSET_MIRROR")
    assert mirror and Path(mirror).expanduser().is_dir(), (
        "preseeded asset mirror required"
    )
    images = [
        row
        for row in _inventory(runner_vm, "image")
        if row["type"] == "alpine" and row["version"] == "3.23" and row["is_present"]
    ]
    assert len(images) == 1, "preseed alpine:3.23; logs tests never pull assets"
    assert 0 < images[0]["minimum_rootfs_size_mib"] <= 512, images
    kernels = [
        row
        for row in _inventory(runner_vm, "kernel")
        if row["is_default"] and row["is_present"]
    ]
    assert len(kernels) == 1, "preseed one present default kernel"
    binaries = _inventory(runner_vm, "bin")
    selected = {}
    for kind in ("firecracker", "jailer"):
        matches = [
            row
            for row in binaries
            if row["type"] == kind
            and row["version"] == "1.17.0"
            and row["is_default"]
            and row["is_present"]
        ]
        assert len(matches) == 1, f"preseed the default {kind} 1.17.0 pair"
        selected[kind] = matches[0]["id"]
    assert _data(runner_vm, "host", "status")["resources"]["nftables_available"] is True
    assert all(
        row["status"] == "stopped" and row["pid"] == 0
        for row in _inventory(runner_vm, "vm")
    ), "logs tests allow at most one live nested guest"
    return {"image": images[0]["id"], "kernel": kernels[0]["id"], **selected}


def _state(
    runner_vm: str, vm: dict[str, Any], status: str = "running"
) -> dict[str, Any]:
    rows = [row for row in _inventory(runner_vm, "vm") if row["id"] == vm["id"]]
    assert len(rows) == 1 and rows[0]["name"] == vm["name"], rows
    inspected = _data(runner_vm, "vm", "inspect", vm["id"])
    assert inspected["vm"]["id"] == vm["id"]
    assert inspected["vm"]["status"] == rows[0]["status"] == status
    return inspected


def _exec(runner_vm: str, vm: dict[str, Any], command: str, *, check: bool = True):
    return _run_mvm(
        runner_vm,
        "exec",
        vm["id"],
        "--user",
        "root",
        "--timeout",
        "2",
        "--",
        command,
        check=check,
        timeout=10,
    )


def _logs(runner_vm: str, selector: str, *flags: str) -> str:
    return _run_mvm(runner_vm, "logs", selector, "--follow=false", *flags).stdout


def _logs_bytes(runner_vm: str, selector: str, *flags: str) -> bytes:
    """Capture physical LF records without text-mode serial CR normalization."""
    assert os.environ.get("MVM_TEST_VM") == runner_vm
    result = subprocess.run(
        ["/usr/local/bin/mvm", "logs", selector, "--follow=false", *flags],
        capture_output=True,
        text=False,
        timeout=90,
        env={**os.environ, "NO_COLOR": "1"},
    )
    assert result.returncode == 0, (result.returncode, result.stdout, result.stderr)
    assert result.stderr == b"", result.stderr
    assert result.stdout.endswith(b"\n"), result.stdout
    return result.stdout


def _assert_boot(output: str) -> None:
    # Alpine image optimization disables serial getty, so login branding is incidental.
    assert "Linux version " in output, output


def _assert_os(output: str) -> None:
    # The admitted Firecracker logger enables both severity and source origin.
    assert _OS_LINE.search(output), output


@pytest.fixture
def logs_vm(runner_vm: str, logs_case: _OwnedLogsCase) -> dict[str, Any]:
    assets = _require_assets(runner_vm)
    net_name = logs_case.attempt("network")
    network = _data(
        runner_vm,
        "network",
        "create",
        net_name,
        "--subnet",
        _unique_subnet(net_name),
        "--no-nat",
    )
    logs_case.remember_receipt("network", net_name, network)
    assert logs_case.adopt("network", net_name)["id"] == network["id"]
    key_name = logs_case.attempt("key")
    created_key = _data(runner_vm, "key", "create", key_name, "--algorithm", "ed25519")
    logs_case.remember_receipt("key", key_name, created_key)
    key = logs_case.adopt("key", key_name)
    assert key is not None and key["is_present"] and not key["is_default"]
    vm_name = logs_case.attempt("vm")
    created = _data(
        runner_vm,
        "vm",
        "create",
        vm_name,
        "--image",
        assets["image"],
        "--kernel",
        assets["kernel"],
        "--network",
        network["id"],
        "--ssh-key",
        key["id"],
        "--vcpu",
        "1",
        "--mem",
        "256M",
        "--disk-size",
        "512M",
        "--user",
        "root",
        "--cloud-init-mode",
        "inject",
        "--skip-deblob",
        "--no-pci",
        "--console",
        "--enable-logging",
        "--boot-args",
        _BOOT_ARGS,
        kind=list,
        timeout=180,
    )
    assert len(created) == 1 and created[0]["name"] == vm_name, created
    logs_case.remember_receipt("vm", vm_name, created[0])
    vm = logs_case.adopt("vm", vm_name)
    assert vm is not None and created[0]["id"] == vm["id"]
    assert (vm["vcpu_count"], vm["mem_size_mib"], vm["disk_size_mib"]) == (1, 256, 512)
    assert (
        vm["binary_id"] == assets["firecracker"]
        and vm["jailer_binary_id"] == assets["jailer"]
    )
    assert vm["ipv4"] and vm["network_id"] == network["id"]
    inspected = _state(runner_vm, vm)
    assert inspected["vm"]["enable_console"] and inspected["vm"]["enable_logging"]
    assert inspected["console"]["relay_running"] is True
    deadline = time.monotonic() + 30
    last = None
    while time.monotonic() < deadline:
        last = _exec(runner_vm, vm, "id -un", check=False)
        if last.returncode == 0 and last.stdout.strip() == "root":
            break
        time.sleep(0.5)
    else:
        pytest.fail(f"guest agent not ready: {last}")
    release = _exec(runner_vm, vm, "cat /etc/os-release").stdout
    release_fields: dict[str, str] = {}
    for line in release.splitlines():
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        assert separator and re.fullmatch(r"[A-Z][A-Z0-9_]*", key), line
        assert key not in release_fields, (key, release)
        parsed = shlex.split(value)
        assert len(parsed) == 1, (line, release)
        release_fields[key] = parsed[0]
    assert release_fields.get("ID") == "alpine", release
    assert re.fullmatch(r"3\.23\.\d+", release_fields.get("VERSION_ID", "")), release
    vm["marker"] = f"logs-serial-{uuid.uuid4().hex}"
    _exec(runner_vm, vm, f"printf '%s\\n' {shlex.quote(vm['marker'])} > /dev/ttyS0")
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        boot = _logs(runner_vm, vm["id"], "--lines", "10000")
        if vm["marker"] in boot and "Linux version " in boot:
            _assert_boot(boot)
            _assert_os(_logs(runner_vm, vm["id"], "--os", "--lines", "10000"))
            return vm
        time.sleep(0.3)
    pytest.fail(f"guest serial marker and kernel boot output were not ready: {boot}")


def _follow(
    runner_vm: str,
    vm: dict[str, Any],
    action: Callable[[], None],
    *,
    os_log: bool = False,
) -> str:
    flags = ["--os"] if os_log else []
    leaf = "log_path" if os_log else "serial_output_path"
    log_path = _state(runner_vm, vm)["filesystem"][leaf]
    assert log_path, (leaf, vm)
    log_identity = Path(log_path).stat()
    with (
        tempfile.TemporaryFile(mode="w+", encoding="utf-8") as stdout,
        tempfile.TemporaryFile(mode="w+", encoding="utf-8") as stderr,
    ):
        process = subprocess.Popen(
            ["/usr/local/bin/mvm", "logs", vm["id"], *flags, "--follow"],
            stdout=stdout,
            stderr=stderr,
            text=True,
            env={**os.environ, "NO_COLOR": "1"},
        )
        try:
            # Wait for the child to open the exact public inspect log target.
            # A fixed delay can emit the test data before follow reaches EOF.
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline and process.poll() is None:
                ready = False
                for descriptor in Path(f"/proc/{process.pid}/fd").iterdir():
                    try:
                        opened = descriptor.stat()
                        if (opened.st_dev, opened.st_ino) == (
                            log_identity.st_dev,
                            log_identity.st_ino,
                        ):
                            position = Path(
                                f"/proc/{process.pid}/fdinfo/{descriptor.name}"
                            ).read_text()
                            offset = re.search(
                                r"^pos:\s+(\d+)$", position, re.MULTILINE
                            )
                            ready = (
                                offset is not None
                                and int(offset[1]) == Path(log_path).stat().st_size
                            )
                    except FileNotFoundError:
                        continue
                    if ready:
                        break
                if ready:
                    break
                time.sleep(0.1)
            else:
                stderr.seek(0)
                pytest.fail(
                    f"follow did not open its log: rc={process.poll()} stderr={stderr.read()}"
                )
            action()
            time.sleep(1)
            assert process.poll() is None, (
                f"follow exited early with status {process.returncode}"
            )
        finally:
            # Terminate only this child, preserving a pre-existing error exit.
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                pytest.fail("logs --follow did not exit after SIGTERM")
        stdout.seek(0)
        stderr.seek(0)
        output, errors = stdout.read(), stderr.read()
        assert process.returncode == 0, (process.returncode, output, errors)
        assert not errors.strip(), errors
        _state(runner_vm, vm)
        return output


class TestLogsBasic:
    def test_logs_basic_output(self, runner_vm, logs_vm):
        """Default output includes serial data emitted by this running guest."""
        result = _logs(runner_vm, logs_vm["name"])
        assert logs_vm["marker"] in result, result
        _state(runner_vm, logs_vm)

    def test_logs_os_flag(self, runner_vm, logs_vm):
        """--os selects Firecracker severity/source logs rather than serial output."""
        output = _logs(runner_vm, logs_vm["name"], "--os")
        _assert_os(output)
        assert logs_vm["marker"] not in output

    def test_logs_lines_flag(self, runner_vm, logs_vm):
        """--lines 5 returns exactly the retained serial tail."""
        _run_mvm(runner_vm, "vm", "stop", logs_vm["id"], "--force")
        _state(runner_vm, logs_vm, "stopped")
        output = _logs_bytes(runner_vm, logs_vm["id"], "--lines", "10000")
        _assert_boot(output.decode("utf-8"))
        assert b"\r" in output, output
        full = output[:-1].split(b"\n")
        assert len(full) > 5
        assert (
            _logs_bytes(runner_vm, logs_vm["name"], "--lines", "5")
            == b"\n".join(full[-5:]) + b"\n"
        )


class TestVMLogs:
    def test_logs_boot_output(self, runner_vm, logs_vm):
        """The serial log contains this guest's kernel boot output and unique marker."""
        output = _logs(runner_vm, logs_vm["name"], "--lines", "10000")
        _assert_boot(output)
        assert logs_vm["marker"] in output

    def test_logs_os_output(self, runner_vm, logs_vm):
        _assert_os(_logs(runner_vm, logs_vm["name"], "--os", "--lines", "10000"))

    def test_logs_follow_runs(self, runner_vm, logs_vm):
        """Follow streams newly emitted guest serial data and exits cleanly on SIGTERM."""
        marker = f"logs-follow-{uuid.uuid4().hex}"
        output = _follow(
            runner_vm,
            logs_vm,
            lambda: _exec(
                runner_vm,
                logs_vm,
                f"for n in 1 2 3 4 5; do printf '%s\\n' {shlex.quote(marker)} > /dev/ttyS0; sleep 0.2; done",
            ),
        )
        assert marker in output, output
        assert logs_vm["marker"] not in output, (
            "follow replayed historical serial lines"
        )

    def test_logs_os_follow(self, runner_vm, logs_vm):
        """Follow observes Firecracker output from real pause/resume operations."""

        def pause_resume() -> None:
            _run_mvm(runner_vm, "vm", "pause", logs_vm["id"])
            _state(runner_vm, logs_vm, "paused")
            _run_mvm(runner_vm, "vm", "resume", logs_vm["id"])
            _state(runner_vm, logs_vm)

        output = _follow(runner_vm, logs_vm, pause_resume, os_log=True)
        _assert_os(output)
        assert logs_vm["marker"] not in output

    def test_logs_on_nonexistent_vm_fails(self, runner_vm, logs_case):
        name = f"logs-absent-{uuid.uuid4().hex}"
        assert all(row["name"] != name for row in _inventory(runner_vm, "vm"))
        result = _run_mvm(runner_vm, "logs", name, "--follow=false", check=False)
        assert result.returncode == 1, (result.stdout, result.stderr)
        assert f"VM not found: {name}" in result.stderr, result.stderr
        assert not result.stdout.strip(), result.stdout

    def test_logs_with_lines_limit(self, runner_vm, logs_vm):
        """Both 5- and 50-line requests return the exact frozen serial tail."""
        _run_mvm(runner_vm, "vm", "stop", logs_vm["id"], "--force")
        _state(runner_vm, logs_vm, "stopped")
        output = _logs_bytes(runner_vm, logs_vm["id"], "--lines", "10000")
        _assert_boot(output.decode("utf-8"))
        assert b"\r" in output, output
        full = output[:-1].split(b"\n")
        assert len(full) > 50, len(full)
        for count in (5, 50):
            tail = _logs_bytes(runner_vm, logs_vm["name"], "--lines", str(count))
            expected = b"\n".join(full[-count:]) + b"\n"
            assert tail == expected, (count, tail, expected)

    def test_logs_by_ip_remain_available_for_stopped_vm(self, runner_vm, logs_vm):
        """A stopped VM retains its IP selector and the complete historical logs."""
        ip = logs_vm["ipv4"]
        _run_mvm(runner_vm, "vm", "stop", logs_vm["id"], "--force")
        inspected = _state(runner_vm, logs_vm, "stopped")
        assert inspected["networking"]["ipv4"] == ip
        assert inspected["console"]["relay_running"] is False
        assert inspected["console"]["relay_pid"] is None
        assert all(
            row["id"] != logs_vm["id"]
            for row in _data(runner_vm, "vm", "ps", kind=list)
        )
        by_id = _logs(runner_vm, logs_vm["id"], "--lines", "10000")
        by_ip = _logs(runner_vm, ip, "--lines", "10000")
        assert by_ip == by_id
        _assert_boot(by_ip)
        assert logs_vm["marker"] in by_ip
        os_by_ip = _logs(runner_vm, ip, "--os", "--lines", "10000")
        assert os_by_ip == _logs(runner_vm, logs_vm["id"], "--os", "--lines", "10000")
        _assert_os(os_by_ip)
        _state(runner_vm, logs_vm, "stopped")


class TestVMLogsByIdentifier:
    def test_logs_by_ip(self, runner_vm, logs_vm):
        """Running VM lookup by IP returns the same real serial log as its full ID."""
        ip = logs_vm["ipv4"]
        by_ip = _logs(runner_vm, ip, "--lines", "10000")
        _assert_boot(by_ip)
        assert logs_vm["marker"] in by_ip
        assert by_ip == _logs(runner_vm, logs_vm["id"], "--lines", "10000")
        _state(runner_vm, logs_vm)
