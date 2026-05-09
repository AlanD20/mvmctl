---
name: git-commit
version: 1.0.0
description: Enforce commit standards and CI compliance before committing to mvmctl
author: mvmctl team
license: MIT
compatibility: opencode
metadata:
  audience: developers
  tags: ["git", "commit", "mvmctl", "ci", "quality"]
  workflow: git
---

## What I do

I enforce critical requirements before any commit to mvmctl:

- **CI compliance** — Verify all 4 gates pass (ruff, format, mypy, pytest 80%)
- **Commit authorship** — Validate Co-authored-by trailers only when appropriate
- **Git hygiene** — Ensure no build artifacts or __pycache__ are staged
- **Atomic commits** — One feature/fix per commit with clear message

## When to use me

Use me before every `git commit` to ensure compliance with project standards.

I am NOT for reviewing code quality — use `@.agents/skills/code-review/` skill for that.

## Pre-Commit Verification

### 1. CI Gates (MUST ALL PASS)

Run these commands and verify clean output:

```bash
# Ruff linting (line length 80, py313)
uv run ruff check src/

# Ruff formatting (double quotes, space indent)
uv run ruff format --check src/

# Type checking (strict mode, NO type: ignore allowed)
uv run mypy src/

# Tests with 80% branch coverage (parallel)
uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80
```

**If checks fail:**
- Fix linting: `uv run ruff check src/ --fix`
- Fix formatting: `uv run ruff format src/`
- Fix type errors with proper annotations (NO `type: ignore`)
- Fix failing tests — NEVER delete tests to make them pass

### 2. Staged Files Check

**NEVER commit**:
- `__pycache__/` directories
- `.pyc`, `.pyo`, `.pyd` files
- `.venv/`, `venv/` directories
- `.pytest_cache/`, `.coverage`, `htmlcov/`
- `dist/`, `build/` directories
- `*.egg-info/`
- `.mypy_cache/`, `.ruff_cache/`
- VM runtime files: `*.pid`, `*.socket`, `*.log`
- Kernel build artifacts: `vmlinux`, `vmlinux-*`, `kernel-build/`

Verify .gitignore covers these patterns: `cat .gitignore | head -50`

### 3. Commit Message Quality

**Format**: Clear description of *why* not just *what*

**Examples**:
```
feat: add kernel build-from-source pipeline

Adds support for building custom kernels via 'mvm kernel build'.
Includes config fragment merging and --clean-build flag.
```

```
fix: resolve image path for non-ext4 formats

Fixes image detection when importing QCOW2 or raw formats.
Uses blkid to detect filesystem type before renaming.
```

**Avoid**:
- Vague messages like "fix stuff" or "update code"
- Multiple unrelated changes in one commit
- Committing without CI passing

## Commit Authorship Rules

### Co-authored-by Guidelines (MANDATORY)

**DO NOT add `Co-authored-by` trailers unless the co-author actually contributed to that specific change.**

- **Correct**: Only when co-author wrote code/review/significant input for THIS commit
- **Incorrect**: Adding co-authors as blanket practice on every commit
- **When in doubt**: **Omit the co-author trailer entirely**

**Correct example**:
```
feat: add new VM snapshot feature

Co-authored-by: Alice <alice@example.com>  # Alice wrote part of this feature
```

**Incorrect example**:
```
style: fix formatting

Co-authored-by: Adam <adam@example.com>  # WRONG - no contribution to this change
```

## Commit Checklist

Before committing:

- [ ] All 4 CI gates pass (ruff, format, mypy, pytest 80%)
- [ ] NO __pycache__ or build artifacts staged
- [ ] .gitignore covers all temporary/runtime files
- [ ] Commit message describes *why* not just *what*
- [ ] One feature or fix per commit (atomic)
- [ ] Co-authored-by only if co-author actually contributed
- [ ] Tests added/updated for new functionality
- [ ] Documentation updated if user-facing changes
- [ ] Layer compliance tests pass

## Quick Reference

| Check | Command | Must Pass |
|-------|---------|-----------|
| Linting | `uv run ruff check src/` | Clean |
| Formatting | `uv run ruff format --check src/` | Clean |
| Type Check | `uv run mypy src/` | Strict mode |
| Tests | `uv run pytest tests/ -q --cov=src/mvmctl -n auto --cov-fail-under=80` | 80% branch |
| Staged files | `git status` | No forbidden files |
| Commit msg | `git log --oneline -1` | Clear and descriptive |

## Pre-Push Verification

Before pushing to remote:

- [ ] CI green on local machine
- [ ] No WIP commits in history
- [ ] Feature branch rebased on latest main
- [ ] All commits follow atomic principle
