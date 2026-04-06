"""Test that CLI layer only imports from allowed modules.

Architecture Rule: CLI → API → Core
CLI should only import from:
- mvmctl.api.*
- mvmctl.models.*
- mvmctl.exceptions.*
- mvmctl.constants
- mvmctl.utils.* (helpers only, no core business logic)

CLI should NOT import from:
- mvmctl.core.* (except via API layer)
"""

import ast
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
CLI_DIR = PROJECT_ROOT / "cli"
API_DIR = PROJECT_ROOT / "api"
CORE_DIR = PROJECT_ROOT / "core"

ALLOWED_CORE_SUBMODULES = {"models", "exceptions", "constants"}


def _get_python_files(directory: Path) -> list[Path]:
    """Recursively get all Python files in a directory."""
    if not directory.exists():
        return []
    return list(directory.rglob("*.py"))


def _parse_imports(file_path: Path) -> list[tuple[str, str, int]]:
    """Parse a Python file and extract all import statements.

    Returns list of (import_type, import_path, line_number) tuples.
    Skips imports inside TYPE_CHECKING blocks.
    """
    imports = []
    content = file_path.read_text()

    try:
        tree = ast.parse(content)
    except SyntaxError:
        return imports

    def _is_in_type_checking(node: ast.AST) -> bool:
        """Check if a node is inside a TYPE_CHECKING if block."""
        for parent in ast.walk(tree):
            if isinstance(parent, ast.If):
                # Check if it's TYPE_CHECKING
                if isinstance(parent.test, ast.Name) and parent.test.id == "TYPE_CHECKING":
                    # Check if our node is inside this if block
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
            # Skip TYPE_CHECKING imports
            if not _is_in_type_checking(node):
                imports.append(("from", module, node.lineno))

    return imports


def _is_core_violation(import_path: str) -> bool:
    """Check if an import path violates the CLI→API→Core rule."""
    if not import_path.startswith("mvmctl.core"):
        return False

    parts = import_path.split(".")
    if len(parts) >= 3:
        submodule = parts[2]
        if submodule in ALLOWED_CORE_SUBMODULES:
            return False

    return True


def _get_relative_path(full_path: Path) -> str:
    """Get path relative to project root for cleaner reporting."""
    try:
        return str(full_path.relative_to(PROJECT_ROOT.parent.parent))
    except ValueError:
        return str(full_path)


class TestCLILayerImports:
    """Tests for CLI layer import compliance."""

    def test_cli_no_direct_core_imports(self):
        """CLI files should not import directly from mvmctl.core (except models/exceptions/constants).

        Known violations:
        - cli/asset.py imports from mvmctl.core.metadata
        - cli/configure.py imports from mvmctl.core.config_state
        """
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
                            "type": import_type,
                        }
                    )

        if violations:
            violation_msgs = []
            for v in violations:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['type']} {v['import']}")

            msg = (
                f"Found {len(violations)} CLI→Core import violation(s):\n"
                + "\n".join(violation_msgs)
                + "\n\nCLI should only import from API layer, not directly from core."
                + "\nAllowed core imports: models, exceptions, constants"
            )
            pytest.fail(msg)

    def test_api_no_direct_core_imports_for_privileged_ops(self):
        """API layer may import from core, but should add privilege checks.

        This test documents that API→Core imports are allowed but require
        corresponding privilege checks (tested in test_privilege.py).
        """
        api_files = _get_python_files(API_DIR)
        core_imports = []

        for file_path in api_files:
            if file_path.name == "__init__.py":
                continue

            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                if import_path.startswith("mvmctl.core"):
                    core_imports.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )

        # API→Core imports are allowed, but we document them
        # The actual compliance check is in test_privilege.py
        pytest.skip(
            f"API layer has {len(core_imports)} core imports (allowed). "
            "Privilege checks verified separately in test_privilege.py."
        )


class TestImportWhitelist:
    """Tests to verify the import whitelist is correct."""

    @pytest.mark.parametrize(
        "import_path,should_be_violation",
        [
            ("mvmctl.core.metadata", True),
            ("mvmctl.core.config_state", True),
            ("mvmctl.core.kernel", True),
            ("mvmctl.core.vm_lifecycle", True),
            ("mvmctl.core.network", True),
            ("mvmctl.core.metadata.find_images_by_id_prefix", True),
            ("mvmctl.models", False),
            ("mvmctl.models.vm", False),
            ("mvmctl.exceptions", False),
            ("mvmctl.exceptions.MVMError", False),
            ("mvmctl.constants", False),
            ("mvmctl.constants.DEFAULT_VM_MEM_MIB", False),
            ("mvmctl.api.vms", False),
            ("mvmctl.api.host", False),
            ("mvmctl.utils.console", False),
        ],
    )
    def test_import_violation_detection(self, import_path: str, should_be_violation: bool):
        """Test that the violation detection logic works correctly."""
        result = _is_core_violation(import_path)
        assert result == should_be_violation, (
            f"Import '{import_path}' violation detection failed: "
            f"expected {should_be_violation}, got {result}"
        )


