"""Tests for cloud-init models."""

from pathlib import Path

from mvmctl.models.cloud_init import CloudInitConfig, CloudInitMode, CloudInitStatus


class TestCloudInitMode:
    """Tests for CloudInitMode StrEnum."""

    def test_auto_value(self):
        assert CloudInitMode.INJECT == "inject"

    def test_custom_value(self):
        assert CloudInitMode.ISO == "iso"

    def test_disabled_value(self):
        assert CloudInitMode.OFF == "off"

    def test_nocloud_net_value(self):
        assert CloudInitMode.NET == "net"

    def test_from_string_auto(self):
        assert CloudInitMode("inject") == CloudInitMode.INJECT

    def test_from_string_custom(self):
        assert CloudInitMode("iso") == CloudInitMode.ISO

    def test_from_string_disabled(self):
        assert CloudInitMode("off") == CloudInitMode.OFF

    def test_from_string_nocloud_net(self):
        assert CloudInitMode("net") == CloudInitMode.NET


class TestCloudInitStatus:
    """Tests for CloudInitStatus StrEnum."""

    def test_pending_value(self):
        assert CloudInitStatus.PENDING == "pending"

    def test_running_value(self):
        assert CloudInitStatus.RUNNING == "running"

    def test_done_value(self):
        assert CloudInitStatus.DONE == "done"

    def test_error_value(self):
        assert CloudInitStatus.ERROR == "error"

    def test_from_string_pending(self):
        assert CloudInitStatus("pending") == CloudInitStatus.PENDING

    def test_from_string_running(self):
        assert CloudInitStatus("running") == CloudInitStatus.RUNNING

    def test_from_string_done(self):
        assert CloudInitStatus("done") == CloudInitStatus.DONE

    def test_from_string_error(self):
        assert CloudInitStatus("error") == CloudInitStatus.ERROR


class TestCloudInitConfig:
    """Tests for CloudInitConfig dataclass."""

    def test_default_construction(self):
        """Test construction with all defaults."""
        config = CloudInitConfig()
        assert config.mode == CloudInitMode.INJECT
        assert config.iso_path is None
        assert config.keep_iso is False
        assert config.nocloud_net_url is None

    def test_custom_mode(self):
        """Test construction with CUSTOM mode."""
        config = CloudInitConfig(mode=CloudInitMode.ISO)
        assert config.mode == CloudInitMode.ISO

    def test_disabled_mode(self):
        """Test construction with DISABLED mode."""
        config = CloudInitConfig(mode=CloudInitMode.OFF)
        assert config.mode == CloudInitMode.OFF

    def test_nocloud_net_mode(self):
        """Test construction with NO_CLOUD_NET mode."""
        config = CloudInitConfig(mode=CloudInitMode.NET)
        assert config.mode == CloudInitMode.NET

    def test_with_iso_path(self):
        """Test construction with iso_path set."""
        path = Path("/path/to/cloud-init.iso")
        config = CloudInitConfig(iso_path=path)
        assert config.iso_path == path

    def test_with_keep_iso_true(self):
        """Test construction with keep_iso=True."""
        config = CloudInitConfig(keep_iso=True)
        assert config.keep_iso is True

    def test_with_nocloud_net_url(self):
        """Test construction with nocloud_net_url set."""
        url = "http://10.0.0.1:8080/"
        config = CloudInitConfig(nocloud_net_url=url)
        assert config.nocloud_net_url == url

    def test_full_construction(self):
        """Test construction with all fields specified."""
        path = Path("/path/to/iso")
        config = CloudInitConfig(
            mode=CloudInitMode.ISO,
            iso_path=path,
            keep_iso=True,
            nocloud_net_url="http://example.com/",
        )
        assert config.mode == CloudInitMode.ISO
        assert config.iso_path == path
        assert config.keep_iso is True
        assert config.nocloud_net_url == "http://example.com/"


