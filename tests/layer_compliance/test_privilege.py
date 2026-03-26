"""Test that API layer functions call check_privileges() appropriately.

Architecture Rule: API layer is the privilege boundary
API functions that perform privileged operations must call check_privileges()
before delegating to core layer.
"""

import ast
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
API_DIR = PROJECT_ROOT / "api"

PRIVILEGED_API_FUNCTIONS = {
    "api/vms.py": ["create_vm", "remove_vm", "snapshot_vm", "load_snapshot"],
    "api/network.py": ["create_network", "remove_network"],
}


def _get_function_body(file_path: Path, function_name: str) -> ast.AST | None:
    """Extract the AST body of a specific function from a file."""
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
    """Check if a function body contains a call to check_privileges()."""
    for child in ast.walk(function_node):
        if isinstance(child, ast.Call):
            if isinstance(child.func, ast.Name):
                if child.func.id == "check_privileges":
                    return True
            elif isinstance(child.func, ast.Attribute):
                if child.func.attr == "check_privileges":
                    return True

    return False


def _get_relative_path(full_path: Path) -> str:
    try:
        return str(full_path.relative_to(PROJECT_ROOT))
    except ValueError:
        return str(full_path)


class TestAPIPrivilegeChecks:
    """Tests for API layer privilege check compliance."""

    @pytest.mark.parametrize(
        "api_file,expected_privileged_functions",
        [
            ("vms.py", ["create_vm", "remove_vm", "snapshot_vm", "load_snapshot"]),
            ("network.py", ["create_network", "remove_network"]),
        ],
    )
    def test_privileged_functions_have_check_privileges(
        self, api_file: str, expected_privileged_functions: list[str]
    ):
        """Verify privileged API functions call check_privileges().

        Known violations:
        - api/vms.py: create_vm, remove_vm, snapshot_vm, load_snapshot
          are re-exported from core without privilege checks
        - api/network.py is correctly implemented (create_network, remove_network
          both have check_privileges calls)
        """
        file_path = API_DIR / api_file

        if not file_path.exists():
            pytest.skip(f"API file not found: {api_file}")

        violations = []

        for func_name in expected_privileged_functions:
            func_node = _get_function_body(file_path, func_name)

            if func_node is None:
                # Function might be imported and re-exported (wrapper function)
                # In this case, we need to check if it's defined locally
                # or if it's just a re-export
                content = file_path.read_text()
                tree = ast.parse(content)

                # Check if this is a simple re-export (imported from core)
                is_reexport = False
                for node in ast.walk(tree):
                    if isinstance(node, ast.ImportFrom):
                        for alias in node.names:
                            if alias.name == func_name or (
                                alias.asname and alias.asname == func_name
                            ):
                                is_reexport = True
                                break

                if is_reexport:
                    violations.append(
                        {
                            "file": api_file,
                            "function": func_name,
                            "line": 0,
                            "reason": "function is re-exported from core without privilege check",
                        }
                    )
                continue

            if not _has_check_privileges_call(func_node):
                violations.append(
                    {
                        "file": api_file,
                        "function": func_name,
                        "line": func_node.lineno,
                        "reason": "missing check_privileges() call",
                    }
                )

        if violations:
            violation_msgs = []
            for v in violations:
                if v["line"] > 0:
                    violation_msgs.append(
                        f"  {v['file']}:{v['line']} - {v['function']}() - {v['reason']}"
                    )
                else:
                    violation_msgs.append(f"  {v['file']} - {v['function']}() - {v['reason']}")

            msg = (
                f"Found {len(violations)} missing privilege check(s):\n"
                + "\n".join(violation_msgs)
                + "\n\nPrivileged API functions must call check_privileges() "
                + "before delegating to core layer."
            )
            pytest.fail(msg)

    def test_network_api_has_privilege_checks(self):
        """Specifically verify network API has correct privilege checks.

        This test serves as a reference implementation - network API
        correctly wraps core functions with check_privileges() calls.
        """
        file_path = API_DIR / "network.py"

        if not file_path.exists():
            pytest.skip("network.py not found")

        content = file_path.read_text()
        tree = ast.parse(content)

        # Find create_network and remove_network functions
        privileged_funcs = ["create_network", "remove_network"]

        for func_name in privileged_funcs:
            func_node = None
            for node in ast.walk(tree):
                if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    if node.name == func_name:
                        func_node = node
                        break

            if func_node is None:
                continue

            if not _has_check_privileges_call(func_node):
                pytest.fail(f"network.{func_name}() is missing check_privileges() call")


class TestPrivilegeCheckPatterns:
    """Tests for privilege check detection logic."""

    def test_detects_direct_check_privileges_call(self):
        """Test detection of direct check_privileges() call."""
        code = """
def my_function():
    check_privileges("/usr/sbin/ip")
    do_something()
"""
        tree = ast.parse(code)
        func_node = tree.body[0]

        assert _has_check_privileges_call(func_node), "Should detect direct check_privileges() call"

    def test_detects_imported_check_privileges_call(self):
        """Test detection of api.host.check_privileges() call."""
        code = """
def my_function():
    from mvmctl.api.host import check_privileges
    check_privileges("/usr/sbin/ip")
    do_something()
"""
        tree = ast.parse(code)
        func_node = tree.body[0]

        assert _has_check_privileges_call(func_node), (
            "Should detect imported check_privileges() call"
        )

    def test_detects_missing_check_privileges(self):
        """Test that absence of check_privileges is detected."""
        code = """
def my_function():
    do_something()
    return result
"""
        tree = ast.parse(code)
        func_node = tree.body[0]

        assert not _has_check_privileges_call(func_node), (
            "Should correctly identify missing check_privileges() call"
        )
