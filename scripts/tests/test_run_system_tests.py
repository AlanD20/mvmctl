from __future__ import annotations

import argparse
import ast
import importlib.util
import os
import shutil
import subprocess
import sys
from pathlib import Path
from types import ModuleType

import pytest


@pytest.fixture
def orchestrator(monkeypatch: pytest.MonkeyPatch) -> ModuleType:
    script = Path(__file__).parents[1] / "run-system-tests.py"
    monkeypatch.syspath_prepend(str(script.parent))
    module_name = "mvmctl_run_system_tests"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = module
    spec.loader.exec_module(module)
    return module


def _assert_selection_rejected_before_side_effects(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
    expected_error: str,
) -> None:
    calls: list[str] = []

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            calls.append(name)
            raise AssertionError(f"{name} must not run for an invalid selection")

        return fail

    for helper in (
        "validate_release_build_paths",
        "_resolve_candidate_build_version",
        "_build_mvm_binary",
        "verify_release_binary_identity",
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper), raising=False)
    monkeypatch.setattr(orchestrator.shutil, "which", unexpected("which"))
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py", *selection])

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2
    assert calls == []
    assert expected_error in capsys.readouterr().err


def _configure_test_registry(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    repo_root: Path,
    tier1: dict[str, list[str]],
    tier2: dict[str, list[str]] | None = None,
    tier3: dict[str, list[str]] | None = None,
) -> None:
    monkeypatch.setattr(orchestrator, "_REPO_ROOT", repo_root)
    monkeypatch.setattr(orchestrator, "TIER1_DOMAINS", tier1)
    monkeypatch.setattr(orchestrator, "TIER2_DOMAINS", tier2 or {})
    monkeypatch.setattr(orchestrator, "TIER3_DOMAINS", tier3 or {})


def _write_system_test(repo_root: Path, relative_path: str) -> None:
    test_file = repo_root / relative_path
    test_file.parent.mkdir(parents=True, exist_ok=True)
    test_file.write_text("def test_registered():\n    pass\n", encoding="utf-8")


def test_default_outer_controller_is_the_canonical_system_binary(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("MVM_BINARY", raising=False)
    script = Path(__file__).parents[1] / "run-system-tests.py"
    monkeypatch.syspath_prepend(str(script.parent))
    module_name = "mvmctl_run_system_tests_default_controller"
    spec = importlib.util.spec_from_file_location(module_name, script)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    monkeypatch.setitem(sys.modules, module_name, module)

    spec.loader.exec_module(module)

    assert module.MVM_BINARY == "/usr/local/bin/mvm"


def test_registry_uses_exec_tier2_and_one_canonical_fresh_env_domain(
    orchestrator: ModuleType,
) -> None:
    assert orchestrator.TIER2_DOMAINS["exec"] == [
        "tests/system/exec/test_exec.py"
    ]
    assert orchestrator.TIER3_DOMAINS["fresh_env"] == [
        "tests/system/vm/test_vm_fresh_env.py"
    ]
    assert "nested_virt" not in orchestrator.TIER3_DOMAINS


def test_duplicate_registry_domain_is_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    orchestrator.TIER2_DOMAINS["cli"] = ["tests/system/cli/test_cli.py"]

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "duplicate system-test domains: cli",
    )


def test_empty_registry_domains_are_rejected_in_deterministic_order(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    orchestrator.TIER1_DOMAINS["cli"].clear()
    orchestrator.TIER2_DOMAINS["volume"].clear()

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "empty system-test domains: cli, volume",
    )


def test_duplicate_registered_file_is_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    registered = "tests/system/cli/test_cli.py"
    _write_system_test(tmp_path, registered)
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"cli": [registered]},
        {"cli_alias": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        f"duplicate registered system-test files: {registered}",
    )


@pytest.mark.parametrize(
    "registered",
    [
        "tests/system/cli/helper.py",
        "outside/test_escape.py",
        "/tmp/test_absolute.py",
        "tests/system/cli/../cli/test_cli.py",
        "tests/system/../../outside/test_escape.py",
    ],
    ids=["non-test", "out-of-tree", "absolute", "non-canonical", "escaping"],
)
def test_invalid_registered_paths_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
    registered: str,
) -> None:
    if not Path(registered).is_absolute():
        _write_system_test(tmp_path, registered)
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"invalid": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        f"invalid system-test paths: {registered}",
    )


