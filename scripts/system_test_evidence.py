"""Retain named-runner pytest evidence and finalize it without rerunning effects."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import re
import socket
import stat
import subprocess
import sys
import tempfile
import uuid
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any

from system_test_outcomes import (
    MAX_PYTEST_OUTCOME_REPORT_BYTES,
    require_complete_pytest_outcomes,
)

MAX_RECEIPT_BYTES = 512 * 1024
MAX_OUTPUT_BYTES = 8 * 1024 * 1024
NAMED_RUNNER = "mvmctl-030-test"
NODE_REPORT_ENV = "MVM_PYTEST_NODE_REPORT"
HOST_NODES = {
    "tests/system/host/test_host_reboot.py::test_host_reboot_pre",
    "tests/system/host/test_host_reboot.py::test_host_reboot_post",
}
_node_ids: dict[str, str] = {}
_node_phases: dict[str, dict[str, Any]] = {}
_node_collection_errors = 0
_node_deselected = 0


def _nodes(values: Any) -> list[str]:
    if (
        not isinstance(values, list)
        or len(values) > 100_000
        or any(not isinstance(node, str) or len(node) > 4096 for node in values)
        or len(set(values)) != len(values)
        or any(
            re.fullmatch(r"tests/system/[a-zA-Z0-9_/]+\.py::[^\r\n]+", node) is None
            or ".." in node.split("::", 1)[0].split("/")
            for node in values
        )
    ):
        raise RuntimeError("invalid or duplicate system node identities")
    return values


def pytest_sessionstart(session):
    """Opt-in companion to the existing, unchanged strict outcome plugin."""
    del session
    global _node_collection_errors, _node_deselected
    if os.environ.get(NODE_REPORT_ENV):
        _node_ids.clear()
        _node_phases.clear()
        _node_collection_errors = _node_deselected = 0


def pytest_collectreport(report):
    global _node_collection_errors
    if os.environ.get(NODE_REPORT_ENV) and report.failed:
        _node_collection_errors += 1


def pytest_deselected(items):
    global _node_deselected
    if os.environ.get(NODE_REPORT_ENV):
        _node_deselected += len(items)


def pytest_collection_finish(session):
    if not os.environ.get(NODE_REPORT_ENV):
        return
    for item in session.items:
        path = str(item.path)
        marker = "tests/system/"
        if marker not in path or "::" not in item.nodeid:
            raise RuntimeError("node evidence requires registered system test paths")
        normalized = (
            marker + path.rsplit(marker, 1)[1] + "::" + item.nodeid.split("::", 1)[1]
        )
        _node_ids[item.nodeid] = normalized
    _nodes(list(_node_ids.values()))


def pytest_runtest_logreport(report):
    if os.environ.get(NODE_REPORT_ENV):
        _node_phases.setdefault(report.nodeid, {})[report.when] = dict(
            outcome=report.outcome, wasxfail=hasattr(report, "wasxfail")
        )


def pytest_sessionfinish(session, exitstatus):
    configured = os.environ.get(NODE_REPORT_ENV)
    if not configured:
        return
    path = Path(configured)
    _write(
        path.parent,
        path.name,
        _encode(
            dict(
                schema_version=1,
                collect_only=bool(session.config.option.collectonly),
                collected=list(_node_ids.values()),
                phases={
                    _node_ids.get(node, node): phases
                    for node, phases in _node_phases.items()
                },
                collection_errors=_node_collection_errors,
                deselected=_node_deselected,
                exit_status=int(exitstatus),
            )
        ),
        MAX_OUTPUT_BYTES,
    )


def require_node_report(
    raw: bytes, *, native: int, outcome=None, collect_only=False
) -> list[str]:
    report = _json(raw)
    if (
        set(report)
        != {
            "schema_version",
            "collect_only",
            "collected",
            "phases",
            "collection_errors",
            "deselected",
            "exit_status",
        }
        or type(report["schema_version"]) is not int
        or report["schema_version"] != 1
        or report["collect_only"] is not collect_only
        or type(native) is not int
        or native != 0
        or type(report["exit_status"]) is not int
        or report["exit_status"] != native
        or type(report["collection_errors"]) is not int
        or report["collection_errors"] != 0
        or type(report["deselected"]) is not int
        or report["deselected"] != 0
    ):
        raise RuntimeError("node report has incomplete or inconsistent native outcomes")
    nodes = _nodes(report["collected"])
    if not nodes or (outcome is not None and len(nodes) != outcome.collected):
        raise RuntimeError("node collection disagrees with strict outcome count")
    if collect_only:
        if report["phases"] != {}:
            raise RuntimeError("collection receipt contains executed tests")
    elif (
        not isinstance(report["phases"], dict)
        or set(report["phases"]) != set(nodes)
        or any(
            phases
            != {
                phase: dict(outcome="passed", wasxfail=False)
                for phase in ("setup", "call", "teardown")
            }
            for phases in report["phases"].values()
        )
    ):
        raise RuntimeError("node execution is missing or not strictly all-pass")
    return nodes


def _json(raw: bytes) -> dict[str, Any]:
    def unique(pairs):
        result = {}
        folded = set()
        for key, value in pairs:
            if key.casefold() in folded:
                raise RuntimeError("duplicate evidence field")
            folded.add(key.casefold())
            result[key] = value
        return result

    try:
        result = json.loads(raw, object_pairs_hook=unique)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError("malformed evidence JSON") from exc
    if not isinstance(result, dict):
        raise RuntimeError("evidence must be an object")
    return result


def _private_directory(path: Path) -> None:
    info = path.lstat()
    if (
        not path.is_absolute()
        or path.resolve() != path
        or not stat.S_ISDIR(info.st_mode)
        or info.st_uid != os.getuid()
        or stat.S_IMODE(info.st_mode) != 0o700
    ):
        raise RuntimeError("evidence directory must be canonical, private and owned")


def _read(path: Path, limit: int = MAX_RECEIPT_BYTES) -> bytes:
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        before = os.fstat(fd)
        if (
            not stat.S_ISREG(before.st_mode)
            or before.st_nlink != 1
            or before.st_size > limit
        ):
            raise RuntimeError(f"invalid or oversized evidence file: {path.name}")
        chunks = []
        remaining = limit + 1
        while remaining:
            part = os.read(fd, remaining)
            if not part:
                break
            chunks.append(part)
            remaining -= len(part)
        raw = b"".join(chunks)

        def identity(row):
            return (
                row.st_dev,
                row.st_ino,
                row.st_uid,
                row.st_gid,
                row.st_mode,
                row.st_nlink,
                row.st_size,
                row.st_mtime_ns,
                row.st_ctime_ns,
            )

        if (
            len(raw) > limit
            or identity(os.fstat(fd)) != identity(before)
            or identity(path.lstat()) != identity(before)
        ):
            raise RuntimeError(f"evidence file changed or exceeds bound: {path.name}")
        return raw
    finally:
        os.close(fd)


def _write(task: Path, name: str, raw: bytes, limit: int = MAX_RECEIPT_BYTES) -> None:
    if len(raw) > limit:
        raise RuntimeError(f"evidence exceeds bound: {name}")
    if re.fullmatch(r"[a-z][a-z0-9_.-]*", name) is None:
        raise RuntimeError("invalid evidence filename")
    _private_directory(task)
    fd = os.open(
        task / name,
        os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC,
        0o600,
    )
    try:
        with os.fdopen(os.dup(fd), "wb") as output:
            output.write(raw)
            output.flush()
        os.fsync(fd)
    finally:
        os.close(fd)
    parent = os.open(task, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)


def _encode(value: dict[str, Any]) -> bytes:
    return (json.dumps(value, sort_keys=True) + "\n").encode()


def source_identity(root: Path) -> dict[str, Any]:
    """Bind the clean executed Python milestone separately from the Go producer."""

    def git(*args):
        return subprocess.check_output(
            ["git", "-C", str(root), *args], text=True
        ).strip()

    if git("status", "--porcelain"):
        raise RuntimeError("qualified source must be clean")
    paths = git("ls-files", "scripts", "tests").splitlines()
    payload = {
        path: hashlib.sha256(_read(root / path, MAX_OUTPUT_BYTES)).hexdigest()
        for path in paths
        if path.endswith((".py", ".ini"))
    }
    return dict(
        revision=git("rev-parse", "HEAD"),
        tree=git("rev-parse", "HEAD^{tree}"),
        payload=payload,
    )


class DomainEvidence:
    """A single domain's literal process reports and observed normal cleanup."""

    def __init__(
        self,
        task: Path,
        domain: str,
        tier: int,
        files: list[str],
        source_payload: dict[str, str],
    ):
        if re.fullmatch(r"[a-z][a-z0-9_]*", domain) is None or tier not in (1, 2, 3):
            raise RuntimeError("invalid evidence domain")
        self.task, self.domain, self.tier, self.files = task, domain, tier, files
        self.source_payload = source_payload
        self.hashes: dict[str, str] = {}
        self.report_cleanup: bool | None = None
        self.runner_cleanup: bool | None = None

    def _retain(self, suffix: str, raw: bytes, limit: int = MAX_RECEIPT_BYTES) -> None:
        name = f"{self.domain}.{suffix}"
        _write(self.task, name, raw, limit)
        self.hashes[name] = hashlib.sha256(raw).hexdigest()

    def record_pytest(
        self, result: subprocess.CompletedProcess, outcomes: bytes
    ) -> None:
        self.record_native(result)
        self.record_outcomes(outcomes)

    def record_native(self, result: subprocess.CompletedProcess) -> None:
        self._retain("native.json", _encode(dict(returncode=result.returncode)))
        self._retain("stdout", (result.stdout or "").encode(), MAX_OUTPUT_BYTES)
        self._retain("stderr", (result.stderr or "").encode(), MAX_OUTPUT_BYTES)

    def record_outcomes(self, outcomes: bytes) -> None:
        self._retain("outcomes.json", outcomes, MAX_PYTEST_OUTCOME_REPORT_BYTES + 1)

    def record_runtime_source(self, raw: bytes, expected: dict[str, str]) -> None:
        self._retain("runtime-source.json", raw)
        if _json(raw) != expected:
            raise RuntimeError("executed runtime source differs from qualified source")

    def record_junit(self, raw: bytes) -> None:
        self._retain("xml", raw, MAX_OUTPUT_BYTES)

    def record_nodes(self, raw: bytes) -> None:
        self._retain("nodes.json", raw, MAX_OUTPUT_BYTES)

    def record_local_node_reports(self, node_path: Path) -> bytes:
        raw = _read(node_path, MAX_OUTPUT_BYTES)
        self.record_nodes(raw)
        self.record_junit(_read(node_path.parent / "pytest.xml", MAX_OUTPUT_BYTES))
        return raw

    def require_nodes(self, raw: bytes, native: int, outcome) -> list[str]:
        nodes = require_node_report(raw, native=native, outcome=outcome)
        selected = self.files
        if any("::" in item for item in selected):
            if set(nodes) != set(_nodes(selected)):
                raise RuntimeError("executed nodes differ from declared remainder")
        elif {node.split("::", 1)[0] for node in nodes} != set(selected):
            raise RuntimeError("executed nodes differ from selected files")
        return nodes

    def close(self, result: dict[str, Any]) -> None:
        self._retain(
            "result.json",
            _encode(
                dict(
                    schema_version=1,
                    domain=self.domain,
                    tier=self.tier,
                    files=self.files,
                    passed=result["passed"],
                    output=result["output"],
                    report_cleanup=self.report_cleanup,
                    runner_cleanup=self.runner_cleanup,
                )
            ),
        )


