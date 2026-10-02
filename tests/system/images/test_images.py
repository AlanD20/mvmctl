"""Image CLI contracts against the installed candidate inside a disposable runner."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shlex
import shutil
import uuid
from contextlib import contextmanager
from pathlib import Path

import pytest

from tests.system.conftest import _guest_run, _json_success_data, _run_mvm

pytestmark = [pytest.mark.system, pytest.mark.domain_image]


def _images(runner_vm):
    return _json_success_data(_run_mvm(runner_vm, "image", "ls", "--json").stdout, list)


def _alpine(runner_vm):
    matches = [
        i
        for i in _images(runner_vm)
        if i["type"] == "alpine" and i["version"] == "3.23" and i["is_present"]
    ]
    assert len(matches) == 1, (
        "runner prerequisite: exactly one present mirrored Alpine 3.23 image"
    )
    return matches[0]


def _backing_identity(path):
    info = Path(path).lstat()
    assert Path(path).is_file() and not Path(path).is_symlink(), path
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns


def _defaults(runner_vm):
    return [i["id"] for i in _images(runner_vm) if i["is_default"]]


def _image_state(items):
    # SetDefault records the selection time, including when restoring the prior default.
    return [
        {key: value for key, value in item.items() if key != "updated_at"}
        for item in items
    ]


def _restore_default(runner_vm, original):
    # The CLI has no clear-default operation; require a prepared default before changing it.
    assert len(original) == 1, "runner prerequisite: one original image default"
    _run_mvm(runner_vm, "image", "default", original[0])
    assert _defaults(runner_vm) == original


@pytest.fixture(autouse=True)
def image_workspace(runner_vm, tmp_path, monkeypatch):
    """Keep warm files private and prove that prepared image records/backings survive each case."""
    before = _images(runner_vm)
    backing = {i["id"]: _backing_identity(i["path"]) for i in before if i["is_present"]}
    monkeypatch.setenv("MVM_TEMP_DIR", str(tmp_path / "work"))
    monkeypatch.delenv("MVM_WARM_POOL", raising=False)
    yield tmp_path
    assert _image_state(_images(runner_vm)) == _image_state(before), (
        "test changed pre-existing image records or leaked an image"
    )
    for image in before:
        if image["id"] in backing:
            assert _backing_identity(image["path"]) == backing[image["id"]]


@pytest.fixture
def isolated_pull_cache(runner_vm, image_workspace, monkeypatch):
    """Force pulls may replace only records created inside this selected cache."""
    with monkeypatch.context() as selection:
        cache = image_workspace / "pull-cache"
        selection.setenv("MVM_CACHE_DIR", str(cache))
        selection.setenv("MVM_CONFIG_DIR", str(image_workspace / "pull-config"))
        selection.setenv("MVM_TEMP_DIR", str(cache / "tmp"))
        _run_mvm(runner_vm, "cache", "init", "--json")
        # Project the already installed trusted release into the private database.
        _run_mvm(runner_vm, "bin", "pull", "1.16.2", "--default", "--json", timeout=300)
        yield cache
        for item in _images(runner_vm):
            _remove_image(runner_vm, item)
        assert _images(runner_vm) == []


def _extract_alpine(runner_vm, directory):
    image = _alpine(runner_vm)
    raw = directory / f"alpine-{uuid.uuid4().hex}.raw"
    _guest_run(
        runner_vm, f"zstd -d {shlex.quote(image['path'])} -o {shlex.quote(str(raw))}"
    )
    assert raw.is_file() and raw.stat().st_size > 0
    # A unique filesystem UUID also prevents content-derived collisions if IDs change later.
    _guest_run(runner_vm, f"tune2fs -U {uuid.uuid4()} {shlex.quote(str(raw))}")
    return raw


def _ext4(runner_vm, directory, size="64M"):
    raw = directory / f"ext4-{uuid.uuid4().hex}.raw"
    seed = directory / f"ext4-seed-{uuid.uuid4().hex}"
    seed.mkdir(mode=0o700)
    (seed / "etc").mkdir()
    (seed / "etc" / "os-release").write_text("ID=linux\n", encoding="utf-8")
    _guest_run(runner_vm, f"truncate -s {shlex.quote(size)} {shlex.quote(str(raw))}")
    _guest_run(
        runner_vm, f"mkfs.ext4 -F -d {shlex.quote(str(seed))} {shlex.quote(str(raw))}"
    )
    return raw


def _partition_disk(runner_vm, directory):
    """Type favors partition 1; disabling type favors the larger partition 2."""
    sources = [_ext4(runner_vm, directory), _ext4(runner_vm, directory, "128M")]
    uuids = [
        _guest_run(
            runner_vm, f"blkid -p -s UUID -o value {shlex.quote(str(source))}"
        ).stdout.strip()
        for source in sources
    ]
    assert uuids[0] and uuids[1] and uuids[0] != uuids[1]
    disk = directory / "partitioned.raw"
    _guest_run(runner_vm, f"truncate -s 194M {shlex.quote(str(disk))}")
    _guest_run(
        runner_vm,
        f"printf 'label: dos\nstart=2048,size=131072,type=83\nstart=133120,size=262144,type=7\n' | sfdisk {shlex.quote(str(disk))}",
    )
    for source, offset in zip(sources, (1, 65), strict=True):
        _guest_run(
            runner_vm,
            f"dd if={shlex.quote(str(source))} of={shlex.quote(str(disk))} bs=1M seek={offset} conv=notrunc status=none",
        )
    return disk, uuids


def _assert_partition_uuid(runner_vm, image, expected, directory):
    """Read the actual imported compressed backing, independently of its UUID metadata."""
    raw = directory / f"readback-{image['id']}.raw"
    _guest_run(
        runner_vm, f"zstd -d {shlex.quote(image['path'])} -o {shlex.quote(str(raw))}"
    )
    actual = _guest_run(
        runner_vm, f"blkid -p -s UUID -o value {shlex.quote(str(raw))}"
    ).stdout.strip()
    assert actual == expected and image["fs_uuid"] == expected


def _qcow2(runner_vm, raw):
    output = raw.with_suffix(".qcow2")
    _guest_run(
        runner_vm,
        f"qemu-img convert -f raw -O qcow2 {shlex.quote(str(raw))} {shlex.quote(str(output))}",
    )
    info = json.loads(
        _guest_run(
            runner_vm, f"qemu-img info --output=json {shlex.quote(str(output))}"
        ).stdout
    )
    assert info["format"] == "qcow2"
    return output


def _remove_image(runner_vm, receipt, *flags):
    """Remove only the exact full-ID record and backing admitted by a creation receipt."""
    current = next(i for i in _images(runner_vm) if i["id"] == receipt["id"])
    assert current["path"] == receipt["path"] and current["type"] == receipt["type"]
    if "_backing_identity" in receipt:
        assert _backing_identity(receipt["path"]) == receipt["_backing_identity"]
    result = _json_success_data(
        _run_mvm(runner_vm, "image", "rm", receipt["id"], *flags, "--json").stdout, dict
    )
    assert len(result["items"]) == 1
    assert result["items"][0]["result"]["code"] == "image.removed"
    assert result["items"][0]["error"] is None
    assert all(i["id"] != receipt["id"] for i in _images(runner_vm))
    assert not Path(receipt["path"]).exists(), receipt


@contextmanager
def _imported(runner_vm, source, *flags):
    """A unique import identity must be absent before effects; successful JSON grants cleanup ownership."""
    name = f"sys-image-{uuid.uuid4().hex}"
    before = _images(runner_vm)
    assert all(i["type"] != name and i["name"] != name for i in before)
    item = None
    identity = None
    try:
        result = _run_mvm(
            runner_vm,
            "image",
            "import",
            name,
            str(source),
            *flags,
            "--json",
            timeout=300,
        )
        receipt = _json_success_data(result.stdout, dict)
        assert re.fullmatch(r"[0-9a-f]{64}", receipt["id"]), receipt
        assert receipt["id"] not in {i["id"] for i in before}, (
            "creation receipt collided with a baseline ID"
        )
        assert receipt["path"] not in {i["path"] for i in before}, (
            "creation receipt reused a baseline backing"
        )
        item = receipt
        identity = _backing_identity(item["path"])
        item["_backing_identity"] = identity
        assert item["type"] == name and item["is_imported"] and item["is_present"], item
        listed = next(i for i in _images(runner_vm) if i["id"] == item["id"])
        assert all(
            listed[key] == item[key]
            for key in (
                "id",
                "type",
                "version",
                "path",
                "fs_type",
                "is_imported",
                "is_present",
            )
        )
        yield item
    finally:
        if item is not None:
            current = next(
                (i for i in _images(runner_vm) if i["id"] == item["id"]), None
            )
            if current is not None:
                assert _backing_identity(item["path"]) == identity, (
                    "owned image backing changed before cleanup"
                )
                _remove_image(runner_vm, item)


@contextmanager
def _booted(runner_vm, image, *, disk_size_mib=None):
    if disk_size_mib is None:
        disk_size_mib = max(512, image["minimum_rootfs_size_mib"] + 64)
    name = f"sys-image-vm-{uuid.uuid4().hex[:8]}"
    before = _json_success_data(_run_mvm(runner_vm, "vm", "ls", "--json").stdout, list)
    assert all(vm["name"] != name for vm in before)
    receipt = None
    try:
        created = _json_success_data(
            _run_mvm(
                runner_vm,
                "vm",
                "create",
                name,
                "--image",
                image["id"],
                "--no-network",
                "--cloud-init-mode",
                "off",
                "--skip-deblob",
                "--no-pci",
                "-s",
                f"{disk_size_mib}M",
                "--mem",
                "256M",
                "--vcpu",
                "1",
                "--json",
                timeout=180,
            ).stdout,
            list,
        )
        assert len(created) == 1
        candidate = created[0]
        assert candidate["id"] not in {vm["id"] for vm in before}
        assert candidate["name"] == name
        receipt = candidate
        assert receipt["status"] == "running" and receipt["image_id"] == image["id"]
        yield receipt
    finally:
        if receipt is not None:
            _run_mvm(
                runner_vm, "vm", "rm", receipt["id"], "--force", "--json", timeout=120
            )
            after = _json_success_data(
                _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
            )
            assert after == before, "VM teardown changed unrelated VMs or leaked its VM"


class TestImagePull:
    @pytest.mark.parametrize(
        "image_args",
        [
            ["alpine:3.23"],
            ["alpine", "--version", "3.23"],
            ["unused-selector", "--type", "alpine", "--version", "3.23"],
        ],
        ids=["slug", "version", "type-override"],
    )
    def test_image_pull(self, runner_vm, image_args):
        """Cached pull flags select the exact prepared image without replacing it."""
        expected = _alpine(runner_vm)
        item = _json_success_data(
            _run_mvm(
                runner_vm, "image", "pull", *image_args, "--json", timeout=300
            ).stdout,
            dict,
        )
        assert item == expected


class TestImageList:
    def test_image_list_json(self, runner_vm):
        image = _alpine(runner_vm)
        assert re.fullmatch(r"[0-9a-f]{64}", image["id"])
        assert image["fs_type"] == "ext4" and image["distro"] == "alpine"

    def test_image_list_table(self, runner_vm):
        image = _alpine(runner_vm)
        output = _run_mvm(runner_vm, "image", "ls").stdout
        assert all(
            field in output.splitlines()[0]
            for field in ("ID", "NAME", "VERSION", "TYPE")
        )
        assert image["id"][:6] in output and image["name"] in output

    def test_image_inspect_human(self, runner_vm):
        image = _alpine(runner_vm)
        output = _run_mvm(runner_vm, "image", "inspect", image["id"]).stdout
        assert f"Image: {image['name']}" in output
        assert f"ID: {image['id']}" in output and f"Path: {image['path']}" in output

    def test_image_list_remote(self, runner_vm):
        """Bound registry metadata to Alpine; no dynamically selected artifact downloads."""
        data = _json_success_data(
            _run_mvm(
                runner_vm, "image", "ls", "--remote", "--type", "alpine", "--json"
            ).stdout,
            list,
        )
        assert data and all(i["type"] == "alpine" for i in data)
        assert any(i["version"] == "3.23" for i in data)

    def test_image_inspect_json(self, runner_vm):
        image = _alpine(runner_vm)
        data = _json_success_data(
            _run_mvm(runner_vm, "image", "inspect", image["id"][:6], "--json").stdout,
            dict,
        )
        assert (
            data["image"]["id"] == image["id"]
            and data["image"]["name"] == image["name"]
        )
        assert (
            data["storage"]["path"] == image["path"]
            and data["storage"]["fs_type"] == "ext4"
        )


class TestImageDefaults:
    @pytest.mark.destructive
    def test_image_set_default(self, runner_vm):
        original = _defaults(runner_vm)
        assert len(original) == 1
        try:
            target = _alpine(runner_vm)["id"]
            _run_mvm(runner_vm, "image", "default", target)
            assert _defaults(runner_vm) == [target]
        finally:
            _restore_default(runner_vm, original)

    def test_set_default_nonexistent_image_fails(self, runner_vm):
        before = _images(runner_vm)
        result = _run_mvm(
            runner_vm,
            "image",
            "default",
            f"missing-{uuid.uuid4().hex}",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["schema_version"] == 1
            and response["status"] == "error"
            and response["data"] is None
        )
        assert response["error"]["code"] == "image.not_found"
        assert _images(runner_vm) == before


class TestImageWarm:
    @pytest.mark.parametrize("selector", ["slug", "prefix", "all"])
    def test_image_warm(self, runner_vm, image_workspace, selector):
        """Warm output contains complete bytes from each selected compressed backing."""
        image = _alpine(runner_vm)
        selected = _images(runner_vm) if selector == "all" else [image]
        args = (
            ["--all"]
            if selector == "all"
            else ["alpine:3.23" if selector == "slug" else image["id"][:6]]
        )
        paths = _json_success_data(
            _run_mvm(runner_vm, "image", "warm", *args, "--json").stdout, list
        )
        assert len(paths) == len(selected)
        for item, path in zip(selected, paths, strict=True):
            warmed = Path(path)
            assert warmed.parent == image_workspace / "work" / "ready"
            assert warmed.name == f"{item['id']}.{item['fs_type']}"
            expected = image_workspace / f"expected-{item['id']}.raw"
            _guest_run(
                runner_vm,
                f"zstd -d {shlex.quote(item['path'])} -o {shlex.quote(str(expected))}",
            )
            assert warmed.stat().st_size == expected.stat().st_size
            with warmed.open("rb") as actual, expected.open("rb") as source:
                assert (
                    hashlib.file_digest(actual, "sha256").digest()
                    == hashlib.file_digest(source, "sha256").digest()
                )
        identities = [_backing_identity(path) for path in paths]
        again = _json_success_data(
            _run_mvm(runner_vm, "image", "warm", *args, "--json").stdout, list
        )
        assert (
            again == paths and [_backing_identity(path) for path in again] == identities
        )

    def test_warm_nonexistent_image_fails(self, runner_vm):
        result = _run_mvm(
            runner_vm,
            "image",
            "warm",
            f"missing-{uuid.uuid4().hex}",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["status"] == "error"
            and response["error"]["code"] == "image.not_found"
        )


class TestImageImport:
    def test_image_import_local_file(self, runner_vm, image_workspace):
        source = _extract_alpine(runner_vm, image_workspace)
        with _imported(runner_vm, source, "--format", "raw") as image:
            assert image["fs_type"] == "ext4" and image["distro"] == "alpine"

    def test_import_from_nonexistent_path_fails(self, runner_vm, image_workspace):
        name = f"missing-{uuid.uuid4().hex}"
        before = _images(runner_vm)
        result = _run_mvm(
            runner_vm,
            "image",
            "import",
            name,
            str(image_workspace / "missing.qcow2"),
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["schema_version"] == 1
            and response["status"] == "error"
            and response["data"] is None
        )
        assert response["error"]["code"] == "image.import.failed"
        assert "source not found" in response["error"]["message"]
        assert _images(runner_vm) == before


class TestImagePullAdvanced:
    @pytest.mark.destructive
    def test_pull_cached_image_with_default_sets_default(self, runner_vm):
        original = _defaults(runner_vm)
        assert len(original) == 1
        try:
            image = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "image",
                    "pull",
                    "alpine:3.23",
                    "--default",
                    "--json",
                    timeout=300,
                ).stdout,
                dict,
            )
            assert image["id"] == _alpine(runner_vm)["id"]
            assert _defaults(runner_vm) == [image["id"]]
        finally:
            _restore_default(runner_vm, original)

    @pytest.mark.parametrize(
        "flags",
        [[], ["--no-cache"], ["--skip-optimization"], ["--disable-detector", "all"]],
        ids=["force", "no-cache", "skip-optimization", "disable-detector"],
    )
    def test_pull_cached_image_with_force_redownloads(
        self, runner_vm, isolated_pull_cache, flags
    ):
        """Mirror-backed replacement preserves the selected default and retires its old backing."""
        first = _json_success_data(
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine:3.23",
                "--default",
                "--json",
                timeout=300,
            ).stdout,
            dict,
        )
        assert first["is_default"] is True
        # IDs include second-resolution creation time: wait for a distinct creation interval.
        _guest_run(runner_vm, "sleep 1")
        second = _json_success_data(
            _run_mvm(
                runner_vm,
                "image",
                "pull",
                "alpine:3.23",
                "--force",
                *flags,
                "--json",
                timeout=300,
            ).stdout,
            dict,
        )
        assert second["is_default"] is True
        assert second["id"] != first["id"]
        assert (
            second["type"] == "alpine"
            and second["version"] == "3.23"
            and second["is_present"]
        )
        assert _defaults(runner_vm) == [second["id"]]
        assert Path(second["path"]).is_file() and not Path(first["path"]).exists()
        assert all(
            i["id"] == second["id"] or not i["is_present"] for i in _images(runner_vm)
        )

    def test_pull_nonexistent_image_fails_gracefully(self, runner_vm):
        result = _run_mvm(
            runner_vm,
            "image",
            "pull",
            f"missing-{uuid.uuid4().hex}",
            "--json",
            check=False,
        )
        assert result.returncode != 0
        response = json.loads(result.stdout)
        assert (
            response["status"] == "error"
            and response["error"]["code"] == "image.pull.failed"
        )


class TestImageImportAdvanced:
    pytestmark = [pytest.mark.tier2]

    @pytest.mark.requires_kvm
    def test_image_import_with_format_qcow2(self, runner_vm, image_workspace):
        source = _qcow2(runner_vm, _extract_alpine(runner_vm, image_workspace))
        with _imported(
            runner_vm, source, "--format", "qcow2", "--skip-optimization"
        ) as image:
            assert image["fs_type"] == "ext4" and image["distro"] == "alpine"
            with _booted(runner_vm, image) as vm:
                guest = _run_mvm(
                    runner_vm, "exec", vm["id"], "--", "cat", "/etc/os-release"
                )
                assert "ID=alpine" in guest.stdout.splitlines()

    def test_image_import_force_overwrite(self, runner_vm, image_workspace):
        source = _ext4(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            again = _json_success_data(
                _run_mvm(
                    runner_vm,
                    "image",
                    "import",
                    image["type"],
                    str(source),
                    "--format",
                    "raw",
                    "--json",
                ).stdout,
                dict,
            )
            assert again["id"] == image["id"]
            _guest_run(runner_vm, "sleep 1")
            before = _images(runner_vm)
            new = None
            try:
                candidate = _json_success_data(
                    _run_mvm(
                        runner_vm,
                        "image",
                        "import",
                        image["type"],
                        str(source),
                        "--format",
                        "raw",
                        "--skip-optimization",
                        "--force",
                        "--json",
                        timeout=300,
                    ).stdout,
                    dict,
                )
                assert re.fullmatch(r"[0-9a-f]{64}", candidate["id"])
                assert candidate["id"] not in {item["id"] for item in before}
                assert candidate["path"] not in {item["path"] for item in before}
                assert candidate["type"] == image["type"]
                new = candidate
                new["_backing_identity"] = _backing_identity(new["path"])
                assert new["is_imported"] and new["is_present"]
                assert not Path(image["path"]).exists()
            finally:
                if new is not None:
                    _remove_image(runner_vm, new)

    def test_image_import_with_root_partition(self, runner_vm, image_workspace):
        """Explicit partition 2 must differ from the automatic detector's partition 1."""
        disk, uuids = _partition_disk(runner_vm, image_workspace)
        qcow2 = _qcow2(runner_vm, disk)
        with _imported(
            runner_vm, qcow2, "--format", "qcow2", "--skip-optimization"
        ) as automatic:
            _assert_partition_uuid(runner_vm, automatic, uuids[0], image_workspace)
        with _imported(
            runner_vm,
            qcow2,
            "--format",
            "qcow2",
            "--root-partition",
            "2",
            "--skip-optimization",
        ) as image:
            _assert_partition_uuid(runner_vm, image, uuids[1], image_workspace)

    @pytest.mark.parametrize(
        "disabled", ["type", "all"], ids=["disable-type", "disable-all"]
    )
    def test_image_import_with_disable_detector(
        self, runner_vm, image_workspace, disabled
    ):
        """The sfdisk path has no label/filesystem scores; type and size decide this layout."""
        disk, uuids = _partition_disk(runner_vm, image_workspace)
        qcow2 = _qcow2(runner_vm, disk)
        # With type enabled: 64 MiB Linux scores .5-.25; 128 MiB neutral scores zero.
        # Disabling type selects partition 2; disabling all ties both at zero.
        with _imported(
            runner_vm, qcow2, "--format", "qcow2", "--skip-optimization"
        ) as automatic:
            _assert_partition_uuid(runner_vm, automatic, uuids[0], image_workspace)
        if disabled == "type":
            with _imported(
                runner_vm,
                qcow2,
                "--format",
                "qcow2",
                "--skip-optimization",
                "--disable-detector",
                disabled,
            ) as image:
                _assert_partition_uuid(runner_vm, image, uuids[1], image_workspace)
        else:
            before = _images(runner_vm)
            name = f"sys-image-{uuid.uuid4().hex}"
            assert all(item["type"] != name for item in before)
            unexpected = None
            try:
                result = _run_mvm(
                    runner_vm,
                    "image",
                    "import",
                    name,
                    str(qcow2),
                    "--format",
                    "qcow2",
                    "--skip-optimization",
                    "--disable-detector",
                    disabled,
                    "--json",
                    check=False,
                    timeout=300,
                )
                if result.returncode == 0:
                    candidate = _json_success_data(result.stdout, dict)
                    assert re.fullmatch(r"[0-9a-f]{64}", candidate["id"])
                    assert candidate["id"] not in {item["id"] for item in before}
                    assert candidate["path"] not in {item["path"] for item in before}
                    assert candidate["type"] == name
                    unexpected = candidate
                    unexpected["_backing_identity"] = _backing_identity(
                        unexpected["path"]
                    )
                assert result.returncode != 0
                response = json.loads(result.stdout)
                assert (
                    response["schema_version"] == 1
                    and response["status"] == "error"
                    and response["data"] is None
                )
                assert response["error"]["code"] == "image.import.failed"
                assert "tie score 0.000000" in response["error"]["message"]
                assert _images(runner_vm) == before
            finally:
                if unexpected is not None:
                    _remove_image(runner_vm, unexpected)

    @pytest.mark.parametrize(
        "flags,version",
        [
            ([], ""),
            (["--version", "1.0"], "1.0"),
            (["--skip-optimization"], ""),
        ],
        ids=[
            "auto-format",
            "version",
            "skip-optimization",
        ],
    )
    def test_image_import_without_format_auto_detect(
        self, runner_vm, image_workspace, flags, version
    ):
        source = _ext4(runner_vm, image_workspace)
        with _imported(runner_vm, source, *flags) as image:
            assert image["version"] == version and image["fs_type"] == "ext4"
            assert image["arch"] == _alpine(runner_vm)["arch"]