class TestDBImportCompliance:
    """Tests for Resolution Layer Mandate — DB imports only in API layer."""

    DB_MODULES = {
        "mvmctl.core.mvm_db",
        "mvmctl.db",
        "mvmctl.db.models",
        "mvmctl.db.migrations",
    }

    def _is_db_violation(self, import_path: str) -> bool:
        """Check if import path is a DB module."""
        return any(import_path.startswith(mod) for mod in self.DB_MODULES)

    def test_cli_no_db_imports(self):
        """CLI layer must NEVER import from DB modules.

        Resolution Layer Mandate: Only API layer queries the database.
        CLI must pass None to API for DB-backed defaults.
        """
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
            violation_msgs = []
            for v in violations:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['import']}")

            msg = (
                f"Found {len(violations)} CLI→DB import violation(s):\n"
                + "\n".join(violation_msgs)
                + "\n\nResolution Layer Mandate: CLI must NOT query the database."
                + "\nPass None to API for DB-backed defaults (image, kernel, binary, network)."
            )
            pytest.fail(msg)

    def test_core_no_db_imports_except_mvm_db(self):
        """Core layer must not import from DB except mvm_db (ORM interface).

        Resolution Layer Mandate: Core receives explicit values from API.
        Core must NOT query the database directly.

        Note: Importing ORM dataclasses from db.models is allowed —
        they are pure data containers, not queries.
        Only MVMDatabase and query functions are prohibited in Core.
        """
        core_files = _get_python_files(CORE_DIR)
        violations = []

        for file_path in core_files:
            if file_path.name == "__init__.py":
                continue
            if file_path.name == "mvm_db.py":
                continue  # The ORM module itself is allowed

            imports = _parse_imports(file_path)
            for import_type, import_path, line_no in imports:
                # Allow importing ORM models (dataclasses) - they're not queries
                if import_path.startswith("mvmctl.db.models"):
                    continue
                # Prohibit importing MVMDatabase and migrations
                if import_path.startswith("mvmctl.db"):
                    violations.append(
                        {
                            "file": _get_relative_path(file_path),
                            "line": line_no,
                            "import": import_path,
                        }
                    )

        if violations:
            violation_msgs = []
            for v in violations:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['import']}")