def test_missing_registered_files_are_rejected_in_deterministic_order(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    missing = [
        "tests/system/zeta/test_zeta.py",
        "tests/system/alpha/test_alpha.py",
    ]
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"missing": missing},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "missing system-test files: "
        "tests/system/alpha/test_alpha.py, tests/system/zeta/test_zeta.py",
    )


@pytest.mark.parametrize("kind", ["directory", "symlink"])
def test_non_regular_registered_files_are_rejected_before_any_probe(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
    kind: str,
) -> None:
    registered = "tests/system/cli/test_cli.py"
    registered_path = tmp_path / registered
    registered_path.parent.mkdir(parents=True)
    if kind == "directory":
        registered_path.mkdir()
    else:
        target = tmp_path / "real_test.py"
        target.write_text("def test_real():\n    pass\n", encoding="utf-8")
        registered_path.symlink_to(target)
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"invalid": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        f"non-regular system-test files: {registered}",
    )


def test_unregistered_system_tests_are_rejected_in_deterministic_order(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    registered = "tests/system/cli/test_cli.py"
    _write_system_test(tmp_path, registered)
    _write_system_test(tmp_path, "tests/system/zeta/test_zeta.py")
    _write_system_test(tmp_path, "tests/system/alpha/test_alpha.py")
    _configure_test_registry(
        orchestrator,
        monkeypatch,
        tmp_path,
        {"cli": [registered]},
    )

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        [],
        "unregistered system-test files: "
        "tests/system/alpha/test_alpha.py, tests/system/zeta/test_zeta.py",
    )


def test_tier3_selection_detection_covers_all_request_forms(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    selections = [
        ["--all"],
        ["--tier", "3"],
        ["--tier", "2,3,1"],
        *[[domain] for domain in orchestrator.TIER3_DOMAINS],
    ]

    for selection in selections:
        args = parser.parse_args(selection)
        assert orchestrator._selection_requests_tier3(args), selection


def test_t1_t2_and_unknown_names_do_not_imply_tier3_host_direct(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    selections = [
        ["--tier", "1"],
        ["--tier", "1,2"],
        [next(iter(orchestrator.TIER1_DOMAINS))],
        [next(iter(orchestrator.TIER2_DOMAINS))],
        ["unknown-domain"],
    ]

    for selection in selections:
        args = parser.parse_args(selection)
        assert not orchestrator._selection_requests_tier3(args), selection


@pytest.mark.parametrize(
    "selection",
    [
        ["not-a-domain", "also-unknown"],
        ["not-a-domain", "also-unknown", "--tier", "1"],
    ],
    ids=["domains-only", "with-tier"],
)
def test_unknown_domains_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        selection,
        "unknown domains: not-a-domain, also-unknown",
    )


@pytest.mark.parametrize(
    "selection",
    [
        ["--all", "cli"],
        ["--all", "--tier", "1"],
        ["cli", "--tier", "1"],
    ],
    ids=["all-and-domains", "all-and-tier", "domains-and-tier"],
)
def test_ambiguous_test_selectors_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        selection,
        "choose exactly one test selector: positional domains, --tier, or --all",
    )


@pytest.mark.parametrize(
    ("selection", "expected_error"),
    [
        (["cli", "cli"], "duplicate domains: cli"),
        (["cli", "config", "cli"], "duplicate domains: cli"),
        (["--tier", "1,1"], "duplicate tiers: 1"),
        (["--tier", "1,2,1"], "duplicate tiers: 1"),
    ],
    ids=["adjacent-domains", "nonadjacent-domains", "adjacent-tiers", "nonadjacent-tiers"],
)
def test_duplicate_test_selectors_are_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
    expected_error: str,
) -> None:
    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        selection,
        expected_error,
    )