class MatrixEvidence:
    """One explicit capture destination; never overwrite or infer prior reports."""

    def __init__(self, task: Path, binding: dict[str, Any]):
        _private_directory(task)
        if any(task.iterdir()):
            raise RuntimeError("record-evidence destination must be empty")
        self.task, self.binding = task, binding
        _write(task, "binding.json", _encode(binding))

    def domain(self, domain: str, tier: int, files: list[str]) -> DomainEvidence:
        return DomainEvidence(
            self.task, domain, tier, files, self.binding["source"]["payload"]
        )

    def close(
        self, results: list[dict[str, Any]], current_source: dict[str, Any]
    ) -> None:
        hashes = {
            path.name: hashlib.sha256(_read(path, MAX_OUTPUT_BYTES)).hexdigest()
            for path in self.task.iterdir()
        }
        _write(
            self.task,
            "terminal.json",
            _encode(
                dict(
                    schema_version=1,
                    source=current_source,
                    completed=True,
                    passed=bool(results)
                    and all(row["passed"] is True for row in results),
                    domains=[row["domain"] for row in results],
                    proof_sha256=hashes,
                )
            ),
        )


def verify_matrix_task(
    task: Path,
    source: dict[str, Any],
    candidate: dict[str, Any],
    registry: dict[str, Any],
) -> dict[str, Any]:
    """Verify literal capture without querying the historical inner controller."""
    _private_directory(task)
    binding = _json(_read(task / "binding.json"))
    terminal = _json(_read(task / "terminal.json"))
    if set(binding) != {
        "schema_version",
        "host",
        "boot_id",
        "source",
        "candidate",
        "controller",
        "registry",
        "selection",
    }:
        raise RuntimeError("invalid matrix binding schema")
    if (
        type(binding["schema_version"]) is not int
        or binding["schema_version"] != 1
        or binding["host"] != NAMED_RUNNER
        or binding["source"] != source
        or binding["candidate"] != candidate
        or binding["registry"] != registry
    ):
        raise RuntimeError("matrix host/source/candidate/registry binding changed")
    if str(uuid.UUID(binding["boot_id"])) != binding["boot_id"]:
        raise RuntimeError("invalid matrix boot identity")
    controller = binding["controller"]
    if (
        set(controller) != {"path", "command", "identity", "sha256"}
        or controller["path"] != "/usr/local/bin/mvm"
        or controller["command"] != ["/usr/local/bin/mvm"]
        or controller["sha256"] != candidate["sha256"]
        or not isinstance(controller["identity"], list)
        or len(controller["identity"]) != 9
        or controller["identity"][2:6] != [0, 0, 0o755, 1]
    ):
        raise RuntimeError(
            "matrix installed controller identity differs from candidate"
        )
    if (
        set(terminal)
        != {
            "schema_version",
            "source",
            "completed",
            "passed",
            "domains",
            "proof_sha256",
        }
        or type(terminal["schema_version"]) is not int
        or terminal["schema_version"] != 1
        or terminal["source"] != source
        or terminal["completed"] is not True
        or terminal["passed"] is not True
    ):
        raise RuntimeError("matrix terminal is incomplete or failed")
    selection = binding["selection"]
    if (
        not isinstance(selection, dict)
        or not selection
        or set(terminal["domains"]) != set(selection)
    ):
        raise RuntimeError("matrix terminal selection differs from binding")
    if len(terminal["domains"]) != len(selection):
        raise RuntimeError("duplicate matrix terminal domain")
    expected_files = {"binding.json"}
    rows = {}
    for domain, selected in selection.items():
        node_selection = isinstance(selected, dict) and "nodes" in selected
        if (
            domain == "host_reboot"
            or domain not in registry
            or {key: value for key, value in selected.items() if key != "nodes"}
            != registry[domain]
            or set(selected)
            != ({"tier", "files", "nodes"} if node_selection else {"tier", "files"})
        ):
            raise RuntimeError("matrix selection is narrowed or unknown")
        tier = selected["tier"]
        files = _nodes(selected["nodes"]) if node_selection else selected["files"]
        if not files or any(
            path.split("::", 1)[0] not in source["payload"] for path in files
        ):
            raise RuntimeError("matrix executed file is missing from source binding")
        if node_selection and any(
            node.split("::", 1)[0] not in selected["files"] for node in files
        ):
            raise RuntimeError("selected node is outside registered domain")
        receipt = _json(_read(task / f"{domain}.result.json"))
        if (
            set(receipt)
            != {
                "schema_version",
                "domain",
                "tier",
                "files",
                "passed",
                "output",
                "report_cleanup",
                "runner_cleanup",
            }
            or type(receipt["schema_version"]) is not int
            or receipt["schema_version"] != 1
            or receipt["domain"] != domain
            or receipt["tier"] != tier
            or receipt["files"] != files
            or receipt["passed"] is not True
            or receipt["report_cleanup"] is not True
            or (tier in (1, 2) and receipt["runner_cleanup"] is not True)
            or (tier == 3 and receipt["runner_cleanup"] is not None)
        ):
            raise RuntimeError(f"matrix domain failed or cleanup incomplete: {domain}")
        native = _json(_read(task / f"{domain}.native.json"))
        if set(native) != {"returncode"} or type(native["returncode"]) is not int:
            raise RuntimeError("invalid matrix native result")
        outcome = require_complete_pytest_outcomes(
            _read(task / f"{domain}.outcomes.json", MAX_PYTEST_OUTCOME_REPORT_BYTES),
            process_returncode=native["returncode"],
        )
        runtime_source = _json(_read(task / f"{domain}.runtime-source.json"))
        if runtime_source != {
            path: digest
            for path, digest in source["payload"].items()
            if path.startswith("tests/system/")
            or (task / f"{domain}.nodes.json").exists()
            and path
            in ("scripts/system_test_evidence.py", "scripts/system_test_outcomes.py")
        }:
            raise RuntimeError(
                "matrix executed runtime source differs from qualified source"
            )
        rows[domain] = outcome.collected
        expected_files.update(
            f"{domain}.{suffix}"
            for suffix in (
                "native.json",
                "outcomes.json",
                "stdout",
                "stderr",
                "runtime-source.json",
                "result.json",
            )
        )
        if (task / f"{domain}.nodes.json").exists():
            nodes = require_node_report(
                _read(task / f"{domain}.nodes.json", MAX_OUTPUT_BYTES),
                native=native["returncode"],
                outcome=outcome,
            )
            if node_selection and set(nodes) != set(files):
                raise RuntimeError("actual nodes differ from selected remainder")
            if not node_selection and {node.split("::", 1)[0] for node in nodes} != set(
                files
            ):
                raise RuntimeError("actual nodes differ from selected files")
            expected_files.add(f"{domain}.nodes.json")
        elif node_selection:
            raise RuntimeError("selected remainder has no actual node receipt")
        if (task / f"{domain}.xml").exists():
            xml = ET.fromstring(_read(task / f"{domain}.xml", MAX_OUTPUT_BYTES))
            cases = list(xml.iter("testcase"))
            if len(cases) != outcome.collected or any(
                list(case.iter(tag))
                for case in cases
                for tag in ("error", "failure", "skipped")
            ):
                raise RuntimeError("matrix JUnit differs from strict outcomes")
            if (task / f"{domain}.nodes.json").exists():
                expected_cases = {_junit_identity(node) for node in nodes}
                actual_cases = [
                    (case.get("classname"), case.get("name")) for case in cases
                ]
                if (
                    len(set(actual_cases)) != len(actual_cases)
                    or set(actual_cases) != expected_cases
                ):
                    raise RuntimeError(
                        "matrix JUnit node identities differ from actual node receipt"
                    )
            expected_files.add(f"{domain}.xml")
        elif (task / f"{domain}.nodes.json").exists():
            raise RuntimeError("actual node receipt has no literal JUnit proof")
    if set(terminal["proof_sha256"]) != expected_files:
        raise RuntimeError("matrix proof files differ from exact selection")
    if set(path.name for path in task.iterdir()) != expected_files | {"terminal.json"}:
        raise RuntimeError("matrix task has missing or unexpected files")
    for name, digest in terminal["proof_sha256"].items():
        if hashlib.sha256(_read(task / name, MAX_OUTPUT_BYTES)).hexdigest() != digest:
            raise RuntimeError(f"matrix proof changed: {name}")
    return dict(
        domains=rows,
        nodes={
            domain: require_node_report(
                _read(task / f"{domain}.nodes.json", MAX_OUTPUT_BYTES), native=0
            )
            for domain in rows
            if (task / f"{domain}.nodes.json").exists()
        },
        binding=binding,
        terminal_sha256=hashlib.sha256(_read(task / "terminal.json")).hexdigest(),
    )


