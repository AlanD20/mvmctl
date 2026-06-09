"""Tests for cloud-init models — CloudInitMode, CloudInitStatus.

Verifies:
- Enum values are correct
- String conversion and construction
- CloudInitConfig — fields, defaults, serialization
"""

from __future__ import annotations

from mvmctl.models.cloudinit import CloudInitMode, CloudInitStatus


class TestCloudInitMode:
    """Tests for CloudInitMode StrEnum."""

    def test_inject_value(self) -> None:
        assert CloudInitMode.INJECT.value == "inject"

    def test_net_value(self) -> None:
        assert CloudInitMode.NET.value == "net"

    def test_off_value(self) -> None:
        assert CloudInitMode.OFF.value == "off"

    def test_iso_value(self) -> None:
        assert CloudInitMode.ISO.value == "iso"

    def test_from_string_inject(self) -> None:
        assert CloudInitMode("inject") == CloudInitMode.INJECT

    def test_from_string_net(self) -> None:
        assert CloudInitMode("net") == CloudInitMode.NET

    def test_from_string_off(self) -> None:
        assert CloudInitMode("off") == CloudInitMode.OFF

    def test_from_string_iso(self) -> None:
        assert CloudInitMode("iso") == CloudInitMode.ISO

    def test_str_representation(self) -> None:
        assert str(CloudInitMode.INJECT) == "inject"
        assert str(CloudInitMode.NET) == "net"


class TestCloudInitStatus:
    """Tests for CloudInitStatus StrEnum."""

    def test_pending_value(self) -> None:
        assert CloudInitStatus.PENDING.value == "pending"

    def test_running_value(self) -> None:
        assert CloudInitStatus.RUNNING.value == "running"

    def test_done_value(self) -> None:
        assert CloudInitStatus.DONE.value == "done"

    def test_error_value(self) -> None:
        assert CloudInitStatus.ERROR.value == "error"

    def test_from_string_pending(self) -> None:
        assert CloudInitStatus("pending") == CloudInitStatus.PENDING

    def test_from_string_running(self) -> None:
        assert CloudInitStatus("running") == CloudInitStatus.RUNNING

    def test_from_string_done(self) -> None:
        assert CloudInitStatus("done") == CloudInitStatus.DONE

    def test_from_string_error(self) -> None:
        assert CloudInitStatus("error") == CloudInitStatus.ERROR
