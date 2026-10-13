from __future__ import annotations

import importlib.util
import json
import os
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path
from types import ModuleType

import pytest


REPORT_ENV = "MVM_PYTEST_OUTCOME_REPORT"


def _valid_outcome_values(**changes: object) -> dict[str, object]:
    values: dict[str, object] = {
        "schema_version": 1,
        "collected": 1,
        "passed": 1,
        "failed": 0,
        "errors": 0,
        "collection_errors": 0,
        "deselected": 0,
        "skipped": 0,
        "xfailed": 0,
        "xpassed": 0,
        "exit_status": 0,
    }
    values.update(changes)
    return values


@pytest.fixture
def outcomes() -> ModuleType:
    script = Path(__file__).parents[1] / "system_test_outcomes.py"
    module_name = "mvmctl_system_test_outcomes"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


@pytest.fixture
def vm_dependency_assets(
    monkeypatch: pytest.MonkeyPatch,
) -> tuple[ModuleType, list[str]]:
    from tests.system import conftest

    events: list[str] = []
    for helper in ("_ensure_binary", "_ensure_kernel", "_ensure_image"):
        monkeypatch.setattr(
            conftest, helper,
            lambda _runner, name=helper: events.append(name),
        )
    return conftest, events


@pytest.mark.parametrize("override", [None, False, True], ids=["unset", "false", "true"])
def test_ensure_vm_deps_preserves_false_intent_and_disables_true_before_assets(
    vm_dependency_assets: tuple[ModuleType, list[str]],
    monkeypatch: pytest.MonkeyPatch,
    override: bool | None,
) -> None:
    conftest, events = vm_dependency_assets
    setting = {"default": False, "override": override}
    baseline = setting.copy()

    def native(cmd: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        assert cmd[0] == "/usr/local/bin/mvm"
        if cmd[1:] == ["config", "get", "settings", "guestfs_enabled", "--json"]:
            events.append("config:get")
            effective = setting["default"] if setting["override"] is None else setting["override"]
            output = json.dumps({
                "schema_version": 1, "status": "success", "data": effective, "error": None,
            })
        else:
            assert cmd[1:] == ["config", "set", "settings", "guestfs_enabled", "false"]
            events.append("config:set")
            setting["override"] = False
            output = ""
        return subprocess.CompletedProcess(cmd, 0, stdout=output, stderr="")

    monkeypatch.setattr(conftest.subprocess, "run", native)

    conftest.ensure_vm_deps("approved-qa-runner")

    expected = ["config:get"]
    if override is True:
        expected.append("config:set")
        assert setting == {**baseline, "override": False}
    else:
        assert setting == baseline
    assert events == expected + ["_ensure_binary", "_ensure_kernel", "_ensure_image"]


@pytest.mark.parametrize("output", [
    "{",
    *(json.dumps({"schema_version": 1, "status": "success", "data": data, "error": None})
      for data in ("false", 0, None)),
], ids=["malformed-json", "string", "integer", "null"])
def test_ensure_vm_deps_rejects_invalid_config_before_effects(
    vm_dependency_assets: tuple[ModuleType, list[str]],
    monkeypatch: pytest.MonkeyPatch,
    output: str,
) -> None:
    conftest, events = vm_dependency_assets

    def native(cmd: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        if cmd != [
            "/usr/local/bin/mvm", "config", "get", "settings", "guestfs_enabled", "--json",
        ]:
            raise RuntimeError(f"Unexpected command after invalid config: {cmd}")
        events.append("config:get")
        return subprocess.CompletedProcess(cmd, 0, stdout=output, stderr="")

    monkeypatch.setattr(conftest.subprocess, "run", native)

    with pytest.raises((json.JSONDecodeError, AssertionError)):
        conftest.ensure_vm_deps("approved-qa-runner")

    assert events == ["config:get"]


def _run_outcome_case(
    tmp_path: Path,
    source: str,
    additional_source: str | None = None,
    *,
    enable_report: bool = True,
    extra_pytest_args: list[str] | None = None,
) -> tuple[subprocess.CompletedProcess[str], Path]:
    test_file = tmp_path / "test_outcome_case.py"
    test_file.write_text(source, encoding="utf-8")
    test_files = [test_file]
    if additional_source is not None:
        additional_file = tmp_path / "test_outcome_additional.py"
        additional_file.write_text(additional_source, encoding="utf-8")
        test_files.append(additional_file)
    report_path = tmp_path / "outcomes.json"
    system_tests = Path(__file__).parents[2] / "tests" / "system"
    env: dict[str, str] = {
        **os.environ,
        "PYTHONPATH": str(system_tests),
        "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1",
    }
    if enable_report:
        env[REPORT_ENV] = str(report_path)
    else:
        env.pop(REPORT_ENV, None)
    result = subprocess.run(
        [
            sys.executable,
            "-m",
            "pytest",
            "-p",
            "conftest",
            "--confcutdir",
            str(tmp_path),
            "-q",
            *(extra_pytest_args or []),
            *(str(path) for path in test_files),
        ],
        cwd=tmp_path,
        capture_output=True,
        text=True,
        timeout=30,
        env=env,
    )
    return result, report_path


def test_pytest_hook_writes_exact_success_outcome_report(tmp_path: Path) -> None:
    result, report_path = _run_outcome_case(
        tmp_path,
        "def test_success():\n    assert True\n",
    )

    assert result.returncode == 0, result.stdout + result.stderr
    assert json.loads(report_path.read_text(encoding="utf-8")) == {
        "schema_version": 1,
        "collected": 1,
        "passed": 1,
        "failed": 0,
        "errors": 0,
        "collection_errors": 0,
        "deselected": 0,
        "skipped": 0,
        "xfailed": 0,
        "xpassed": 0,
        "exit_status": 0,
    }


def test_pytest_hook_records_deselected_items_separately(
    tmp_path: Path,
) -> None:
    result, report_path = _run_outcome_case(
        tmp_path,
        "def test_selected():\n    pass\n"
        "def test_filtered_out():\n    pass\n",
        extra_pytest_args=["-k", "selected"],
    )

    assert result.returncode == 0, result.stdout + result.stderr
    assert json.loads(report_path.read_text(encoding="utf-8")) == {
        "schema_version": 1,
        "collected": 1,
        "passed": 1,
        "failed": 0,
        "errors": 0,
        "collection_errors": 0,
        "deselected": 1,
        "skipped": 0,
        "xfailed": 0,
        "xpassed": 0,
        "exit_status": 0,
    }


def test_pytest_hook_is_inactive_without_fixed_report_environment(
    tmp_path: Path,
) -> None:
    result, report_path = _run_outcome_case(
        tmp_path,
        "def test_success():\n    pass\n",
        enable_report=False,
    )

    assert result.returncode == 0, result.stdout + result.stderr
    assert not report_path.exists()


@pytest.mark.parametrize(
    ("mode", "enable_report", "expected_outcome", "removed"),
    [
        ("success", True, "passed", True),
        ("success", False, "passed", True),
        ("call-failure", True, "failed", False),
        ("late-teardown-failure", True, "errors", False),
        ("setup-failure", True, "errors", False),
        ("skip", True, "skipped", False),
        ("xfail", True, "xfailed", False),
        ("xpass", True, "xpassed", False),
        ("preservation-failure", True, "errors", False),
        ("external-symlink", True, "passed", True),
        ("replaced-root", True, "errors", False),
        ("outside-root", True, "errors", False),
        ("cleanup-failure", True, "errors", False),
        ("cleanup-failure", False, "errors", False),
    ],
)
def test_image_workspace_cleanup_obeys_final_outcomes(
    tmp_path: Path,
    mode: str,
    enable_report: bool,
    expected_outcome: str,
    removed: bool,
) -> None:
    source = (
        f"MODE = {mode!r}\n"
        + """
import shutil
from pathlib import Path

import pytest
import conftest


@pytest.fixture
def later_teardown():
    yield
    if MODE == "late-teardown-failure":
        raise RuntimeError("another finalizer failed after image preservation")


@pytest.fixture(autouse=True)
def image_workspace(request, tmp_path, later_teardown):
    Path("workspace-path").write_text(str(tmp_path), encoding="utf-8")
    (tmp_path / "work").mkdir()
    (tmp_path / "work" / "artifact.raw").write_bytes(b"diagnostic artifact")
    outside = Path.cwd() / "outside"
    outside.mkdir()
    (outside / "keep").write_bytes(b"external evidence")
    root = outside if MODE == "outside-root" else tmp_path
    scratch = conftest._register_image_workspace(request, root)
    if MODE == "setup-failure":
        raise RuntimeError("setup failed after registration")
    if MODE == "cleanup-failure":
        def fail_cleanup():
            raise OSError("injected image scratch cleanup failure")
        scratch.remove = fail_cleanup
    yield tmp_path
    if MODE == "preservation-failure":
        raise AssertionError("image preservation assertion failed")
    scratch.eligible = True


def test_image_case(image_workspace):
    if MODE == "call-failure":
        raise AssertionError("image call failed")
    if MODE == "skip":
        pytest.skip("synthetic tooling outcome")
    if MODE == "xfail":
        pytest.xfail("synthetic tooling outcome")
    if MODE == "external-symlink":
        (image_workspace / "work" / "link").symlink_to(
            Path.cwd() / "outside", target_is_directory=True
        )
    if MODE == "replaced-root":
        image_workspace.rename(Path.cwd() / "original-workspace")
        shutil.copytree(Path.cwd() / "outside", image_workspace)


if MODE == "xpass":
    test_image_case = pytest.mark.xfail(reason="synthetic tooling outcome")(
        test_image_case
    )
"""
    )
    junit_path = tmp_path / "junit.xml"
    result, report_path = _run_outcome_case(
        tmp_path,
        source,
        enable_report=enable_report,
        extra_pytest_args=["--junitxml", str(junit_path)],
    )

    expected_exit = 1 if expected_outcome in ("failed", "errors") else 0
    assert result.returncode == expected_exit, result.stdout + result.stderr
    workspace = Path((tmp_path / "workspace-path").read_text(encoding="utf-8"))
    assert workspace.exists() is not removed
    if not removed and mode != "replaced-root":
        assert (workspace / "work" / "artifact.raw").read_bytes() == (
            b"diagnostic artifact"
        )
    assert (tmp_path / "outside" / "keep").read_bytes() == b"external evidence"
    if mode == "replaced-root":
        assert (workspace / "keep").read_bytes() == b"external evidence"
        original_artifact = tmp_path / "original-workspace" / "work" / "artifact.raw"
        assert original_artifact.read_bytes() == b"diagnostic artifact"
        assert "image workspace root was replaced" in result.stdout
    if mode == "cleanup-failure":
        assert "image workspace scratch cleanup failed" in result.stdout
        assert "injected image scratch cleanup failure" in result.stdout

    junit_case = ET.parse(junit_path).find(".//testcase[@name='test_image_case']")
    assert junit_case is not None
    if expected_outcome == "errors":
        assert junit_case.find("error") is not None
    elif expected_outcome == "failed":
        assert junit_case.find("failure") is not None
    if enable_report:
        expected = _valid_outcome_values(
            **{"passed": 0, expected_outcome: 1, "exit_status": expected_exit}
        )
        assert json.loads(report_path.read_text(encoding="utf-8")) == expected
    else:
        assert not report_path.exists()


@pytest.mark.parametrize(
    ("source", "exit_status", "collected", "outcome", "collection_errors"),
    [
        ("VALUE = 1\n", 5, 0, None, 0),
        (
            "import pytest\n"
            "@pytest.mark.skip(reason='synthetic')\n"
            "def test_skip():\n    pass\n",
            0,
            1,
            "skipped",
            0,
        ),
        (
            "import pytest\n"
            "@pytest.mark.xfail(reason='synthetic')\n"
            "def test_xfail():\n    assert False\n",
            0,
            1,
            "xfailed",
            0,
        ),
        (
            "import pytest\n"
            "@pytest.mark.xfail(reason='synthetic')\n"
            "def test_xpass():\n    pass\n",
            0,
            1,
            "xpassed",
            0,
        ),
        ("def test_failure():\n    assert False\n", 1, 1, "failed", 0),
        ("def test_broken(:\n    pass\n", 2, 0, None, 1),
        (
            "import pytest\n"
            "@pytest.fixture\n"
            "def broken():\n    raise RuntimeError('setup')\n"
            "def test_setup_error(broken):\n    pass\n",
            1,
            1,
            "errors",
            0,
        ),
        (
            "import pytest\n"
            "@pytest.fixture\n"
            "def broken():\n"
            "    yield\n"
            "    raise RuntimeError('teardown')\n"
            "def test_teardown_error(broken):\n    pass\n",
            1,
            1,
            "errors",
            0,
        ),
    ],
    ids=[
        "zero-collection",
        "skip",
        "xfail",
        "xpass",
        "failure",
        "collection-error",
        "setup-error",
        "teardown-error",
    ],
)
def test_pytest_hook_classifies_exact_outcomes(
    tmp_path: Path,
    source: str,
    exit_status: int,
    collected: int,
    outcome: str | None,
    collection_errors: int,
) -> None:
    result, report_path = _run_outcome_case(tmp_path, source)

    assert result.returncode == exit_status, result.stdout + result.stderr
    report = json.loads(report_path.read_text(encoding="utf-8"))
    expected_counts = {
        "passed": 0,
        "failed": 0,
        "errors": 0,
        "collection_errors": collection_errors,
        "deselected": 0,
        "skipped": 0,
        "xfailed": 0,
        "xpassed": 0,
    }
    if outcome is not None:
        expected_counts[outcome] = 1
    assert report == {
        "schema_version": 1,
        "collected": collected,
        **expected_counts,
        "exit_status": exit_status,
    }


def test_pytest_hook_separates_collection_and_item_outcomes(
    tmp_path: Path,
) -> None:
    result, report_path = _run_outcome_case(
        tmp_path,
        "def test_success():\n    pass\n",
        additional_source="def test_broken(:\n    pass\n",
    )

    assert result.returncode == 2, result.stdout + result.stderr
    assert json.loads(report_path.read_text(encoding="utf-8")) == {
        "schema_version": 1,
        "collected": 1,
        "passed": 0,
        "failed": 0,
        "errors": 1,
        "collection_errors": 1,
        "deselected": 0,
        "skipped": 0,
        "xfailed": 0,
        "xpassed": 0,
        "exit_status": 2,
    }


def test_complete_outcome_validator_accepts_only_all_passed(
    outcomes: ModuleType,
) -> None:
    payload = json.dumps(
        {
            "schema_version": 1,
            "collected": 3,
            "passed": 3,
            "failed": 0,
            "errors": 0,
            "collection_errors": 0,
            "deselected": 0,
            "skipped": 0,
            "xfailed": 0,
            "xpassed": 0,
            "exit_status": 0,
        }
    ).encode()

    report = outcomes.require_complete_pytest_outcomes(
        payload,
        process_returncode=0,
    )

    assert report.collected == 3
    assert report.passed == 3


@pytest.mark.parametrize(
    ("kind", "message"),
    [
        ("directory", "not a regular file"),
        ("symlink", "cannot open"),
        ("oversized", "exceeds 4096 bytes"),
    ],
)
def test_local_outcome_reader_rejects_untrusted_or_oversized_files(
    outcomes: ModuleType,
    tmp_path: Path,
    kind: str,
    message: str,
) -> None:
    report_path = tmp_path / "outcomes.json"
    if kind == "directory":
        report_path.mkdir()
    elif kind == "symlink":
        target = tmp_path / "target.json"
        target.write_text(
            json.dumps(_valid_outcome_values()),
            encoding="utf-8",
        )
        report_path.symlink_to(target)
    else:
        report_path.write_bytes(b" " * 4097)

    with pytest.raises(RuntimeError, match=message):
        outcomes.read_pytest_outcome_report(report_path)


@pytest.mark.parametrize(
    ("payload", "process_returncode", "message"),
    [
        (b"{", 0, "malformed JSON"),
        (json.dumps([]).encode(), 0, "top-level JSON object"),
        (
            json.dumps(
                {
                    key: value
                    for key, value in _valid_outcome_values().items()
                    if key != "passed"
                }
            ).encode(),
            0,
            "missing fields: passed",
        ),
        (
            json.dumps(_valid_outcome_values(surprise=0)).encode(),
            0,
            "unknown fields: surprise",
        ),
        (
            json.dumps(_valid_outcome_values())
            .replace('"passed": 1', '"passed": 1, "passed": 0')
            .encode(),
            0,
            "duplicate field: passed",
        ),
        (
            json.dumps(_valid_outcome_values())
            .replace('"passed": 1', '"passed": 1, "Passed": 0')
            .encode(),
            0,
            "case-fold duplicate fields: Passed, passed",
        ),
        (
            json.dumps(_valid_outcome_values(schema_version=2)).encode(),
            0,
            "schema version",
        ),
        (
            json.dumps(_valid_outcome_values(passed=True)).encode(),
            0,
            "passed must be an integer",
        ),
        (
            json.dumps(_valid_outcome_values(skipped=-1)).encode(),
            0,
            "skipped must not be negative",
        ),
        (
            json.dumps(
                _valid_outcome_values(collected=100_001, passed=100_001)
            ).encode(),
            0,
            "collected exceeds",
        ),
        (
            json.dumps(_valid_outcome_values(exit_status=6)).encode(),
            6,
            "unknown pytest exit status",
        ),
        (
            json.dumps(_valid_outcome_values(collected=2)).encode(),
            0,
            "outcome counts do not equal collected",
        ),
        (
            json.dumps(_valid_outcome_values()).encode(),
            1,
            "does not match process return code",
        ),
        (
            json.dumps(_valid_outcome_values()).encode() + b" " * 4096,
            0,
            "exceeds 4096 bytes",
        ),
    ],
    ids=[
        "malformed",
        "non-object",
        "missing-field",
        "unknown-field",
        "duplicate-field",
        "case-fold-duplicate-field",
        "schema",
        "wrong-type",
        "negative",
        "count-bound",
        "unknown-exit",
        "inconsistent-counts",
        "exit-mismatch",
        "oversized",
    ],
)
def test_outcome_validator_rejects_malformed_or_inconsistent_reports(
    outcomes: ModuleType,
    payload: bytes,
    process_returncode: int,
    message: str,
) -> None:
    with pytest.raises(RuntimeError, match=message):
        outcomes.require_complete_pytest_outcomes(
            payload,
            process_returncode=process_returncode,
        )


@pytest.mark.parametrize(
    ("changes", "process_returncode", "message"),
    [
        (
            {"collected": 0, "passed": 0, "exit_status": 5},
            5,
            "collection is empty",
        ),
        ({"passed": 0, "skipped": 1}, 0, "skipped=1"),
        ({"passed": 0, "xfailed": 1}, 0, "xfailed=1"),
        ({"passed": 0, "xpassed": 1}, 0, "xpassed=1"),
        ({"deselected": 1}, 0, "deselected=1"),
        ({"passed": 0, "failed": 1, "exit_status": 1}, 1, "failed=1"),
        ({"passed": 0, "errors": 1, "exit_status": 1}, 1, "errors=1"),
        (
            {
                "collected": 0,
                "passed": 0,
                "collection_errors": 1,
                "exit_status": 2,
            },
            2,
            "collection_errors=1",
        ),
    ],
    ids=[
        "zero",
        "skip",
        "xfail",
        "xpass",
        "deselected",
        "failure",
        "error",
        "collection-error",
    ],
)
def test_outcome_validator_rejects_incomplete_pytest_outcomes(
    outcomes: ModuleType,
    changes: dict[str, object],
    process_returncode: int,
    message: str,
) -> None:
    payload = json.dumps(_valid_outcome_values(**changes)).encode()

    with pytest.raises(RuntimeError, match=message):
        outcomes.require_complete_pytest_outcomes(
            payload,
            process_returncode=process_returncode,
        )