class TestCloudInitConfigSerialization:
    """Tests for CloudInitConfig to_dict/from_dict methods."""

    def test_to_dict_defaults(self):
        """Test serialization with default values."""
        config = CloudInitConfig()
        data = config.to_dict()
        assert data == {
            "mode": "inject",
            "iso_path": None,
            "keep_iso": False,
            "nocloud_net_url": None,
        }

    def test_to_dict_with_values(self):
        """Test serialization with custom values."""
        config = CloudInitConfig(
            mode=CloudInitMode.ISO,
            iso_path=Path("/path/to/iso"),
            keep_iso=True,
            nocloud_net_url="http://10.0.0.1:8080/",
        )
        data = config.to_dict()
        assert data == {
            "mode": "iso",
            "iso_path": "/path/to/iso",
            "keep_iso": True,
            "nocloud_net_url": "http://10.0.0.1:8080/",
        }

    def test_to_dict_disabled_mode(self):
        """Test serialization with DISABLED mode."""
        config = CloudInitConfig(mode=CloudInitMode.OFF)
        data = config.to_dict()
        assert data["mode"] == "off"

    def test_to_dict_nocloud_net_mode(self):
        """Test serialization with NO_CLOUD_NET mode."""
        config = CloudInitConfig(mode=CloudInitMode.NET)
        data = config.to_dict()
        assert data["mode"] == "net"

    def test_from_dict_defaults(self):
        """Test deserialization with minimal data."""
        data = {}
        config = CloudInitConfig.from_dict(data)
        assert config.mode == CloudInitMode.INJECT
        assert config.iso_path is None
        assert config.keep_iso is False
        assert config.nocloud_net_url is None

    def test_from_dict_full(self):
        """Test deserialization with all fields."""
        data = {
            "mode": "iso",
            "iso_path": "/path/to/iso",
            "keep_iso": True,
            "nocloud_net_url": "http://example.com/",
        }
        config = CloudInitConfig.from_dict(data)
        assert config.mode == CloudInitMode.ISO
        assert config.iso_path == Path("/path/to/iso")
        assert config.keep_iso is True
        assert config.nocloud_net_url == "http://example.com/"

    def test_from_dict_auto_mode(self):
        """Test deserialization with auto mode string."""
        data = {"mode": "inject"}
        config = CloudInitConfig.from_dict(data)
        assert config.mode == CloudInitMode.INJECT

    def test_from_dict_disabled_mode(self):
        """Test deserialization with disabled mode string."""
        data = {"mode": "off"}
        config = CloudInitConfig.from_dict(data)
        assert config.mode == CloudInitMode.OFF

    def test_from_dict_nocloud_net_mode(self):
        """Test deserialization with nocloud-net mode string."""
        data = {"mode": "net"}
        config = CloudInitConfig.from_dict(data)
        assert config.mode == CloudInitMode.NET

    def test_from_dict_none_iso_path(self):
        """Test deserialization with null iso_path."""
        data = {"iso_path": None}
        config = CloudInitConfig.from_dict(data)
        assert config.iso_path is None

    def test_from_dict_missing_iso_path(self):
        """Test deserialization without iso_path key."""
        data = {"mode": "inject"}
        config = CloudInitConfig.from_dict(data)
        assert config.iso_path is None

    def test_from_dict_missing_nocloud_net_url(self):
        """Test deserialization without nocloud_net_url key."""
        data = {"mode": "inject"}
        config = CloudInitConfig.from_dict(data)
        assert config.nocloud_net_url is None

    def test_roundtrip_serialization(self):
        """Test that to_dict/from_dict are inverse operations."""
        original = CloudInitConfig(
            mode=CloudInitMode.ISO,
            iso_path=Path("/path/to/iso"),
            keep_iso=True,
            nocloud_net_url="http://10.0.0.1:8080/",
        )
        data = original.to_dict()
        restored = CloudInitConfig.from_dict(data)
        assert restored.mode == original.mode
        assert restored.iso_path == original.iso_path
        assert restored.keep_iso == original.keep_iso
        assert restored.nocloud_net_url == original.nocloud_net_url

    def test_roundtrip_serialization_defaults(self):
        """Test roundtrip with default values."""
        original = CloudInitConfig()
        data = original.to_dict()
        restored = CloudInitConfig.from_dict(data)
        assert restored.mode == original.mode
        assert restored.iso_path == original.iso_path
        assert restored.keep_iso == original.keep_iso
        assert restored.nocloud_net_url == original.nocloud_net_url

    def test_roundtrip_serialization_nocloud_net(self):
        """Test roundtrip with NO_CLOUD_NET mode."""
        original = CloudInitConfig(
            mode=CloudInitMode.NET,
            nocloud_net_url="http://10.0.0.1:8080/",
        )
        data = original.to_dict()
        restored = CloudInitConfig.from_dict(data)
        assert restored.mode == original.mode
        assert restored.nocloud_net_url == original.nocloud_net_url
