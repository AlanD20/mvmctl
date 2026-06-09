#!/usr/bin/env python3
"""Check per-file skip ratios from pytest JUnit XML or JSON report output.

Usage:
    python scripts/check_skip_ratio.py --junit-xml path/to/report.xml
    python scripts/check_skip_ratio.py --junit-xml path/to/reports/dir/
    python scripts/check_skip_ratio.py --json-report path/to/report.json
    python scripts/check_skip_ratio.py < report.json              # stdin JSON

    python scripts/check_skip_ratio.py --junit-xml report.xml --verbose
    python scripts/check_skip_ratio.py --junit-xml report.xml --threshold 15
    python scripts/check_skip_ratio.py -v --junit-xml report.xml --threshold 15

Exit code: 0 (all files within threshold) or 1 (one or more files exceed threshold).
"""

from __future__ import annotations

import argparse
import json
import sys
import xml.etree.ElementTree as ET
from collections import defaultdict
from pathlib import Path


def _parse_junit_xml(file_path: Path) -> dict[str, dict[str, int]]:
    """Parse a JUnit XML file and return per-file test counts.

    Returns a dict mapping normalized file paths to dicts with keys:
    passed, failed, skipped, errors, total.
    """
    tree = ET.parse(file_path)
    root = tree.getroot()

    per_file: dict[str, dict[str, int]] = defaultdict(
        lambda: {
            "passed": 0,
            "failed": 0,
            "skipped": 0,
            "errors": 0,
            "total": 0,
        },
    )

    # Handle both <testsuites> and bare <testsuite> structures
    if root.tag == "testsuites":
        suites = root.findall("testsuite")
    elif root.tag == "testsuite":
        suites = [root]
    else:
        suites = root.findall(".//testsuite")

    for suite in suites:
        for testcase in suite.findall("testcase"):
            # Prefer the 'file' attribute (modern pytest JUnit XML).
            # Fall back to 'classname' for older formats.
            file_attr = testcase.get("file")
            if file_attr:
                file_key = file_attr
            else:
                classname = testcase.get("classname", "")
                file_key = _classname_to_file_key(classname)

            has_failure = testcase.find("failure") is not None
            has_error = testcase.find("error") is not None
            has_skipped = testcase.find("skipped") is not None

            stats = per_file[file_key]
            stats["total"] += 1

            if has_skipped:
                stats["skipped"] += 1
            elif has_error:
                stats["errors"] += 1
            elif has_failure:
                stats["failed"] += 1
            else:
                stats["passed"] += 1

    return dict(per_file)


def _classname_to_file_key(classname: str) -> str:
    """Convert a dotted JUnit classname to a normalized file key.

    Examples::

        "tests.system.vm.test_vm"         → "tests/system/vm/test_vm.py"
        "tests.system.vm.test_vm.TestVM"  → "tests/system/vm/test_vm.py"
    """
    if not classname:
        return "unknown.py"
    parts = classname.split(".")
    # Strip trailing class name (starts with uppercase)
    # Module components typically start lowercase.
    while parts and parts[-1][0].isupper() if parts[-1] else False:
        parts.pop()
    if not parts:
        return "unknown.py"
    return "/".join(parts) + ".py"


def _parse_json_report(
    file_path: Path | None = None,
    json_data: str | None = None,
) -> dict[str, dict[str, int]]:
    """Parse a pytest JSON report and return per-file test counts."""
    if file_path:
        data = json.loads(file_path.read_text())
    elif json_data:
        data = json.loads(json_data)
    else:
        return {}

    per_file: dict[str, dict[str, int]] = defaultdict(
        lambda: {
            "passed": 0,
            "failed": 0,
            "skipped": 0,
            "errors": 0,
            "total": 0,
        },
    )

    for test in data.get("tests", []):
        nodeid = test.get("nodeid", "")
        # nodeid format: "path/to/file.py::TestClass::test_method"
        file_key = nodeid.split("::")[0] if "::" in nodeid else nodeid
        if not file_key:
            continue

        # Determine outcome from the 'call' stage (most reliable)
        call = test.get("call") or {}
        outcome = call.get("outcome", test.get("outcome", "passed"))

        stats = per_file[file_key]
        stats["total"] += 1

        if outcome == "skipped":
            stats["skipped"] += 1
        elif outcome == "failed":
            # Distinguish 'error' from 'failure' via keywords if available
            keywords = test.get("keywords", [])
            if isinstance(keywords, list) and "error" in keywords:
                stats["errors"] += 1
            else:
                stats["failed"] += 1
        elif outcome == "error":
            stats["errors"] += 1
        else:
            stats["passed"] += 1

    return dict(per_file)


def _merge_results(
    results_list: list[dict[str, dict[str, int]]],
) -> dict[str, dict[str, int]]:
    """Merge multiple per-file result dicts additively."""
    merged: dict[str, dict[str, int]] = {}
    for results in results_list:
        for file_key, stats in results.items():
            if file_key not in merged:
                merged[file_key] = dict(stats)
            else:
                for k in ("passed", "failed", "skipped", "errors", "total"):
                    merged[file_key][k] += stats[k]
    return merged


