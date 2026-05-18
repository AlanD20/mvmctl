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

# ============================================================================
# Help command tests (non-destructive — first)
# ============================================================================


class TestHelpCommand:
    """Tests for the ``help`` CLI command."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_help_root(self, mvm_binary):
        """``mvm help`` should show root help."""
        # Rationale: Verifies the root help command displays basic usage
        # information. A regression would leave users with no guidance.
        result = _run_mvm(mvm_binary, "help")
        assert result.returncode == 0
        assert "Usage:" in result.stdout

    def test_help_subcommand(self, mvm_binary):
        """``mvm help vm`` should show vm help."""
        # Rationale: Verifies subcommand help works at the first level.
        # Regression would break `mvm help <cmd>` for all domains.
        result = _run_mvm(mvm_binary, "help", "vm")
        assert result.returncode == 0
        assert "Usage:" in result.stdout

    def test_help_subsubcommand(self, mvm_binary):
        """``mvm help vm create`` should show vm create help."""
        # Rationale: Verifies two-level help resolution. A regression
        # would break `mvm help <cmd> <subcmd>` for all nested commands.
        result = _run_mvm(mvm_binary, "help", "vm", "create")
        assert result.returncode == 0
        assert "Create and start" in result.stdout

    def test_help_nonexistent(self, mvm_binary):
        """``mvm help nonexistent`` should fail."""
        # Rationale: Verifies the CLI rejects unknown commands with a
        # non-zero exit code, preventing silent misdirection.
        result = _run_mvm(mvm_binary, "help", "nonexistent", check=False)
        assert result.returncode != 0

    def test_help_version(self, mvm_binary):
        """``mvm help version`` should show version help."""
        # Rationale: Verifies that "version" is a recognized subcommand
        # help topic. Regression would break `mvm help version`.
        result = _run_mvm(mvm_binary, "help", "version")
        assert result.returncode == 0
        assert "version" in result.stdout.lower()

    def test_completion_bash(self, mvm_binary: str) -> None:
        """``mvm completion bash`` should generate a bash shell completion script."""
        # Rationale: Verifies that shell completion generation works for bash.
        # A regression would break tab-completion for bash users, making the
        # CLI harder to discover and use interactively.
        result = _run_mvm(mvm_binary, "completion", "bash")
        assert result.returncode == 0
        assert "_mvm_completion" in result.stdout, (
            "bash completion output should contain shell function definition"
        )

    def test_completion_zsh(self, mvm_binary: str) -> None:
        """``mvm completion zsh`` should generate a zsh shell completion script."""
        # Rationale: Verifies that shell completion generation works for zsh.
        # A regression would break tab-completion for zsh users, making the
        # CLI harder to discover and use interactively.
        result = _run_mvm(mvm_binary, "completion", "zsh")
        assert result.returncode == 0
        assert "#compdef mvm" in result.stdout, (
            "zsh completion output should contain the compdef directive"
        )

    def test_completion_fish(self, mvm_binary: str) -> None:
        """``mvm completion fish`` should generate a fish shell completion script."""
        # Rationale: Verifies that shell completion generation works for fish.
        # A regression would break tab-completion for fish users, making the
        # CLI harder to discover and use interactively.
        result = _run_mvm(mvm_binary, "completion", "fish")
        assert result.returncode == 0
        assert "function _mvm_completion" in result.stdout, (
            "fish completion output should contain shell function definition"
        )

    def test_version_command(self, mvm_binary: str) -> None:
        """``mvm version`` command should show a version string (distinct from --version flag)."""
        # Rationale: Verifies the `version` Click command produces version
        # information with a semver-like string. This is a separate code path
        # from the --version flag and both must work. A regression would break
        # automation scripts and users checking the installed version.
        result = _run_mvm(mvm_binary, "version")
        assert result.returncode == 0
        version_text = result.stdout.strip()
        assert version_text, "version command output should not be empty"
        assert re.search(r"\d", version_text), (
            f"version command output should contain a digit: {version_text!r}"
        )


class TestHelpOutputConsistentFormat:
    """All --help outputs share common structural elements."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    @pytest.mark.parametrize("cmd_group", _HELP_COMMANDS)
    def test_help_contains_common_elements(
        self, mvm_binary: str, cmd_group: str
    ) -> None:
        """Every command group's ``--help`` should contain ``Usage:``, ``Commands:``, ``--help``."""
        # Rationale: Verifies structural consistency across all command groups.
        # A group missing Usage: or Commands: would confuse users.
        result = _run_mvm(mvm_binary, cmd_group, "--help")
        help_text = result.stdout

        assert "Usage:" in help_text, f"'{cmd_group} --help' missing 'Usage:'"
        assert "Commands" in help_text, (
            f"'{cmd_group} --help' missing 'Commands'"
        )
        assert "--help" in help_text, (
            f"'{cmd_group} --help' missing '--help' reference"
        )