def _junit_identity(node: str) -> tuple[str, str]:
    path, *parts = node.split("::")
    return (
        ".".join([path.removesuffix(".py").replace("/", "."), *parts[:-1]]),
        parts[-1],
    )


def collect_named_runner_nodes(orchestrator, task: Path) -> None:
    """Observe the complete registry once, without running fixtures or lifecycle."""
    if (
        socket.gethostname() != NAMED_RUNNER
        or Path.cwd().resolve() != orchestrator._REPO_ROOT
        or any(
            os.environ.get(key) != NAMED_RUNNER
            for key in ("MVM_TEST_VM", "MVM_TEST_QUALIFICATION_HOST")
        )
    ):
        raise RuntimeError(
            "full registry collection requires the exact named-QA source context"
        )
    orchestrator.verify_release_binary_identity(
        controller_command=orchestrator.MVM_BINARY,
        configured_candidate=orchestrator.MVM_CANDIDATE_CONFIGURED,
        candidate=Path(orchestrator.MVM_CANDIDATE_BINARY),
        requested_version=orchestrator._get_mvm_version(),
    )
    source = source_identity(orchestrator._REPO_ROOT)
    registry = orchestrator._evidence_registry()
    capture = MatrixEvidence(
        task,
        dict(
            schema_version=1,
            host=NAMED_RUNNER,
            boot_id=Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
            source=source,
            candidate=dict(
                sha256=orchestrator._sha256_runner_input(
                    Path(orchestrator.MVM_CANDIDATE_BINARY)
                ),
                version="mvm " + orchestrator._get_mvm_version(),
            ),
            controller=orchestrator._host_reboot_controller(),
            registry=registry,
            selection=registry,
        ),
    )
    with tempfile.TemporaryDirectory(
        prefix="collection-runtime-", dir=task
    ) as runtime_tmp:
        result = subprocess.run(
            [
                sys.executable,
                "-B",
                "-m",
                "pytest",
                "-p",
                "system_test_evidence",
                "-p",
                "no:cacheprovider",
                "--collect-only",
                "-q",
                *[path for row in registry.values() for path in row["files"]],
            ],
            cwd=orchestrator._REPO_ROOT,
            capture_output=True,
            text=True,
            timeout=120,
            env={
                **os.environ,
                "PYTHONDONTWRITEBYTECODE": "1",
                "TMPDIR": runtime_tmp,
                "PYTHONPATH": str(orchestrator._REPO_ROOT / "scripts"),
                NODE_REPORT_ENV: str(task / "collection-nodes.json"),
                "MVM_PYTEST_OUTCOME_REPORT": "",
            },
        )
    _write(task, "collection-native.json", _encode(dict(returncode=result.returncode)))
    _write(task, "collection-stdout", result.stdout.encode(), MAX_OUTPUT_BYTES)
    _write(task, "collection-stderr", result.stderr.encode(), MAX_OUTPUT_BYTES)
    require_node_report(
        _read(task / "collection-nodes.json", MAX_OUTPUT_BYTES),
        native=result.returncode,
        collect_only=True,
    )
    current = source_identity(orchestrator._REPO_ROOT)
    if current != source:
        raise RuntimeError("qualified source changed during full collection")
    capture.close([dict(domain="full_collection", passed=True)], current)