def _check_skip_ratios(
    per_file: dict[str, dict[str, int]],
    threshold: float,
    verbose: bool,
) -> int:
    """Check per-file skip ratios and return exit code (0=pass, 1=fail).

    Prints summary and per-file breakdown when *verbose* is True.
    """
    violating: list[tuple[str, float, dict[str, int]]] = []
    total_passed = 0
    total_failed = 0
    total_skipped = 0
    total_errors = 0
    total_tests = 0

    sorted_files = sorted(per_file.items())

    for file_key, stats in sorted_files:
        total = stats["total"]
        if total == 0:
            continue
        total_passed += stats["passed"]
        total_failed += stats["failed"]
        total_skipped += stats["skipped"]
        total_errors += stats["errors"]
        total_tests += total

        ratio = stats["skipped"] / total
        violates = ratio > threshold

        if violates:
            violating.append((file_key, ratio, stats))

        if verbose:
            pct = ratio * 100
            status = "VIOLATES" if violates else "ok"
            flag = " !" if violates else "  "
            print(
                f"  {file_key:75s} {stats['passed']:4d} passed  "
                f"{stats['failed']:4d} failed  {stats['skipped']:4d} skipped  "
                f"{stats['errors']:4d} errors  ({pct:5.1f}% skipped){flag} [{status}]",
            )

    if verbose and sorted_files:
        print()

    total_ratio = total_skipped / total_tests if total_tests > 0 else 0
    print(
        f"Total: {total_passed} passed, {total_failed} failed, "
        f"{total_skipped} skipped, {total_errors} errors "
        f"({total_tests} tests)",
    )
    print(
        f"Overall skip ratio: {total_ratio * 100:.1f}%  "
        f"Threshold: {threshold * 100:.1f}%",
    )

    if violating:
        print()
        print("FAIL: Files exceeding skip ratio threshold:")
        for file_key, ratio, stats in violating:
            print(
                f"  {file_key:75s}  {ratio * 100:5.1f}% skipped  "
                f"({stats['skipped']}/{stats['total']} tests)",
            )
        return 1

    if total_tests == 0:
        print("Warning: No test results found to evaluate.")
        return 0

    print()
    print("OK: All files within skip ratio threshold.")
    return 0


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Check per-file skip ratios from pytest output.",
    )

    input_group = parser.add_mutually_exclusive_group()
    input_group.add_argument(
        "--junit-xml",
        type=str,
        default=None,
        help="Path to JUnit XML file or directory containing JUnit XML files",
    )
    input_group.add_argument(
        "--json-report",
        type=str,
        default=None,
        help="Path to pytest JSON report file",
    )

    parser.add_argument(
        "--threshold",
        type=float,
        default=10.0,
        help="Skip ratio threshold in percent (default: 10.0)",
    )
    parser.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="Print per-file breakdown",
    )

    args = parser.parse_args()

    threshold = args.threshold / 100.0

    results_list: list[dict[str, dict[str, int]]] = []

    if args.junit_xml:
        junit_path = Path(args.junit_xml)
        if junit_path.is_dir():
            xml_files = sorted(junit_path.glob("*.xml"))
            if not xml_files:
                print(
                    f"Error: No XML files found in {junit_path}",
                    file=sys.stderr,
                )
                sys.exit(1)
            for xml_file in xml_files:
                try:
                    results_list.append(_parse_junit_xml(xml_file))
                except ET.ParseError as e:
                    print(
                        f"Warning: Failed to parse {xml_file}: {e}",
                        file=sys.stderr,
                    )
        else:
            if not junit_path.exists():
                print(
                    f"Error: JUnit XML file not found: {junit_path}",
                    file=sys.stderr,
                )
                sys.exit(1)
            try:
                results_list.append(_parse_junit_xml(junit_path))
            except ET.ParseError as e:
                print(
                    f"Error: Failed to parse {junit_path}: {e}",
                    file=sys.stderr,
                )
                sys.exit(1)
    elif args.json_report:
        json_path = Path(args.json_report)
        if not json_path.exists():
            print(
                f"Error: JSON report file not found: {json_path}",
                file=sys.stderr,
            )
            sys.exit(1)
        try:
            results_list.append(_parse_json_report(file_path=json_path))
        except json.JSONDecodeError as e:
            print(
                f"Error: Failed to parse {json_path}: {e}",
                file=sys.stderr,
            )
            sys.exit(1)
    else:
        # Fall back to stdin as JSON
        if sys.stdin.isatty():
            print(
                "Error: No input provided. Use --junit-xml, --json-report, "
                "or pipe JSON report via stdin.",
                file=sys.stderr,
            )
            sys.exit(1)
        stdin_data = sys.stdin.read()
        if not stdin_data.strip():
            print("Error: Empty stdin input.", file=sys.stderr)
            sys.exit(1)
        try:
            results_list.append(_parse_json_report(json_data=stdin_data))
        except json.JSONDecodeError as e:
            print(
                f"Error: Failed to parse stdin as JSON: {e}",
                file=sys.stderr,
            )
            sys.exit(1)

    if not results_list:
        print("Error: No test results could be parsed.", file=sys.stderr)
        sys.exit(1)

    per_file = _merge_results(results_list)

    if not per_file:
        print("Error: No test results found in input.", file=sys.stderr)
        sys.exit(1)

    exit_code = _check_skip_ratios(per_file, threshold, args.verbose)
    sys.exit(exit_code)


if __name__ == "__main__":
    main()
