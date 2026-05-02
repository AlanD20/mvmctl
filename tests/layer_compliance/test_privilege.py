"""Test that API layer privileged functions call check_privileges().

Architecture Rule: API layer is the privilege boundary.
Functions that perform privileged host operations must call check_privileges().
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
API_DIR = PROJECT_ROOT / "api"

PRIVILEGED_API_FUNCTIONS = {
    "vm_operations.py": ["remove"],
}


def _get_function_body(file_path: Path, function_name: str) -> ast.AST | None:
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return None
    for node in ast.walk(tree):
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            if node.name == function_name:
                return node
    return None


def _has_check_privileges_call(function_node: ast.AST) -> bool:
    for child in ast.walk(function_node):
        if isinstance(child, ast.Call):
            if isinstance(child.func, ast.Name):
                if child.func.id in ("check_privileges", "check_privileges_interactive"):
                    return True
            elif isinstance(child.func, ast.Attribute):
                if child.func.attr in ("check_privileges", "check_privileges_interactive"):
                    return True
    return False


class TestAPIPrivilegeChecks:
    """Verify privileged API functions call check_privileges()."""

    @pytest.mark.parametrize(
        "api_file,expected_privileged_functions",
        [
            ("vm_operations.py", ["remove"]),
            ("network_operations.py", []),
        ],
    )
    def test_privileged_functions_have_privilege_check(
        self, api_file: str, expected_privileged_functions: list[str]
    ):
        file_path = API_DIR / api_file
        if not file_path.exists():
            pytest.skip(f"API file not found: {api_file}")

        violations = []
        for func_name in expected_privileged_functions:
            func_node = _get_function_body(file_path, func_name)
            if func_node is None:
                violations.append(f"{api_file} - {func_name}() not found")
                continue
            if not _has_check_privileges_call(func_node):
                violations.append(f"{api_file}:{func_node.lineno} - {func_name}() missing privilege check")

        if violations:
            pytest.fail("Missing privilege check(s):\n" + "\n".join(violations))
