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
                    violations.append({
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "import": import_path,
                        "type": import_type,
                    })
        
        if violations:
            violation_msgs = []
            for v in violations:
                violation_msgs.append(
                    f"  {v['file']}:{v['line']} - {v['type']} {v['import']}"
                )
            
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
                    core_imports.append({
                        "file": _get_relative_path(file_path),
                        "line": line_no,
                        "import": import_path,
                    })
        
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
            ("mvmctl.core.metadata.find_images_by_short_id", True),
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
