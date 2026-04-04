"""Tests for api/config.py - verifies re-exports are accessible."""

from mvmctl.api.config import (
    dump_config,
    get_config_value,
    get_default_binary_entry,
    get_default_image_entry,
    get_default_kernel_entry,
    get_defaults_config,
    get_firecracker_config,
    get_full_user_config,
    initialize_default_config,
    load_config,
    set_config_value,
    set_defaults_value,
    validate_config,
)


class TestReExports:
    def test_dump_config_callable(self):
        assert callable(dump_config)

    def test_load_config_callable(self):
        assert callable(load_config)

    def test_validate_config_callable(self):
        assert callable(validate_config)

    def test_get_config_value_callable(self):
        assert callable(get_config_value)

    def test_set_config_value_callable(self):
        assert callable(set_config_value)

    def test_get_full_user_config_callable(self):
        assert callable(get_full_user_config)

    def test_get_firecracker_config_callable(self):
        assert callable(get_firecracker_config)

    def test_get_defaults_config_callable(self):
        assert callable(get_defaults_config)

    def test_set_defaults_value_callable(self):
        assert callable(set_defaults_value)

    def test_initialize_default_config_callable(self):
        assert callable(initialize_default_config)

    def test_get_default_image_entry_callable(self):
        assert callable(get_default_image_entry)

    def test_get_default_kernel_entry_callable(self):
        assert callable(get_default_kernel_entry)

    def test_get_default_binary_entry_callable(self):
        assert callable(get_default_binary_entry)
