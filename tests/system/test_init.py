"""Init wizard system tests."""

from __future__ import annotations

import pytest

from tests.system.conftest import _run_mvm

pytestmark = pytest.mark.system


class TestInitWizard:
    """Test the mvm init wizard (non-destructive, no sudo required with --skip-host)."""

    def test_init_non_interactive_skip_host(self, mvm_binary):
        """Run init in non-interactive mode skipping host setup.

        This should succeed without sudo because --skip-host avoids
        the privileged host-init step.
        """
        result = _run_mvm(
            mvm_binary,
            "init",
            "--non-interactive",
            "--skip-host",
            check=False,
        )

        assert result.returncode == 0
        output = result.stdout + result.stderr
        assert any(
            phrase in output
            for phrase in ("Host ready", "Setup Wizard", "success")
        )
