"""Tests for utils/yaml.py."""

import pytest

from mvmctl.utils.yaml import (
    optional_int,
    optional_str,
    parse_set_val_list,
    require_str,
    require_str_list,
)


class TestRequireStr:
    """Tests for require_str()."""

    def test_returns_string_value(self):
        """Should return string value when key exists and is string."""
        result = require_str({"name": "test"}, "name")
        assert result == "test"

    def test_raises_on_missing_key(self):
        """Should raise ValueError when key is missing."""
        with pytest.raises(ValueError, match="field 'name' must be a string"):
            require_str({}, "name")

    def test_raises_on_non_string_value(self):
        """Should raise ValueError when value is not a string."""
        with pytest.raises(ValueError, match="field 'name' must be a string"):
            require_str({"name": 123}, "name")

    def test_raises_on_none_value(self):
        """Should raise ValueError when value is None."""
        with pytest.raises(ValueError, match="field 'name' must be a string"):
            require_str({"name": None}, "name")


class TestOptionalStr:
    """Tests for optional_str()."""

    def test_returns_string_value(self):
        """Should return string value when key exists and is string."""
        result = optional_str({"name": "test"}, "name")
        assert result == "test"

    def test_returns_none_on_missing_key(self):
        """Should return None when key is missing."""
        result = optional_str({}, "name")
        assert result is None

    def test_returns_none_on_non_string(self):
        """Should return None when value is not a string."""
        result = optional_str({"name": 123}, "name")
        assert result is None


class TestOptionalInt:
    """Tests for optional_int()."""

    def test_returns_int_value(self):
        """Should return int value when key exists and is int."""
        result = optional_int({"count": 42}, "count")
        assert result == 42

    def test_returns_none_on_missing_key(self):
        """Should return None when key is missing."""
        result = optional_int({}, "count")
        assert result is None

    def test_returns_none_on_non_int(self):
        """Should return None when value is not an int."""
        result = optional_int({"count": "42"}, "count")
        assert result is None


class TestRequireStrList:
    """Tests for require_str_list()."""

    def test_returns_list_of_strings(self):
        """Should return list of strings when value is valid."""
        result = require_str_list({"items": ["a", "b"]}, "items")
        assert result == ["a", "b"]

    def test_returns_empty_list_on_missing_key(self):
        """Should return empty list when key is missing."""
        result = require_str_list({}, "items")
        assert result == []

    def test_raises_on_non_list(self):
        """Should raise ValueError when value is not a list."""
        with pytest.raises(ValueError, match="field 'items' must be a list of strings"):
            require_str_list({"items": "not-a-list"}, "items")

    def test_raises_on_list_with_non_strings(self):
        """Should raise ValueError when list contains non-strings."""
        with pytest.raises(ValueError, match="field 'items' must be a list of strings"):
            require_str_list({"items": ["a", 123]}, "items")


class TestParseSetValList:
    """Tests for parse_set_val_list()."""

    def test_returns_dict_entries(self):
        """Should parse {option, value} dict entries."""
        result = parse_set_val_list({"opts": [{"option": "key", "value": "val"}]}, "opts")
        assert result == [("key", "val")]

    def test_returns_tuple_entries(self):
        """Should parse two-element list entries."""
        result = parse_set_val_list({"opts": [["key", "val"]]}, "opts")
        assert result == [("key", "val")]

    def test_returns_empty_list_on_missing_key(self):
        """Should return empty list when key is missing."""
        result = parse_set_val_list({}, "opts")
        assert result == []

    def test_raises_on_non_list(self):
        """Should raise ValueError when value is not a list."""
        with pytest.raises(ValueError, match="field 'opts' must be a list"):
            parse_set_val_list({"opts": "not-a-list"}, "opts")

    def test_raises_on_invalid_entry_shape(self):
        """Should raise ValueError when entry has unexpected shape."""
        with pytest.raises(ValueError, match="field 'opts' entries must be"):
            parse_set_val_list({"opts": [{"bad": "entry"}]}, "opts")

    def test_raises_on_wrong_length_tuple(self):
        """Should raise ValueError when tuple entry has wrong length."""
        with pytest.raises(ValueError, match="field 'opts' entries must be"):
            parse_set_val_list({"opts": [["only-one"]]}, "opts")

    def test_mixed_entries(self):
        """Should handle mix of dict and tuple entries."""
        result = parse_set_val_list(
            {"opts": [{"option": "k1", "value": "v1"}, ["k2", "v2"]]}, "opts"
        )
        assert result == [("k1", "v1"), ("k2", "v2")]
