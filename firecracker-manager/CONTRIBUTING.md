# Contributing to firecracker-manager

Thanks for wanting to contribute. This guide covers everything you need to get set up and productive.

## Prerequisites

- **Python 3.13+** — check with `python3 --version`
- **uv** — the package manager used for this project. Install with:
  ```bash
  curl -LsSf https://astral.sh/uv/install.sh | sh
  ```
- **Linux** — the project targets Linux with KVM. Most development and testing works on any modern Linux distro.
- **git**

## Development Setup

```bash
git clone <repo-url>
cd firecracker-manager

# Install all dependencies including dev tools
uv sync --group dev

# Verify the CLI is available
uv run fcm --help
```

This creates a `.venv/` directory and installs everything there. You don't need to activate it manually; `uv run` handles that.

## Project Structure

```
firecracker-manager/
├── src/fcm/
│   ├── cli/          # Typer command groups (vm.py, image.py, kernel.py, config.py)
│   ├── core/         # Business logic (vm_manager.py, image_manager.py, etc.)
│   ├── models/       # Pydantic data models
│   └── utils/        # Shared helpers (paths, process, networking)
├── tests/
│   ├── unit/         # Pure unit tests (no root, no KVM)
│   └── integration/  # Tests that need system resources
├── pyproject.toml
└── README.md
```

The CLI layer (`cli/`) stays thin. It parses args, calls into `core/`, and formats output with Rich. Business logic lives in `core/`.

## Running Tests

```bash
# Run all tests
uv run pytest tests/ -v

# Run a specific file
uv run pytest tests/unit/test_vm_manager.py -v

# Run tests matching a name pattern
uv run pytest tests/ -v -k "test_create"

# Run with coverage
uv run pytest tests/ --cov=src/fcm --cov-report=term-missing
```

Unit tests don't need root or KVM. Integration tests might — check their docstrings.

## Code Style

This project uses **ruff** for linting and formatting, and **mypy** for type checking.

```bash
# Check for lint issues
uv run ruff check src/

# Auto-fix what ruff can fix
uv run ruff check src/ --fix

# Format code
uv run ruff format src/

# Type check
uv run mypy src/
```

All code should pass ruff with no errors. mypy is configured with `--strict`; new code should aim for full type annotations.

## Adding a New Command

1. Find the right group file in `src/fcm/cli/` (e.g., `vm.py` for `fcm vm ...`).
2. Add a new Typer command function. Follow the existing pattern:
   ```python
   @app.command()
   def my_command(
       name: str = typer.Option(..., "--name", help="VM name"),
   ) -> None:
       """Short description shown in --help."""
       manager = get_vm_manager()
       result = manager.do_thing(name)
       console.print(result)
   ```
3. Put the actual logic in the corresponding `core/` module.
4. Add a test in `tests/unit/`.

For entirely new command groups, create both `src/fcm/cli/mygroup.py` and register the Typer app in `src/fcm/cli/__init__.py` or `main.py`.

## Adding a Test

Tests live in `tests/unit/` for pure logic and `tests/integration/` for anything touching the filesystem or system calls.

```python
# tests/unit/test_my_feature.py
import pytest
from fcm.core.my_module import MyClass


def test_something_works() -> None:
    obj = MyClass(config={"key": "value"})
    result = obj.do_thing()
    assert result == expected_value


def test_something_fails_gracefully() -> None:
    obj = MyClass(config={})
    with pytest.raises(ValueError, match="config key missing"):
        obj.do_thing()
```

Use `pytest.fixture` for shared setup. Keep unit tests fast; avoid `sleep()` or network calls.

## Commit Conventions

This project follows [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add fcm vm pause command
fix: handle missing kernel path gracefully
test: add unit tests for image converter
docs: update quick start in README
chore: bump ruff to 0.4.0
refactor: extract network helpers to utils/networking.py
```

Keep the subject line under 72 characters. Add a body if the change needs explanation.

## Pull Request Process

1. Fork the repo and create a branch from `main`:
   ```bash
   git checkout -b feat/my-feature
   ```
2. Make your changes. Keep commits focused.
3. Run tests and linting:
   ```bash
   uv run ruff check src/ && uv run pytest tests/ -v
   ```
4. Push your branch and open a PR against `main`.
5. Fill in the PR description with what changed and why.
6. A maintainer will review and merge once tests pass.

Don't force-push to a PR branch after review starts. If you need to rebase, coordinate with the reviewer.

## Environment Variables

All FCM environment variables use the `FCM_` prefix.

| Variable | Description | Default |
|---|---|---|
| `FCM_CACHE_DIR` | Override the cache directory for images, kernels, and VM state | `~/.cache/firecracker-manager` |
| `FCM_CONFIG_FILE` | Override the config file path | `~/.config/fcm/config.yaml` |
| `FCM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARNING`, `ERROR`) | `INFO` |
| `FCM_FIRECRACKER_BIN` | Path to the Firecracker binary | auto-detected from `$PATH` |

When writing code that reads config or paths, always go through the settings module rather than reading env vars directly. That keeps everything in one place and makes testing easier.

## Questions

Open an issue if something in this guide is unclear or out of date.