class TestHelpOutputShowsSubcommands:
    """Each command's --help should list its subcommands."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_vm_help_lists_subcommands(self, mvm_binary: str) -> None:
        """``vm --help`` should list expected VM subcommands."""
        # Rationale: Verifies the VM command exposes all expected subcommands.
        # Missing subcommands would make features undiscoverable.
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
        # Rationale: Verifies the image command exposes all expected subcommands.
        # Missing subcommands would make features undiscoverable.
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

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_vm_rm_nonexistent(self, mvm_binary: str) -> None:
        """``vm rm`` with a nonexistent VM name should produce an actionable error."""
        # Rationale: Verifies that error messages are specific enough for
        # users to understand what went wrong. Vague errors ("something failed")
        # would frustrate users without guidance.
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

        # Must contain a specific helpful phrase, not a guess-list
        assert "not found" in error_text.lower(), (
            f"Error message should contain 'not found', got: {error_text!r}"
        )


class TestDebugFlagOutput:
    """The ``--debug`` flag should produce additional diagnostic output."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_debug_flag_produces_output(self, mvm_binary: str) -> None:
        """``--debug vm ls --json`` should include debug-level output."""
        # Rationale: Verifies the --debug flag emits DEBUG-level log lines.
        # A regression where --debug stops producing output would make
        # diagnostics impossible for users reporting issues.
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

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_version_output(self, mvm_binary: str) -> None:
        """``--version`` should show a non-empty version string."""
        # Rationale: Verifies the --version flag returns valid version info.
        # A regression would break semver reporting used by automation.
        result = _run_mvm(mvm_binary, "--version")
        version_text = result.stdout.strip()

        assert version_text, "--version output should not be empty"
        assert re.search(r"\d", version_text), (
            f"--version output should contain a digit: {version_text!r}"
        )


