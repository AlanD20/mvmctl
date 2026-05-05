"""Tests for RelationEnricher — batch relation enrichment engine."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from mvmctl.core._shared._enrichment import RelationEnricher, RelationSpec


class TestRelationEnricherValidate:
    def test_validates_paths(self):
        enricher = RelationEnricher()
        registry = {"kernel": MagicMock(), "image": MagicMock()}
        with pytest.raises(ValueError, match="Unknown relation 'network'"):
            enricher._validate_paths(["network"], registry)

    def test_empty_include_does_nothing(self):
        enricher = RelationEnricher()
        entities = [MagicMock()]
        enricher.enrich(entities, [], {})
        # Should not raise, entities unchanged


class TestRelationEnricherForward:
    """Forward relation: entity.fk_field → resolver.method(fk_val)."""

    def test_sets_related_entity(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="image_id",
            resolver="image",
            method="by_id",
            relation_name="image",
        )
        entity = MagicMock(image_id="img-001")
        mock_resolver = MagicMock()
        mock_resolver.by_id.return_value = "ImageItem(img-001)"

        enricher._resolve_forward([entity], spec, mock_resolver)
        assert entity.image == "ImageItem(img-001)"

    def test_skips_missing_fk(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="image_id",
            resolver="image",
            method="by_id",
        )
        entity = MagicMock(image_id=None)
        mock_resolver = MagicMock()
        enricher._resolve_forward([entity], spec, mock_resolver)
        mock_resolver.by_id.assert_not_called()

    def test_deduplicates_fk_values(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="image_id",
            resolver="image",
            method="by_id",
        )
        e1 = MagicMock(image_id="img-001")
        e2 = MagicMock(image_id="img-001")
        mock_resolver = MagicMock()
        mock_resolver.by_id.return_value = "image"
        enricher._resolve_forward([e1, e2], spec, mock_resolver)
        assert mock_resolver.by_id.call_count == 1

    def test_batch_method(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="image_id",
            resolver="image",
            method="by_id",
            batch_method="by_id_batch",
        )
        e1 = MagicMock(image_id="img-001")
        e2 = MagicMock(image_id="img-002")
        mock_resolver = MagicMock()
        mock_resolver.by_id_batch.return_value = {
            "img-001": "Image1",
            "img-002": "Image2",
        }
        enricher._resolve_forward([e1, e2], spec, mock_resolver)
        mock_resolver.by_id_batch.assert_called_once()
        assert e1.image == "Image1"
        assert e2.image == "Image2"

    def test_empty_fk_list(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="image_id",
            resolver="image",
            method="by_id",
        )
        mock_resolver = MagicMock()
        enricher._resolve_forward([], spec, mock_resolver)
        mock_resolver.by_id.assert_not_called()


class TestRelationEnricherReverse:
    """Reverse relation: entity.id → resolver.list_by_x(entity.id)."""

    def test_sets_related_list(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="id",
            resolver="network_lease",
            method="list_by_network_id",
            relation_name="leases",
            is_reverse=True,
        )
        entity = MagicMock(id="net-001")
        mock_resolver = MagicMock()
        mock_resolver.list_by_network_id.return_value = ["lease1", "lease2"]

        enricher._resolve_reverse([entity], spec, mock_resolver)
        assert entity.leases == ["lease1", "lease2"]

    def test_batch_reverse(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="id",
            resolver="network_lease",
            method="list_by_network_id",
            relation_name="leases",
            is_reverse=True,
            batch_method="list_by_network_id_batch",
        )
        e1 = MagicMock(id="net-001")
        e2 = MagicMock(id="net-002")
        mock_resolver = MagicMock()
        mock_resolver.list_by_network_id_batch.return_value = {
            "net-001": ["l1"],
            "net-002": ["l2"],
        }
        enricher._resolve_reverse([e1, e2], spec, mock_resolver)
        mock_resolver.list_by_network_id_batch.assert_called_once()
        assert e1.leases == ["l1"]


class TestRelationEnricherNested:
    """Nested relation: entity.parent.child → resolver.method(parent_id)."""

    def test_resolves_nested_relation(self):
        enricher = RelationEnricher()
        spec = RelationSpec(
            fk_field="network",
            resolver="network_lease",
            method="list_by_network_id",
            relation_name="leases",
            is_reverse=True,
            batch_method="list_by_network_id_batch",
        )
        parent = MagicMock(id="net-001")
        entity = MagicMock()
        entity.network = parent
        mock_resolver = MagicMock()
        mock_resolver.list_by_network_id_batch.return_value = {
            "net-001": ["lease1"],
        }
        enricher._resolve_nested(
            [entity], "network.leases", spec, mock_resolver
        )
        assert parent.leases == ["lease1"]


class TestRelationEnricherFull:
    """Full enrich flow with include paths and registry."""

    def test_validates_all_paths(self):
        enricher = RelationEnricher()
        registry = {
            "network": RelationSpec(
                fk_field="network_id",
                resolver="network",
                method="by_id",
            ),
            "network.leases": RelationSpec(
                fk_field="network",
                resolver="network_lease",
                method="list_by_network_id",
                is_reverse=True,
            ),
        }
        enricher._validate_paths(["network", "network.leases"], registry)
        # Should not raise

    def test_raises_on_unknown_path(self):
        enricher = RelationEnricher()
        with pytest.raises(ValueError):
            enricher._validate_paths(["unknown"], {})

    def test_enrich_empty_entities(self):
        enricher = RelationEnricher()
        enricher.enrich([], [], {})
        # Should not raise