def verify_collection_task(task: Path, source, candidate, registry) -> dict[str, Any]:
    _private_directory(task)
    binding = _json(_read(task / "binding.json"))
    terminal = _json(_read(task / "terminal.json"))
    if (
        set(binding)
        != {
            "schema_version",
            "host",
            "boot_id",
            "source",
            "candidate",
            "controller",
            "registry",
            "selection",
        }
        or set(terminal)
        != {
            "schema_version",
            "source",
            "completed",
            "passed",
            "domains",
            "proof_sha256",
        }
        or type(binding.get("schema_version")) is not int
        or type(terminal.get("schema_version")) is not int
        or terminal["schema_version"] != 1
        or binding.get("source") != source
        or binding.get("candidate") != candidate
        or binding.get("registry") != registry
        or binding.get("selection") != registry
        or binding.get("host") != NAMED_RUNNER
        or binding.get("schema_version") != 1
        or terminal.get("completed") is not True
        or terminal.get("passed") is not True
        or terminal.get("source") != source
        or terminal.get("domains") != ["full_collection"]
    ):
        raise RuntimeError("full collection binding is incomplete or changed")
    # Use the same installed-controller predicate as matrix receipts.
    controller = binding.get("controller", {})
    if (
        set(controller) != {"path", "command", "sha256", "identity"}
        or controller.get("path") != "/usr/local/bin/mvm"
        or controller.get("command") != ["/usr/local/bin/mvm"]
        or controller.get("sha256") != candidate["sha256"]
        or not isinstance(controller.get("identity"), list)
        or len(controller["identity"]) != 9
        or controller["identity"][2:6] != [0, 0, 0o755, 1]
    ):
        raise RuntimeError("full collection controller differs from candidate")
    if str(uuid.UUID(binding["boot_id"])) != binding["boot_id"]:
        raise RuntimeError("invalid full collection boot identity")
    expected = {
        "binding.json",
        "collection-native.json",
        "collection-stdout",
        "collection-stderr",
        "collection-nodes.json",
    }
    if set(terminal.get("proof_sha256", {})) != expected or {
        path.name for path in task.iterdir()
    } != expected | {"terminal.json"}:
        raise RuntimeError("full collection proof files differ")
    for name, digest in terminal["proof_sha256"].items():
        if hashlib.sha256(_read(task / name, MAX_OUTPUT_BYTES)).hexdigest() != digest:
            raise RuntimeError("full collection proof changed")
    native = _json(_read(task / "collection-native.json"))
    if set(native) != {"returncode"}:
        raise RuntimeError("invalid collection native receipt")
    nodes = require_node_report(
        _read(task / "collection-nodes.json", MAX_OUTPUT_BYTES),
        native=native["returncode"],
        collect_only=True,
    )
    owners = {path: domain for domain, row in registry.items() for path in row["files"]}
    if {node.split("::", 1)[0] for node in nodes} != set(owners):
        raise RuntimeError("full collection has missing or unregistered files")
    by_domain = {
        domain: [node for node in nodes if owners[node.split("::", 1)[0]] == domain]
        for domain in registry
    }
    if "host_reboot" in registry and set(by_domain["host_reboot"]) != HOST_NODES:
        raise RuntimeError("full collection host phase nodes changed")
    return dict(
        nodes=by_domain,
        terminal_sha256=hashlib.sha256(_read(task / "terminal.json")).hexdigest(),
    )


