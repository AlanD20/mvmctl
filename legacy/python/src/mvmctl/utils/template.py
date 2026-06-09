from __future__ import annotations


def render_template(template: str, variables: dict[str, str]) -> str:
    try:
        return template.format(**variables)
    except KeyError as exc:
        key = str(exc.args[0]) if exc.args else "unknown"
        raise ValueError(f"Missing template variable: {key}") from exc


def render_optional_template(
    template: str | None, variables: dict[str, str]
) -> str | None:
    if template is None:
        return None
    return render_template(template, variables)