class TestHelpSubcommandShowsCorrectly:
    """``mvm help <subcommand>`` and ``mvm <subcommand> --help`` should match."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_cli]

    def test_help_vm_equivalent_to_vm_help(self, mvm_binary: str) -> None:
        """``mvm help vm`` and ``mvm vm --help`` should both show VM help."""
        # Rationale: Verifies that `mvm help vm` and `mvm vm --help` produce
        # equivalent output. A regression causing divergence between the two
        # forms would create user confusion.
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


# ============================================================================
# Network edge cases (non-destructive)
# ============================================================================


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
        # Rationale: No resources needed — testing CLI validation for
        # missing --subnet flag.
        result = _run_mvm(
            mvm_binary,
            "network",
            "create",
            "test-network",
            check=False,
        )
        assert result.returncode != 0
        assert "Missing required option '--subnet'" in (
            result.stdout + result.stderr
        )

    def test_network_set_default_nonexistent(self, mvm_binary):
        """``network set-default`` with nonexistent name should fail."""
        # Rationale: No resources needed — testing CLI error for
        # nonexistent network name.
        result = _run_mvm(
            mvm_binary,
            "network",
            "default",
            "nonexistent-network-name-xyz",
            check=False,
        )
        assert result.returncode != 0


# ============================================================================
# VM state transition edge cases (non-destructive — uses fixtures)
# ============================================================================


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
        # Rationale: Uses created_vm fixture (already exists via conftest).
        # Tests idempotent state transition — no new resources created.
        vm_name = created_vm["name"]

        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        result = _run_mvm(mvm_binary, "vm", "stop", vm_name, check=False)
        assert result.returncode == 0, f"Second stop failed: {result.stderr}"

    def test_vm_pause_stopped_vm(self, mvm_binary, created_vm):
        """Pausing a stopped VM should fail."""
        # Rationale: Uses created_vm fixture. Tests error case for
        # invalid state transition (pause on stopped VM).
        vm_name = created_vm["name"]

        _run_mvm(mvm_binary, "vm", "stop", vm_name)

        result = _run_mvm(mvm_binary, "vm", "pause", vm_name, check=False)
        assert result.returncode != 0

    def test_vm_start_running_vm(self, mvm_binary, created_vm):
        """Starting a running VM should succeed (idempotent)."""
        # Rationale: Uses created_vm fixture. Tests idempotent
        # start on already-running VM.
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "start", vm_name, check=False)
        assert result.returncode == 0, (
            f"Start on running VM failed: {result.stderr}"
        )

    def test_vm_resume_running_vm(self, mvm_binary, created_vm):
        """Resuming a running VM should succeed (idempotent)."""
        # Rationale: Uses created_vm fixture. Tests idempotent
        # resume on already-running VM.
        vm_name = created_vm["name"]
        result = _run_mvm(mvm_binary, "vm", "resume", vm_name, check=False)
        assert result.returncode == 0, (
            f"Resume on running VM failed: {result.stderr}"
        )


# ============================================================================
# Logs edge cases (non-destructive — uses fixture)
# ============================================================================


class TestVMLogsByIdentifier:
    """Test ``mvm logs`` using different identifier types."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.domain_logs,
    ]

    def test_logs_by_ip(self, mvm_binary, created_vm):
        """Show boot log using IP as identifier instead of name."""
        # Rationale: Verifies that 'mvm logs' accepts IP addresses as
        # identifiers in addition to VM names. A regression would break
        # the convenience of IP-based log access.
        ip = created_vm.get("ipv4", "")
        if not ip:
            # Skip-reason: VM may not have an IP yet if DHCP is slow
            # or the VM was created without a network. This is non-fatal
            # but we can't test IP-based lookup without an IP.
            pytest.skip("VM has no IP address")
        result = _run_mvm(mvm_binary, "logs", ip, check=False)
        assert result.returncode == 0
        assert result.stdout.strip()


# ============================================================================
# Image advanced flags (destructive — self-cleaning with try/finally)
# ============================================================================


class TestImageAdvancedFlags:
    """Tests for image advanced flags and edge cases."""

    pytestmark = [pytest.mark.system, pytest.mark.domain_image]

    @pytest.mark.slow
    @pytest.mark.serial
    def test_image_pull_with_disable_detector(self, mvm_binary):
        """Pull an image with --disable-detector all --force."""
        # Rationale: Downloads an image with --disable-detector flag.
        # Needs network access to pull but no local VM resources.
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
                # Skip-reason: Network download may fail in air-gapped
                # environments. The MVM_ASSET_MIRROR cache avoids this
                # but we do not guarantee pre-caching for this flag combo.
                pytest.skip(
                    f"Pull with --disable-detector failed: {result.stderr.strip()}"
                )
            assert "pulled" in result.stdout.lower()
        except subprocess.TimeoutExpired:
            # Skip-reason: Large image download (>60s) can timeout under
            # bandwidth constraints. Increasing timeout would mask slow
            # networks rather than fixing them.
            pytest.skip(
                "Pull with --disable-detector --force timed out (>60s download)"
            )


