"""Immutable kernel succession after terminal cache retirement — Tier 2."""

from __future__ import annotations

import json
import os
import re
import shlex
import uuid
from pathlib import Path

import pytest

from tests.system.conftest import _guest_run, _json_success_data, _run_mvm
from tests.system.kernel.test_kernel import _KernelPullOwner

pytestmark = [pytest.mark.system, pytest.mark.domain_kernel]


@pytest.fixture
def kernel_successor_caches(runner_vm, tmp_path, monkeypatch):
    """Two fresh physical cache keys, one operator and one immutable kernel identity."""
    from tests.system.cache.test_cache import (
        _SnapshotVolumeCacheRetirement,
        _cache_inventory,
        _cache_release_receipt,
        _prune_backing,
        _prune_metadata,
        _retirement_cache_candidate,
    )

    class SuccessorCache(_SnapshotVolumeCacheRetirement):
        def select(self, environment):
            environment.setenv("MVM_CACHE_DIR", str(self.cache_dir))
            environment.setenv("MVM_CONFIG_DIR", str(self.config_dir))
            environment.setenv("MVM_TEMP_DIR", str(self.cache_dir / "tmp"))
            environment.setenv("TMPDIR", str(self.cache_dir / "tmp"))
            environment.delenv("MVM_WARM_POOL", raising=False)

        def terminal_kernel_receipt(self, identifier):
            device, inode = self.roots[self.cache_dir][:2]
            operator = Path("/var/lib/mvmctl/protected-backing") / str(os.getuid())
            anchor = operator / f"{os.major(device)}-{os.minor(device)}-{inode}"
            record = anchor / "kernel" / identifier / "record.json"
            before = self.root_file_receipt(record)
            value = json.loads(_guest_run(
                self.runner_vm, "sudo -n cat -- " + shlex.quote(str(record)),
            ).stdout)
            assert self.root_file_receipt(record) == before
            assert value["owner"] == os.getuid() and value["id"] == identifier
            assert value["kind"] == "kernel" and value["phase"] == "removed", value
            assert not value["references"] and not value.get("next_references"), value
            assert value["kernel_digest"] == self.sources["kernel"][2][2], value
            paths = (record, record.parent / "lock", anchor / "store-use.lock",
                     operator / "logical" / "kernel" / identifier / "lock")
            return {str(path): self.root_file_receipt(path) for path in paths}

        def finish(self):
            self.resources.cleanup(check_assets=False)
            for domain in ("kernel", "image"):
                for identifier in self.receipts[domain]:
                    if identifier in self.inventory(domain):
                        self.check_bindings(domain, identifier)
                        removed = _json_success_data(self.native(
                            domain, "rm", identifier, "--force", "--json", timeout=120,
                        ).stdout, dict)
                        assert len(removed["items"]) == 1 and removed["items"][0]["error"] is None, removed
                        assert removed["items"][0]["result"]["code"] == f"{domain}.removed", removed
                    self.assert_absent(domain, identifier)
                assert not self.inventory(domain), domain
            for path, (identity, content) in self.staging.items():
                assert _prune_metadata(path.lstat()) == identity and _prune_backing(path) == content
                path.unlink()  # Exactly this owner-recorded unprivileged image conversion.
            self.staging.clear()
            identifier, = self.receipts["kernel"]
            terminal = self.terminal_kernel_receipt(identifier)
            cleaned = _json_success_data(self.native(
                "cache", "clean", "--force", "--json", timeout=210,
            ).stdout, dict)
            assert cleaned["cache_dir"] == str(self.cache_dir) and cleaned["cache_dir_removed"] is True
            assert not cleaned["prune_result"]["failed_ids"] and not cleaned["prune_result"]["had_running_vms"]
            self.cleaned = True
            assert not os.path.lexists(self.cache_dir)
            assert self.terminal_kernel_receipt(identifier) == terminal
            # Store retirement changes store.json; its completed receipt then remains fixed through succession.
            device, inode = self.roots[self.cache_dir][:2]
            anchor = Path("/var/lib/mvmctl/protected-backing") / str(os.getuid()) / (
                f"{os.major(device)}-{os.minor(device)}-{inode}"
            )
            store = anchor / "store.json"
            retired = json.loads(_guest_run(
                self.runner_vm, "sudo -n cat -- " + shlex.quote(str(store)),
            ).stdout)
            assert retired["owner"] == os.getuid() and retired["retirement"]["phase"] == "removed", retired
            self.retired_authority = {**terminal, str(store): self.root_file_receipt(store)}
            self.terminal_complete = True

    assert os.geteuid() != 0, "kernel succession must use the ordinary operator"
    baseline = _cache_inventory(runner_vm)
    baseline["snapshot"] = _json_success_data(_run_mvm(runner_vm, "snapshot", "ls", "--json").stdout, list)
    kernels = [row for row in baseline["kernel"] if row["type"] == "firecracker"
               and row["version"].removeprefix("v") == "6.1.155" and row["is_present"]]
    assert len(kernels) == 1, "preseed the existing Linux 6.1.155 kernel; no acquisition fallback"
    inspected = _json_success_data(_run_mvm(runner_vm, "kernel", "inspect", kernels[0]["id"], "--json").stdout, dict)
    source = Path(inspected["storage"]["path"])
    sources = {"kernel": (kernels[0], source, _prune_backing(source))}
    mirror = Path(os.environ["MVM_ASSET_MIRROR"]).expanduser().resolve(strict=True)
    cases, candidates, failures = [], [], []
    try:
        for label in ("predecessor", "successor"):
            cache, created = _retirement_cache_candidate(runner_vm, tmp_path, tmp_path / f"{label}-candidates")
            candidates.extend(created)
            config = tmp_path / f"{label}-config"
            config.mkdir(mode=0o700)
            cases.append(SuccessorCache(runner_vm, cache, config, baseline, sources, tmp_path / f"{label}-receipts"))
        cases[1].kernel_namespace = cases[0].kernel_namespace
        for case in cases:
            with monkeypatch.context() as selection:
                case.select(selection)
                case.initialize()
                case.borrowed_release = _cache_release_receipt(case, "1.17.0")
                case.reuse_release()
                case.image = case.import_alpine(mirror)
        yield cases
    finally:
        for case in cases:
            try:
                with monkeypatch.context() as selection:
                    case.select(selection)
                    failures.extend(case.cleanup())
            except (Exception, pytest.fail.Exception) as error:
                failures.append(f"retain {case.output}: {error!r}")
        for path, identity, fd in candidates:
            try:
                complete = all(case.terminal_complete for case in cases) and not failures
                if complete and path not in {case.cache_dir for case in cases}:
                    assert _prune_metadata(os.fstat(fd)) == _prune_metadata(path.lstat()) == identity
                    assert not os.listdir(fd)
                    path.rmdir()  # Exactly this creation-owned, unused empty cache candidate.
            finally:
                os.close(fd)
        for domain, expected in baseline.items():
            try:
                current = _json_success_data(_run_mvm(runner_vm, domain, "ls", "--json").stdout,
                                             dict if domain == "config" else list)
                assert current == expected, (domain, expected, current)
            except Exception as error:
                failures.append(f"shared {domain} preservation: {error!r}")
        assert not failures, failures


