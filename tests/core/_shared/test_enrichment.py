"""Tests for the RelationEnricher engine."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

import pytest

from mvmctl.core._shared import Database
from mvmctl.core._shared import RelationEnricher
from mvmctl.core.binary._repository import BinaryRepository
from mvmctl.core.image._repository import ImageRepository
from mvmctl.core.kernel._repository import KernelRepository
from mvmctl.core.network._repository import LeaseRepository, NetworkRepository
from mvmctl.core.vm._repository import VMRepository
from mvmctl.models.binary import BinaryItem
from mvmctl.models.image import ImageItem
from mvmctl.models.kernel import KernelItem
from mvmctl.models.network import NetworkItem, NetworkLeaseItem
from mvmctl.models.vm import VMInstanceItem


def _ts() -> str:
    return datetime.now(tz=timezone.utc).isoformat()


@pytest.fixture
def db() -> Database:
    return Database()


@pytest.fixture
def enricher() -> RelationEnricher:
    return RelationEnricher()


class TestRelationEnricher:
    def test_enrich_forward_single(
        self, db: Database, enricher: RelationEnricher
    ) -> None:
        """VM with include=["image"] should set vm.image to ImageItem."""
        kernel_repo = KernelRepository(db)
        image_repo = ImageRepository(db)
        network_repo = NetworkRepository(db)
        binary_repo = BinaryRepository(db)
        vm_repo = VMRepository(db)

        kernel = KernelItem(
            id="k" * 64,
            name="vmlinux",
            base_name="vmlinux",
            version="5.10",
            arch="x86_64",
            type="elf",
            path="/k",
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        kernel_repo.upsert(kernel)

        image = ImageItem(
            id="i" * 64,
            os_slug="ubuntu",
            os_name="Ubuntu",
            arch="x86_64",
            path="u.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        image_repo.upsert(image)

        network = NetworkItem(
            id="n" * 64,
            name="net1",
            subnet="10.0.0.0/24",
            bridge="br0",
            ipv4_gateway="10.0.0.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        network_repo.upsert(network)

        binary = BinaryItem(
            id="b" * 64,
            name="fc",
            version="1.0",
            full_version="1.0.0",
            ci_version=None,
            path="/fc",
            is_default=False,
            is_present=True,
            created_at=_ts(),
            updated_at=_ts(),
        )
        binary_repo.upsert(binary)

        vm = VMInstanceItem(
            id="v" * 64,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id=network.id,
            tap_device="tap0",
            image_id=image.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm_repo.upsert(vm)

        vms = [vm]
        from mvmctl.core.vm._resolver import VMResolver

        enricher.enrich(vms, ["image"], VMResolver.RELATIONS, db)

        assert vms[0].image is not None
        assert vms[0].image.id == image.id

    def test_enrich_forward_batch(
        self, db: Database, enricher: RelationEnricher
    ) -> None:
        """Two VMs with different images should resolve both in one batch."""
        kernel_repo = KernelRepository(db)
        image_repo = ImageRepository(db)
        network_repo = NetworkRepository(db)
        binary_repo = BinaryRepository(db)
        vm_repo = VMRepository(db)

        kernel = KernelItem(
            id="k" * 64,
            name="vmlinux",
            base_name="vmlinux",
            version="5.10",
            arch="x86_64",
            type="elf",
            path="/k",
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        kernel_repo.upsert(kernel)

        img1 = ImageItem(
            id="i1" + "a" * 62,
            os_slug="ubuntu",
            os_name="Ubuntu",
            arch="x86_64",
            path="u1.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        img2 = ImageItem(
            id="i2" + "b" * 62,
            os_slug="debian",
            os_name="Debian",
            arch="x86_64",
            path="u2.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        image_repo.upsert(img1)
        image_repo.upsert(img2)

        network = NetworkItem(
            id="n" * 64,
            name="net1",
            subnet="10.0.0.0/24",
            bridge="br0",
            ipv4_gateway="10.0.0.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        network_repo.upsert(network)

        binary = BinaryItem(
            id="b" * 64,
            name="fc",
            version="1.0",
            full_version="1.0.0",
            ci_version=None,
            path="/fc",
            is_default=False,
            is_present=True,
            created_at=_ts(),
            updated_at=_ts(),
        )
        binary_repo.upsert(binary)

        vm1 = VMInstanceItem(
            id="v1" + "a" * 62,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id=network.id,
            tap_device="tap0",
            image_id=img1.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm2 = VMInstanceItem(
            id="v2" + "b" * 62,
            name="vm2",
            status="stopped",
            pid=0,
            ipv4="10.0.0.3",
            mac="00:00:00:00:00:02",
            network_id=network.id,
            tap_device="tap1",
            image_id=img2.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock2",
            config_path="/cfg2",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r2",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm_repo.upsert(vm1)
        vm_repo.upsert(vm2)

        vms = [vm1, vm2]
        from mvmctl.core.vm._resolver import VMResolver

        enricher.enrich(vms, ["image"], VMResolver.RELATIONS, db)

        assert vms[0].image is not None
        assert vms[0].image.id == img1.id
        assert vms[1].image is not None
        assert vms[1].image.id == img2.id

    def test_enrich_reverse_single(
        self, db: Database, enricher: RelationEnricher
    ) -> None:
        """Image with include=['vm'] should set image.vms to a list."""
        kernel_repo = KernelRepository(db)
        image_repo = ImageRepository(db)
        network_repo = NetworkRepository(db)
        binary_repo = BinaryRepository(db)
        vm_repo = VMRepository(db)

        kernel = KernelItem(
            id="k" * 64,
            name="vmlinux",
            base_name="vmlinux",
            version="5.10",
            arch="x86_64",
            type="elf",
            path="/k",
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        kernel_repo.upsert(kernel)

        image = ImageItem(
            id="i" * 64,
            os_slug="ubuntu",
            os_name="Ubuntu",
            arch="x86_64",
            path="u.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        image_repo.upsert(image)

        network = NetworkItem(
            id="n" * 64,
            name="net1",
            subnet="10.0.0.0/24",
            bridge="br0",
            ipv4_gateway="10.0.0.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        network_repo.upsert(network)

        binary = BinaryItem(
            id="b" * 64,
            name="fc",
            version="1.0",
            full_version="1.0.0",
            ci_version=None,
            path="/fc",
            is_default=False,
            is_present=True,
            created_at=_ts(),
            updated_at=_ts(),
        )
        binary_repo.upsert(binary)

        vm = VMInstanceItem(
            id="v" * 64,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id=network.id,
            tap_device="tap0",
            image_id=image.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm_repo.upsert(vm)

        images = [image]
        from mvmctl.core.image._resolver import ImageResolver

        enricher.enrich(images, ["vm"], ImageResolver.RELATIONS, db)

        assert images[0].vms is not None
        assert isinstance(images[0].vms, list)
        assert len(images[0].vms) == 1
        assert images[0].vms[0].id == vm.id

    def test_enrich_reverse_batch(
        self,
        db: Database,
        enricher: RelationEnricher,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Two images with VMs should use by_image_id_batch."""
        kernel_repo = KernelRepository(db)
        image_repo = ImageRepository(db)
        network_repo = NetworkRepository(db)
        binary_repo = BinaryRepository(db)
        vm_repo = VMRepository(db)

        kernel = KernelItem(
            id="k" * 64,
            name="vmlinux",
            base_name="vmlinux",
            version="5.10",
            arch="x86_64",
            type="elf",
            path="/k",
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        kernel_repo.upsert(kernel)

        img1 = ImageItem(
            id="i1" + "a" * 62,
            os_slug="ubuntu",
            os_name="Ubuntu",
            arch="x86_64",
            path="u1.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        img2 = ImageItem(
            id="i2" + "b" * 62,
            os_slug="debian",
            os_name="Debian",
            arch="x86_64",
            path="u2.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        image_repo.upsert(img1)
        image_repo.upsert(img2)

        network = NetworkItem(
            id="n" * 64,
            name="net1",
            subnet="10.0.0.0/24",
            bridge="br0",
            ipv4_gateway="10.0.0.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        network_repo.upsert(network)

        binary = BinaryItem(
            id="b" * 64,
            name="fc",
            version="1.0",
            full_version="1.0.0",
            ci_version=None,
            path="/fc",
            is_default=False,
            is_present=True,
            created_at=_ts(),
            updated_at=_ts(),
        )
        binary_repo.upsert(binary)

        vm1 = VMInstanceItem(
            id="v1" + "a" * 62,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id=network.id,
            tap_device="tap0",
            image_id=img1.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm2 = VMInstanceItem(
            id="v2" + "b" * 62,
            name="vm2",
            status="stopped",
            pid=0,
            ipv4="10.0.0.3",
            mac="00:00:00:00:00:02",
            network_id=network.id,
            tap_device="tap1",
            image_id=img2.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock2",
            config_path="/cfg2",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r2",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm_repo.upsert(vm1)
        vm_repo.upsert(vm2)

        batch_calls: list[Any] = []
        from mvmctl.core.vm._resolver import VMResolver

        original_batch = VMResolver.by_image_id_batch

        def tracking_batch(
            self: VMResolver, image_ids: list[str]
        ) -> dict[str, list[VMInstanceItem]]:
            batch_calls.append(image_ids)
            return original_batch(self, image_ids)

        monkeypatch.setattr(VMResolver, "by_image_id_batch", tracking_batch)

        images = [img1, img2]
        from mvmctl.core.image._resolver import ImageResolver

        enricher.enrich(images, ["vm"], ImageResolver.RELATIONS, db)

        assert len(batch_calls) == 1
        assert sorted(batch_calls[0]) == sorted([img1.id, img2.id])
        assert images[0].vms is not None
        assert len(images[0].vms) == 1
        assert images[0].vms[0].id == vm1.id
        assert images[1].vms is not None
        assert len(images[1].vms) == 1
        assert images[1].vms[0].id == vm2.id

    def test_enrich_nested(
        self, db: Database, enricher: RelationEnricher
    ) -> None:
        """VM with include=['network.leases'] should set vm.network.leases to a list."""
        kernel_repo = KernelRepository(db)
        image_repo = ImageRepository(db)
        network_repo = NetworkRepository(db)
        binary_repo = BinaryRepository(db)
        vm_repo = VMRepository(db)
        lease_repo = LeaseRepository(db)

        kernel = KernelItem(
            id="k" * 64,
            name="vmlinux",
            base_name="vmlinux",
            version="5.10",
            arch="x86_64",
            type="elf",
            path="/k",
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        kernel_repo.upsert(kernel)

        image = ImageItem(
            id="i" * 64,
            os_slug="ubuntu",
            os_name="Ubuntu",
            arch="x86_64",
            path="u.img",
            fs_type="ext4",
            minimum_rootfs_size_mib=1024,
            original_size=1024,
            is_default=False,
            is_present=True,
            pulled_at=_ts(),
            created_at=_ts(),
            updated_at=_ts(),
        )
        image_repo.upsert(image)

        network = NetworkItem(
            id="n" * 64,
            name="net1",
            subnet="10.0.0.0/24",
            bridge="br0",
            ipv4_gateway="10.0.0.1",
            bridge_active=False,
            nat_enabled=False,
            is_default=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        network_repo.upsert(network)

        binary = BinaryItem(
            id="b" * 64,
            name="fc",
            version="1.0",
            full_version="1.0.0",
            ci_version=None,
            path="/fc",
            is_default=False,
            is_present=True,
            created_at=_ts(),
            updated_at=_ts(),
        )
        binary_repo.upsert(binary)

        vm = VMInstanceItem(
            id="v" * 64,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id=network.id,
            tap_device="tap0",
            image_id=image.id,
            kernel_id=kernel.id,
            binary_id=binary.id,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vm_repo.upsert(vm)

        lease = NetworkLeaseItem(
            network_id=network.id, ipv4="10.0.0.5", leased_at=_ts()
        )
        lease_repo.acquire(lease.network_id, lease.ipv4, vm_id=vm.id)

        vms = [vm]
        from mvmctl.core.vm._resolver import VMResolver

        enricher.enrich(
            vms, ["network", "network.leases"], VMResolver.RELATIONS, db
        )

        assert vms[0].network is not None
        assert vms[0].network.id == network.id
        assert vms[0].network.leases is not None
        assert isinstance(vms[0].network.leases, list)
        assert len(vms[0].network.leases) == 1
        assert vms[0].network.leases[0].ipv4 == lease.ipv4

    def test_enrich_unknown_path(self, enricher: RelationEnricher) -> None:
        """include=['foo'] should raise ValueError."""
        from mvmctl.core.vm._resolver import VMResolver

        with pytest.raises(ValueError, match="Unknown relation 'foo'"):
            enricher.enrich([], ["foo"], VMResolver.RELATIONS, None)

    def test_enrich_empty_fk_values(
        self, db: Database, enricher: RelationEnricher
    ) -> None:
        """Entities with null FKs should not error and attributes stay None."""
        vm = VMInstanceItem(
            id="v" * 64,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id="n" * 64,
            tap_device="tap0",
            image_id="",
            kernel_id="k" * 64,
            binary_id="b" * 64,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vms = [vm]
        from mvmctl.core.vm._resolver import VMResolver

        enricher.enrich(vms, ["image"], VMResolver.RELATIONS, db)
        assert vms[0].image is None

    def test_enrich_no_include(
        self, db: Database, enricher: RelationEnricher
    ) -> None:
        """Empty include list should not modify entities."""
        vm = VMInstanceItem(
            id="v" * 64,
            name="vm1",
            status="stopped",
            pid=0,
            ipv4="10.0.0.2",
            mac="00:00:00:00:00:01",
            network_id="n" * 64,
            tap_device="tap0",
            image_id="i" * 64,
            kernel_id="k" * 64,
            binary_id="b" * 64,
            api_socket_path="/sock",
            config_path="/cfg",
            cloud_init_mode="none",
            vcpu_count=1,
            mem_size_mib=128,
            disk_size_mib=1024,
            rootfs_path="/r",
            rootfs_suffix=".ext4",
            enable_pci=False,
            enable_logging=False,
            enable_metrics=False,
            enable_console=False,
            created_at=_ts(),
            updated_at=_ts(),
        )
        vms = [vm]
        from mvmctl.core.vm._resolver import VMResolver

        enricher.enrich(vms, [], VMResolver.RELATIONS, db)
        assert vms[0].image is None