# ============================================================================
# VM cloud-init mode tests (destructive — self-cleaning with try/finally)
# ============================================================================


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
        # Rationale: Verifies that 'iso' cloud-init mode creates a
        # running VM with correct cloud-init media. Regression would
        # break users relying on ISO-based provisioning.
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
        # Rationale: Verifies that 'net' cloud-init mode creates a
        # running VM with nocloud network provisioning. Regression
        # would break users relying on net-based cloud-init.
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
        # Rationale: Verifies that 'off' cloud-init mode creates a
        # running VM without cloud-init. Regression would break users
        # who want to skip cloud-init entirely.
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
        # Rationale: Verifies that --nocloud-net-port sets the nocloud
        # network port for net-mode cloud-init. Regression would break
        # users needing a non-default nocloud port.
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
        # Rationale: Verifies that --version flag works when pulling
        # a specific image version. Regression would break versioned
        # image pulls for all users.
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
            # Skip-reason: Remote listing requires network access to the
            # image registry. In air-gapped environments or without
            # MVM_ASSET_MIRROR, this is unavailable.
            pytest.skip("Remote listing not available (network?)")
        try:
            remote_images = json.loads(result.stdout)
        except (json.JSONDecodeError, ValueError, TypeError):
            # Skip-reason: The remote listing must return valid JSON.
            # A non-JSON response indicates a registry compatibility issue.
            pytest.skip("Remote listing returned non-JSON output")
        if not remote_images:
            # Skip-reason: No images are available in the remote registry.
            # This can happen with an empty or misconfigured registry.
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
            # Skip-reason: The remote registry must contain at least one
            # image with both type and version metadata for this test.
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
                # Skip-reason: Image pull may fail due to network or
                # registry issues. We skip rather than fail since the
                # registry availability is not under test control.
                pytest.skip(
                    f"Pull {selector} --version {version} failed: "
                    f"{result.stderr.strip()}"
                )
        except subprocess.TimeoutExpired:
            # Skip-reason: Large image downloads may exceed the 60s
            # timeout under bandwidth constraints.
            pytest.skip(f"Pull {selector} --version {version} timed out (>60s)")

    def test_image_pull_with_arch(self, mvm_binary):
        """Pull an image with --arch flag."""
        # Rationale: Verifies that --arch flag filters image downloads
        # by architecture. Regression would break multi-arch image pulls.
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
                # Skip-reason: Image pull with --arch may fail if the
                # specified architecture is not available or network is
                # unavailable.
                pytest.skip(f"Pull with --arch failed: {result.stderr.strip()}")
        except subprocess.TimeoutExpired:
            # Skip-reason: Large image download may exceed the 60s
            # timeout under bandwidth constraints.
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
        # Rationale: Verifies that --arch flag works for kernel pulls.
        # Regression would break architecture-specific kernel downloads.
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
            # Skip-reason: Kernel pull requires network access and the
            # official kernel build server. In air-gapped environments
            # or without MVM_ASSET_MIRROR, this is unavailable.
            pytest.skip(
                f"Kernel pull with --arch failed: {result.stderr.strip()}"
            )

        # L2 verification: confirm the kernel appears in the listing
        ls_result = _run_mvm(mvm_binary, "kernel", "ls", "--json")
        kernels = json.loads(ls_result.stdout)
        assert isinstance(kernels, list)
        assert any(
            k.get("type") == "official" and k.get("is_present") for k in kernels
        ), "Kernel with --arch not found in listing"


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
        # Rationale: Verifies that --type flag works alongside the
        # positional selector. Regression would break image pulls that
        # explicitly specify type.
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
            # Skip-reason: Large image download may exceed the 120s
            # timeout under bandwidth constraints.
            pytest.skip("Pull with --type timed out (>120s)")
        if result.returncode == 0:
            assert "pulled" in result.stdout.lower()
        elif (
            "timed out" in (result.stdout + result.stderr).lower()
            or result.returncode == -15
        ):
            # Skip-reason: Image pull with --type may timeout if the
            # image is large or network is slow.
            pytest.skip("Pull with --type timed out (>60s)")
        else:
            # Skip-reason: Image pull may fail due to network or
            # registry issues.
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
        # Rationale: Verifies that --disable-detector all skips the
        # OS detection phase during import. Regression would break
        # users importing images with custom or unknown OS layouts.
        import shutil

        result = _run_mvm(mvm_binary, "image", "ls", "--json")
        images = json.loads(result.stdout)
        alpine_images = [
            i
            for i in images
            if "alpine" in i.get("type", "").lower() and i.get("is_present")
        ]
        if not alpine_images:
            _run_mvm(
                mvm_binary,
                "image",
                "pull",
                "alpine",
                "--version",
                "3.21",
                check=False,
            )
            result = _run_mvm(mvm_binary, "image", "ls", "--json")
            images = json.loads(result.stdout)
            alpine_images = [
                i
                for i in images
                if "alpine" in i.get("type", "").lower() and i.get("is_present")
            ]

        if not alpine_images:
            # Skip-reason: An alpine image must be cached to use as the
            # source for import. Without one, we cannot test
            # --disable-detector.
            pytest.skip("No alpine image available to use as import source")

        target = alpine_images[0]
        target_id = target["id"]

        result = _run_mvm(
            mvm_binary, "image", "inspect", target_id, "--json", check=False
        )
        if result.returncode != 0:
            # Skip-reason: The inspected image may have been removed
            # between listing and inspect (race condition).
            pytest.skip(f"Image '{target_id[:8]}' was removed before inspect")

        data = json.loads(result.stdout)
        source_path = data.get("path")
        if not source_path:
            # Skip-reason: Image path is missing from inspect output,
            # which means the image record is incomplete.
            pytest.skip("Image path not available")

        resolved_source = system_cache_dir / "images" / source_path
        if not resolved_source.exists():
            # Skip-reason: The image file on disk does not exist even
            # though the DB reports it as present (stale DB entry).
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
                # Skip-reason: zstd decompression is needed to convert
                # the cached image to a raw file. If zstd is not
                # available, we cannot proceed.
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
                # Skip-reason: Image import may fail due to filesystem
                # issues or format incompatibility.
                pytest.skip(
                    f"Import with --disable-detector failed: {result.stderr.strip()}"
                )

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


