"""
Batch relation enrichment for resolvers.

Provides a centralized engine that enriches entity lists with related
entities using batch queries. Prevents N+1 query problems by collecting
all FK values and resolving them in a single query per relation.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, TypeVar

from mvmctl.core._shared._resolver_registry import get as get_resolver

T = TypeVar("T")


@dataclass
class RelationSpec:
    """
    Specification for a single relation enrichment.

    Attributes:
        fk_field: Field name on the source entity. For forward relations this
            is the FK field (e.g., "image_id"). For reverse relations this is
            the source entity's ID field (e.g., "id"). For nested relations
            this is the parent attribute name (e.g., "network").
        resolver: Registered resolver name (string, not class).
        method: Resolver method name for single-value resolution.
        relation_name: Explicit attribute name to set on the entity. If None,
            defaults to fk_field with "_id" removed for forward relations,
            or the path leaf for nested relations.
        is_reverse: True for reverse relations (source.id -> list[targets]).
        batch_method: Optional batch method name. If set, called with a list
            of IDs instead of looping over single-value method calls.

    """

    fk_field: str
    resolver: str
    method: str
    relation_name: str | None = None
    is_reverse: bool = False
    batch_method: str | None = None


class RelationEnricher:
    """
    Batch-enrich entities with relations using declared registries.

    Processes includes in dependency order (parents before children).
    Uses batch queries for resolve_many scenarios.
    """

    def enrich(
        self,
        entities: list[T],
        include: list[str],
        registry: dict[str, RelationSpec],
    ) -> None:
        """
        Enrich entities in-place with resolved relations.

        Args:
            entities: List of entity instances to enrich (modified in-place).
            include: Relation paths to resolve (e.g., ["kernel", "network.leases"]).
            registry: Resolver's RELATIONS dict mapping path → RelationSpec.

        Raises:
            ValueError: If an include path is not in the registry.

        """
        if not include:
            return

        self._validate_paths(include, registry)
        sorted_paths = sorted(include, key=lambda p: p.count("."))

        for path in sorted_paths:
            self._resolve_relation(entities, path, registry)

    def _validate_paths(
        self, include: list[str], registry: dict[str, RelationSpec]
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
        registry: dict[str, RelationSpec],
    ) -> None:
        spec = registry[path]
        resolver_cls = get_resolver(spec.resolver)
        resolver = resolver_cls()

        if "." in path:
            self._resolve_nested(entities, path, spec, resolver)
        elif spec.is_reverse:
            self._resolve_reverse(entities, spec, resolver)
        else:
            self._resolve_forward(entities, spec, resolver)

    def _resolve_forward(
        self,
        entities: list[T],
        spec: RelationSpec,
        resolver: Any,
    ) -> None:
        fk_values: list[str] = []
        seen: set[str] = set()
        for entity in entities:
            val = getattr(entity, spec.fk_field, None)
            if val and val not in seen:
                seen.add(val)
                fk_values.append(val)

        if not fk_values:
            return

        if spec.batch_method:
            batch_fn = getattr(resolver, spec.batch_method)
            results: dict[str, Any] = batch_fn(fk_values)
        else:
            results = {}
            for fk_val in fk_values:
                method = getattr(resolver, spec.method)
                results[fk_val] = method(fk_val)

        relation_name = spec.relation_name or spec.fk_field.removesuffix("_id")
        for entity in entities:
            val = getattr(entity, spec.fk_field, None)
            if val:
                setattr(entity, relation_name, results.get(val))

    def _resolve_reverse(
        self,
        entities: list[T],
        spec: RelationSpec,
        resolver: Any,
    ) -> None:
        source_ids: list[str] = []
        seen: set[str] = set()
        for entity in entities:
            val = getattr(entity, spec.fk_field, None)
            if val and val not in seen:
                seen.add(val)
                source_ids.append(val)

        if not source_ids:
            return

        if spec.batch_method:
            batch_fn = getattr(resolver, spec.batch_method)
            results: dict[str, list[Any]] = batch_fn(source_ids)
        else:
            results = {}
            for sid in source_ids:
                method = getattr(resolver, spec.method)
                results[sid] = method(sid)

        relation_name = spec.relation_name or spec.fk_field
        for entity in entities:
            val = getattr(entity, spec.fk_field, None)
            if val:
                setattr(entity, relation_name, results.get(val, []))

    def _resolve_nested(
        self,
        entities: list[T],
        path: str,
        spec: RelationSpec,
        resolver: Any,
    ) -> None:
        child_attr = path.rsplit(".", 1)[-1]

        parent_ids: list[str] = []
        seen: set[str] = set()
        for entity in entities:
            parent = getattr(entity, spec.fk_field, None)
            if parent is not None:
                parent_id = getattr(parent, "id", None)
                if parent_id and parent_id not in seen:
                    seen.add(parent_id)
                    parent_ids.append(parent_id)

        if not parent_ids:
            return

        if spec.batch_method:
            batch_fn = getattr(resolver, spec.batch_method)
            results: dict[str, Any] = batch_fn(parent_ids)
        else:
            results = {}
            for parent_id in parent_ids:
                method = getattr(resolver, spec.method)
                results[parent_id] = method(parent_id)

        for entity in entities:
            parent = getattr(entity, spec.fk_field, None)
            if parent is not None:
                parent_id = getattr(parent, "id", None)
                if parent_id:
                    setattr(parent, child_attr, results.get(parent_id))


__all__ = ["RelationEnricher", "RelationSpec"]
