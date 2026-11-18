"""Release fencing acknowledgement and conservative inspection stay distinct."""

import pytest

from tests.system.network.test_floating_ip_operator import _ownerless


@pytest.mark.parametrize("inspection", [False, True])
@pytest.mark.parametrize("fault", [None, "authority", "identity", "owner", "lease"])
def test_ownerless_receipt_requires_exact_context_and_empty_ownership(inspection, fault):
    resource = dict(id="a" * 32, network_id="b" * 64, name="service", ipv4="10.1.0.100", generation=1)
    item = dict(resource, generation=4, phase="unassociated", retired=False,
                authority_state="unobserved" if inspection else "absent",
                owner_vm_id="", owner_origin="", lifetime="", owner_boot_id="",
                owner_launch_generation=0, lease_remaining_ms=0)
    if fault == "authority":item["authority_state"] = "absent" if inspection else "unobserved"
    elif fault == "identity":item["id"] = "c" * 32
    elif fault == "owner":item["owner_vm_id"] = "d" * 32
    elif fault == "lease":item["lease_remaining_ms"] = 1
    if fault is None:
        _ownerless(item, resource, inspection=inspection)
    else:
        with pytest.raises(AssertionError):
            _ownerless(item, resource, inspection=inspection)
