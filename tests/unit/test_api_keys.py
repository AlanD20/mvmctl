"""Tests for api/keys.py."""

from unittest.mock import patch

from mvmctl.api.keys import (
    KeyInfo,
    add_key,
    clear_default_keys,
    create_key,
    export_key,
    get_default_keys,
    get_key,
    inspect_key,
    list_keys,
    remove_key,
    resolve_key_inputs,
    set_default_keys,
)


class TestSetDefaultKeys:
    """Tests for set_default_keys()."""

    def test_delegates_to_core(self):
        """Should delegate to _core_set_default_keys."""
        with patch("mvmctl.api.keys._core_set_default_keys") as mock_set:
            set_default_keys(["key1", "key2"])
            mock_set.assert_called_once_with(["key1", "key2"])


class TestGetDefaultKeys:
    """Tests for get_default_keys()."""

    def test_delegates_to_core(self):
        """Should delegate to _core_get_default_keys."""
        with patch("mvmctl.api.keys._core_get_default_keys") as mock_get:
            mock_get.return_value = ["key1"]
            result = get_default_keys()
            assert result == ["key1"]
            mock_get.assert_called_once()


class TestClearDefaultKeys:
    """Tests for clear_default_keys()."""

    def test_delegates_to_core(self):
        """Should delegate to _core_clear_default_keys."""
        with patch("mvmctl.api.keys._core_clear_default_keys") as mock_clear:
            clear_default_keys()
            mock_clear.assert_called_once()


class TestResolveKeyInputs:
    """Tests for resolve_key_inputs()."""

    def test_resolves_each_input(self, mocker):
        """Should resolve each key input using KeyResolver."""
        mock_resolver = mocker.MagicMock()
        mock_resolver.resolve_many.return_value = mocker.MagicMock(
            items=["resolved-key1", "resolved-key2"],
            errors=[],
        )
        mocker.patch("mvmctl.api.keys.KeyResolver", return_value=mock_resolver)

        result = resolve_key_inputs(["key1", "key2"])
        assert result == ["resolved-key1", "resolved-key2"]

    def test_empty_list(self):
        """Should return empty list for empty input."""
        result = resolve_key_inputs([])
        assert result == []


class TestReExports:
    """Tests that re-exported core functions are accessible."""

    def test_key_info_is_accessible(self):
        assert KeyInfo is not None

    def test_list_keys_is_callable(self):
        assert callable(list_keys)

    def test_get_key_is_callable(self):
        assert callable(get_key)

    def test_add_key_is_callable(self):
        assert callable(add_key)

    def test_create_key_is_callable(self):
        assert callable(create_key)

    def test_remove_key_is_callable(self):
        assert callable(remove_key)

    def test_inspect_key_is_callable(self):
        assert callable(inspect_key)

    def test_export_key_is_callable(self):
        assert callable(export_key)
