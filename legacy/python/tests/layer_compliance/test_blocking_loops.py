"""AST-based enforcement of blocking-loop anti-patterns.

Detects busy-wait loops where ``while True:`` spins around
``select.select()`` without a terminal statement for the "not ready"
case.  Such loops burn CPU when the timeout expires and nothing is
ready to read.

Allowed patterns
----------------
* ``if X not in ready: return/break/raise``
* ``if X in ready: ... else: return/break/raise``
* ``if not ready: return/break/raise``

Any other shape inside ``while True:`` that calls ``select.select()``
is reported as a violation.
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"


def _get_python_files(directory: Path) -> list[Path]:
    if not directory.exists():
        return []
    return list(directory.rglob("*.py"))


def _is_select_select_call(node: ast.AST) -> bool:
    """Match ``select.select(...)`` (module function call)."""
    return (
        isinstance(node, ast.Call)
        and isinstance(node.func, ast.Attribute)
        and isinstance(node.func.value, ast.Name)
        and node.func.value.id == "select"
        and node.func.attr == "select"
    )


def _is_terminal_statement(node: ast.AST) -> bool:
    """Return, Break, and Raise are terminal loop statements."""
    return isinstance(node, (ast.Return, ast.Break, ast.Raise))


def _has_terminal_statement(body: list[ast.stmt]) -> bool:
    """Shallow check: does the statement list contain a terminal node?"""
    return any(_is_terminal_statement(stmt) for stmt in body)


def _get_ready_name(assign: ast.Assign) -> str | None:
    """Extract the target name from ``ready, _, _ = select.select(...)``."""
    if len(assign.targets) != 1:
        return None
    target = assign.targets[0]
    if isinstance(target, ast.Tuple) and target.elts:
        first = target.elts[0]
        if isinstance(first, ast.Name):
            return first.id
    elif isinstance(target, ast.Name):
        return target.id
    return None


def _is_while_true(node: ast.While) -> bool:
    """Match ``while True:``."""
    return isinstance(node.test, ast.Constant) and node.test.value is True


def _check_loop_body(body: list[ast.stmt], ready_name: str) -> bool:
    """Return ``True`` if the loop body has a proper "not ready" exit."""
    for stmt in body:
        if isinstance(stmt, ast.If):
            test = stmt.test

            # Pattern 1: ``if X not in ready: return/break/raise``
            if (
                isinstance(test, ast.Compare)
                and len(test.ops) == 1
                and isinstance(test.ops[0], ast.NotIn)
                and len(test.comparators) == 1
                and isinstance(test.comparators[0], ast.Name)
                and test.comparators[0].id == ready_name
                and _has_terminal_statement(stmt.body)
            ):
                return True

            # Pattern 2: ``if X in ready: ... else: return/break/raise``
            if (
                isinstance(test, ast.Compare)
                and len(test.ops) == 1
                and isinstance(test.ops[0], ast.In)
                and len(test.comparators) == 1
                and isinstance(test.comparators[0], ast.Name)
                and test.comparators[0].id == ready_name
                and stmt.orelse
                and _has_terminal_statement(stmt.orelse)
            ):
                return True

            # Pattern 3: ``if not ready: return/break/raise``
            if (
                isinstance(test, ast.UnaryOp)
                and isinstance(test.op, ast.Not)
                and isinstance(test.operand, ast.Name)
                and test.operand.id == ready_name
                and _has_terminal_statement(stmt.body)
            ):
                return True

    return False


def _get_select_read_list_len(call: ast.Call) -> int | None:
    """Return the number of elements in the first arg if it's a list literal."""
    if not call.args:
        return None
    first = call.args[0]
    if isinstance(first, ast.List):
        return len(first.elts)
    return None


def _has_ready_check(body: list[ast.stmt], ready_name: str) -> bool:
    """Shallow check for any ``if X in ready`` or ``if X not in ready``."""
    for stmt in body:
        if isinstance(stmt, ast.If):
            test = stmt.test
            if isinstance(test, ast.Compare) and len(test.ops) == 1:
                if isinstance(test.ops[0], (ast.In, ast.NotIn)):
                    if (
                        len(test.comparators) == 1
                        and isinstance(test.comparators[0], ast.Name)
                        and test.comparators[0].id == ready_name
                    ):
                        return True
    return False


def _find_violations(file_path: Path) -> list[dict[str, object]]:
    """Parse *file_path* and return any busy-wait violations."""
    violations: list[dict[str, object]] = []
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return violations

    for node in ast.walk(tree):
        if not isinstance(node, ast.While):
            continue
        if not _is_while_true(node):
            continue

        ready_name: str | None = None
        select_call: ast.Call | None = None

        for stmt in node.body:
            # Look for ``ready, _, _ = select.select(...)``
            if isinstance(stmt, ast.Assign):
                for child in ast.walk(stmt.value):
                    if _is_select_select_call(child):
                        select_call = child  # type: ignore[assignment]
                        ready_name = _get_ready_name(stmt)
                        break
            # The call may also appear standalone (unlikely but possible)
            elif isinstance(stmt, ast.Expr):
                if _is_select_select_call(stmt.value):
                    select_call = stmt.value  # type: ignore[assignment]

        if select_call is None:
            continue

        # If we cannot determine the ready variable name, we conservatively
        # flag the loop — the anti-pattern requires inspecting ``ready``.
        if ready_name is None:
            violations.append(
                {
                    "file": str(file_path),
                    "line": node.lineno,
                    "reason": "select.select() result is not unpacked to a known variable",
                }
            )
            continue

        # Strict check: the loop must have a terminal statement for the
        # "not ready" case.
        if _check_loop_body(node.body, ready_name):
            continue

        read_list_len = _get_select_read_list_len(select_call)

        # Single-fd select (or unknown variable) without a not-ready exit
        # is the classic busy-wait anti-pattern.
        if read_list_len is None or read_list_len == 1:
            violations.append(
                {
                    "file": str(file_path),
                    "line": node.lineno,
                    "reason": (
                        "while True + select.select() without a terminal "
                        "statement for the 'not ready' case"
                    ),
                }
            )
            continue

        # Multi-fd select without a not-ready exit: allow if there are
        # ``if X in ready`` / ``if X not in ready`` checks, indicating a
        # multiplexed event loop.
        if not _has_ready_check(node.body, ready_name):
            violations.append(
                {
                    "file": str(file_path),
                    "line": node.lineno,
                    "reason": (
                        "while True + select.select() with multiple fds "
                        "but no ready-variable checks"
                    ),
                }
            )

    return violations


def _get_relative_path(full_path: Path) -> str:
    try:
        return str(full_path.relative_to(PROJECT_ROOT.parent.parent))
    except ValueError:
        return str(full_path)


class TestBlockingLoops:
    """Ensure ``while True:`` loops around ``select.select()`` have a
    proper exit path for the timeout / "not ready" case."""

    def test_no_busy_wait_select_loops(self):
        all_files = _get_python_files(PROJECT_ROOT)
        violations: list[dict[str, object]] = []

        for file_path in all_files:
            if file_path.name == "__init__.py":
                continue
            violations.extend(_find_violations(file_path))

        if violations:
            msgs = [
                f"  {_get_relative_path(Path(str(v['file'])))}:{v['line']} - {v['reason']}"
                for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} busy-wait loop violation(s):\n"
                + "\n".join(msgs)
                + "\n\nwhile True + select.select() must have a terminal statement "
                + "(return/break/raise) for the 'not ready' case."
            )