def test_unique_tier_selection_preserves_requested_order(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(["--tier", "2,1,3"])

    orchestrator._validate_test_selection_args(parser, args)

    assert args.tier == [2, 1, 3]


def test_selection_with_no_registered_domains_is_rejected_before_any_probe(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    tmp_path: Path,
) -> None:
    _configure_test_registry(orchestrator, monkeypatch, tmp_path, {})

    _assert_selection_rejected_before_side_effects(
        orchestrator,
        monkeypatch,
        capsys,
        ["--tier", "2"],
        "test selection resolves to zero tests",
    )


def test_no_arguments_print_help_and_exit_without_any_probe(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    monkeypatch.setattr(
        orchestrator.shutil,
        "which",
        lambda *_args, **_kwargs: pytest.fail("no-args help must not probe binaries"),
    )
    monkeypatch.setattr(orchestrator.sys, "argv", ["run-system-tests.py"])

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 0
    assert "usage:" in capsys.readouterr().out.lower()


@pytest.mark.parametrize(
    "selection",
    [
        ["--prepare"],
        ["--volume"],
        ["--image"],
        ["--volume", "--image", "--prepare"],
        ["--rebuild", "--candidate-version", "0.3.0"],
    ],
    ids=["prepare", "volume", "image", "combined", "rebuild"],
)
def test_preparation_only_modes_do_not_require_a_test_selector(
    orchestrator: ModuleType,
    selection: list[str],
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(selection)

    orchestrator._validate_test_selection_args(parser, args)


def test_host_direct_help_is_an_acknowledgment_not_a_clean_host_guarantee(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(["--host-direct", "--all"])
    help_text = parser.format_help().lower()

    assert args.host_direct is True
    assert "acknowledge" in help_text
    assert "guarantee" not in help_text


@pytest.mark.parametrize(
    "selection",
    [
        ["--all", "--rebuild", "--candidate-version", "0.3.0"],
        ["--tier", "3"],
        ["env"],
    ],
    ids=["all", "tier", "positional-domain"],
)
def test_tier3_without_host_direct_is_rejected_before_any_side_effect(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    calls: list[str] = []

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            calls.append(name)
            raise AssertionError(f"{name} must not run before host-direct consent")

        return fail

    for helper in (
        "_resolve_candidate_build_version",
        "_build_mvm_binary",
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper))
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", *selection],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2
    assert calls == []
    error = capsys.readouterr().err.lower()
    assert "--host-direct" in error
    assert "acknowledge" in error


@pytest.mark.parametrize(
    "selection",
    [
        ["--release-qualification", "--host-direct", "--rebuild", "--candidate-version", "0.3.0"],
        ["--release-qualification", "--all", "--rebuild", "--candidate-version", "0.3.0"],
        ["--release-qualification", "--all", "--host-direct", "--candidate-version", "0.3.0"],
        ["--release-qualification", "--all", "--host-direct", "--rebuild"],
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "env",
        ],
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "--tier",
            "1,2,3",
        ],
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
            "--skip-volume-check",
        ],
    ],
    ids=[
        "missing-all",
        "missing-host-direct",
        "missing-rebuild",
        "missing-version",
        "positional-domain",
        "tier-filter",
        "skip-volume-check",
    ],
)
def test_invalid_release_qualification_is_rejected_before_any_probe_or_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
    selection: list[str],
) -> None:
    calls: list[str] = []

    def unexpected(name: str):
        def fail(*_args: object, **_kwargs: object) -> None:
            calls.append(name)
            raise AssertionError(f"{name} must not run for an invalid release mode")

        return fail

    for helper in (
        "validate_release_build_paths",
        "_resolve_candidate_build_version",
        "_build_mvm_binary",
        "verify_release_binary_identity",
        "_require_distinct_candidate_controller",
        "_get_mvm_version",
        "run_prepare",
        "ensure_shared_volume",
        "ensure_test_network",
    ):
        monkeypatch.setattr(orchestrator, helper, unexpected(helper), raising=False)
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", *selection],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2
    assert calls == []
    assert "--release-qualification requires" in capsys.readouterr().err


