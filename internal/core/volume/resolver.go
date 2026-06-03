package volume

import (
	"context"
	"encoding/json"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// RELATIONS defines the cross-domain relations for volume enrichment.
var RELATIONS = map[string]model.RelationSpec{
	"vm": {
		FKField:      "id",
		Resolver:     "vm",
		Method:       "find_by_volume_ids",
		RelationName: "vms",
		IsReverse:    true,
		BatchMethod:  "by_volume_id_batch",
	},
}

// ResolveResult holds the result of resolving multiple volume identifiers.
// Matches Python's VolumeResolveResult exactly: items, errors, exit_code.
type ResolveResult struct {
	Volumes  []*model.VolumeItem
	Errors   []string
	ExitCode int
}

// EnrichFunc is a function that enriches volumes in-place with relations.
// Set by the API layer during wiring to avoid circular imports.
type EnrichFunc func(ctx context.Context, volumes []*model.VolumeItem, include []string, relations map[string]model.RelationSpec)

// Resolver resolves volume identifiers (name, ID prefix) to volume objects.
// Matches Python's VolumeResolver.
type Resolver struct {
	repo       Repository
	include    []string
	enrichFunc EnrichFunc
}

// NewResolver creates a new volume resolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{repo: repo}
}

// SetEnrichFunc sets the enrichment function called after each resolution.
// Must be set for cross-domain relation enrichment.
func (r *Resolver) SetEnrichFunc(fn EnrichFunc) {
	r.enrichFunc = fn
}

// SetInclude sets the relation names to include during resolution.
func (r *Resolver) SetInclude(include []string) {
	r.include = include
}

// enrich enriches volumes with relations if include is set.
func (r *Resolver) enrich(ctx context.Context, volumes []*model.VolumeItem) []*model.VolumeItem {
	if r.include != nil && len(volumes) > 0 && r.enrichFunc != nil {
		r.enrichFunc(ctx, volumes, r.include, RELATIONS)
	}
	return volumes
}

// ByID resolves a volume by ID prefix. Returns error if not found or ambiguous.
// Matches Python's VolumeResolver.by_id() — uses FindByPrefix query ONLY.
// Python does NOT attempt exact ID match first; it only uses find_by_prefix.
func (r *Resolver) ByID(ctx context.Context, volumeID string) (*model.VolumeItem, error) {
	// Python only uses find_by_prefix — no exact Get() first.
	matches, err := r.repo.FindByPrefix(ctx, volumeID)
	if err != nil {
		return nil, err
	}

	if len(matches) == 0 {
		// Python: raise VolumeNotFoundError(f"Volume not found: {volume_id!r}")
		return nil, ErrVolumeNotFound(volumeID)
	}
	if len(matches) > 1 {
		// Python: raise VolumeNotFoundError(f"Volume ID is ambiguous: {volume_id!r}")
		return nil, ErrVolumeAmbiguous(volumeID)
	}
	return r.enrich(ctx, matches)[0], nil
}

// ByName resolves a volume by exact name.
// Matches Python's VolumeResolver.by_name().
func (r *Resolver) ByName(ctx context.Context, name string) (*model.VolumeItem, error) {
	v, err := r.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if v == nil {
		// Python: raise VolumeNotFoundError(f"Volume not found by name: {name!r}")
		return nil, ErrVolumeNotFoundByName(name)
	}
	return r.enrich(ctx, []*model.VolumeItem{v})[0], nil
}

// Resolve resolves a volume by name or ID prefix (tries name first).
// Matches Python's VolumeResolver.resolve().
func (r *Resolver) Resolve(ctx context.Context, identifier string) (*model.VolumeItem, error) {
	// Try name first. ByName() already enriches, so return directly.
	v, err := r.ByName(ctx, identifier)
	if err == nil {
		return v, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err
	}

	// Fall back to ID prefix. ByID() already enriches.
	return r.ByID(ctx, identifier)
}

