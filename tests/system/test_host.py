"""Host configuration system tests."""

from __future__ import annotations

import json

import pytest

from tests.system.conftest import _run_mvm

pytestmark = pytest.mark.system


class TestHostStatus:
    """Test host status and inspection operations (non-destructive)."""

    def test_host_ls_basic(self, mvm_binary):
        """Show current host configuration state (table output)."""
        result = _run_mvm(mvm_binary, "host", "ls", check=False)
        # host ls may fail if host not initialized, which is acceptable
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        assert "KVM" in result.stdout or "/dev/kvm" in result.stdout

    def test_host_ls_json(self, mvm_binary):
        """Show current host configuration state in JSON format."""
        result = _run_mvm(mvm_binary, "host", "ls", "--json", check=False)
        if result.returncode != 0:
            pytest.skip("Host not initialized (run 'mvm host init' first)")
        data = json.loads(result.stdout)
        assert "kvm_accessible" in data
        assert "required_binaries" in data
        assert "ip_forward" in data
