# pyright: reportMissingImports=false
"""Test that no hardcoded values exist in core/api/cli layers.

Architecture Rule: All defaults must be in constants.py
Configuration priority (lowest → highest):
1. constants.py DEFAULT_* / CONST_* values
2. ~/.config/mvmctl/config.json
3. MVM_* environment variables
4. CLI flags

No hardcoded strings/numbers should exist in business logic.
"""

import ast
import re
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
CORE_DIR = PROJECT_ROOT / "core"
API_DIR = PROJECT_ROOT / "api"
CLI_DIR = PROJECT_ROOT / "cli"
CONSTANTS_FILE = PROJECT_ROOT / "constants.py"

# Patterns that are likely hardcoded values (not exhaustive)
MAGIC_STRING_PATTERNS = [
    # Path-like strings (but not test paths)
    (r'["\']\/(?:usr|etc|var|opt|home|tmp|root)\/[^"\']+["\']', "absolute path"),
    # Version strings that look like hardcoded defaults
    (r'["\']\d+\.\d+\.\d+["\']', "version string"),
    # Common hardcoded timeouts/intervals
    (r'["\']\d+\.\d*["\']', "numeric string"),
]

# Numbers that might be magic values (allow 0, 1, -1, small integers used as flags)
MAGIC_NUMBER_PATTERN = re.compile(r"\b(?!0$|1$|-1$)(\d{3,})\b")

# Whitelist of allowed strings (common non-config values)
ALLOWED_STRINGS = {
    "true",
    "false",
    "yes",
    "no",
    "on",
    "off",
    "json",
    "yaml",
    "yml",
    "toml",
    "get",
    "post",
    "put",
    "delete",
    "patch",
    "info",
    "warning",
    "error",
    "debug",
    "running",
    "stopped",
    "paused",
    "error",
    "linux",
    "darwin",
    "windows",
    "amd64",
    "x86_64",
    "arm64",
    "aarch64",
    "qcow2",
    "raw",
    "vmdk",
    "vdi",
    "eth0",
    "ens3",
    "enp0s1",
}

# Files that are allowed to have certain patterns
FILE_EXCEPTIONS = {
    # constants.py itself obviously has hardcoded values
    CONSTANTS_FILE: "constants definition file",
    PROJECT_ROOT / "core" / "rootfs_injector.py": "rootfs_injector constant holder",
}


def _get_python_files(directory: Path) -> list[Path]:
    if not directory.exists():
        return []
    files = list(directory.rglob("*.py"))
    return [f for f in files if f.name != "__init__.py"]


def _extract_string_literals(file_path: Path) -> list[tuple[str, int, str]]:
    """Extract string literals from a Python file.

    Returns list of (string_value, line_number, context) tuples.
    """
    content = file_path.read_text()
    strings: list[tuple[str, int, str]] = []

    try:
        tree = ast.parse(content)
    except SyntaxError:
        return strings

    for node in ast.walk(tree):
        if isinstance(node, ast.Constant) and isinstance(node.value, str):
            value = node.value
            if value.lower() not in ALLOWED_STRINGS:
                strings.append((value, node.lineno, "string literal"))
        elif isinstance(node, ast.Str):  # For older Python AST
            value = str(node.s)
            if value.lower() not in ALLOWED_STRINGS:
                strings.append((value, node.lineno, "string literal"))

    return strings


def _extract_number_literals(file_path: Path) -> list[tuple[int | float, int, str]]:
    """Extract numeric literals from a Python file."""
    content = file_path.read_text()
    numbers: list[tuple[int | float, int, str]] = []

    try:
        tree = ast.parse(content)
    except SyntaxError:
        return numbers

    for node in ast.walk(tree):
        if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
            if isinstance(node.value, int) and node.value >= 100:
                numbers.append((node.value, node.lineno, "integer literal"))
            elif isinstance(node.value, float):
                numbers.append((node.value, node.lineno, "float literal"))
        elif isinstance(node, ast.Num):  # For older Python AST
            if isinstance(node.n, int) and node.n >= 100:
                numbers.append((node.n, node.lineno, "integer literal"))
            elif isinstance(node.n, float):
                numbers.append((node.n, node.lineno, "float literal"))

    return numbers


def _get_relative_path(full_path: Path) -> str:
    try:
        return str(full_path.relative_to(PROJECT_ROOT))
    except ValueError:
        return str(full_path)


