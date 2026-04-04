from unittest.mock import patch

from mvmctl.cli._helpers import get_combined_marker, is_bridge_alive, is_vm_process_running


def test_is_vm_process_running_none_pid():
    assert is_vm_process_running(None) is False


def test_is_bridge_alive_file_not_found():
    with patch("mvmctl.cli._helpers.subprocess.run", side_effect=FileNotFoundError):
        assert is_bridge_alive("mvm-nonexistent") is False


def test_get_combined_marker_default_and_missing():
    assert get_combined_marker(is_default=True, is_missing=True) == "*X "


def test_get_combined_marker_missing_not_default():
    assert get_combined_marker(is_default=False, is_missing=True) == " X "


def test_get_combined_marker_default_not_missing():
    assert get_combined_marker(is_default=True, is_missing=False) == "*  "


def test_get_combined_marker_neither():
    assert get_combined_marker(is_default=False, is_missing=False) == "   "