class TestImageImportSetDefault:
    @pytest.mark.destructive
    def test_image_import_with_set_default(self, runner_vm, image_workspace):
        original = _defaults(runner_vm)
        assert len(original) == 1
        source = _ext4(runner_vm, image_workspace)
        try:
            with _imported(
                runner_vm, source, "--format", "raw", "--default", "--skip-optimization"
            ) as image:
                assert _defaults(runner_vm) == [image["id"]]
        finally:
            _restore_default(runner_vm, original)


class TestImageImportCreateVM:
    pytestmark = [pytest.mark.tier2, pytest.mark.requires_kvm, pytest.mark.slow]

    def test_imported_image_vm_creation(self, runner_vm, image_workspace):
        source = _extract_alpine(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            with _booted(runner_vm, image) as vm:
                assert (
                    "ID=alpine"
                    in _run_mvm(
                        runner_vm, "exec", vm["id"], "--", "cat", "/etc/os-release"
                    ).stdout.splitlines()
                )

    def test_import_ubuntu_tar_rootfs(self, runner_vm, image_workspace):
        """Retain tar-rootfs import/boot coverage using the prepared mirror, never curl."""
        mirror = os.environ.get("MVM_ASSET_MIRROR")
        assert mirror, (
            "runner prerequisite: MVM_ASSET_MIRROR points to the prepared read-only mirror"
        )
        source = Path(mirror) / "noble-server-cloudimg-amd64-root.tar.xz"
        assert source.is_file(), (
            f"runner prerequisite: mirrored Ubuntu tar-rootfs {source}"
        )
        mirror_identity = _backing_identity(source)
        with source.open("rb") as original:
            mirror_digest = hashlib.file_digest(original, "sha256").digest()
        try:
            # Only the private copy is an import source; the mirror remains outside cleanup ownership.
            local = image_workspace / source.name
            assert not local.exists()
            shutil.copyfile(source, local)
            assert _backing_identity(source) == mirror_identity
            assert local.stat().st_size == mirror_identity[2]
            with local.open("rb") as copied:
                assert hashlib.file_digest(copied, "sha256").digest() == mirror_digest
            with _imported(
                runner_vm, local, "--format", "tar-rootfs", "--skip-optimization"
            ) as image:
                assert image["distro"] == "ubuntu" and image["fs_type"] == "ext4"
                warmed = _json_success_data(
                    _run_mvm(runner_vm, "image", "warm", image["id"], "--json").stdout,
                    list,
                )
                assert len(warmed) == 1
                backing = Path(warmed[0])
                assert backing.parent == image_workspace / "work" / "ready"
                assert backing.name == f"{image['id']}.ext4"
                backing_bytes = _backing_identity(backing)[2]
                minimum_mib = image["minimum_rootfs_size_mib"]
                assert (
                    backing_bytes > 0
                    and isinstance(minimum_mib, int)
                    and minimum_mib > 0
                )
                disk_mib = max(minimum_mib, (backing_bytes + 1048575) // 1048576)
                with _booted(runner_vm, image, disk_size_mib=disk_mib) as vm:
                    release = _run_mvm(
                        runner_vm, "exec", vm["id"], "--", "cat", "/etc/os-release"
                    ).stdout.splitlines()
                    assert "ID=ubuntu" in release and 'VERSION_ID="24.04"' in release
        finally:
            assert _backing_identity(source) == mirror_identity
            with source.open("rb") as original:
                assert hashlib.file_digest(original, "sha256").digest() == mirror_digest


class TestImageRemove:
    @pytest.mark.destructive
    @pytest.mark.parametrize("flags", [[], ["--force"]], ids=["normal", "force"])
    def test_image_remove_with_fixture(self, runner_vm, image_workspace, flags):
        source = _ext4(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            _remove_image(runner_vm, image, *flags)


class TestImageDependencyDeletion:
    pytestmark = [pytest.mark.tier2, pytest.mark.requires_kvm]

    @pytest.mark.parametrize("stopped", [False, True], ids=["running", "stopped"])
    def test_delete_image_used_by_stopped_vm_fails(
        self, runner_vm, image_workspace, stopped
    ):
        source = _extract_alpine(runner_vm, image_workspace)
        with _imported(
            runner_vm, source, "--format", "raw", "--skip-optimization"
        ) as image:
            with _booted(runner_vm, image) as vm:
                if stopped:
                    _run_mvm(
                        runner_vm,
                        "vm",
                        "stop",
                        vm["id"],
                        "--force",
                        "--json",
                        timeout=120,
                    )
                before = _images(runner_vm)
                identity = _backing_identity(image["path"])
                result = _run_mvm(
                    runner_vm, "image", "rm", image["id"], "--json", check=False
                )
                assert result.returncode != 0
                response = json.loads(result.stdout)
                assert response["status"] == "error"
                assert response["data"]["items"][0]["result"]["code"] == "image.in_use"
                assert (
                    _images(runner_vm) == before
                    and _backing_identity(image["path"]) == identity
                )
                vms = _json_success_data(
                    _run_mvm(runner_vm, "vm", "ls", "--json").stdout, list
                )
                assert next(i for i in vms if i["id"] == vm["id"])["status"] == (
                    "stopped" if stopped else "running"
                )

    @pytest.mark.destructive
    def test_delete_default_image_promotes_other_or_clears(
        self, runner_vm, image_workspace
    ):
        """Remove a formerly-default owned image after explicitly selecting its replacement."""
        original = _defaults(runner_vm)
        assert len(original) == 1
        with _imported(
            runner_vm,
            _ext4(runner_vm, image_workspace),
            "--format",
            "raw",
            "--skip-optimization",
        ) as image:
            try:
                _run_mvm(runner_vm, "image", "default", image["id"])
                assert _defaults(runner_vm) == [image["id"]]
                _restore_default(runner_vm, original)
                _remove_image(runner_vm, image)
                assert _defaults(runner_vm) == original
            finally:
                _restore_default(runner_vm, original)