# This named carry is one accepted literal journey, not an extensible trust hook.
OFFICIAL_KERNEL_CANDIDATE = dict(
    sha256="57c60574b60180fd26322165079f330b4448291925c745e6ec3da8ae54f189e6",
    version="mvm 0.3.0-rc.openrc-c54bfe88",
)
OFFICIAL_KERNEL_SEALS = {
    "official-eight.json": "b26f751e1f4b1f8cb951b246af3c59643da95c34ea38cee28737ac8fa874bb2d",
    "official-eight.xml": "a31bf3e7c99fcd0798e67e2aa834aff1bd58f72cef7e3454a3f34cac82d34078",
    "terminal.json": "bcb372f1a5350c3dcc7b1d07388e6681eefa639041f9c8d58659e5821e909569",
}
OFFICIAL_KERNEL_INPUTS = {
    **{
        path: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        for path in (
            "tests/__init__.py",
            "tests/system/__init__.py",
            "tests/system/network/__init__.py",
            "tests/system/kernel/__init__.py",
        )
    },
    "tests/system/network/test_network.py": "baf7a941a8be1f80a2a62dd06b1baa22096609cc81502207f74b4eb79770551b",
    "tests/system/conftest.py": "f3ae73656f5edcd8931744cc5144478e7213382822a37b451f105803923d5743",
    "tests/system/pytest.ini": "6a7e8b5e8c12e8f0a532d684dc96c46369164950f28a17e8da044b109822fb16",
    "scripts/system_test_outcomes.py": "efddfad849a3b9d0b867f08acab4c02f07780bfe48361a2ba4e2e1b0be6a7216",
    "tests/system/network/test_policies.py": "805d0debbb303ac75aac1b3bb241645232528d8976b686877dadcbda72a03c1e",
    "tests/system/kernel/test_kernel_import.py": "1c246cc0fe8145e70556f6c9995b19d5f024e8d49977741a04ed72298336874e",
    "tests/system/kernel/test_kernel.py": "dff12aed2c399fa865f4f3f5ceb2f9bfa5b8b61ea965b4e5c0e4430d8e8ffd59",
    "tests/system/host/test_host.py": "8ff215defb4a5ce53bd349de6698b5514fb5d6c3e2a13e509e8a03eb1b52f0c4",
    "scripts/tests/test_official_kernel_pull_sequence.py": "e9c74433211aa08d21a35ea3339949ff97363e7fd93b471026aad444c3fca084",
}


