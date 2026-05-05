"""Test that architectural import boundaries are enforced.

Architecture Rules:
1. CLI → API → Core (CLI only imports from api/ layer)
2. API may import from core but NOT from Controller classes
3. Core services must NOT import from other core services
4. Core must NOT import from API (reverse-layer violation)
"""

from __future__ import annotations

import ast
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
CLI_DIR = PROJECT_ROOT / "cli"
API_DIR = PROJECT_ROOT / "api"
CORE_DIR = PROJECT_ROOT / "core"
SERVICES_DIR = PROJECT_ROOT / "services"

ALLOWED_CORE_SUBMODULES = {"models", "exceptions", "constants"}


def _get_python_files(directory: Path) -> list[Path]:
    if not directory.exists():
        return []
    return list(directory.rglob("*.py"))


def _parse_imports(file_path: Path) -> list[tuple[str, str, int]]:
    """Parse a Python file and extract all import statements.
    Skips imports inside TYPE_CHECKING blocks.
    """
    imports = []
    content = file_path.read_text()
    try:
        tree = ast.parse(content)
    except SyntaxError:
        return imports

    def _is_in_type_checking(node: ast.AST) -> bool:
        for parent in ast.walk(tree):
            if isinstance(parent, ast.If):
                if (
                    isinstance(parent.test, ast.Name)
                    and parent.test.id == "TYPE_CHECKING"
                ):
                    for child in ast.walk(parent):
                        if child is node:
                            return True
        return False

    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                imports.append(("import", alias.name, node.lineno))
        elif isinstance(node, ast.ImportFrom):
            module = node.module or ""
            if not _is_in_type_checking(node):
                imports.append(("from", module, node.lineno))
    return imports


def _is_core_violation(import_path: str) -> bool:
    """Check if an import path violates the CLI→API→Core rule."""
    if not import_path.startswith("mvmctl.core"):
        return False
    parts = import_path.split(".")
    if len(parts) >= 3 and parts[2] in ALLOWED_CORE_SUBMODULES:
        return False
    return True


def _get_relative_path(full_path: Path) -> str:
    try:
        return str(full_path.relative_to(PROJECT_ROOT.parent.parent))
    except ValueError:
        return str(full_path)


class TestCLILayerImports:
    """CLI must NOT import from core directly."""

    def test_cli_no_direct_core_imports(self):
        cli_files = _get_python_files(CLI_DIR)
        violations = []
        for file_path in cli_files:
            if file_path.name == "__init__.py":
                continue
            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                if _is_core_violation(import_path):
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )
        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['import']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} CLI→Core import violation(s):\n"
                + "\n".join(msgs)
                + "\n\nCLI should only import from API layer."
            )


class TestAPILayerImports:
    """API must NOT import Controller classes from core."""

    def test_api_no_controller_imports(self):
        """API layer must not import Controller classes directly.

        Controllers are stateful single-entity lifecycle managers.
        API should use Operation classes and service methods instead.
        """
        api_files = _get_python_files(API_DIR)
        violations = []
        for file_path in api_files:
            if file_path.name == "__init__.py":
                continue
            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                if "Controller" in import_path:
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )
        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['import']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} API→Controller import violation(s):\n"
                + "\n".join(msgs)
                + "\n\nAPI layer must NOT import Controller classes directly."
                + "\nUse Operation classes and Service methods instead."
            )


class TestCoreServiceIsolation:
    """Core services must NOT import from other core services.

    Services can only import from:
    - mvmctl.core._shared (shared infrastructure)
    - mvmctl.models (data models)
    - mvmctl.exceptions
    - mvmctl.constants
    - mvmctl.utils (helpers)
    """

    SERVICE_DIRS = [
        "binary",
        "image",
        "kernel",
        "key",
        "network",
        "host",
        "logs",
        "console",
        "ssh",
        "config",
        "cloudinit",
        "cache",
    ]

    def test_no_cross_service_imports(self):
        violations = []
        for domain in self.SERVICE_DIRS:
            domain_dir = CORE_DIR / domain
            if not domain_dir.exists():
                continue
            for file_path in domain_dir.rglob("_service.py"):
                self._check_service_file(file_path, violations)
        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['import']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} cross-service import violation(s):\n"
                + "\n".join(msgs)
                + "\n\nCore services must only import from mvmctl.core._shared,"
                + " not from other core services."
            )

    def _check_service_file(self, file_path: Path, violations: list) -> None:
        imports = _parse_imports(file_path)
        for import_type, import_path, line_no in imports:
            # Allow: mvmctl.core._shared
            if import_path.startswith("mvmctl.core._shared"):
                continue
            # Allow: mvmctl.core.{domain}._repository (repository is allowed)
            if "._repository" in import_path:
                continue
            # Allow: mvmctl.core.{domain}._resolver
            if "._resolver" in import_path:
                continue
            # Check for: mvmctl.core.X._service where X is not current domain
            if import_path.startswith("mvmctl.core."):
                parts = import_path.split(".")
                if len(parts) >= 4 and parts[3] == "_service":
                    # Allow same-domain _service imports
                    current_domain = file_path.parent.name
                    if parts[2] != current_domain:
                        violations.append(
                            {
                                "file": _get_relative_path(file_path),
                                "line": line_no,
                                "import": import_path,
                            }
                        )


class TestAPIVsCoreBoundary:
    """Core must NOT import from API (reverse-layer violation)."""

    def test_core_no_api_imports(self):
        core_files = _get_python_files(CORE_DIR)
        violations = []
        for file_path in core_files:
            if file_path.name == "__init__.py":
                continue
            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                if import_path.startswith("mvmctl.api"):
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )
        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['import']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} Core→API import violation(s):\n"
                + "\n".join(msgs)
                + "\n\nCore must NOT import from the API layer (reverse-layer violation)."
            )


class TestDBImportCompliance:
    """CLI must NOT import from DB modules."""

    DB_MODULES = {
        "mvmctl.db",
        "mvmctl.db.models",
        "mvmctl.db.migrations",
    }

    def _is_db_violation(self, import_path: str) -> bool:
        return any(import_path.startswith(mod) for mod in self.DB_MODULES)

    def test_cli_no_db_imports(self):
        cli_files = _get_python_files(CLI_DIR)
        violations = []
        for file_path in cli_files:
            if file_path.name == "__init__.py":
                continue
            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                if self._is_db_violation(import_path):
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )
        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['import']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} CLI→DB import violation(s):\n"
                + "\n".join(msgs)
                + "\n\nCLI must NOT import from DB modules directly."
            )

    def test_core_no_db_imports_except_db_models(self):
        """Core must not import from db/ directly (db.models dataclasses are OK)."""
        core_files = _get_python_files(CORE_DIR)
        violations = []
        for file_path in core_files:
            if file_path.name == "__init__.py":
                continue
            # _shared/_db.py is the central DB interface - allowed
            if file_path.name == "_db.py":
                continue
            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                if import_path.startswith("mvmctl.db.models"):
                    continue
                if import_path.startswith("mvmctl.db"):
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )
        if violations:
            msgs = [
                f"  {v['file']}:{v['line']} - {v['import']}" for v in violations
            ]
            pytest.fail(
                f"Found {len(violations)} Core→DB import violation(s):\n"
                + "\n".join(msgs)
            )
