#!/usr/bin/env python3
"""Verify check_skip_ratio.py with known JUnit XML and JSON inputs.

Creates temporary input files, runs check_skip_ratio.py on each, and
validates exit codes.  This is a standalone verification (not pytest).

Usage:
    python scripts/check_skip_ratio_verify.py
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_DIR = Path(__file__).resolve().parent.parent
CHECK_SCRIPT = REPO_DIR / "scripts" / "check_skip_ratio.py"


def _run_check(
    args: list[str], input_data: str | None = None
) -> subprocess.CompletedProcess[str]:
    cmd = [sys.executable, str(CHECK_SCRIPT), *args]
    return subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=30,
        input=input_data,
    )


def test_junit_xml_single_file_passes() -> None:
    """Single JUnit XML, all files within 10% skip threshold."""
    xml = """<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" errors="0" failures="0" skipped="1" tests="20" time="1.0">
    <testcase classname="tests.system.vm.test_vm" name="test_a" file="tests/system/vm/test_vm.py">
    </testcase>
    <testcase classname="tests.system.vm.test_vm" name="test_b" file="tests/system/vm/test_vm.py">
    </testcase>
    <testcase classname="tests.system.vm.test_vm" name="test_c" file="tests/system/vm/test_vm.py">
      <skipped type="pytest.skip" message="requires KVM"/>
    </testcase>
  </testsuite>
</testsuites>"""
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".xml", delete=False
    ) as f:
        f.write(xml)
        tmp_path = f.name
    try:
        result = _run_check(["--junit-xml", tmp_path])
        # 1 skip out of 3 = 33.3% — exceeds 10% threshold — should fail
        assert result.returncode != 0, (
            f"Expected non-zero exit, got {result.returncode}\n"
            f"stdout: {result.stdout}\nstderr: {result.stderr}"
        )
    finally:
        Path(tmp_path).unlink(missing_ok=True)


def test_junit_xml_single_file_fails() -> None:
    """Single JUnit XML with a violating file, high threshold."""
    xml = """<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="pytest" errors="0" failures="0" skipped="10" tests="20" time="1.0">
    <testcase classname="tests.system.vm.test_vm" name="test_a" file="tests/system/vm/test_vm.py">
    </testcase>
    <testcase classname="tests.system.vm.test_vm" name="test_b" file="tests/system/vm/test_vm.py">
    </testcase>
    <testcase classname="tests.system.vm.test_vm" name="test_c" file="tests/system/vm/test_vm.py">
      <skipped type="pytest.skip" message="env not ready"/>
    </testcase>
    <testcase classname="tests.system.network.test_net" name="test_d" file="tests/system/network/test_net.py">
    </testcase>
    <testcase classname="tests.system.network.test_net" name="test_e" file="tests/system/network/test_net.py">
      <skipped type="pytest.skip" message="no bridge"/>
    </testcase>
  </testsuite>
</testsuites>"""
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".xml", delete=False
    ) as f:
        f.write(xml)
        tmp_path = f.name
    try:
        # threshold=90% — only flag files with >90% skips (none should)
        result = _run_check(["--junit-xml", tmp_path, "--threshold", "90"])
        print(f"  stdout: {result.stdout.strip()}")
        # With 90% threshold, 1/3 (33%) and 1/2 (50%) are both under — pass
        assert result.returncode == 0, (
            f"Expected exit 0, got {result.returncode}\n"
            f"stdout: {result.stdout}\nstderr: {result.stderr}"
        )
    finally:
        Path(tmp_path).unlink(missing_ok=True)


def test_junit_xml_directory() -> None:
    """Multiple XML files in a directory, merged results."""
    xml1 = """<?xml version="1.0" encoding="utf-8"?>
<testsuite name="pytest" errors="0" failures="0" skipped="0" tests="2" time="0.5">
  <testcase classname="tests.system.vm.test_vm" name="test_a" file="tests/system/vm/test_vm.py"/>
  <testcase classname="tests.system.vm.test_vm" name="test_b" file="tests/system/vm/test_vm.py"/>
</testsuite>"""
    xml2 = """<?xml version="1.0" encoding="utf-8"?>
<testsuite name="pytest" errors="0" failures="0" skipped="1" tests="3" time="0.5">
  <testcase classname="tests.system.vm.test_vm" name="test_c" file="tests/system/vm/test_vm.py">
    <skipped type="pytest.skip" message="requires KVM"/>
  </testcase>
  <testcase classname="tests.system.vm.test_vm" name="test_d" file="tests/system/vm/test_vm.py"/>
