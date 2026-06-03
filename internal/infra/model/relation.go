// Package model consolidates ALL model types from across the Go codebase into
// a single shared package. No domain imports anything outside the model package.
package model

// RelationSpec specifies a single cross-domain relation enrichment.
//
// Fields (matching Python's RelationSpec dataclass):
//   - FKField: Field name on the source entity. For forward relations this
//     is the FK field (e.g., "image_id"). For reverse relations this is
//     the source entity's ID field (e.g., "id"). For nested relations
//     this is the parent attribute name (e.g., "network").
//   - Resolver: Registered resolver name (string, not class). Used for
//     soft-fail debug messages matching Python's format.
//   - Method: Resolver method name for single-value resolution.
//   - RelationName: Explicit attribute name to set on the entity. If empty,
//     defaults to FKField with "_id" removed for forward relations,
//     or the path leaf for nested relations.
//   - IsReverse: True for reverse relations (source.id -> list[targets]).
//   - BatchMethod: Optional batch method name. If set, called with a list
//     of IDs instead of looping over single-value method calls.
type RelationSpec struct {
	FKField      string
	Resolver     string
	Method       string
	RelationName string
	IsReverse    bool
	BatchMethod  string
}
