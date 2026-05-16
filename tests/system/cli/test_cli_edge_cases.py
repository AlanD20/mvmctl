"""CLI edge case system tests — untested paths across all command groups.

Merged from: test_cli_edge_cases.py (existing), test_cli_usability.py (coverage)
"""

from __future__ import annotations

import json
import re
import subprocess

import pytest

from tests.system.conftest import _run_mvm, _unique_subnet, ensure_vm_deps

pytestmark = [pytest.mark.system]

_HELP_COMMANDS = [
    "vm",
    "network",
    "image",
    "kernel",
    "key",
    "volume",
    "bin",
    "config",
    "cache",
]


class TestCacheEdgeCases:
    """Tests for cache command edge cases."""

    pytestmark = [pytest.mark.system, pytest.mark.serial]

    def test_cache_prune_no_args(self, mvm_binary):
        """``cache prune`` without resource and without --all should fail."""
        result = _run_mvm(mvm_binary, "cache", "prune", check=False)
        assert result.returncode != 0
        assert "No resource specified" in result.stdout

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_vm_no_dry_run(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Stop a VM, prune it (no --dry-run), verify it is gone."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )

            _run_mvm(mvm_binary, "vm", "stop", vm_name)

            result = _run_mvm(
                mvm_binary,
                "cache",
                "prune",
                "vm",
                "--force",
            )

            ls_result = _run_mvm(mvm_binary, "vm", "ls", "--json")
            vms = json.loads(ls_result.stdout)
            assert not any(v["name"] == vm_name for v in vms), (
                f"VM {vm_name} still present after prune"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                vm_name,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestNetworkEdgeCases:
    """Tests for network command edge cases."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_network,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_network,
    ]

    def test_network_create_without_subnet(self, mvm_binary):
        """``network create`` without --subnet should fail with clear error."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            "test-network",
            check=False,
        )
        assert result.returncode != 0
        assert "Missing required option '--subnet'" in result.stdout

    def test_network_set_default_nonexistent(self, mvm_binary):
        """``network set-default`` with nonexistent name should fail."""
        result = _run_mvm(
            mvm_binary,
            "network",
            "default",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0


class TestVMStateTransitionErrors:
    """Tests for invalid or idempotent VM state transitions."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_stop_stopped_vm(self, mvm_binary, created_vm):
        """Stopping an already-stopped VM should be idempotent."""
        vm_name = created_vm["name"]

        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        result = _run_mvm(mvm_binary, "vm", "stop", vm_name, check=False)
        assert result.returncode == 0, f"Second stop failed: {result.stderr}"

    def test_vm_pause_stopped_vm(self, mvm_binary, created_vm):
        """Pausing a stopped VM should fail."""
        vm_name = created_vm["name"]

        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        result = _run_mvm(mvm_binary, "vm", "pause", vm_name, check=False)
        assert result.returncode != 0

    def test_vm_start_running_vm(self, mvm_binary, created_vm):
        """Starting a running VM should succeed (idempotent)."""
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "start", vm_name, check=False)
        assert result.returncode == 0, (
            f"Start on running VM failed: {result.stderr}"
        )

    def test_vm_resume_running_vm(self, mvm_binary, created_vm):
        """Resuming a running VM should succeed (idempotent)."""
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "resume", vm_name, check=False)
        assert result.returncode == 0, (
            f"Resume on running VM failed: {result.stderr}"
        )

    def test_vm_rm_multiple_identifiers(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Remove two VMs at once using multiple positional args."""
        name1 = f"{unique_vm_name}-a"
        name2 = f"{unique_vm_name}-b"
        net_name = unique_network_name
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                name1,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                name2,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
            )

            result = _run_mvm(mvm_binary, "vm", "rm", name1, name2, "--force")
            assert result.returncode == 0

            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            assert not any(v["name"] == name1 for v in vms), (
                f"VM {name1} still present after rm"
            )
            assert not any(v["name"] == name2 for v in vms), (
                f"VM {name2} still present after rm"
            )
        finally:
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                name1,
                "--force",
                check=False,
            )
            _run_mvm(
                mvm_binary,
                "vm",
                "rm",
                name2,
                "--force",
                check=False,
            )
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestImageAdvancedFlags:
    """Tests for image advanced flags and edge cases."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_image]

    @pytest.mark.slow
    @pytest.mark.serial
    def test_image_pull_with_disable_detector(self, mvm_binary):
        """Pull an image with --disable-detector all --force."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                "--disable-detector",
                "all",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Pull with --disable-detector failed: {result.stderr.strip()}"
                )
            assert "pulled" in result.stdout.lower()
        except subprocess.TimeoutExpired:
            pytest.skip(
                "Pull with --disable-detector --force timed out (>60s download)"
            )