// ResolveMany resolves multiple volume identifiers.
// Matches Python's VolumeResolver.resolve_many() exactly.
// Python prefixes each error with the identifier: f"{identifier}: {e}"
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) *ResolveResult {
	// First, deduplicate input identifiers (matching Python's seen_inputs)
	uniqueIDs := infra.Dedup(identifiers)

	var volumes []*model.VolumeItem
	var errList []string
	// Track resolved volume IDs separately (matching Python's resolved_ids)
	resolvedIDs := make(map[string]bool)

	for _, ident := range uniqueIDs {
		v, err := r.Resolve(ctx, ident)
		if err != nil {
			// Python: errors.append(f"{identifier}: {e}")
			errList = append(errList, err.Error())
			continue
		}
		if !resolvedIDs[v.ID] {
			resolvedIDs[v.ID] = true
			volumes = append(volumes, v)
		}
	}

	volumes = r.enrich(ctx, volumes)

	// Python: exit_code = 1 if errors and not items else 0
	exitCode := 0
	if len(errList) > 0 && len(volumes) == 0 {
		exitCode = 1
	}

	return &ResolveResult{Volumes: volumes, Errors: errList, ExitCode: exitCode}
}

// ResolveByIDs resolves volumes by their exact IDs.
// Matches Python's VolumeResolver.resolve_by_ids() — returns dict[str, VolumeItem]
// (map[string]*Volume in Go).
//
// Python returns {} for empty input (dict comprehension over empty list yields
// empty dict). Go must return an initialized empty map, not nil.
func (r *Resolver) ResolveByIDs(ctx context.Context, ids []string) (map[string]*model.VolumeItem, error) {
	if len(ids) == 0 {
		return map[string]*model.VolumeItem{}, nil
	}
	volumes, err := r.repo.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*model.VolumeItem, len(volumes))
	for _, vol := range volumes {
		result[vol.ID] = vol
	}
	return result, nil
}

// ResolveByVMVolumeIDs resolves volume IDs from a list of JSON-serialized volume ID arrays.
// Matches Python's VolumeResolver.resolve_by_vm_volume_ids() exactly.
//
// Python signature:
//
//	def resolve_by_vm_volume_ids(
//	    self, json_ids_list: list[str]
//	) -> dict[str, list[VolumeItem]]:
//
// Each element in json_ids_list is a JSON string containing an array of volume IDs.
// Collects all unique IDs, resolves them in batch, and maps results back to input strings.
func (r *Resolver) ResolveByVMVolumeIDs(ctx context.Context, idsList []string) (map[string][]*model.VolumeItem, error) {
	// Collect all unique IDs across all JSON strings (matching Python's approach)
	allIDs := make(map[string]struct{})
	for _, jsonStr := range idsList {
		var parsed []string
		if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
			// Skip invalid JSON, non-list, etc. (matches Python's except json.JSONDecodeError, TypeError)
			continue
		}
		for _, id := range parsed {
			allIDs[id] = struct{}{}
		}
	}

	// Resolve all IDs in batch (matching Python's self.resolve_by_ids())
	var resolvedMap map[string]*model.VolumeItem
	if len(allIDs) > 0 {
		ids := make([]string, 0, len(allIDs))
		for id := range allIDs {
			ids = append(ids, id)
		}
		resolved, err := r.repo.FindByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		resolvedMap = make(map[string]*model.VolumeItem, len(resolved))
		for _, vol := range resolved {
			resolvedMap[vol.ID] = vol
		}
	}

	// Map results back to input strings (matching Python's result dict)
	//
	// Python:
	//   for json_str in json_ids_list:
	//       vols: list[VolumeItem] = []          ← always initialized empty list
	//       try:
	//           parsed = json.loads(json_str)
	//           ...
	//       except (json.JSONDecodeError, TypeError):
	//           pass                             ← vols stays empty list
	//       result[json_str] = vols              ← always stored
	//
	// Go must match: for invalid JSON, store []*Volume{} (initialized empty slice),
	// NOT nil. An empty initialized slice and nil slice iterate identically (zero
	// iterations) but differ on reflect.DeepEqual and JSON serialization.
	result := make(map[string][]*model.VolumeItem, len(idsList))
	for _, jsonStr := range idsList {
		vols := []*model.VolumeItem{} // Always create an initialized empty slice (matching Python's list())
		var parsed []string
		if err := json.Unmarshal([]byte(jsonStr), &parsed); err == nil {
			for _, id := range parsed {
				if vol, ok := resolvedMap[id]; ok {
					vols = append(vols, vol)
				}
			}
		}
		result[jsonStr] = vols
	}

	return result, nil
}
