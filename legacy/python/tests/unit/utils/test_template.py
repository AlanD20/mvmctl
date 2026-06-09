"""Tests for utils/template.py — template rendering."""

from __future__ import annotations

import pytest

from mvmctl.utils.template import render_optional_template, render_template


class TestRenderTemplate:
    """Tests for render_template()."""

    def test_basic_substitution(self):
        """Should substitute variables in template."""
        result = render_template("Hello {name}!", {"name": "World"})
        assert result == "Hello World!"

    def test_multiple_variables(self):
        """Should substitute multiple variables."""
        result = render_template(
            "{greeting} {name}", {"greeting": "Hi", "name": "Alice"}
        )
        assert result == "Hi Alice"

    def test_missing_variable_raises_value_error(self):
        """Should raise ValueError for missing template variable."""
        with pytest.raises(ValueError, match="Missing template variable: name"):
            render_template("Hello {name}!", {})

    def test_empty_template(self):
        """Should return empty string for empty template."""
        result = render_template("", {"key": "value"})
        assert result == ""

    def test_no_variables_in_template(self):
        """Should return template unchanged when no variables present."""
        result = render_template("Hello World!", {"key": "value"})
        assert result == "Hello World!"


class TestRenderOptionalTemplate:
    """Tests for render_optional_template()."""

    def test_none_returns_none(self):
        """Should return None when template is None."""
        result = render_optional_template(None, {"key": "value"})
        assert result is None

    def test_string_template_substitutes(self):
        """Should substitute variables when template is a string."""
        result = render_optional_template("Hello {name}!", {"name": "World"})
        assert result == "Hello World!"

    def test_missing_variable_raises_value_error(self):
        """Should raise ValueError for missing variable in non-None template."""
        with pytest.raises(ValueError, match="Missing template variable: name"):
            render_optional_template("Hello {name}!", {})