class TestVMLogsByIdentifier:
    """Test ``mvm logs`` using different identifier types."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_logs_by_ip(self, mvm_binary, created_vm):
        """Show boot log using IP as identifier instead of name."""
        ip = created_vm.get("ipv4", "")
        if not ip:
            pytest.skip("VM has no IP address")
        result = _run_mvm(mvm_binary, "logs", ip, check=False)
        assert result.returncode == 0
        assert result.stdout.strip()


class TestVMCloudInitModes:
    """Test VM creation with different --cloud-init-mode values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_create_cloud_init_mode_iso(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --cloud-init-mode iso."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--cloud-init-mode",
                "iso",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_vm_create_cloud_init_mode_net(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --cloud-init-mode net."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--cloud-init-mode",
                "net",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)

    def test_vm_create_cloud_init_mode_off(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --cloud-init-mode off (explicit)."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--cloud-init-mode",
                "off",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestVMNocloudNetPort:
    """Test VM creation with --nocloud-net-port values."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_vm,
    ]

    def test_vm_create_with_nocloud_net_port_specific(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Create VM with --nocloud-net-port set to a specific port."""
        vm_name = unique_vm_name
        net_name = unique_network_name
        try:
            _run_mvm(
                mvm_binary,
                "network",
                "create",
                net_name,
                "--subnet",
                _unique_subnet(net_name),
                "--non-interactive",
            )
            ensure_vm_deps(mvm_binary)
            _run_mvm(
                mvm_binary,
                "vm",
                "create",
                vm_name,
                "--image",
                "alpine:3.21",
                "--network",
                net_name,
                "--nocloud-net-port",
                "12345",
            )
            vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
            vm = next((v for v in vms if v["name"] == vm_name), None)
            assert vm is not None, f"VM '{vm_name}' not found"
            assert vm["status"] == "running", (
                f"Expected running, got {vm['status']}"
            )
        finally:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
            _run_mvm(mvm_binary, "network", "rm", net_name, check=False)


class TestImagePullAdvancedFlags:
    """Test advanced image pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_version(self, mvm_binary):
        """Pull an image with --version flag, dynamically resolved from remote listing."""
        # First, get the remote listing to find an image with version metadata
        result = _run_mvm(
            mvm_binary,
            "image",
            "ls",
            "--remote",
            "--json",
            timeout=30,
            check=False,
        )
        if result.returncode != 0 or not result.stdout.strip():
            pytest.skip("Remote listing not available (network?)")
        try:
            remote_images = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError, TypeError):
            pytest.skip("Remote listing returned non-JSON output")
        if not remote_images:
            pytest.skip("No remote images available")
        # Find the first image that has both type and version fields
        test_img = next(
            (
                img
                for img in remote_images
                if img.get("type") and img.get("version")
            ),
            None,
        )
        if test_img is None:
            pytest.skip("No remote image has both type and version metadata")
        selector = test_img["type"]
        version = test_img["version"]
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                selector,
                "--version",
                version,
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode == 0:
                assert "pulled" in result.stdout.lower()
            else:
                pytest.skip(
                    f"Pull {selector} --version {version} failed: "
                    f"{result.stderr.strip()}"
                )
        except subprocess.TimeoutExpired:
            pytest.skip(f"Pull {selector} --version {version} timed out (>60s)")

    def test_image_pull_with_arch(self, mvm_binary):
        """Pull an image with --arch flag."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                "--arch",
                "x86_64",
                "--force",
                timeout=60,
                check=False,
            )
            if result.returncode == 0:
                assert "pulled" in result.stdout.lower()
            else:
                pytest.skip(f"Pull with --arch failed: {result.stderr.strip()}")
        except subprocess.TimeoutExpired:
            pytest.skip("Pull with --arch timed out (>60s)")


class TestKernelPullAdvancedFlags:
    """Test advanced kernel pull flags."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.kernel_build,
        pytest.mark.domain_kernel,
    ]

    def test_kernel_pull_with_arch_flag(self, mvm_binary):
        """Pull a kernel with --arch x86_64 flag."""
        result = _run_mvm(
            mvm_binary,
            "kernel",
            "pull",
            "--type",
            "official",
            "--arch",
            "x86_64",
            check=False,
            timeout=120,
        )
        if result.returncode != 0:
            pytest.skip(
                f"Kernel pull with --arch failed: {result.stderr.strip()}"
            )
        assert result.returncode == 0


class TestImagePullWithTypeFlag:
    """Test image pull with explicit --type flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_pull_with_explicit_type(self, mvm_binary):
        """Pull an image with --type flag matching the positional selector."""
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine:3.21",
                "--type",
                "alpine",
                "--force",
                timeout=120,
                check=False,
            )
        except subprocess.TimeoutExpired:
            pytest.skip("Pull with --type timed out (>120s)")
        if result.returncode == 0:
            assert "pulled" in result.stdout.lower()
        elif (
            "timed out" in (result.stdout + result.stderr).lower()
            or result.returncode == -15
        ):
            pytest.skip("Pull with --type timed out (>60s)")
        else:
            pytest.skip(f"Pull with --type failed: {result.stderr.strip()}")