</testsuite>"""

    with tempfile.TemporaryDirectory() as tmpdir:
        Path(tmpdir, "run1.xml").write_text(xml1)
        Path(tmpdir, "run2.xml").write_text(xml2)
        # 1 skip out of 4 = 25% — exceeds default 10% — should fail
        result = _run_check(["--junit-xml", tmpdir])
        assert result.returncode != 0, (
            f"Expected non-zero exit, got {result.returncode}\n"
            f"stdout: {result.stdout}\nstderr: {result.stderr}"
        )
        # With 30% threshold, 25% should pass
        result2 = _run_check(["--junit-xml", tmpdir, "--threshold", "30"])
        assert result2.returncode == 0, (
            f"Expected exit 0, got {result2.returncode}\n"
            f"stdout: {result2.stdout}\nstderr: {result2.stderr}"
        )


def test_json_report() -> None:
    """JSON report input via file."""
    report = {
        "created": 1234567890.0,
        "duration": 5.0,
        "exitcode": 0,
        "summary": {"passed": 3, "failed": 0, "skipped": 1, "total": 4},
        "tests": [
            {
                "nodeid": "tests/system/vm/test_vm.py::test_a",
                "outcome": "passed",
                "keywords": [],
                "call": {"outcome": "passed"},
            },
            {
                "nodeid": "tests/system/vm/test_vm.py::test_b",
                "outcome": "passed",
                "keywords": [],
                "call": {"outcome": "passed"},
            },
            {
                "nodeid": "tests/system/vm/test_vm.py::test_c",
                "outcome": "passed",
                "keywords": [],
                "call": {"outcome": "passed"},
            },
            {
                "nodeid": "tests/system/vm/test_vm.py::test_d",
                "outcome": "skipped",
                "keywords": ["skip"],
                "call": {"outcome": "skipped"},
            },
        ],
    }
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".json", delete=False
    ) as f:
        json.dump(report, f)
        tmp_path = f.name
    try:
        # 1 skip out of 4 = 25% — exceeds default 10% — should fail
        result = _run_check(["--json-report", tmp_path])
        assert result.returncode != 0, (
            f"Expected non-zero exit, got {result.returncode}\n"
            f"stdout: {result.stdout}\nstderr: {result.stderr}"
        )
        # With 30% threshold, 25% is OK
        result2 = _run_check(["--json-report", tmp_path, "--threshold", "30"])
        assert result2.returncode == 0, (
            f"Expected exit 0, got {result2.returncode}\n"
            f"stdout: {result2.stdout}\nstderr: {result2.stderr}"
        )
    finally:
        Path(tmp_path).unlink(missing_ok=True)


def test_json_report_stdin() -> None:
    """JSON report piped via stdin."""
    report = {
        "tests": [
            {
                "nodeid": "tests/system/vm/test_vm.py::test_ok",
                "outcome": "passed",
                "call": {"outcome": "passed"},
            },
        ],
    }
    result = _run_check([], input_data=json.dumps(report))
    # 0 skips out of 1 — should pass
    assert result.returncode == 0, (
        f"Expected exit 0, got {result.returncode}\n"
        f"stdout: {result.stdout}\nstderr: {result.stderr}"
    )


def test_verbose_output() -> None:
    """--verbose produces per-file breakdown."""
    xml = """<?xml version="1.0" encoding="utf-8"?>
<testsuite name="pytest" errors="0" failures="0" skipped="0" tests="1" time="0.1">
  <testcase classname="tests.system.vm.test_vm" name="test_a" file="tests/system/vm/test_vm.py"/>
</testsuite>"""
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".xml", delete=False
    ) as f:
        f.write(xml)
        tmp_path = f.name
    try:
        result = _run_check(["--junit-xml", tmp_path, "--verbose"])
        assert result.returncode == 0
        assert "passed" in result.stdout
        assert "tests/system/vm/test_vm.py" in result.stdout
    finally:
        Path(tmp_path).unlink(missing_ok=True)


def test_no_file_attribute_fallback() -> None:
    """JUnit XML without 'file' attribute falls back to classname conversion."""
    xml = """<?xml version="1.0" encoding="utf-8"?>
<testsuite name="pytest" errors="0" failures="0" skipped="1" tests="2" time="0.1">
  <testcase classname="tests.system.vm.test_vm" name="test_a"/>
  <testcase classname="tests.system.vm.test_vm.TestVM" name="test_b">
    <skipped type="pytest.skip" message="no KVM"/>
  </testcase>
</testsuite>"""
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".xml", delete=False
    ) as f:
        f.write(xml)
        tmp_path = f.name
    try:
        # 1 skip out of 2 = 50% — exceeds default 10% — should fail
        result = _run_check(["--junit-xml", tmp_path])
        assert result.returncode != 0
        # Verify both test cases mapped to the same file
        assert "tests/system/vm/test_vm.py" in result.stdout
    finally:
        Path(tmp_path).unlink(missing_ok=True)


def main() -> None:
    tests = [
        (
            "test_junit_xml_single_file_passes",
            test_junit_xml_single_file_passes,
        ),
        ("test_junit_xml_single_file_fails", test_junit_xml_single_file_fails),
        ("test_junit_xml_directory", test_junit_xml_directory),
        ("test_json_report", test_json_report),
        ("test_json_report_stdin", test_json_report_stdin),
        ("test_verbose_output", test_verbose_output),
        ("test_no_file_attribute_fallback", test_no_file_attribute_fallback),
    ]

    failed = 0
    for name, func in tests:
        try:
            func()
            print(f"  PASS  {name}")
        except AssertionError as e:
            print(f"  FAIL  {name}")
            print(f"         {e}")
            failed += 1
        except Exception as e:
            print(f"  FAIL  {name} (exception)")
            print(f"         {e}")
            failed += 1

    print()
    if failed:
        print(f"FAILED: {failed} of {len(tests)} verification test(s)")
        sys.exit(1)
    else:
        print(f"PASSED: all {len(tests)} verification test(s)")


if __name__ == "__main__":
    main()