def verify_official_kernel_carry(task: Path, source, candidate) -> list[str]:
    """Read original native/strict/JUnit proof; never relabel it as current runtime."""
    if candidate != OFFICIAL_KERNEL_CANDIDATE:
        raise RuntimeError(
            "official8 carry candidate changed; all eight nodes require new proof or an approved relevant-input carry"
        )
    if any(
        source["payload"].get(path) != digest
        for path, digest in OFFICIAL_KERNEL_INPUTS.items()
    ):
        raise RuntimeError("official8 carry reachable source inputs changed")
    if not task.is_absolute() or task.resolve(strict=True) != task:
        raise RuntimeError("official8 retained task must be canonical")
    reports = task / "runtime-reports"
    if reports.resolve(strict=True) != reports:
        raise RuntimeError("official8 reports directory is not canonical")
    raw = {
        name: _read(reports / name, MAX_OUTPUT_BYTES) for name in OFFICIAL_KERNEL_SEALS
    }
    if any(
        hashlib.sha256(raw[name]).hexdigest() != digest
        for name, digest in OFFICIAL_KERNEL_SEALS.items()
    ):
        raise RuntimeError("official8 original sealed report changed")
    terminal = _json(raw["terminal.json"])
    if (
        terminal.get("passed") is not True
        or terminal.get("failure") is not None
        or terminal.get("case_native_exit") != 0
        or terminal.get("fixture_baseline_preserved") is not True
        or terminal.get("preservation_errors") != []
    ):
        raise RuntimeError("official8 original native or preservation proof failed")
    acquired = terminal.get("acquisition_results", [])
    if (
        len(acquired) != 1
        or acquired[0].get("typed_kernel_retirements") != 8
        or acquired[0].get("typed_terminal_cache_clean") != 1
    ):
        raise RuntimeError("official8 original cleanup proof is incomplete")
    nodes = _nodes(acquired[0]["nodes"])
    outcomes = require_complete_pytest_outcomes(
        raw["official-eight.json"], process_returncode=terminal["case_native_exit"]
    )
    cases = list(ET.fromstring(raw["official-eight.xml"]).iter("testcase"))
    actual = [
        "tests/system/kernel/test_kernel.py::"
        + case.attrib["classname"].removeprefix("tests.system.kernel.test_kernel.")
        + "::"
        + case.attrib["name"]
        for case in cases
    ]
    if (
        len(nodes) != 8
        or outcomes.collected != 8
        or set(actual) != set(nodes)
        or len(actual) != 8
        or any(
            list(case.iter(tag))
            for case in cases
            for tag in ("error", "failure", "skipped")
        )
    ):
        raise RuntimeError(
            "official8 exact JUnit nodes differ from original strict proof"
        )
    return nodes