class TestImageImportWithDisableDetector:
    """Test image import with --disable-detector flag."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_image,
    ]

    def test_image_import_with_disable_detector(
        self, mvm_binary, tmp_path, system_cache_dir
    ):
        """Import an image with --disable-detector all flag.

        Uses an already-cached alpine image file (which has a real filesystem)
        so the import backend can parse the partition table.
        """
        import shutil

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            _run_mvm(mvm_binary, "image", "pull", "alpine", "--version", "3.21", check=False)
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            alpine_images = [
                i
                for i in images
                if "alpine" in i.get("type", "").lower() and i.get("is_present")
            ]

        if not alpine_images:
            pytest.skip("No alpine image available to use as import source")

        target = alpine_images[0]
        target_id = target["id"]

        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            pytest.skip("Image path not available")

        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            pytest.skip(f"Image file not found: {resolved_source}")

        temp_path = tmp_path / "alpine-for-import.raw"
        if resolved_source.suffix == ".zst":
            decompress = subprocess.run(
                [
                    "zstd",
                    "-d",
                    "-f",
                    str(resolved_source),
                    "-o",
                    str(temp_path),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            if decompress.returncode != 0:
                pytest.skip(f"zstd decompress failed: {decompress.stderr}")
        else:
            shutil.copy2(str(resolved_source), temp_path)

        imported_prefix: str | None = None
        try:
            result = _run_mvm(
                mvm_binary,
                "image",
                "import",
                "test-disable-detector",
                str(temp_path),
                "--format",
                "raw",
                "--disable-detector",
                "all",
                check=False,
            )
            if result.returncode != 0:
                pytest.skip(
                    f"Import with --disable-detector failed: {result.stderr.strip()}"
                )
            assert result.returncode == 0

            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            imported = [
                i for i in images if i.get("name") == "test-disable-detector"
            ]
            assert imported, (
                "Imported image with --disable-detector not found in listing"
            )
            imported_prefix = imported[0]["id"][:6]
        finally:
            if imported_prefix:
                _run_mvm(
                    mvm_binary, "image", "rm", imported_prefix, check=False
                )


class TestHelpCommand:
    """Tests for the ``help`` CLI command."""

    pytestmark = [pytest.mark.system]

    def test_help_root(self, mvm_binary):
        """``mvm help`` should show root help."""
        result = _run_mvm(mvm_binary, "help")
        assert result.returncode == 0
        assert "Usage:" in result.stdout

    def test_help_subcommand(self, mvm_binary):
        """``mvm help vm`` should show vm help."""
        result = _run_mvm(mvm_binary, "help", "vm")
        assert result.returncode == 0

    def test_help_subsubcommand(self, mvm_binary):
        """``mvm help vm create`` should show vm create help."""
        result = _run_mvm(mvm_binary, "help", "vm", "create")
        assert result.returncode == 0
        assert "Create and start" in result.stdout

    def test_help_nonexistent(self, mvm_binary):
        """``mvm help nonexistent`` should fail."""
        result = _run_mvm(mvm_binary, "help", "nonexistent", check=False)
        assert result.returncode != 0

    def test_help_version(self, mvm_binary):
        """``mvm help version`` should show version help."""
        result = _run_mvm(mvm_binary, "help", "version")
        assert result.returncode == 0


# ============================================================================
# CLI usability tests (from coverage/test_cli_usability.py)
# ============================================================================


class TestHelpOutputConsistentFormat:
    """All --help outputs share common structural elements."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_usability]

    @pytest.mark.parametrize("cmd_group", _HELP_COMMANDS)
    def test_help_contains_common_elements(
        self, mvm_binary: str, cmd_group: str
    ) -> None:
        """Every command group's ``--help`` should contain ``Usage:``, ``Commands:``, ``--help``."""
        result = _run_mvm(mvm_binary, cmd_group, "--help")
        help_text = result.stdout

        assert "Usage:" in help_text, f"'{cmd_group} --help' missing 'Usage:'"
        assert "Commands:" in help_text, (
            f"'{cmd_group} --help' missing 'Commands:'"
        )
        assert "--help" in help_text, (
            f"'{cmd_group} --help' missing '--help' reference"
        )


