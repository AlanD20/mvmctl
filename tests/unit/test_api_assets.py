from mvmctl.api.assets import AssetInfo


def test_asset_info_typed_dict():
    info: AssetInfo = {
        "type": "binary",
        "name": "1.5.0",
        "active": True,
        "size_mib": 100.5,
        "details": "/path/to/binary",
    }
    assert info["type"] == "binary"
    assert info["active"] is True


def test_asset_info_with_none_values():
    info: AssetInfo = {
        "type": "kernel",
        "name": "vmlinux",
        "active": None,
        "size_mib": None,
        "details": None,
    }
    assert info["active"] is None
    assert info["size_mib"] is None