def accounted_nodes(collection, reports, carried=()) -> set[str]:
    required = {
        node for nodes in collection["nodes"].values() for node in nodes
    } - HOST_NODES
    accounted = set()
    for nodes in [
        list(carried),
        *[nodes for report in reports for nodes in report["nodes"].values()],
    ]:
        if set(nodes) - required:
            raise RuntimeError(
                "proof contains nodes outside full registered collection"
            )
        if accounted.intersection(nodes):
            raise RuntimeError("duplicate executed or carried node coverage")
        accounted.update(nodes)
    if any(set(report["nodes"]) != set(report["domains"]) for report in reports):
        raise RuntimeError(
            "matrix proof lacks actual node receipts; file/count summaries are not node coverage"
        )
    return accounted


def selected_node_remainder(
    manifest: Path, collection, reports, carried, domains: set[str]
) -> dict[str, list[str]]:
    declared = _json(_read(manifest, MAX_OUTPUT_BYTES))
    if (
        set(declared) != {"schema_version", "collection_terminal_sha256", "nodes"}
        or declared["schema_version"] != 1
        or type(declared["schema_version"]) is not int
    ):
        raise RuntimeError("invalid selected-node remainder declaration")
    selected = _nodes(declared["nodes"])
    accounted = accounted_nodes(collection, reports, carried)
    remainder = {
        node
        for domain, nodes in collection["nodes"].items()
        if domain in domains and domain != "host_reboot"
        for node in nodes
    } - accounted
    if (
        declared["collection_terminal_sha256"] != collection["terminal_sha256"]
        or set(selected) != remainder
    ):
        raise RuntimeError(
            "declared selected nodes are not the exact unproved registered remainder"
        )
    if not remainder:
        raise RuntimeError(
            "selected registered remainder is empty; no tests should be replayed"
        )
    return {
        domain: [node for node in collection["nodes"][domain] if node in remainder]
        for domain in domains
        if domain != "host_reboot" and set(collection["nodes"][domain]) & remainder
    }


