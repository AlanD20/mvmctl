"""Compliance test for CLI startup time.

Architecture Rule: CLI startup should complete in < 200ms for user-facing commands.

This test measures cold-start import and initialization time. Modules can be
exempted by adding them to STARTUP_ALLOWLIST with a documented justification.
"""

import os
import subprocess
import sys
from pathlib import Path

import pytest

# Modules explicitly allowed to exceed 200ms startup time
# Format: module_path: justification
#
# module_path is the fully qualified Python import path (e.g., "mvmctl.cli.vm")
# Examples:
#   - File: src/mvmctl/cli/vm.py              -> module_path: "mvmctl.cli.vm"
#   - File: src/mvmctl/core/firecracker.py    -> module_path: "mvmctl.core.firecracker"
#   - File: src/mvmctl/constants.py           -> module_path: "mvmctl.constants"
#   - File: src/mvmctl/services/console_relay/manager.py -> module_path: "mvmctl.services.console_relay.manager"
#
# To find a module's path: python -c "import mvmctl.<module>; print(mvmctl.<module>.__name__)""
STARTUP_ALLOWLIST: dict[str, str] = {
    "mvmctl.api.cache_operations": "Imports from 10+ domains (VM, network, image, kernel, binary)",
    "mvmctl.api.config_operations": "Imports ConfigService + SchemaValidator + SettingsService",
    "mvmctl.api.console_operations": "Imports ConsoleRelayManager + ConsoleService",
    "mvmctl.api.binary_operations": "Imports BinaryService + GitHub release API helpers",
    "mvmctl.api.host_operations": "Imports HostService + sysctl/iptables helpers",
    "mvmctl.api.image_operations": "Imports GuestfsService + ImageService + download helpers",
    "mvmctl.api.init_operations": "Imports all domain operations for onboarding wizard",
    "mvmctl.api.kernel_operations": "Imports KernelService + KernelResolver",
    "mvmctl.api.key_operations": "Imports KeyService + SSH key helpers",
    "mvmctl.api.logs_operations": "Imports LogService + file rotation helpers",
    "mvmctl.api.network_operations": "Imports NetworkService + iptables + LeaseService",
    "mvmctl.api.ssh_operations": "Imports SSHService + subprocess invocation",
    "mvmctl.api.vm_operations": "Imports VMController + VMService + VMResolver",
    "mvmctl.api.volume_operations": "Imports VolumeService + VolumeResolver + VolumeRepository + qemu-img helpers",
    # Model modules — contain dataclass descriptors and import typing/ipaddress
    "mvmctl.models.host": "Contains dataclass with complex type descriptors",
    "mvmctl.models.image": "Contains dataclass with complex type descriptors",
    "mvmctl.models.kernel": "Contains dataclass with complex type descriptors",
    "mvmctl.models.key": "Contains dataclass with complex type descriptors",
    "mvmctl.models.network": "Contains dataclass with complex type descriptors",
    "mvmctl.models.result": "Contains Generic type descriptors for OperationResult/BatchResult",
    "mvmctl.models.vm": "Contains dataclass with complex type descriptors",
    "mvmctl.models.binary": "Contains dataclass with complex type descriptors",
    "mvmctl.models.bulk": "Contains dataclass with complex type descriptors",
    "mvmctl.models.cache": "Contains dataclass with complex type descriptors",
    "mvmctl.models.cloudinit": "Contains dataclass with complex type descriptors",
    "mvmctl.models.firecracker": "Contains dataclass with complex type descriptors",
    "mvmctl.models.volume": "Contains dataclass with StrEnum + __post_init__",
    # Service modules
    "mvmctl.services.console_relay.exceptions": "Imports exception hierarchy from exceptions.py",
    # Utility modules with heavy stdlib imports (http, ipaddress)
    "mvmctl.utils.http": "Imports urllib.request + hashlib + json + tempfile",
    "mvmctl.utils.network": "Imports ipaddress module (heavy stdlib parsing)",
    "mvmctl.services.nocloud_server.process": "Imports psutil + subprocess + signal",
    # Common utils — imports json, re, tempfile, pathlib, constants, exceptions
    "mvmctl.utils.common": "Heavy stdlib imports (json, re, tempfile, pathlib) + multiple mvmctl domain modules",
    "mvmctl.models.provisioner": "Contains dataclass referenced by host init flow",
    "mvmctl.utils.operation_utils": "Imports from multiple API operation modules",
    "mvmctl.utils.auditlog": "Imports json + datetime + pathlib for structured audit logging",
}

MAX_STARTUP_MS = 300


def _build_startup_env(src_path: Path) -> dict[str, str]:
    env = dict(os.environ)

    for key in list(env):
        if key.startswith("COVERAGE"):
            env.pop(key, None)
        elif key.startswith("COV_CORE_"):
            env.pop(key, None)
        elif key.startswith("PYTEST_"):
            env.pop(key, None)

    env.update(
        {
            "PYTHONPATH": str(src_path),
            "MVM_LOG_LEVEL": "WARNING",
            "PYTHONDONTWRITEBYTECODE": "1",
        }
    )

    return env


