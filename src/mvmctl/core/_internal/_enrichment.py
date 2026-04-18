"""Batch relation enrichment for resolvers.

Provides a centralized engine that enriches entity lists with related
entities using batch queries. Prevents N+1 query problems by collecting
all FK values and resolving them in a single query per relation.
"""

from __future__ import annotations

from typing import Any, TypeVar

T = TypeVar("T")


class RelationEnricher:
    """Batch-enrich entities with relations using declared registries.

    Processes includes in dependency order (parents before children).
    Uses batch queries for resolve_many scenarios.
    """

    def enrich(
        self,
        entities: list[T],
        include: list[str],
        registry: dict[str, tuple[str, type, str]],
        db: Any,
    ) -> None:
        """Enrich entities in-place with resolved relations.

        Args:
            entities: List of entity instances to enrich (modified in-place).
            include: Relation paths to resolve (e.g., ["kernel", "network.leases"]).
            registry: Resolver's RELATIONS dict mapping path → (fk_field, resolver_cls, method_name).
            db: Database instance for creating resolver instances.

        Raises:
            ValueError: If an include path is not in the registry.
        """
        if not include:
            return

        # 1. Validate all paths
        self._validate_paths(include, registry)

        # 2. Sort by depth so parents resolve before children
        sorted_paths = sorted(include, key=lambda p: p.count("."))

        # 3. Batch-resolve each relation
        for path in sorted_paths:
            self._resolve_relation(entities, path, registry, db)

    def _validate_paths(
        self, include: list[str], registry: dict[str, tuple[str, type, str]]
    ) -> None:
        for path in include:
            if path not in registry:
                available = ", ".join(sorted(registry.keys()))
                raise ValueError(
                    f"Unknown relation '{path}'. Available: {available}"
                )

    def _resolve_relation(
        self,
        entities: list[T],
        path: str,
        registry: dict[str, tuple[str, type, str]],
        db: Any,
    ) -> None:
        fk_field, resolver_cls, method_name = registry[path]
        parts = path.split(".")

        if len(parts) == 1:
            # Direct relation: VM → Kernel
            # fk_field is the FK field on the entity (e.g., "kernel_id")
            self._resolve_direct(
                entities, fk_field, resolver_cls, method_name, db
            )
        else:
            # Nested relation: VM → Network → Leases
            # fk_field is the parent attribute name (e.g., "network")
            self._resolve_nested(
                entities, path, fk_field, resolver_cls, method_name, db
            )

    def _resolve_direct(
        self,
        entities: list[T],
        fk_field: str,
        resolver_cls: type,
        method_name: str,
        db: Any,
    ) -> None:
        # Collect unique FK values, deduplicated
        fk_values: list[str] = []
        seen: set[str] = set()
        for entity in entities:
            val = getattr(entity, fk_field, None)
            if val and val not in seen:
                seen.add(val)
                fk_values.append(val)

        if not fk_values:
            return

        # Batch resolve using the resolver's method.
        # Resolvers accept a repo parameter; pass None to use default repo/db.
        resolver = resolver_cls()
        results: dict[str, Any] = {}
        for fk_val in fk_values:
            method = getattr(resolver, method_name)
            results[fk_val] = method(fk_val)

        # Assign back to entities
        # Attribute name is derived from FK field: "kernel_id" → "kernel"
        attr_name = fk_field.removesuffix("_id")
        for entity in entities:
            val = getattr(entity, fk_field, None)
            if val:
                setattr(entity, attr_name, results.get(val))

    def _resolve_nested(
        self,
        entities: list[T],
        path: str,
        parent_attr: str,
        resolver_cls: type,
        method_name: str,
        db: Any,
    ) -> None:
        # For nested: parent_attr is the parent attribute on the entity (e.g., "network")
        # We resolve children on the parent object
        child_attr = path.rsplit(".", 1)[-1]  # "leases" from "network.leases"

        # Collect unique parent IDs from resolved parent objects, deduplicated
        parent_ids: list[str] = []
        seen: set[str] = set()
        for entity in entities:
            parent = getattr(entity, parent_attr, None)
            if parent is not None:
                parent_id = getattr(parent, "id", None)
                if parent_id and parent_id not in seen:
                    seen.add(parent_id)
                    parent_ids.append(parent_id)

        if not parent_ids:
            return

        # Batch resolve for each parent
        resolver = resolver_cls()
        results: dict[str, Any] = {}
        for parent_id in parent_ids:
            method = getattr(resolver, method_name)
            results[parent_id] = method(parent_id)

        # Assign results to parent objects
        for entity in entities:
            parent = getattr(entity, parent_attr, None)
            if parent is not None:
                parent_id = getattr(parent, "id", None)
                if parent_id:
                    setattr(parent, child_attr, results.get(parent_id))


__all__ = ["RelationEnricher"]
