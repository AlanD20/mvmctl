# Agent Instructions — Firecracker Manager CLI

---

> [!IMPORTANT]
> This project is already partially implemented. Before writing any code, you must first
> audit what exists. For every requirement in every phase document, locate the relevant
> code, read it, and determine whether the requirement is already met.
>
> **DO NOT reinvent or rewrite working code.**
>
> If a requirement is **not yet implemented** — implement it.
>
> If a requirement is **already implemented** — review the logic line by line and verify it
> correctly satisfies the requirement as written. Fix only what does not meet the spec.
>
> Work through each phase document requirement by requirement, in order. For each one:
> locate the code path, read it, compare it against the requirement, then act. Do not skim.
> Do not assume something works because it exists.

---

## Your Task

Implement the `firecracker-manager` Python CLI project to completion across all phases.
The work is defined in a set of phase requirement documents (`python-cli-phase-*.md`).
You must read and implement every requirement in every phase document before considering
the project done.

---

## Repository Layout

The repository contains two distinct things:

1. **`firecracker-manager/`** — the Python CLI project you are building. This is your
   working directory. All code, tests, configuration, and documentation go here.

2. **Everything else** (`assets/`, `multi-vm/`, `single-vm/`, `custom-images/`,
   `environment_setup.sh`, `ssh/`) — bash proof-of-concept scripts. These are **read-only
   reference material**. Study them to understand what each operation does at the system
   level (how networking is configured, how cloud-init is embedded, how Firecracker is
   launched, etc.), then implement the equivalent behaviour in Python inside
   `firecracker-manager/`. Do not modify the bash scripts. Do not call them from Python at
   runtime.

---

## Phase Documents and Precedence

Requirements are split across multiple phase documents, named `python-cli-phase-1.md`,
`python-cli-phase-2.md`, `python-cli-phase-3.md`, and so on.

**Precedence rule — this is strict and non-negotiable:**

> A requirement defined in a higher-numbered phase overrides the same requirement in any
> lower-numbered phase. Phase 5 overrides phase 4, phase 4 overrides phase 3, and so on.

Before implementing any requirement, scan all higher-numbered phase documents to check
whether it has been revised or superseded. Implementing something that a later phase
changes is wasted work and introduces inconsistency. Read all phase documents in full
before writing a single line of code.

---

## Cross-Phase Regression Rule

> [!IMPORTANT]
> Every time you make a change — no matter how small — you must verify that it has not
> broken any requirement from any earlier phase.
>
> This means: after every implementation step, run the full test suite. A passing test
> suite is the minimum bar. Beyond that, mentally walk the affected code path against the
> relevant requirements from all phases and confirm the behaviour is still correct.
>
> **Never assume that earlier phases are still intact after making changes. Always verify.**

This applies especially when:
- Renaming or refactoring a command, flag, or function referenced in multiple phases
- Changing a shared module (`constants.py`, `models.py`, `config.py`, `exceptions.py`)
- Modifying the cache directory layout or file formats
- Updating networking, privilege, or host management logic

---

## Documentation Consistency Rule

> [!IMPORTANT]
> Every change that affects user-facing behaviour, CLI commands, flags, config keys,
> environment variables, file paths, or API functions **must be reflected immediately in
> all relevant documentation files.**
>
> Documentation that does not match the code is treated as a bug, not a cosmetic issue.

After any such change, update every affected file from this list before marking the
requirement complete:

- `README.md` — if the change affects installation, quickstart, commands, flags, or config
- `docs/API.md` — if the change affects any public API function, model, or exception
- `docs/RELEASE.md` — if the change affects the build, versioning, or release process
- `CONTRIBUTING.md` — if the change affects dev setup, project structure, or build flags
- `phase-N-status.md` — always, for every requirement completed or partially completed
- `phase-status.md` — whenever a phase transitions state

Do not batch documentation updates for the end. Update inline as you go.

---

## Testing Rule

> [!IMPORTANT]
> Tests are not optional and are never deferred. A requirement is not complete until its
> tests pass. A phase is not complete until every test in the suite passes.
>
> **The test suite must be green at the end of every implementation step, not just at the
> end of a phase.**

Specific requirements:

- Write tests for every new API function, CLI command, config behaviour, and edge case
  introduced by the requirement you are implementing
- Use `pytest`, `pytest-mock`, and the `tmp_path` fixture for all filesystem operations
- Mock all subprocess calls (`ip`, `iptables`, `sysctl`, `firecracker`) — tests must never
  require root, KVM, or a real Linux network stack to pass
- Coverage must stay at or above 80% — the CI pipeline enforces this and a drop below the
  threshold is treated as a failure
- If a change breaks an existing test, fix the test to match the new correct behaviour
  (if the behaviour change was intentional per a phase requirement) or fix the code (if
  the breakage was unintentional). Do not delete or skip tests to make the suite pass

---

## Status Tracking

You must maintain two status files as you work.

### `phase-status.md` — top-level index (at the repo root)

Single-glance view of all phases. Update it whenever a phase transitions state.

```markdown
# Phase Status

| Phase | Document | Status | Last Updated |
|---|---|---|---|
| Phase 1 | python-cli-phase-1.md | Complete | 2025-01-01 |
| Phase 2 | python-cli-phase-2.md | In Progress | 2025-01-02 |
| Phase 3 | python-cli-phase-3.md | Not Started | — |
```

Valid status values: `Not Started`, `In Progress`, `Complete`, `Blocked` (with a note
explaining what is blocking).

### `phase-N-status.md` — per-phase detail file

One file per phase. Create it when you begin that phase. Update it as you complete
individual requirements.

```markdown
# Phase N Status

## Summary
Status: In Progress
Started: 2025-01-02
Completed: —

## Requirements

| # | Requirement | Status | Notes |
|---|---|---|---|
| 1 | Project identity and build flags | Complete | |
| 2 | Cache directory layout | Complete | |
| 3 | vm create command | In Progress | Flags done, cloud-init pending |
| 4 | Network auto-setup | Not Started | |
```

Requirement descriptions must match the section headings in the phase document closely
enough that a reader can locate the corresponding spec without ambiguity.

---

## Implementation Order

Work through phases in order: complete phase 1 before starting phase 2, and so on.
Within a phase, implement requirements in the order they appear in the document unless a
dependency forces a different order (models and exceptions must exist before API functions
that use them).

After completing each phase:

1. Run the full test suite — it must be green before advancing
2. Update `phase-N-status.md` to `Complete` with a completion date
3. Update `phase-status.md` to reflect the new state
4. Do not carry forward known broken behaviour into the next phase

---

## General Rules

- **Phase documents are the source of truth.** If something is not specified in any phase
  document, use best judgement and document the decision in `phase-N-status.md` under a
  Decisions section.
- **Precedence is strict.** Before implementing any requirement, check whether a later
  phase document overrides it.
- **Do not modify the proof-of-concept bash scripts.** They are reference only.
- **`__pycache__` directories must never be committed.** Ensure `.gitignore` covers
  `__pycache__/` recursively (`**/__pycache__/`) and all other generated artifacts:
  `*.pyc`, `*.pyo`, `dist/`, `build/`, `*.egg-info/`, `.coverage`, `htmlcov/`,
  `.mypy_cache/`, `.ruff_cache/`, and any runtime files (`*.pid`, `*.socket`, `*.log`).
- **Status files must stay current.** An outdated status file is worse than no status
  file. Update them continuously as you work, not in a single batch at the end.
- **Documentation must stay current.** An undocumented change is an incomplete change.
  Update all affected documentation files before marking any requirement done.
