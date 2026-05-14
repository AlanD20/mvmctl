# Enrichment Pattern: RelationSpec + Batch Methods

Cross-entity data (e.g., "which VMs reference this image?") is loaded via the enrichment system, not via ad-hoc repository queries in Service classes. Each domain declares `RelationSpec` entries in its Resolver's `RELATIONS` dict, which the `RelationEnricher` dispatches to the related domain's Resolver via batch methods returning `dict[str, list[Item]]`. Domain resolvers must be registered in two places: the `register()` call in the resolver file and `_RESOLVER_MODULE_PATHS` in `_resolver_registry.py`. The enrichment runs in the API layer before calling Service methods — Services receive pre-enriched items and read `item.vms or []`. This replaces an earlier pattern where Services imported VMRepository directly (a cross-domain violation).

## Implementation Note (2026-05)

The "two registrations" rule applies to domain resolvers used in enrichment (vm, kernel, image, binary, network, key, volume, iptables_rule, network_lease). Internal tracker resolvers (e.g., `_nftables_tracker._resolver` registered as `"nftables_rule"`) may call `register()` without a `_RESOLVER_MODULE_PATHS` entry — they are only accessed via `_shared` imports, not through enrichment auto-discovery.
