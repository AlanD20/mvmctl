"""Retain named-runner pytest evidence and finalize it without rerunning effects."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import os
import re
import stat
import subprocess
import sys
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
        if (
            domain == "host_reboot"
            or domain not in registry
            or selected != registry[domain]
        ):
            raise RuntimeError("matrix selection is narrowed or unknown")
        tier, files = selected["tier"], selected["files"]
        if any(path not in source["payload"] for path in files):
            raise RuntimeError("matrix executed file is missing from source binding")
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
        if (task / f"{domain}.xml").exists():
            xml = ET.fromstring(_read(task / f"{domain}.xml", MAX_OUTPUT_BYTES))
            cases = list(xml.iter("testcase"))
            if len(cases) != outcome.collected or any(
                list(case.iter(tag))
                for case in cases
                for tag in ("error", "failure", "skipped")
            ):
                raise RuntimeError("matrix JUnit differs from strict outcomes")
            expected_files.add(f"{domain}.xml")
    if set(terminal["proof_sha256"]) != expected_files:
        raise RuntimeError("matrix proof files differ from exact selection")
    if set(path.name for path in task.iterdir()) != expected_files | {"terminal.json"}:
        raise RuntimeError("matrix task has missing or unexpected files")
    for name, digest in terminal["proof_sha256"].items():
        if hashlib.sha256(_read(task / name, MAX_OUTPUT_BYTES)).hexdigest() != digest:
            raise RuntimeError(f"matrix proof changed: {name}")
    return dict(
        domains=rows,
        binding=binding,
        terminal_sha256=hashlib.sha256(_read(task / "terminal.json")).hexdigest(),
    )


def finalize_named_runner_release_evidence(
    orchestrator, matrix_tasks: list[Path], host_reboot_task: Path
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
    verified = [
        verify_matrix_task(task, source, candidate, registry) for task in matrix_tasks
    ]
    domains = {}
    for report in verified:
        for domain, collected in report["domains"].items():
            if domain in domains:
                raise RuntimeError(f"duplicate matrix coverage: {domain}")
            domains[domain] = collected
    if set(domains) != set(registry) - {"host_reboot"}:
        raise RuntimeError("matrix coverage is incomplete")
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
            module, args.matrix_task, args.host_reboot_task
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