def test_release_qualification_parser_accepts_only_the_full_acknowledged_gate(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    args = parser.parse_args(
        [
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0-rc.1",
        ]
    )

    orchestrator._validate_release_qualification_args(parser, args)

    assert args.release_qualification is True
    assert args.candidate_version == "0.3.0-rc.1"
    assert "release binary identity" in parser.format_help().lower()


def test_release_identity_runs_after_build_and_before_host_resource_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    capsys: pytest.CaptureFixture[str],
) -> None:
    events: list[str] = []

    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        [
            "run-system-tests.py",
            "--release-qualification",
            "--all",
            "--host-direct",
            "--rebuild",
            "--candidate-version",
            "0.3.0",
        ],
    )
    monkeypatch.setattr(
        orchestrator,
        "validate_release_build_paths",
        lambda **_kwargs: events.append("path-configuration"),
        raising=False,
    )
    monkeypatch.setattr(
        orchestrator,
        "_resolve_candidate_build_version",
        lambda version: events.append(f"resolve-{version}") or version,
    )
    monkeypatch.setattr(
        orchestrator,
        "_build_mvm_binary",
        lambda version: events.append(f"build-{version}"),
    )

    def fail_identity(**kwargs: object) -> None:
        events.append(f"identity-{kwargs['requested_version']}")
        raise RuntimeError("release identity mismatch")

    monkeypatch.setattr(
        orchestrator,
        "verify_release_binary_identity",
        fail_identity,
        raising=False,
    )

    def unexpected_mutation(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("host/resource mutation must follow identity validation")

    for helper in ("run_prepare", "ensure_shared_volume", "ensure_test_network"):
        monkeypatch.setattr(orchestrator, helper, unexpected_mutation)

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 1
    assert events == [
        "path-configuration",
        "resolve-0.3.0",
        "build-0.3.0",
        "identity-0.3.0",
    ]
    assert "release identity mismatch" in capsys.readouterr().out


def test_guest_product_commands_use_the_exact_installed_cli(
    orchestrator: ModuleType,
) -> None:
    tree = ast.parse(Path(orchestrator.__file__).read_text(encoding="utf-8"))
    unqualified = sorted(
        {
            node.value
            for node in ast.walk(tree)
            if isinstance(node, ast.Constant)
            and isinstance(node.value, str)
            and "MVM_ASSET_MIRROR=/mnt mvm " in node.value
        }
    )

    assert unqualified == []


def test_all_guest_user_initialization_uses_the_pinned_firecracker_version(
    orchestrator: ModuleType,
) -> None:
    tree = ast.parse(Path(orchestrator.__file__).read_text(encoding="utf-8"))
    init_commands = [
        node.value
        for node in ast.walk(tree)
        if isinstance(node, ast.Constant)
        and isinstance(node.value, str)
        and "/usr/local/bin/mvm init" in node.value
    ]
    init_calls = [
        node
        for node in ast.walk(tree)
        if isinstance(node, ast.Call)
        and isinstance(node.func, ast.Name)
        and node.func.id == "_initialize_runner_user"
    ]

    assert init_commands == [orchestrator.RUNNER_USER_INIT_COMMAND]
    assert len(init_calls) == 5
    assert orchestrator.RUNNER_USER_INIT_COMMAND == (
        "sudo mkdir -p /mnt && sudo mount /dev/vdb /mnt && "
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm init --non-interactive "
        "--binary-version 1.16.0"
    )


def test_runner_user_initialization_uses_the_exact_installed_cli(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._initialize_runner_user(
        "base-img-builder",
        timeout=180,
        capture=False,
    )

    assert calls == [
        (
            (
                "exec",
                "base-img-builder",
                "--user",
                "runner",
                "--timeout",
                "10",
                "--",
                orchestrator.RUNNER_USER_INIT_COMMAND,
            ),
            {"timeout": 180, "capture": False},
        )
    ]


def test_streamed_mvm_failure_preserves_command_and_rc_when_output_is_none(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/outer/controller-mvm")

    def failed_stream(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[None]:
        assert kwargs["capture_output"] is False
        return subprocess.CompletedProcess(command, 23, stdout=None, stderr=None)

    monkeypatch.setattr(orchestrator.subprocess, "run", failed_stream)

    with pytest.raises(RuntimeError) as captured:
        orchestrator.mvm(
            "image",
            "pull",
            "ubuntu-minimal:24.04",
            capture=False,
        )

    message = str(captured.value)
    assert (
        "mvm command failed: /outer/controller-mvm image pull "
        "ubuntu-minimal:24.04"
    ) in message
    assert "rc: 23" in message
    assert "stderr: (streamed; not captured)" in message


def test_builder_assets_are_pulled_in_separate_sequential_execs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    orchestrator._pull_builder_assets("base-img-builder")

    expected_pull_commands = [
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm kernel pull "
        "--type firecracker --version v1.15 --default",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull alpine:3.23",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull "
        "ubuntu-minimal:24.04",
        "MVM_ASSET_MIRROR=/mnt /usr/local/bin/mvm image pull ubuntu "
        "--version 24.04",
    ]
    pull_calls = calls[:-1]
    assert [args[-1] for args, _kwargs in pull_calls] == expected_pull_commands
    assert len(pull_calls) == 4
    assert not any("binary pull firecracker" in args[-1] for args, _ in calls)
    for args, kwargs in pull_calls:
        assert args[:-1] == (
            "exec",
            "base-img-builder",
            "--user",
            "runner",
            "--timeout",
            "10",
            "--",
        )
        assert " & " not in args[-1]
        assert "pids=" not in args[-1]
        assert kwargs["capture"] is False
        assert kwargs["timeout"] == 600

    verify_args, verify_kwargs = calls[-1]
    assert verify_args[:2] == ("exec", "base-img-builder")
    assert "Verifying cached image integrity" in verify_args[-1]
    assert verify_kwargs == {"timeout": 180, "capture": False}


def test_builder_asset_pull_preserves_first_streamed_failure(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    commands: list[str] = []
    original_failure = RuntimeError(
        "mvm command failed: streamed ubuntu-minimal pull; rc: 23; "
        "stderr: (streamed; not captured)"
    )

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        commands.append(args[-1])
        if "ubuntu-minimal:24.04" in args[-1]:
            raise original_failure
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError) as captured:
        orchestrator._pull_builder_assets("base-img-builder")

    assert captured.value is original_failure
    assert commands[-1].endswith("image pull ubuntu-minimal:24.04")
    assert not any("--version 24.04" in command for command in commands)
    assert not any("Verifying cached image integrity" in command for command in commands)


def test_build_base_image_cleans_builder_when_asset_pull_fails(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    candidate = tmp_path / "mvm-candidate"
    candidate.write_bytes(b"candidate")
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        stdout = ""
        if args[:2] == ("exec", orchestrator.BASE_VM_NAME) and args[-1].startswith("stat -c%s"):
            size = candidate.stat().st_size
            stdout = f"{size}\n{size}\n"
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    original_failure = RuntimeError("asset pull failed")
    initialization_order: list[str] = []

    def record_host_init(_vm_name: str) -> None:
        initialization_order.append("host-init")

    def record_runner_init(
        _vm_name: str,
        *,
        timeout: int,
        capture: bool = True,
    ) -> None:
        assert timeout == 180
        assert capture is False
        initialization_order.append("runner-init-1.16.0")

    def fail_asset_pull(_vm_name: str) -> None:
        initialization_order.append("asset-pulls")
        raise original_failure

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_install_system_binary_in_runner", lambda _vm_name: None
    )
    monkeypatch.setattr(
        orchestrator, "_initialize_system_binary_in_runner", record_host_init
    )
    monkeypatch.setattr(orchestrator, "_initialize_runner_user", record_runner_init)
    monkeypatch.setattr(orchestrator, "_pull_builder_assets", fail_asset_pull)

    with pytest.raises(RuntimeError) as captured:
        orchestrator._build_base_image("0.3.0-test", rebuild=True)

    assert captured.value is original_failure
    assert initialization_order == [
        "host-init",
        "runner-init-1.16.0",
        "asset-pulls",
    ]
    builder_removals = [
        args
        for args in calls
        if args == ("vm", "rm", orchestrator.BASE_VM_NAME, "--force")
    ]
    assert len(builder_removals) == 2
    assert not any(args[:2] == ("image", "import") for args in calls)


@pytest.mark.parametrize("backing_kind", ["missing", "directory", "symlink"])
def test_ensure_shared_volume_rebuilds_stale_volume_records(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    backing_kind: str,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    if backing_kind == "directory":
        backing.mkdir()
    elif backing_kind == "symlink":
        target = tmp_path / "target.raw"
        target.write_bytes(b"volume")
        backing.symlink_to(target)

    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(backing)),
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    seeded: list[bool] = []
    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)
    monkeypatch.setattr(
        orchestrator, "_create_and_seed_volume", lambda: seeded.append(True)
    )

    orchestrator.ensure_shared_volume()

    assert calls == [
        (
            ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force"),
            {"timeout": 30},
        )
    ]
    assert seeded == [True]


def test_ensure_shared_volume_rejects_poisoned_arbitrary_path_without_mutation(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(unrelated)),
    )

    def unexpected_mvm(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("poisoned records must not trigger volume mutation")

    monkeypatch.setattr(orchestrator, "mvm", unexpected_mvm)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator.ensure_shared_volume()

    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_uses_unprivileged_mkfs_directory_copy(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "manifest.json").write_text("{}", encoding="utf-8")
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"\0" * 4096)
    owner_before = (backing.stat().st_uid, backing.stat().st_gid)
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    orchestrator._populate_volume_image(backing, mirror)

    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command[:4] == ["mkfs.ext4", "-F", "-d", str(mirror)]
    assert command[4].startswith("/proc/self/fd/")
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 900
    assert kwargs["pass_fds"] == (int(command[4].rsplit("/", 1)[-1]),)
    assert (backing.stat().st_uid, backing.stat().st_gid) == owner_before


def test_populate_volume_image_rejects_symlink_backing_before_mkfs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    target = tmp_path / "target.raw"
    target.write_bytes(b"volume")
    backing.symlink_to(target)
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not run for a symlink backing path")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="symlink"):
        orchestrator._populate_volume_image(backing, mirror)


def test_populate_volume_image_rejects_symlinked_cache_ancestor_before_mkfs(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    actual_cache = tmp_path / "actual-cache"
    actual_backing = actual_cache / "volumes" / "asset-mirror.raw"
    actual_backing.parent.mkdir(parents=True)
    actual_backing.write_bytes(b"volume")
    configured_cache = tmp_path / "configured-cache"
    configured_cache.symlink_to(actual_cache, target_is_directory=True)
    recorded_backing = configured_cache / "volumes" / "asset-mirror.raw"
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(configured_cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not traverse a symlinked cache ancestor")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="safely open shared volume directory"):
        orchestrator._populate_volume_image(recorded_backing, mirror)

    assert actual_backing.read_bytes() == b"volume"


def test_create_volume_removes_record_when_created_backing_is_missing(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    cache = tmp_path / "cache"
    missing_backing = cache / "volumes" / "asset-mirror.raw"
    missing_backing.parent.mkdir(parents=True)
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(missing_backing)),
    )
    calls: list[tuple[tuple[str, ...], dict[str, object]]] = []

    def fake_mvm(*args: str, **kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append((args, kwargs))
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="backing file is missing"):
        orchestrator._create_and_seed_volume()

    assert calls[0][0][:2] == ("volume", "create")
    assert calls[-1] == (
        ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force"),
        {"check": False, "timeout": 30},
    )


@pytest.mark.parametrize("leftover_kind", ["dangling_symlink", "directory", "file"])
def test_stale_record_cleanup_must_remove_backing_path_before_volume_create(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    leftover_kind: str,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    if leftover_kind == "dangling_symlink":
        backing.symlink_to(tmp_path / "missing-target.raw")
    elif leftover_kind == "directory":
        backing.mkdir()
    else:
        backing.write_bytes(b"volume")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(backing)),
    )
    calls: list[tuple[str, ...]] = []

    def stale_remove(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        if args[:2] == ("volume", "create"):
            raise AssertionError("volume create must not follow stale cleanup")
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", stale_remove)

    with pytest.raises(RuntimeError, match="still exists before volume create"):
        orchestrator.ensure_shared_volume(rebuild=True)

    assert calls == [
        ("volume", "rm", orchestrator.SHARED_VOLUME_NAME, "--force")
    ]
    assert backing.lstat()


def test_created_volume_with_unsafe_record_path_skips_automatic_cleanup(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    monkeypatch.setattr(orchestrator, "ASSET_MIRROR_HOST", str(mirror))
    monkeypatch.setattr(
        orchestrator,
        "_inspect_volume_path",
        lambda _name: (True, str(unrelated)),
    )
    calls: list[tuple[str, ...]] = []

    def fake_mvm(*args: str, **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(args)
        return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator, "mvm", fake_mvm)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator._create_and_seed_volume()

    assert len(calls) == 1
    assert calls[0][:2] == ("volume", "create")
    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_rejects_arbitrary_regular_file(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    (cache / "volumes").mkdir(parents=True)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    unrelated = tmp_path / "unrelated.data"
    unrelated.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("mkfs must not run for an arbitrary path")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="unexpected shared volume backing path"):
        orchestrator._populate_volume_image(unrelated, mirror)

    assert unrelated.read_bytes() == b"must survive"


def test_populate_volume_image_detects_path_replacement_and_pins_original_inode(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    backing.write_bytes(b"volume")
    original_inode = backing.stat().st_ino
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    victim = tmp_path / "victim.data"
    victim.write_bytes(b"must survive")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))
    command_seen: list[str] = []

    def replace_path_during_mkfs(
        command: list[str], **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        command_seen.extend(command)
        backing.unlink()
        backing.symlink_to(victim)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(
        orchestrator.subprocess, "run", replace_path_during_mkfs
    )

    with pytest.raises(RuntimeError, match="replaced during population"):
        orchestrator._populate_volume_image(backing, mirror)

    pinned_fd = int(command_seen[-1].rsplit("/", 1)[-1])
    assert command_seen[-1] == f"/proc/self/fd/{pinned_fd}"
    assert original_inode != victim.stat().st_ino
    assert victim.read_bytes() == b"must survive"


@pytest.mark.skipif(
    shutil.which("mkfs.ext4") is None or shutil.which("debugfs") is None,
    reason="e2fsprogs is required for the direct ext4 population check",
)
def test_populate_volume_image_directly_seeds_a_real_ext4_image(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    cache = tmp_path / "cache"
    backing = cache / "volumes" / "asset-mirror.raw"
    backing.parent.mkdir(parents=True)
    with backing.open("wb") as volume_file:
        volume_file.truncate(32 * 1024 * 1024)
    owner_before = (backing.stat().st_uid, backing.stat().st_gid)
    mirror = tmp_path / "mirror"
    mirror.mkdir()
    (mirror / "manifest.json").write_text('{"seeded": true}\n', encoding="utf-8")
    monkeypatch.setenv("MVM_CACHE_DIR", str(cache))

    orchestrator._populate_volume_image(backing, mirror)

    inspected = subprocess.run(
        ["debugfs", "-R", "cat /manifest.json", str(backing)],
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert inspected.returncode == 0, inspected.stderr
    assert inspected.stdout == '{"seeded": true}\n'
    assert (backing.stat().st_uid, backing.stat().st_gid) == owner_before


def test_candidate_version_probe_is_exact_and_isolated_from_host_state(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    candidate = tmp_path / "candidate-mvm"
    candidate.write_bytes(b"candidate")
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(orchestrator, "MVM_BINARY", "/host/controller-mvm")
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        return subprocess.CompletedProcess(
            command, 0, stdout="mvm 0.3.0-rc.1\n", stderr=""
        )

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    version = orchestrator._get_mvm_version()

    assert version == "0.3.0-rc.1"
    assert len(calls) == 1
    command, kwargs = calls[0]
    assert command == [str(candidate), "--version"]
    assert kwargs["capture_output"] is True
    assert kwargs["text"] is True
    assert kwargs["timeout"] == 10
    env = kwargs["env"]
    assert isinstance(env, dict)
    for variable in ("MVM_CACHE_DIR", "MVM_CONFIG_DIR", "MVM_TEMP_DIR"):
        assert env[variable] != os.environ.get(variable)
        assert "mvm-candidate-version-" in env[variable]


def test_rebuild_outputs_only_to_the_configured_candidate(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "dist" / "mvm"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    calls: list[list[str]] = []

    def fake_run(command: list[str], **_kwargs: object) -> subprocess.CompletedProcess[str]:
        calls.append(command)
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)
    monkeypatch.setattr(
        orchestrator, "_get_mvm_version", lambda: "0.3.0-rc.1"
    )

    orchestrator._build_mvm_binary("0.3.0-rc.1")

    assert len(calls) == 1
    assert calls[0][-4:-2] == ["--version", "0.3.0-rc.1"]
    assert calls[0][-2:] == ["--output", str(candidate)]
    assert str(controller) not in calls[0]


def test_rebuild_rejects_candidate_with_wrong_reported_version(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "dist" / "mvm"
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda command, **_kwargs: subprocess.CompletedProcess(
            command, 0, stdout="", stderr=""
        ),
    )
    monkeypatch.setattr(orchestrator, "_get_mvm_version", lambda: "0.2.6")

    with pytest.raises(RuntimeError, match="requested 0.3.0, reported 0.2.6"):
        orchestrator._build_mvm_binary("0.3.0")


def test_explicit_candidate_version_does_not_consult_git(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("an explicit candidate version must not consult git")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    assert orchestrator._resolve_candidate_build_version("v0.3.0-rc.2") == (
        "0.3.0-rc.2"
    )


def test_candidate_version_uses_one_exact_release_tag(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    calls: list[tuple[list[str], dict[str, object]]] = []

    def fake_run(
        command: list[str], **kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        calls.append((command, kwargs))
        stdout = "v0.3.0-rc.3\n" if command[1] == "tag" else ""
        return subprocess.CompletedProcess(
            command,
            0,
            stdout=stdout,
            stderr="",
        )

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    assert orchestrator._resolve_candidate_build_version(None) == "0.3.0-rc.3"
    assert len(calls) == 2
    assert calls[0][0] == [
        "git",
        "tag",
        "--points-at",
        "HEAD",
        "--list",
        "v[0-9]*",
    ]
    assert calls[1][0] == [
        "git",
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
    ]
    for _command, kwargs in calls:
        assert kwargs["cwd"] == str(orchestrator._REPO_ROOT)
        assert kwargs["capture_output"] is True
        assert kwargs["text"] is True
        assert kwargs["timeout"] == 10


@pytest.mark.parametrize("tag_output", ["", "v0.2.0\nv0.3.0\n", "release-0.3.0\n"])
def test_candidate_version_rejects_untagged_or_ambiguous_checkout(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tag_output: str,
) -> None:
    monkeypatch.setattr(
        orchestrator.subprocess,
        "run",
        lambda command, **_kwargs: subprocess.CompletedProcess(
            command, 0, stdout=tag_output, stderr=""
        ),
    )

    with pytest.raises(RuntimeError, match="--candidate-version"):
        orchestrator._resolve_candidate_build_version(None)


def test_candidate_version_rejects_dirty_exact_release_tag(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    def fake_run(
        command: list[str], **_kwargs: object
    ) -> subprocess.CompletedProcess[str]:
        stdout = "v0.3.0\n" if command[1] == "tag" else " M internal/file.go\n"
        return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(orchestrator.subprocess, "run", fake_run)

    with pytest.raises(RuntimeError, match="--candidate-version"):
        orchestrator._resolve_candidate_build_version(None)


def test_candidate_version_flag_is_typed(
    orchestrator: ModuleType,
) -> None:
    parser = orchestrator._build_parser()
    parsed = parser.parse_args(
        ["--rebuild", "--candidate-version", "v0.3.0-rc.4"]
    )
    assert parsed.candidate_version == "0.3.0-rc.4"

    with pytest.raises(SystemExit):
        parser.parse_args(["--rebuild", "--candidate-version", "not-a-version"])


@pytest.mark.parametrize(
    "version",
    [
        "01.2.3",
        "1.02.3",
        "1.2.03",
        "1.2.3-01",
        "1.2.3-",
        "1.2.3-rc..1",
        "1.2.3-rc.",
        "1.2.3+",
        "1.2.3+meta..build",
        "1.2.3+meta.",
    ],
)
def test_candidate_version_parser_rejects_non_semver_identifiers(
    orchestrator: ModuleType,
    version: str,
) -> None:
    with pytest.raises(argparse.ArgumentTypeError):
        orchestrator._parse_candidate_version(version)


def test_candidate_version_flag_requires_rebuild(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        orchestrator.sys,
        "argv",
        ["run-system-tests.py", "--candidate-version", "0.3.0"],
    )

    with pytest.raises(SystemExit) as captured:
        orchestrator.main()

    assert captured.value.code == 2


def test_rebuild_rejects_candidate_controller_alias_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(controller))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not start when candidate aliases controller")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="outer controller"):
        orchestrator._build_mvm_binary("0.3.0")


def test_rebuild_rejects_candidate_symlink_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    target = tmp_path / "candidate-target"
    target.write_bytes(b"candidate")
    configured = tmp_path / "candidate-mvm"
    configured.symlink_to(target)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", configured)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(target))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not follow a candidate symlink")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="symlink"):
        orchestrator._build_mvm_binary("0.3.0")


def test_rebuild_rejects_candidate_controller_hardlink_before_build(
    orchestrator: ModuleType,
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    controller = tmp_path / "controller-mvm"
    controller.write_bytes(b"controller")
    candidate = tmp_path / "candidate-mvm"
    os.link(controller, candidate)
    monkeypatch.setattr(orchestrator, "MVM_BINARY", str(controller))
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_CONFIGURED", candidate)
    monkeypatch.setattr(orchestrator, "MVM_CANDIDATE_BINARY", str(candidate))

    def unexpected_run(*_args: object, **_kwargs: object) -> None:
        raise AssertionError("build must not replace a controller inode alias")

    monkeypatch.setattr(orchestrator.subprocess, "run", unexpected_run)

    with pytest.raises(RuntimeError, match="outer controller"):
        orchestrator._build_mvm_binary("0.3.0")