def finalize_named_runner_release_evidence(
    orchestrator,
    matrix_tasks: list[Path],
    host_reboot_task: Path,
    *,
    collection_task: Path | None = None,
    official_kernel_carry: Path | None = None,
) -> dict[str, Any]:
    """Join exact inner reports with the unchanged outer reboot admission reader."""

    class RegistryParser(argparse.ArgumentParser):
        def error(self, message):
            raise RuntimeError(message)

    orchestrator._validate_system_test_registry(RegistryParser())
    source = source_identity(orchestrator._REPO_ROOT)
    candidate = dict(
        sha256=orchestrator._sha256_runner_input(
            Path(orchestrator.MVM_CANDIDATE_BINARY)
        ),
        version="mvm " + orchestrator._get_mvm_version(),
    )
    registry = orchestrator._evidence_registry()
    if collection_task is None:
        raise RuntimeError(
            "final release requires retained current full-registry node collection"
        )
    collection = verify_collection_task(collection_task, source, candidate, registry)
    verified = [
        verify_matrix_task(task, source, candidate, registry) for task in matrix_tasks
    ]
    carried = (
        verify_official_kernel_carry(official_kernel_carry, source, candidate)
        if official_kernel_carry
        else []
    )
    accounted = accounted_nodes(collection, verified, carried)
    missing = sorted(
        {node for nodes in collection["nodes"].values() for node in nodes}
        - HOST_NODES
        - accounted
    )
    if missing:
        raise RuntimeError("matrix node coverage is incomplete: " + ", ".join(missing))
    domains = {
        domain: len(nodes)
        for domain, nodes in collection["nodes"].items()
        if domain != "host_reboot"
    }
    reboot = orchestrator.require_completed_host_reboot(host_reboot_task)
    if source_identity(orchestrator._REPO_ROOT) != source:
        raise RuntimeError("qualified source changed during finalization")
    return dict(
        schema_version=1,
        passed=True,
        verdict="PASS",
        source=source,
        candidate=candidate,
        domains=domains,
        executed_nodes=sorted(accounted - set(carried)),
        carried_nodes=carried,
        collection_terminal_sha256=collection["terminal_sha256"],
        matrix_terminal_sha256=[report["terminal_sha256"] for report in verified],
        host_reboot=reboot,
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Read-only named-runner release evidence finalization"
    )
    parser.add_argument("--matrix-task", type=Path, action="append", required=True)
    parser.add_argument("--host-reboot-task", type=Path, required=True)
    parser.add_argument("--qualified-source", type=Path, required=True)
    parser.add_argument("--collection-task", type=Path, required=True)
    parser.add_argument("--official-kernel-carry", type=Path)
    args = parser.parse_args()
    try:
        root = args.qualified_source.resolve(strict=True)
        if root != args.qualified_source or not root.is_absolute():
            raise RuntimeError(
                "qualified source must be an absolute canonical directory"
            )
        if root != Path(__file__).resolve().parent.parent:
            raise RuntimeError("run the finalizer from the exact qualified source")
        spec = importlib.util.spec_from_file_location(
            "mvmctl_evidence_orchestrator", root / "scripts/run-system-tests.py"
        )
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        result = finalize_named_runner_release_evidence(
            module,
            args.matrix_task,
            args.host_reboot_task,
            collection_task=args.collection_task,
            official_kernel_carry=args.official_kernel_carry,
        )
    except Exception as exc:
        print(
            json.dumps(
                dict(
                    schema_version=1,
                    passed=False,
                    verdict="INCOMPLETE",
                    reason=str(exc),
                ),
                sort_keys=True,
            )
        )
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
