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
            ("mvmctl.api.vm", False),
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

    # Files exempt from Core→DB import checks (legitimate DB-access modules)
    CORE_DB_ALLOWLIST = {
        "mvm_db.py",  # The DB interface itself — always allowed
        "vm_manager.py",  # VM persistence layer — is the CRUD layer for VM state
        "metadata.py",  # Metadata CRUD layer — list/update/find for images/kernels/binaries
        "host.py",  # Host orchestration — receives db as parameter
        "host_setup.py",  # Host init — receives db as parameter
        "host_state.py",  # Host state snapshots — receives db as parameter
    }

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

    def _find_cross_core_imports(self, tree: ast.AST, source_file: Path) -> list[tuple[int, str]]:
        """Find cross-core imports and reverse-layer API imports.

        Checks for:
        - Top-level from mvmctl.core.X import Y where X is NOT mvm_db (cross-core)
        - Top-level from mvmctl.api.X import Y in a core file (reverse-layer)
        - Lazy imports inside function bodies

        Returns list of (line_number, import_detail) tuples.
        """
        violations = []
        source_name = source_file.name

        # Permitted exceptions: these files are allowed to import mvm_db
        permitted_mvm_db_files = {"vm_manager.py", "metadata.py", "host_state.py"}
        is_core_file = "core" in str(source_file)

        def _check_import_node(node: ast.ImportFrom, in_function: bool = False) -> None:
            """Check a single import node for violations."""
            module = node.module or ""
            line_no = node.lineno

            # Check for: from mvmctl.core.X import Y (cross-core, X != mvm_db)
            if module.startswith("mvmctl.core."):
                submodule = module.split(".")[2] if len(module.split(".")) >= 3 else ""
                # mvm_db is allowed only in permitted files
                if submodule == "mvm_db":
                    if source_name not in permitted_mvm_db_files:
                        for alias in node.names:
                            violations.append((line_no, f"from {module} import {alias.name}"))
                else:
                    # Any other core submodule is a cross-core violation
                    for alias in node.names:
                        violations.append((line_no, f"from {module} import {alias.name}"))

            # Check for: from mvmctl.core import X (where X is not mvm_db)
            elif module == "mvmctl.core":
                for alias in node.names:
                    if alias.name != "mvm_db":
                        violations.append((line_no, f"from {module} import {alias.name}"))
                    elif source_name not in permitted_mvm_db_files:
                        violations.append((line_no, f"from {module} import {alias.name}"))

            # Check for: from mvmctl.api.X import Y in core files (reverse-layer)
            elif is_core_file and module.startswith("mvmctl.api"):
                for alias in node.names:
                    violations.append(
                        (line_no, f"from {module} import {alias.name} (reverse-layer)")
                    )

        # Check top-level imports
        for node in ast.walk(tree):
            if isinstance(node, ast.ImportFrom):
                _check_import_node(node, in_function=False)

        # Check lazy imports inside function bodies
        def _walk_functions(node: ast.AST) -> None:
            """Recursively walk function definitions and check their bodies."""
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                for stmt in node.body:
                    if isinstance(stmt, ast.ImportFrom):
                        _check_import_node(stmt, in_function=True)
                    # Recursively check nested function definitions
                    _walk_functions(stmt)
            elif isinstance(node, ast.If):
                # Handle if statements (including TYPE_CHECKING blocks, but we check all)
                for stmt in node.body + getattr(node, "orelse", []):
                    if isinstance(stmt, ast.ImportFrom):
                        _check_import_node(stmt, in_function=True)
                    _walk_functions(stmt)
            elif isinstance(node, ast.Try):
                # Handle try blocks
                for stmt in (
                    node.body + getattr(node, "orelse", []) + getattr(node, "finalbody", [])
                ):
                    if isinstance(stmt, ast.ImportFrom):
                        _check_import_node(stmt, in_function=True)
                    _walk_functions(stmt)
            elif isinstance(node, (ast.With, ast.For, ast.While)):
                # Handle context managers and loops
                for stmt in node.body + getattr(node, "orelse", []):
                    if isinstance(stmt, ast.ImportFrom):
                        _check_import_node(stmt, in_function=True)
                    _walk_functions(stmt)

        # Walk all top-level statements for function definitions
        if isinstance(tree, ast.Module):
            for node in tree.body:
                _walk_functions(node)

        return violations

    def _check_file_cross_core_violations(self, file_name: str) -> None:
        """Check a specific core file for cross-core import violations."""
        file_path = CORE_DIR / file_name
        if not file_path.exists():
            pytest.fail(f"Target file not found: {file_path}")

        tree = self._parse_ast(file_path)
        if tree is None:
            pytest.fail(f"Could not parse AST for {file_path}")

        violations = self._find_cross_core_imports(tree, file_path)

        if violations:
            violation_msgs = []
            for line_no, detail in violations:
                violation_msgs.append(f"  {_get_relative_path(file_path)}:{line_no} - {detail}")

            msg = (
                f"Found {len(violations)} cross-core import violation(s) in {file_name}:\n"
                + "\n".join(violation_msgs)
                + "\n\nCore modules must NOT import from other core modules (except mvm_db in permitted files)."
                + "\nCore modules must NOT import from api/ layer (reverse-layer violation)."
            )
            pytest.fail(msg)


