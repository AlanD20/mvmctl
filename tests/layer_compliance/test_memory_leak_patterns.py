"""Compliance test for memory leak anti-patterns.

Architecture Rule: Code must not contain patterns known to cause unbounded
memory growth. Memory leaks most commonly arise from:
1. Infinite loops without guaranteed exit
2. Unbounded data accumulation
3. Resource leaks (sockets, files, threads)
4. Mock abuse in tests creating infinite call recording

This test uses AST static analysis to detect known anti-patterns.
For runtime leak detection, use scripts/profile_test_memory.py.
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
TESTS_ROOT = Path(__file__).parent.parent.parent / "tests"


# =============================================================================
# Helpers
# =============================================================================


def _get_python_files(directory: Path) -> list[Path]:
    if not directory.exists():
        return []
    return [
        p
        for p in directory.rglob("*.py")
        if p.name != "__init__.py" and "archive" not in p.parts
    ]


def _build_parent_map(tree: ast.AST) -> dict[ast.AST, ast.AST]:
    parent_map: dict[ast.AST, ast.AST] = {}
    for parent in ast.walk(tree):
        for child in ast.iter_child_nodes(parent):
            parent_map[child] = parent
    return parent_map


def _get_enclosing_function(
    node: ast.AST, parent_map: dict[ast.AST, ast.AST]
) -> ast.FunctionDef | ast.AsyncFunctionDef | ast.Module | None:
    current = node
    while current in parent_map:
        parent = parent_map[current]
        if isinstance(parent, (ast.FunctionDef, ast.AsyncFunctionDef)):
            return parent
        current = parent
    # Module scope — return the tree itself if it's a Module
    if isinstance(tree := parent_map.get(node), ast.Module) or isinstance(
        node, ast.Module
    ):
        return tree if isinstance(tree, ast.Module) else None
    return None


def _is_in_with_context(
    node: ast.AST, parent_map: dict[ast.AST, ast.AST]
) -> bool:
    """Check whether *node* is the context-expression of a ``with`` statement."""
    current = node
    path: list[ast.AST] = []
    while current in parent_map:
        path.append(current)
        parent = parent_map[current]
        if isinstance(parent, ast.With):
            for item in parent.items:
                if item.context_expr in path:
                    return True
        current = parent
    return False


def _is_inside_loop(
    node: ast.AST, parent_map: dict[ast.AST, ast.AST]
) -> ast.While | ast.For | None:
    current = node
    while current in parent_map:
        parent = parent_map[current]
        if isinstance(parent, (ast.While, ast.For)):
            return parent
        current = parent
    return None


def _get_variable_name(node: ast.expr) -> str | None:
    if isinstance(node, ast.Name):
        return node.id
    if isinstance(node, ast.Attribute) and isinstance(node.value, ast.Name):
        return f"{node.value.id}.{node.attr}"
    return None


def _find_calls_in_body(
    body: list[ast.stmt], target_var: str, attrs: set[str]
) -> bool:
    for stmt in body:
        for child in ast.walk(stmt):
            if isinstance(child, ast.Call) and isinstance(
                child.func, ast.Attribute
            ):
                var = _get_variable_name(child.func.value)
                if var == target_var and child.func.attr in attrs:
                    return True
    return False


def _find_reassignment_in_body(body: list[ast.stmt], target_var: str) -> bool:
    for stmt in body:
        for child in ast.walk(stmt):
            if isinstance(child, ast.Assign):
                for t in child.targets:
                    if _get_variable_name(t) == target_var:
                        return True
            elif isinstance(child, ast.AnnAssign) and child.target is not None:
                if _get_variable_name(child.target) == target_var:
                    return True
    return False


def _is_returned_from_function(
    node: ast.AST, parent_map: dict[ast.AST, ast.AST]
) -> bool:
    """Check whether the expression containing *node* is returned."""
    current = node
    while current in parent_map:
        parent = parent_map[current]
        if isinstance(parent, ast.Return):
            return True
        if isinstance(
            parent, (ast.FunctionDef, ast.AsyncFunctionDef, ast.Module)
        ):
            return False
        current = parent
    return False


def _get_assigned_var_name(
    node: ast.AST, parent_map: dict[ast.AST, ast.AST]
) -> str | None:
    """Return the simple variable name if *node* is assigned to a Name."""
    parent = parent_map.get(node)
    if isinstance(parent, ast.Assign):
        if len(parent.targets) == 1:
            return _get_variable_name(parent.targets[0])
    elif isinstance(parent, ast.AnnAssign) and parent.target is not None:
        return _get_variable_name(parent.target)
    return None


def _has_method_call_in_scope(
    scope: ast.AST | None, var_name: str, methods: set[str]
) -> bool:
    if scope is None:
        return False
    if isinstance(scope, (ast.FunctionDef, ast.AsyncFunctionDef)):
        body = scope.body
    elif isinstance(scope, ast.Module):
        body = scope.body
    else:
        return False

    for stmt in body:
        for child in ast.walk(stmt):
            if isinstance(child, ast.Call) and isinstance(
                child.func, ast.Attribute
            ):
                target_var = _get_variable_name(child.func.value)
                if target_var == var_name and child.func.attr in methods:
                    return True
    return False


def _get_relative_path(full_path: Path) -> str:
    try:
        return str(full_path.relative_to(PROJECT_ROOT.parent.parent))
    except ValueError:
        return str(full_path)


# =============================================================================
# Category 1: Infinite loops without guaranteed exit
# =============================================================================

CATEGORY_1_ALLOWLIST: dict[str, str] = {
    "src/mvmctl/core/logs/_service.py": (
        "Log-following generator intentionally runs until the consumer closes it. "
        "yield statement provides cooperative termination."
    ),
}


def _is_while_true(node: ast.While) -> bool:
    return isinstance(node.test, ast.Constant) and node.test.value is True


def _has_terminal_statement_deep(body: list[ast.stmt]) -> bool:
    for stmt in body:
        for child in ast.walk(stmt):
            if isinstance(child, (ast.Break, ast.Return, ast.Raise)):
                return True
            if isinstance(child, ast.Call):
                if (
                    isinstance(child.func, ast.Attribute)
                    and isinstance(child.func.value, ast.Name)
                    and child.func.value.id == "sys"
                    and child.func.attr == "exit"
                ):
                    return True
    return False


def _find_infinite_loop_violations(file_path: Path) -> list[dict[str, object]]:
    violations: list[dict[str, object]] = []
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return violations

    relative = _get_relative_path(file_path)
    if relative in CATEGORY_1_ALLOWLIST:
        return violations

    for node in ast.walk(tree):
        if not isinstance(node, ast.While):
            continue
        if not _is_while_true(node):
            continue
        if not _has_terminal_statement_deep(node.body):
            violations.append(
                {
                    "file": relative,
                    "line": node.lineno,
                    "reason": (
                        "while True loop has no guaranteed exit "
                        "(break/return/raise/sys.exit) at any nesting level"
                    ),
                }
            )
    return violations


class TestInfiniteLoops:
    """Detect ``while True:`` loops with no guaranteed terminal statement."""

    def test_no_infinite_loops_without_exit(self):
        all_files = _get_python_files(PROJECT_ROOT)
        violations: list[dict[str, object]] = []
        for file_path in all_files:
            violations.extend(_find_infinite_loop_violations(file_path))

        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['reason']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} infinite-loop violation(s):\n"
                + "\n".join(msgs)
                + "\n\nwhile True loops must have a terminal statement "
                + "(break/return/raise/sys.exit) somewhere in their body."
            )


# =============================================================================
# Category 2: Unbounded data accumulation in loops
# =============================================================================

CATEGORY_2_ALLOWLIST: dict[str, str] = {}


def _is_append_or_extend_call(node: ast.Call) -> bool:
    return isinstance(node.func, ast.Attribute) and node.func.attr in {
        "append",
        "extend",
    }


def _get_append_target_var(node: ast.Call) -> str | None:
    return _get_variable_name(node.func.value)


def _looks_infinite_iterator(node: ast.AST) -> bool:
    """Return True only for clearly infinite iterators."""
    # Known infinite generators from itertools and similar
    if isinstance(node, ast.Call):
        func_name: str | None = None
        if isinstance(node.func, ast.Name):
            func_name = node.func.id
        elif isinstance(node.func, ast.Attribute):
            func_name = node.func.attr
        if func_name in {"count", "cycle", "repeat"}:
            return True
    return False


def _find_accumulation_violations(file_path: Path) -> list[dict[str, object]]:
    violations: list[dict[str, object]] = []
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return violations

    parent_map = _build_parent_map(tree)
    relative = _get_relative_path(file_path)
    if relative in CATEGORY_2_ALLOWLIST:
        return violations

    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        if not _is_append_or_extend_call(node):
            continue

        loop = _is_inside_loop(node, parent_map)
        if loop is None:
            continue

        var_name = _get_append_target_var(node)
        if var_name is None:
            continue

        if isinstance(loop, ast.While):
            if not _is_while_true(loop):
                continue
        elif isinstance(loop, ast.For):
            if not _looks_infinite_iterator(loop.iter):
                continue

        # Check for cleanup: .clear(), .pop(), or reassignment
        if _find_calls_in_body(loop.body, var_name, {"clear", "pop"}):
            continue
        if _find_reassignment_in_body(loop.body, var_name):
            continue

        violations.append(
            {
                "file": relative,
                "line": node.lineno,
                "reason": (
                    f"{node.func.attr}() on '{var_name}' inside "
                    f"{'while True' if isinstance(loop, ast.While) else 'for'} loop "
                    "without .clear(), .pop(), or reassignment of the target"
                ),
            }
        )

    return violations


class TestUnboundedAccumulation:
    """Detect unbounded list accumulation inside loops."""

    def test_no_unbounded_accumulation(self):
        all_files = _get_python_files(PROJECT_ROOT)
        violations: list[dict[str, object]] = []
        for file_path in all_files:
            violations.extend(_find_accumulation_violations(file_path))

        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['reason']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} unbounded-accumulation violation(s):\n"
                + "\n".join(msgs)
                + "\n\nLists built inside loops must be cleared, popped, or "
                + "reassigned to prevent unbounded growth."
            )


# =============================================================================
# Category 3: Resource leaks
# =============================================================================

CATEGORY_3_ALLOWLIST: dict[str, str] = {
    "src/mvmctl/cli/console.py": (
        "Socket is returned to the caller which owns cleanup via _interact()."
    ),
    "src/mvmctl/core/vm/_firecracker.py": (
        "Firecracker process is managed externally via PID file and signal handlers; "
        "poll() is used for immediate-exit detection only."
    ),
    "src/mvmctl/services/console_relay/manager.py": (
        "Subprocess is spawned as a long-running daemon; lifecycle is managed "
        "externally via PID tracking and SIGTERM."
    ),
    "src/mvmctl/services/nocloud_server/manager.py": (
        "Subprocess is spawned as a long-running daemon; lifecycle is managed "
        "externally via PID tracking and SIGTERM."
    ),
    "src/mvmctl/services/console_relay/client.py": (
        "Socket is stored as an instance variable and explicitly closed in disconnect()."
    ),
}


def _is_socket_socket_call(node: ast.Call) -> bool:
    return (
        isinstance(node.func, ast.Attribute)
        and isinstance(node.func.value, ast.Name)
        and node.func.value.id == "socket"
        and node.func.attr == "socket"
    )


def _is_open_call(node: ast.Call) -> bool:
    return isinstance(node.func, ast.Name) and node.func.id == "open"


def _is_subprocess_popen_call(node: ast.Call) -> bool:
    return (
        isinstance(node.func, ast.Attribute)
        and isinstance(node.func.value, ast.Name)
        and node.func.value.id == "subprocess"
        and node.func.attr == "Popen"
    )


def _find_resource_leak_violations(file_path: Path) -> list[dict[str, object]]:
    violations: list[dict[str, object]] = []
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return violations

    parent_map = _build_parent_map(tree)
    relative = _get_relative_path(file_path)
    if relative in CATEGORY_3_ALLOWLIST:
        return violations

    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue

        if _is_socket_socket_call(node) or _is_open_call(node):
            if _is_in_with_context(node, parent_map):
                continue

            var_name = _get_assigned_var_name(node, parent_map)
            if var_name is None:
                # Not assigned — could be a transient call; skip to reduce noise
                continue
            if var_name.startswith("self."):
                # Instance variables are managed by class lifecycle
                continue
            if _is_returned_from_function(node, parent_map):
                # Ownership transferred to caller
                continue

            scope = _get_enclosing_function(node, parent_map)
            if _has_method_call_in_scope(scope, var_name, {"close"}):
                continue

            kind = (
                "socket.socket()" if _is_socket_socket_call(node) else "open()"
            )
            violations.append(
                {
                    "file": relative,
                    "line": node.lineno,
                    "reason": (
                        f"{kind} result assigned to '{var_name}' not used as "
                        "context manager and has no .close() in scope"
                    ),
                }
            )

        elif _is_subprocess_popen_call(node):
            var_name = _get_assigned_var_name(node, parent_map)
            if var_name is None:
                continue
            if var_name.startswith("self."):
                continue
            if _is_returned_from_function(node, parent_map):
                continue

            scope = _get_enclosing_function(node, parent_map)
            if _has_method_call_in_scope(
                scope, var_name, {"wait", "communicate", "kill"}
            ):
                continue

            violations.append(
                {
                    "file": relative,
                    "line": node.lineno,
                    "reason": (
                        f"subprocess.Popen() result assigned to '{var_name}' "
                        "without .wait(), .communicate(), or .kill() in scope"
                    ),
                }
            )

    return violations


class TestResourceLeaks:
    """Detect resource leaks from sockets, files, and subprocesses."""

    def test_no_resource_leaks(self):
        all_files = _get_python_files(PROJECT_ROOT)
        violations: list[dict[str, object]] = []
        for file_path in all_files:
            violations.extend(_find_resource_leak_violations(file_path))

        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['reason']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} resource-leak violation(s):\n"
                + "\n".join(msgs)
                + "\n\nSockets/files should use context managers or have explicit .close(). "
                + "subprocess.Popen results should call .wait(), .communicate(), or .kill()."
            )


# =============================================================================
# Category 4: Mock abuse in tests
# =============================================================================

CATEGORY_4_ALLOWLIST: dict[str, str] = {}

BLOCKING_IO_NAMES = {
    "select.select",
    "socket.recv",
    "socket.send",
    "open",
    "read",
    "write",
    "sleep",
    "wait",
}


def _is_patch_call(node: ast.Call) -> bool:
    if isinstance(node.func, ast.Name):
        return "patch" in node.func.id
    if isinstance(node.func, ast.Attribute):
        return "patch" in node.func.attr
    return False


def _get_patch_target(node: ast.Call) -> str | None:
    if (
        node.args
        and isinstance(node.args[0], ast.Constant)
        and isinstance(node.args[0].value, str)
    ):
        return node.args[0].value
    for kw in node.keywords:
        if (
            kw.arg == "target"
            and isinstance(kw.value, ast.Constant)
            and isinstance(kw.value.value, str)
        ):
            return kw.value.value
    return None


def _has_return_value_or_side_effect(node: ast.Call) -> bool:
    for kw in node.keywords:
        if kw.arg in {"return_value", "side_effect"}:
            return True
    return False


def _is_blocking_io_target(target: str) -> bool:
    return any(name in target for name in BLOCKING_IO_NAMES)


def _is_blocking_io_attribute(attr: str) -> bool:
    return attr in {"read", "write", "recv", "send", "wait", "sleep"}


def _is_magicmock_call(node: ast.Call) -> bool:
    if isinstance(node.func, ast.Name) and "MagicMock" in node.func.id:
        return True
    if isinstance(node.func, ast.Attribute) and "MagicMock" in node.func.attr:
        return True
    return False


def _find_mock_abuse_violations(file_path: Path) -> list[dict[str, object]]:
    violations: list[dict[str, object]] = []
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return violations

    parent_map = _build_parent_map(tree)
    relative = _get_relative_path(file_path)
    if relative in CATEGORY_4_ALLOWLIST:
        return violations

    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue

        # Pattern 1: patch() with return_value/side_effect on blocking I/O
        if _is_patch_call(node):
            if not _has_return_value_or_side_effect(node):
                continue
            target = _get_patch_target(node)
            if target is None or not _is_blocking_io_target(target):
                continue
            if _is_inside_loop(node, parent_map) is not None:
                violations.append(
                    {
                        "file": relative,
                        "line": node.lineno,
                        "reason": (
                            f"patch('{target}') with return_value/side_effect "
                            "used inside a loop — creates infinite mock call recording"
                        ),
                    }
                )

        # Pattern 2: MagicMock(return_value=...) assigned to blocking I/O attribute
        elif _is_magicmock_call(node):
            if not _has_return_value_or_side_effect(node):
                continue
            assign_parent = parent_map.get(node)
            if isinstance(assign_parent, ast.Assign):
                for assign_target in assign_parent.targets:
                    if isinstance(
                        assign_target, ast.Attribute
                    ) and _is_blocking_io_attribute(assign_target.attr):
                        if _is_inside_loop(node, parent_map) is not None:
                            violations.append(
                                {
                                    "file": relative,
                                    "line": node.lineno,
                                    "reason": (
                                        f"MagicMock assigned to .{assign_target.attr} "
                                        "inside a loop — creates infinite mock call recording"
                                    ),
                                }
                            )

    return violations


class TestMockAbuse:
    """Detect MagicMock/patch abuse for blocking I/O inside loops."""

    def test_no_mock_abuse_in_loops(self):
        all_files = _get_python_files(TESTS_ROOT)
        violations: list[dict[str, object]] = []
        for file_path in all_files:
            violations.extend(_find_mock_abuse_violations(file_path))

        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['reason']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} mock-abuse violation(s):\n"
                + "\n".join(msgs)
                + "\n\nMocking blocking I/O inside loops creates unbounded call "
                + "recording (memory leak). Move patch/MagicMock outside the loop."
            )