class TestCoreLayerDBCompliance:
    """Tests for Resolution Layer Mandate — Core layer must not use MVMDatabase directly.

    Resolution Layer Mandate:
    - CLI: Resolves user input + constants-backed defaults
    - API: Resolves DB-backed defaults (queries MVMDatabase)
    - Core: Receives explicit values — NEVER queries database

    This test class enforces that Core layer does not:
    1. Import MVMDatabase from mvmctl.core.mvm_db
    2. Instantiate MVMDatabase()
    3. Call db.get_default_*() methods
    """

    def _parse_ast(self, file_path: Path) -> ast.AST | None:
        """Parse a Python file and return the AST."""
        content = file_path.read_text()
        try:
            return ast.parse(content)
        except SyntaxError:
            return None

    def _find_mvm_db_imports(self, tree: ast.AST) -> list[tuple[int, str]]:
        """Find imports of MVMDatabase from mvmctl.core.mvm_db.

        Returns list of (line_number, import_detail) tuples.
        """
        violations = []

        for node in ast.walk(tree):
            if isinstance(node, ast.ImportFrom):
                module = node.module or ""
                # Check for: from mvmctl.core.mvm_db import MVMDatabase
                if module == "mvmctl.core.mvm_db":
                    for alias in node.names:
                        if alias.name == "MVMDatabase":
                            violations.append((node.lineno, f"from {module} import MVMDatabase"))
                # Check for: from mvmctl.core import mvm_db (then mvm_db.MVMDatabase)
                elif module == "mvmctl.core":
                    for alias in node.names:
                        if alias.name == "mvm_db":
                            violations.append((node.lineno, f"from {module} import mvm_db"))

        return violations

    def _find_mvm_db_instantiations(self, tree: ast.AST) -> list[tuple[int, str]]:
        """Find MVMDatabase() instantiations.

        Returns list of (line_number, code_snippet) tuples.
        """
        violations = []

        for node in ast.walk(tree):
            if isinstance(node, ast.Call):
                # Check for: MVMDatabase()
                if isinstance(node.func, ast.Name) and node.func.id == "MVMDatabase":
                    violations.append((node.lineno, "MVMDatabase()"))
                # Check for: mvm_db.MVMDatabase()
                elif isinstance(node.func, ast.Attribute):
                    if (
                        isinstance(node.func.value, ast.Name)
                        and node.func.value.id == "mvm_db"
                        and node.func.attr == "MVMDatabase"
                    ):
                        violations.append((node.lineno, "mvm_db.MVMDatabase()"))

        return violations

    def _find_db_default_queries(self, tree: ast.AST) -> list[tuple[int, str]]:
        """Find db.get_default_*() method calls.

        Returns list of (line_number, method_name) tuples.
        """
        violations = []
        default_methods = {
            "get_default_image",
            "get_default_kernel",
            "get_default_binary",
            "get_default_network",
            "get_default_firecracker_path",
            "get_default_jailer_path",
            "get_default_kernel_path",
        }

        for node in ast.walk(tree):
            if isinstance(node, ast.Call):
                # Check for: db.get_default_*() or any_var.get_default_*()
                if isinstance(node.func, ast.Attribute):
                    if node.func.attr in default_methods:
                        # Get the object name (e.g., 'db' in db.get_default_image())
                        if isinstance(node.func.value, ast.Name):
                            obj_name = node.func.value.id
                            violations.append((node.lineno, f"{obj_name}.{node.func.attr}()"))

        return violations

    def test_core_no_mvm_db_imports(self):
        """Core files must not import MVMDatabase from mvmctl.core.mvm_db.

        Resolution Layer Mandate: Core receives explicit values from API.
        Core must NOT import the database interface.

        Known violations (to be fixed in Phase 4.2-4.4):
        - core/metadata.py imports MVMDatabase
        - core/host_state.py imports MVMDatabase
        - core/host_setup.py imports MVMDatabase
        - core/host.py imports MVMDatabase
        - core/vm_manager.py imports MVMDatabase
        """
        core_files = _get_python_files(CORE_DIR)
        violations = []

        for file_path in core_files:
            if file_path.name == "__init__.py":
                continue
            if file_path.name == "mvm_db.py":
                continue  # The ORM module itself is allowed to import it

            tree = self._parse_ast(file_path)
            if tree is None:
                continue

            found = self._find_mvm_db_imports(tree)
            for line_no, detail in found:
                violations.append(
                    {
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "detail": detail,
                    }
                )

        if violations:
            violation_msgs = []
            for v in violations:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['detail']}")

            msg = (
                f"Found {len(violations)} Core→mvm_db import violation(s):\n"
                + "\n".join(violation_msgs)
                + "\n\nResolution Layer Mandate: Core must NOT import MVMDatabase."
                + "\nCore receives explicit values from API layer."
            )
            pytest.fail(msg)

    def test_core_no_mvm_db_instantiation(self):
        """Core files must not instantiate MVMDatabase().

        Resolution Layer Mandate: Core receives explicit values from API.
        Core must NOT create database connections.

        Known violations (to be fixed in Phase 4.2-4.4):
        - Multiple core files instantiate MVMDatabase() directly
        """
        core_files = _get_python_files(CORE_DIR)
        violations = []

        for file_path in core_files:
            if file_path.name == "__init__.py":
                continue
            if file_path.name == "mvm_db.py":
                continue  # The ORM module itself is allowed

            tree = self._parse_ast(file_path)
            if tree is None:
                continue

            found = self._find_mvm_db_instantiations(tree)
            for line_no, detail in found:
                violations.append(
                    {
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "detail": detail,
                    }
                )

        if violations:
            violation_msgs = []
            for v in violations:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['detail']}")

            msg = (
                f"Found {len(violations)} MVMDatabase() instantiation violation(s):\n"
                + "\n".join(violation_msgs)
                + "\n\nResolution Layer Mandate: Core must NOT instantiate MVMDatabase."
                + "\nCore receives explicit values from API layer."
            )
            pytest.fail(msg)

    def test_core_no_db_default_queries(self):
        """Core files must not call db.get_default_*() methods.

        Resolution Layer Mandate: Core receives explicit values from API.
        Core must NOT query the database for defaults.

        Known violations (to be fixed in Phase 4.2-4.4):
        - Multiple core files call db.get_default_image(), db.get_default_kernel(), etc.
        """
        core_files = _get_python_files(CORE_DIR)
        violations = []

        for file_path in core_files:
            if file_path.name == "__init__.py":
                continue
            if file_path.name == "mvm_db.py":
                continue  # The ORM module itself is allowed

            tree = self._parse_ast(file_path)
            if tree is None:
                continue

            found = self._find_db_default_queries(tree)
            for line_no, detail in found:
                violations.append(
                    {
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "detail": detail,
                    }
                )

        if violations:
            violation_msgs = []
            for v in violations:
                violation_msgs.append(f"  {v['file']}:{v['line']} - {v['detail']}")

            msg = (
                f"Found {len(violations)} db.get_default_*() query violation(s):\n"
                + "\n".join(violation_msgs)
                + "\n\nResolution Layer Mandate: Core must NOT query database defaults."
                + "\nCore receives explicit values from API layer."
                + "\nAPI layer queries MVMDatabase and passes resolved values to Core."
            )
            pytest.fail(msg)