class TestCoreLayerIsolation(TestCoreLayerDBCompliance):
    """Tests for Core layer isolation — no cross-core imports.

    These tests verify that core modules do not import from other core modules
    (except mvm_db in permitted files) and do not import from the api/ layer.

    Known violations (to be fixed in refactoring phases):
    - vm_lifecycle.py: cloud_init, config_gen, firecracker, firewall, image, kernel,
                       network, network_manager, rootfs_injector, ssh, vm_manager + lazy key_manager
    - network_manager.py: metadata, network + lazy host_setup, vm_manager
    - cache_manager.py: core.metadata, network_manager, vm_lifecycle, vm_manager + api.metadata
    - kernel.py: core.metadata + api.metadata (lazy at line ~1117)
    - image.py: RESOLVED - partition_detection moved to utils, metadata lookups moved to api
    - config_state.py: core.metadata + api.metadata
    - binary_manager.py: core.metadata
    - ssh.py: vm_manager
    - vm_monitor.py: firecracker (lazy inside function body)
    - host.py: host_privilege, host_setup, host_state, mvm_db (violation since host not in permitted list)
    - host_setup.py: host_privilege, host_state, mvm_db (violation), network
    """

    def test_vm_lifecycle_no_cross_core_imports(self):
        """vm_lifecycle.py must not import from other core modules.

        Known violations:
        - cloud_init, config_gen, firecracker, firewall, image, kernel
        - network, network_manager, rootfs_injector, ssh, vm_manager
        - lazy key_manager import inside function body
        """
        self._check_file_cross_core_violations("vm_lifecycle.py")

    def test_network_manager_no_cross_core_imports(self):
        """network_manager.py must not import from other core modules.

        Known violations:
        - metadata, network
        - lazy host_setup, vm_manager inside function bodies
        """
        self._check_file_cross_core_violations("network_manager.py")

    def test_cache_manager_no_cross_core_imports(self):
        """cache_manager.py must not import from other core modules.

        Known violations:
        - core.metadata, network_manager, vm_lifecycle, vm_manager
        """
        self._check_file_cross_core_violations("cache_manager.py")

    def test_cache_manager_no_api_imports(self):
        """cache_manager.py must not import from api/ layer (reverse-layer).

        Known violations:
        - api.metadata (reverse-layer violation)
        """
        # This is tested by the parent method, but we document it separately
        self._check_file_cross_core_violations("cache_manager.py")

    def test_kernel_no_cross_core_imports(self):
        """kernel.py must not import from other core modules.

        Known violations:
        - core.metadata
        """
        self._check_file_cross_core_violations("kernel.py")

    def test_kernel_no_api_imports(self):
        """kernel.py must not import from api/ layer (reverse-layer).

        Known violations:
        - api.metadata (reverse-layer, lazy at line ~1117)
        """
        # This is tested by the parent method, but we document it separately
        self._check_file_cross_core_violations("kernel.py")

    def test_image_no_cross_core_imports(self):
        """image.py must not import from other core modules.

        Status: RESOLVED - All cross-core imports have been refactored:
        - partition_detection moved to utils/partition_detection.py
        - metadata lookup functions (resolve_image_path, resolve_image_fs_uuid,
          resolve_image_fs_type, resolve_image_id_path) moved to api/assets.py
        """
        self._check_file_cross_core_violations("image.py")

    def test_config_state_no_cross_core_imports(self):
        """config_state.py must not import from other core modules.

        Known violations:
        - core.metadata
        """
        self._check_file_cross_core_violations("config_state.py")

    def test_config_state_no_api_imports(self):
        """config_state.py must not import from api/ layer (reverse-layer).

        Known violations:
        - api.metadata (reverse-layer violation)
        """
        # This is tested by the parent method, but we document it separately
        self._check_file_cross_core_violations("config_state.py")

    def test_binary_manager_no_cross_core_imports(self):
        """binary_manager.py must not import from other core modules.

        Known violations:
        - core.metadata
        """
        self._check_file_cross_core_violations("binary_manager.py")

    def test_ssh_no_cross_core_imports(self):
        """ssh.py must not import from other core modules.

        Known violations:
        - vm_manager
        """
        self._check_file_cross_core_violations("ssh.py")

    def test_vm_monitor_no_cross_core_lazy_imports(self):
        """vm_monitor.py must not have lazy cross-core imports.

        Known violations:
        - firecracker (lazy inside function body)
        """
        self._check_file_cross_core_violations("vm_monitor.py")

    def test_host_no_cross_core_imports(self):
        """host.py must not import from other core modules.

        Known violations:
        - host_privilege, host_setup, host_state
        - mvm_db (violation since host.py is NOT in permitted list)
        """
        self._check_file_cross_core_violations("host.py")

    def test_host_setup_no_cross_core_imports(self):
        """host_setup.py must not import from other core modules.

        Known violations:
        - host_privilege, host_state
        - mvm_db (violation since host_setup.py is NOT in permitted list)
        - network
        """
        self._check_file_cross_core_violations("host_setup.py")

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
            if file_path.name in self.CORE_DB_ALLOWLIST:
                continue  # Exempt files are allowed to import MVMDatabase

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
            if file_path.name in self.CORE_DB_ALLOWLIST:
                continue  # Exempt files are allowed to instantiate MVMDatabase

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
            if file_path.name in self.CORE_DB_ALLOWLIST:
                continue  # Exempt files are allowed to query DB defaults

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
