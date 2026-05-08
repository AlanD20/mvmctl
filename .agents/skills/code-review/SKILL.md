---
name: code-review
version: 1.0.0
description: Validate code against mvmctl project conventions and quality gates
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["python", "firecracker", "mvmctl", "quality", "ci"]
  workflow: development
---

## What I do

I validate code changes against mvmctl's strict project conventions:

- **Architecture compliance** — Enforce layer boundaries (cli → api → core → models)
- **Error handling** — Verify typed exception hierarchy usage
- **Configuration patterns** — Check FALLBACK_* constants and runtime resolution
- **Type safety** — Ensure strict mypy compliance (NO type: ignore)
- **CLI patterns** — Validate Typer configuration and None-defaults
- **Testing requirements** — Confirm mocking patterns and coverage gates
- **CI compliance** — Verify all 4 gates pass (ruff, mypy, pytest 80%)

## When to use me

Use me when reviewing code changes, pull requests, or refactoring to ensure compliance with project standards.

I am NOT for designing new architecture — use `@.agents/skills/architect/` skill for that.

## Checks

### Architecture
- [ ] cli/ imports only from api/, never core/ directly
- [ ] api/ adds `HostPrivilegeHelper.check_privileges()` before privileged ops
- [ ] core/ raises typed exceptions, never prints
- [ ] models/ has only dataclasses with `__post_init__` validation

### Error Handling
- [ ] NO bare `except:` — catch specific exception types
- [ ] Core raises domain exceptions (MVMError subclasses)
- [ ] Include `from e` when re-raising

### Configuration
- [ ] NO hardcoded defaults — use `typer.Option(None, ...)`
- [ ] Runtime resolution via `_defaults = _get_vm_defaults()`
- [ ] FALLBACK_* constants only in constants.py
- [ ] Env vars use `MVM_` prefix

### Type Safety
- [ ] All functions have type annotations
- [ ] NO `as any` casts
- [ ] NO `# type: ignore` comments
- [ ] mypy strict mode passes: `uv run mypy src/`

### CLI Patterns
- [ ] `app = typer.Typer(no_args_is_help=True, rich_markup_mode=None, add_completion=False)`
- [ ] Typer defaults are `None` (not hardcoded)
- [ ] Multiple args use `Optional[List[str]] = typer.Argument(None)`

### Testing
- [ ] Tests mock subprocess — NO real sudo/KVM/network
- [ ] Use `CliRunner` for CLI tests
- [ ] Use `mocker.patch()` for simple mocks
- [ ] Use `@patch()` for subprocess
- [ ] Unit tests use `isolate_config_and_cache` fixture

### Subprocess
- [ ] List form only: `["ip", "link", "add", ...]`
- [ ] NO `shell=True`
- [ ] Capture stderr in exceptions
- [ ] Subprocess calls ONLY in core/

### CI Gates (MUST ALL PASS)
```bash
uv run ruff check src/
uv run ruff format --check src/
uv run mypy src/
uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80
```

## Quick Reference

| Forbidden | Correct |
|-----------|---------|
| `print()` in core/ | Raise exception; format in CLI |
| `typer.Option(2, ...)` | `typer.Option(None, ...)` + runtime |
| `import from core` in cli/ | Import from `api` only |
| `except Exception: pass` | Catch specific types from exceptions.py |
| Hardcoded defaults | constants.py FALLBACK_* |
| `subprocess(..., shell=True)` | List form only |
| `type: ignore` or `as any` | Proper type annotations |
| Skip failing tests | Fix the test; coverage drop fails CI |