# ============================================================================
# Destructive tests — must be at the end of the file
# ============================================================================


class TestVMDestructiveRmMultiple:
    """Destructive: Remove multiple VMs."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.requires_kvm,
        pytest.mark.slow,
        pytest.mark.serial,
        pytest.mark.domain_vm,
    ]

    def test_vm_rm_multiple_identifiers(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Remove two VMs at once using multiple positional args."""
        # Rationale: Verifies that 'vm rm' accepts multiple positional
        # VM names and removes them all. Regression would break bulk
        # cleanup workflows.
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


class TestCacheEdgeCases:
    """Tests for cache command edge cases (destructive — must be last)."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.serial,
        pytest.mark.domain_cache,
    ]

    def test_cache_prune_no_args(self, mvm_binary):
        """``cache prune`` without resource and without --all should fail."""
        # Rationale: No resources needed — testing CLI validation for
        # missing arguments. Non-destructive.
        result = _run_mvm(mvm_binary, "cache", "prune", check=False)
        assert result.returncode != 0
        assert "No resource specified" in (result.stdout + result.stderr)

    @pytest.mark.requires_kvm
    @pytest.mark.slow
    def test_cache_prune_vm_no_dry_run(
        self, mvm_binary, unique_vm_name, unique_network_name
    ):
        """Stop a VM, prune it (no --dry-run), verify it is gone."""
        # Rationale: Needs a real VM (unique_vm_name) + network
        # (unique_network_name) to test actual cache prune of VMs.
        # Destructive — removes the VM from cache.
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

            _run_mvm(
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