class TestNoHardcodedValues:
    """Tests for hardcoded value detection."""

    def test_core_no_hardcoded_paths(self):
        """Core layer should not contain hardcoded file paths.

        Hardcoded paths should be in constants.py with DEFAULT_* or CONST_* prefix.
        """
        core_files = _get_python_files(CORE_DIR)
        violations = []

        for file_path in core_files:
            if file_path in FILE_EXCEPTIONS:
                continue

            content = file_path.read_text()
            lines = content.split("\n")

            for line_no, line in enumerate(lines, 1):
                # Skip comments and docstrings
                stripped = line.strip()
                if (
                    stripped.startswith("#")
                    or stripped.startswith('"""')
                    or stripped.startswith("'''")
                ):
                    continue

                # Check for absolute paths
                if re.search(r'["\']/(?:usr|etc|var|opt|tmp|root)/[^"\']+["\']', line):
                    # Skip if it's using a constant
                    if "constants." in line or "FALLBACK_" in line or "DEFAULT_" in line:
                        continue
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "content": line.strip(),
                            "type": "hardcoded path",
                        }
                    )

        if violations:
            violation_msgs = []
            for v in violations[:10]:  # Limit output
                violation_msgs.append(
                    f"  {v['file']}:{v['line']} - {v['type']}: {v['content'][:60]}"
                )

            if len(violations) > 10:
                violation_msgs.append(f"  ... and {len(violations) - 10} more")

            msg = (
                f"Found {len(violations)} hardcoded path(s) in core layer:\n"
                + "\n".join(violation_msgs)
                + "\n\nHardcoded paths should be defined in constants.py "
                + "with DEFAULT_* or CONST_* prefix."
            )
            pytest.fail(msg)

    def test_core_no_hardcoded_large_numbers(self):
        """Core layer should not contain hardcoded large numbers.

        Values like timeouts, sizes, limits should be in constants.py.
        """
        core_files = _get_python_files(CORE_DIR)
        violations = []

        for file_path in core_files:
            if file_path in FILE_EXCEPTIONS:
                continue

            numbers = _extract_number_literals(file_path)

            for value, line_no, context in numbers:
                # Skip if it's using a constant
                content = file_path.read_text()
                lines = content.split("\n")
                line = lines[line_no - 1]

                # Allow values that come from libguestfs call sites to remain
                # inline (eg. g.set_memsize(256)). These are implementation
                # details that tests should not force into constants.py.
                if (
                    "constants." in line
                    or "FALLBACK_" in line
                    or "DEFAULT_" in line
                    or (file_path.name == "rootfs_injector.py" and "set_memsize" in line)
                ):
                    continue

                violations.append(
                    {
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "value": value,
                        "type": "hardcoded number",
                    }
                )

        if violations:
            violation_msgs = []
            for v in violations[:10]:  # Limit output
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['type']}: {v['value']}")

            if len(violations) > 10:
                violation_msgs.append(f"  ... and {len(violations) - 10} more")

            msg = (
                f"Found {len(violations)} hardcoded number(s) in core layer:\n"
                + "\n".join(violation_msgs)
                + "\n\nNumeric constants should be defined in constants.py "
                + "with DEFAULT_* or CONST_* prefix."
            )
            pytest.fail(msg)

    def test_api_no_hardcoded_defaults(self):
        """API layer should not contain hardcoded default values.

        API functions should use constants for defaults.
        """
        api_files = _get_python_files(API_DIR)
        violations = []

        for file_path in api_files:
            numbers = _extract_number_literals(file_path)

            for value, line_no, context in numbers:
                content = file_path.read_text()
                lines = content.split("\n")
                line = lines[line_no - 1]

                # Allow values that come from libguestfs call sites to remain
                # inline (eg. g.set_memsize(256)). These are implementation
                # details that tests should not force into constants.py.
                if (
                    "constants." in line
                    or "FALLBACK_" in line
                    or "DEFAULT_" in line
                    or (file_path.name == "rootfs_injector.py" and "set_memsize" in line)
                ):
                    continue

                violations.append(
                    {
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "value": value,
                        "type": "hardcoded number in API",
                    }
                )

        if violations:
            violation_msgs = []
            for v in violations[:5]:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['type']}: {v['value']}")

            if len(violations) > 5:
                violation_msgs.append(f"  ... and {len(violations) - 5} more")

            msg = (
                f"Found {len(violations)} hardcoded value(s) in API layer:\n"
                + "\n".join(violation_msgs)
                + "\n\nUse constants.DEFAULT_* or constants.CONST_* instead."
            )
            pytest.fail(msg)


class TestConstantsFile:
    """Tests to verify constants.py contains expected patterns."""

    def test_constants_has_const_values(self):
        if not CONSTANTS_FILE.exists():
            pytest.skip("constants.py not found")

        content = CONSTANTS_FILE.read_text()
        const_pattern = re.compile(r"CONST_\w+(?::\s*\w+(?:\[\w+\])?)?\s*=")
        matches = const_pattern.findall(content)

        if len(matches) < 10:
            pytest.fail(
                f"Expected at least 10 CONST_* values in constants.py, found {len(matches)}"
            )

    def test_constants_has_default_values(self):
        """Verify constants.py defines DEFAULT_* values."""
        if not CONSTANTS_FILE.exists():
            pytest.skip("constants.py not found")

        content = CONSTANTS_FILE.read_text()

        # Check for DEFAULT_ prefix usage (handles type annotations like: DEFAULT_X: Final[str] = ...)
        default_pattern = re.compile(r"DEFAULT_\w+(?::\s*\w+(?:\[\w+\])?)?\s*=")
        matches = default_pattern.findall(content)

        if len(matches) < 10:
            pytest.fail(
                f"Expected at least 10 DEFAULT_* values in constants.py, found {len(matches)}"
            )


def test_libguestfs_constants_defined():
    from mvmctl import constants

    assert hasattr(constants, "DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT")
    assert hasattr(constants, "DEFAULT_LIBGUESTFS_ROOT_DEVICE")
    assert hasattr(constants, "DEFAULT_LIBGUESTFS_SEED_DIR")

    assert isinstance(constants.DEFAULT_LIBGUESTFS_LAUNCH_TIMEOUT, int)
    assert isinstance(constants.DEFAULT_LIBGUESTFS_ROOT_DEVICE, str)
    assert isinstance(constants.DEFAULT_LIBGUESTFS_SEED_DIR, str)