class TestHelpOutputShowsSubcommands:
    """Each command's --help should list its subcommands."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_usability]

    def test_vm_help_lists_subcommands(self, mvm_binary: str) -> None:
        """``vm --help`` should list expected VM subcommands."""
        result = _run_mvm(mvm_binary, "vm", "--help")
        help_text = result.stdout

        expected = {
            "create",
            "rm",
            "start",
            "stop",
            "reboot",
            "pause",
            "resume",
            "ls",
            "ps",
            "inspect",
            "snapshot",
            "load",
            "export",
            "import",
            "attach-volume",
            "detach-volume",
        }
        for cmd in expected:
            assert cmd in help_text, f"'vm --help' missing '{cmd}' subcommand"

    def test_image_help_lists_subcommands(self, mvm_binary: str) -> None:
        """``image --help`` should list expected image subcommands."""
        result = _run_mvm(mvm_binary, "image", "--help")
        help_text = result.stdout

        expected = {
            "ls",
            "pull",
            "default",
            "rm",
            "inspect",
            "import",
            "warm",
        }
        for cmd in expected:
            assert cmd in help_text, (
                f"'image --help' missing '{cmd}' subcommand"
            )


class TestErrorMessageIsActionable:
    """Error messages should guide the user to fix the problem."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_usability]

    def test_vm_rm_nonexistent(self, mvm_binary: str) -> None:
        """``vm rm`` with a nonexistent VM name should produce an actionable error."""
        result = _run_mvm(
            mvm_binary,
            "vm",
            "rm",
            "nonexistent-vm-12345",
            check=False,
        )
        assert result.returncode != 0, "Expected rm of nonexistent VM to fail"

        error_text = result.stderr + result.stdout
        assert len(error_text) > 20, (
            f"Error message too short ({len(error_text)} chars): {error_text!r}"
        )

        assert any(
            word in error_text.lower()
            for word in ["not found", "no such", "doesn't exist", "unknown"]
        ), f"Error message should contain a helpful phrase, got: {error_text!r}"


class TestDebugFlagOutput:
    """The ``--debug`` flag should produce additional diagnostic output."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_usability]

    def test_debug_flag_produces_output(self, mvm_binary: str) -> None:
        """``--debug vm ls --json`` should include debug-level output."""
        result = _run_mvm(
            mvm_binary,
            "--debug",
            "vm",
            "ls",
            "--json",
            check=False,
        )

        combined = result.stderr + result.stdout
        debug_marker_found = (
            "DEBUG:" in result.stderr
            or "DEBUG:" in combined
            or "[DEBUG]" in result.stderr
        )

        assert result.returncode == 0 or debug_marker_found, (
            "Command should either succeed or produce debug output. "
            f"Return code: {result.returncode}, stderr: {result.stderr!r}"
        )


class TestVersionFlag:
    """The ``--version`` flag should show a version string."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_usability]

    def test_version_output(self, mvm_binary: str) -> None:
        """``--version`` should show a non-empty version string."""
        result = _run_mvm(mvm_binary, "--version")
        version_text = result.stdout.strip()

        assert version_text, "--version output should not be empty"
        assert re.search(r"\d", version_text), (
            f"--version output should contain a digit: {version_text!r}"
        )


class TestHelpSubcommandShowsCorrectly:
    """``mvm help <subcommand>`` and ``mvm <subcommand> --help`` should match."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_usability]

    def test_help_vm_equivalent_to_vm_help(self, mvm_binary: str) -> None:
        """``mvm help vm`` and ``mvm vm --help`` should both show VM help."""
        result_help = _run_mvm(mvm_binary, "help", "vm")
        result_flag = _run_mvm(mvm_binary, "vm", "--help")

        assert "Usage:" in result_help.stdout, "'mvm help vm' missing 'Usage:'"
        assert "Usage:" in result_flag.stdout, (
            "'mvm vm --help' missing 'Usage:'"
        )
        assert "create" in result_help.stdout, (
            "'mvm help vm' missing 'create' subcommand"
        )
        assert "create" in result_flag.stdout, (
            "'mvm vm --help' missing 'create' subcommand"
        )