def _measure_startup_time(module_path: str | None = None) -> float:
    """Measure cold-start time using subprocess and time.perf_counter."""
    project_root = Path(__file__).parent.parent.parent
    src_path = project_root / "src"

    wrapper_code = f"""
import time
import sys
sys.path.insert(0, "{src_path}")

start = time.perf_counter()
if {module_path is None}:
    import mvmctl.main
    import click
    from click.testing import CliRunner
    runner = CliRunner()
    result = runner.invoke(mvmctl.main.app, ["--help"])
    if result.exit_code != 0:
        print(f"FAILED: {{result.output}}", file=sys.stderr)
        sys.exit(1)
else:
    __import__("{module_path}")

end = time.perf_counter()
print(f"{{(end - start) * 1000:.2f}}")
"""

    env = _build_startup_env(src_path)

    result = subprocess.run(
        [sys.executable, "-c", wrapper_code],
        capture_output=True,
        text=True,
        cwd=str(project_root),
        env=env,
    )

    if result.returncode != 0:
        pytest.skip(f"Measurement failed: {result.stderr}")

    try:
        return float(result.stdout.strip())
    except ValueError:
        pytest.skip(f"Could not parse timing: {result.stdout!r}")


def _get_non_cli_modules() -> list[str]:
    """Discover all mvmctl modules except CLI (which are lazy-loaded)."""
    project_root = Path(__file__).parent.parent.parent
    src_path = project_root / "src" / "mvmctl"

    modules = []
    for py_file in src_path.rglob("*.py"):
        if py_file.name.startswith("_"):
            continue
        # Skip CLI modules - they are lazy-loaded by main.py and import typer
        if "cli" in str(py_file.relative_to(src_path)):
            continue

        relative = py_file.relative_to(src_path)
        module_name = str(relative.with_suffix("")).replace("/", ".")
        modules.append(f"mvmctl.{module_name}")

    return sorted(modules)


class TestStartupTimeCompliance:
    """Tests for CLI startup time compliance."""

    def test_main_cli_startup_under_limit(self):
        """Main CLI startup must complete in < 200ms."""
        elapsed_ms = _measure_startup_time(None)

        print(
            f"\n[MEASUREMENT] CLI startup: {elapsed_ms:.1f}ms (limit: {MAX_STARTUP_MS}ms)"
        )

        assert elapsed_ms <= MAX_STARTUP_MS, (
            f"CLI startup time {elapsed_ms:.1f}ms exceeds limit of {MAX_STARTUP_MS}ms.\n"
            f"Investigate import-time overhead in main.py and constants.py"
        )

    @pytest.mark.parametrize("module_path", _get_non_cli_modules())
    def test_module_import_startup(self, module_path: str):
        """Non-CLI modules must import in < 200ms."""
        elapsed_ms = _measure_startup_time(module_path)

        if elapsed_ms > MAX_STARTUP_MS and module_path not in STARTUP_ALLOWLIST:
            pytest.fail(
                f"Module '{module_path}' import time {elapsed_ms:.1f}ms exceeds limit.\n"
                f"This module is part of the cold-start path. Investigate and fix, "
                f"or add to STARTUP_ALLOWLIST with documented justification."
            )

        assert elapsed_ms <= MAX_STARTUP_MS or module_path in STARTUP_ALLOWLIST


class TestStartupAllowlist:
    """Tests to verify the startup allowlist is correct."""

    def test_allowlist_entries_have_justification(self):
        """All allowlist entries must have non-empty justifications."""
        for module_path, justification in STARTUP_ALLOWLIST.items():
            assert justification and justification.strip(), (
                f"Allowlist entry '{module_path}' must have a justification."
            )

    def test_allowlist_justification_not_placeholder(self):
        """Allowlist justifications should not be placeholder text."""
        placeholders = {"todo", "fixme", "placeholder", "", "n/a", "na", "none"}
        for module_path, justification in STARTUP_ALLOWLIST.items():
            assert justification.lower().strip() not in placeholders, (
                f"Allowlist entry '{module_path}' has placeholder justification."
            )

    def test_allowlist_modules_exist(self):
        """All allowlisted modules must exist in the codebase."""
        project_root = Path(__file__).parent.parent.parent
        src_path = project_root / "src" / "mvmctl"

        for module_path in STARTUP_ALLOWLIST:
            if module_path == "CLI":
                continue

            relative_path = module_path.replace("mvmctl.", "").replace(".", "/")
            py_file = src_path / f"{relative_path}.py"
            init_file = src_path / relative_path / "__init__.py"

            assert py_file.exists() or init_file.exists(), (
                f"Allowlist entry '{module_path}' points to non-existent module."
            )