class TestKernelCacheSuccessor:
    pytestmark = [pytest.mark.tier2, pytest.mark.destructive, pytest.mark.needs_kvm, pytest.mark.slow]

    def test_identical_kernel_boot_after_completed_cache_retirement(self, runner_vm, kernel_successor_caches, monkeypatch):
        """Removal alone refuses succession; completed retirement permits identical import and real boot."""
        predecessor, successor = kernel_successor_caches

        def boot(case):
            from tests.system.vm.test_jailer import _resources

            case.start_lifecycle()
            created = _json_success_data(case.native(
                "vm", "create", f"kernel-successor-{uuid.uuid4().hex}", "--image", case.image["id"],
                "--kernel", case.kernel["id"], "--no-network", "--cloud-init-mode", "off", "--skip-deblob",
                "--no-pci", "--disk-size", "512M", "--mem", "256M", "--vcpu", "1", "--json",
                timeout=180, lifecycle=True,
            ).stdout, list)
            assert len(created) == 1, created
            vm = next(row for row in case.resources.rows("vm") if row["id"] == created[0]["id"])
            assert vm["status"] == "running" and vm["kernel_id"] == case.kernel["id"], vm
            live = _json_success_data(case.native("vm", "inspect", vm["id"], "--json").stdout, dict)
            assert live["vm"]["status"] == "running" and live["vm"]["pid"] == vm["pid"] > 0, live
            guest = case.native("exec", vm["id"], "--timeout", "30", "--",
                                "uname -r; cat /proc/sys/kernel/random/boot_id").stdout.splitlines()
            assert len(guest) == 2 and re.fullmatch(r"6\.1\.155(?:[-+].*)?", guest[0]), guest
            assert str(uuid.UUID(guest[1])) == guest[1], guest
            case.resources.cleanup()
            _resources.reset(case.token)
            case.token = None
            return guest[1]

        with monkeypatch.context() as selection:
            predecessor.select(selection)
            predecessor.kernel = predecessor.import_kernel("successor", default=True)
            identifier = predecessor.kernel["id"]
            assert identifier not in {row["id"] for row in predecessor.baseline["kernel"]}
            first_boot = boot(predecessor)
            predecessor.check_bindings("kernel", identifier)
            predecessor.native("kernel", "rm", identifier, "--force", "--json")
            predecessor.assert_absent("kernel", identifier)
            before_retirement = predecessor.terminal_kernel_receipt(identifier)

        with monkeypatch.context() as selection:
            successor.select(selection)
            result = _run_mvm(
                runner_vm, "kernel", "import", f"refused-successor-{uuid.uuid4().hex}",
                str(successor.sources["kernel"][1]), "--version", predecessor.kernel["version"],
                "--json", check=False,
            )
            for stream in ("stdout", "stderr"):
                (successor.output / f"before-retirement.{stream}").write_text(getattr(result, stream), encoding="utf-8")
            assert result.returncode != 0, (result.stdout, result.stderr)
            refusal = json.loads(result.stdout)
            assert refusal["schema_version"] == 1 and refusal["status"] == "error", refusal
            assert refusal["error"]["code"] == "kernel.import_failed", refusal
            assert refusal["error"]["class"] == 2, refusal  # Public ClassConflict enum.
            assert refusal["error"]["details"]["predecessor_store_retirement_required"] is True, refusal
            assert not successor.inventory("kernel"), "refused import must not publish a public kernel"
            staged = successor.cache_dir / "kernels" / identifier
            assert _KernelPullOwner.file_receipt(staged)[1] == successor.sources["kernel"][2][2], staged

        with monkeypatch.context() as selection:
            predecessor.select(selection)
            assert predecessor.terminal_kernel_receipt(identifier) == before_retirement
            predecessor.finish()

        with monkeypatch.context() as selection:
            successor.select(selection)
            successor.kernel = successor.import_kernel("successor", default=True)
            assert successor.kernel["id"] == identifier
            assert boot(successor) != first_boot
            for path, expected in predecessor.retired_authority.items():
                assert predecessor.root_file_receipt(Path(path)) == expected, path
            assert not os.path.lexists(predecessor.cache_dir), "succession must not recreate the retired cache"
            successor.finish()
            for path, expected in predecessor.retired_authority.items():
                assert predecessor.root_file_receipt(Path(path)) == expected, path
