// Package enricher provides cross-domain enrichment — populating relation fields.
// This is the ONLY package that imports across multiple core/* packages.
//
// Matches src/mvmctl/core/_shared/_enrichment.py exactly in behavior.
// Uses explicit switch/case dispatch per relation (NO reflect, NO string dispatch).
package enricher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/image"
	"mvmctl/internal/core/kernel"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/core/volume"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// ── RelationSpec ────────────────────────────────────────────────────────────
// Matches Python's RelationSpec dataclass exactly.

// RelationSpec specifies a single relation enrichment.
//
// Fields (matching Python dataclass order):
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

// ── Domain relation registries ─────────────────────────────────────────────
// These match Python's Resolver.RELATIONS dicts exactly.
// Keyed by relation path (e.g., "kernel", "network.leases").

// VMRelations matches Python's VMResolver.RELATIONS.
var VMRelations = map[string]RelationSpec{
	"kernel": {
		FKField: "kernel_id", Resolver: "kernel", Method: "get_kernel",
		RelationName: "kernel",
	},
	"image": {
		FKField: "image_id", Resolver: "image", Method: "get_image",
		RelationName: "image",
	},
	"binary": {
		FKField: "binary_id", Resolver: "binary", Method: "get_binary",
		RelationName: "binary",
	},
	"network": {
		FKField: "network_id", Resolver: "network", Method: "get_network",
		RelationName: "network",
	},
	"network.leases": {
		FKField: "network", Resolver: "network_lease",
		Method: "list_by_network_id_batch", RelationName: "leases",
		BatchMethod: "list_by_network_id_batch",
	},
	"volumes": {
		FKField: "volume_ids", Resolver: "volume",
		Method: "", RelationName: "volumes",
		BatchMethod: "resolve_by_vm_volume_ids",
	},
}

// NetworkRelations matches Python's Resolver.RELATIONS.
var NetworkRelations = map[string]RelationSpec{
	"leases": {
		FKField: "id", Resolver: "network_lease",
		Method: "list_by_network_id_batch", RelationName: "leases",
		IsReverse: true, BatchMethod: "list_by_network_id_batch",
	},
	"vm": {
		FKField: "id", Resolver: "vm",
		Method: "by_network_id_batch", RelationName: "vm",
		IsReverse: true, BatchMethod: "by_network_id_batch",
	},
}

// ImageRelations matches Python's Resolver.RELATIONS.
var ImageRelations = map[string]RelationSpec{
	"vm": {
		FKField: "id", Resolver: "vm",
		Method: "by_image_id_batch", RelationName: "vm",
		IsReverse: true, BatchMethod: "by_image_id_batch",
	},
}

// KernelRelations matches Python's Resolver.RELATIONS.
var KernelRelations = map[string]RelationSpec{
	"vm": {
		FKField: "id", Resolver: "vm",
		Method: "by_kernel_id_batch", RelationName: "vm",
		IsReverse: true, BatchMethod: "by_kernel_id_batch",
	},
}

// BinaryRelations matches Python's Resolver.RELATIONS.
var BinaryRelations = map[string]RelationSpec{
	"vm": {
		FKField: "id", Resolver: "vm",
		Method: "by_binary_id_batch", RelationName: "vm",
		IsReverse: true, BatchMethod: "by_binary_id_batch",
	},
}

// VolumeRelations matches Python's VolumeResolver.RELATIONS.
var VolumeRelations = map[string]RelationSpec{
	"vm": {
		FKField: "id", Resolver: "vm",
		Method: "by_volume_id_batch", RelationName: "vm",
		IsReverse: true, BatchMethod: "by_volume_id_batch",
	},
}

// KeyRelations matches Python's KeyResolver.RELATIONS (empty — no relations defined).
var KeyRelations = map[string]RelationSpec{}

// ── Enricher ────────────────────────────────────────────────────────────────

// Enricher provides cross-domain enrichment — populating relation fields.
// This is the ONLY package that imports across multiple core/* packages.
type Enricher struct {
	vmRepo      vm.Repository
	networkRepo network.Repository
	leaseRepo   network.LeaseRepository
	imageRepo   image.Repository
	kernelRepo  kernel.Repository
	binaryRepo  binary.Repository
	volumeRepo  volume.Repository
}

// New creates an Enricher with the given repositories.
func New(
	vmRepo vm.Repository,
	networkRepo network.Repository,
	leaseRepo network.LeaseRepository,
	imageRepo image.Repository,
	kernelRepo kernel.Repository,
	binaryRepo binary.Repository,
	volumeRepo volume.Repository,
) *Enricher {
	return &Enricher{
		vmRepo:      vmRepo,
		networkRepo: networkRepo,
		leaseRepo:   leaseRepo,
		imageRepo:   imageRepo,
		kernelRepo:  kernelRepo,
		binaryRepo:  binaryRepo,
		volumeRepo:  volumeRepo,
	}
}

// ── Generic Enrich ─────────────────────────────────────────────────────────

// Enrich validates include paths against a registry, sorts them by dependency
// order (parents before children), and dispatches to the correct domain method.
//
// Matches Python's RelationEnricher.enrich(entities, include, registry).
// entities must be a pointer to a slice of entity pointers (e.g., *[]*model.VM).
func (e *Enricher) Enrich(ctx context.Context, entities any, include []string, registry map[string]RelationSpec) error {
	if len(include) == 0 {
		return nil
	}

	// Validate paths
	if err := validatePaths(include, registry); err != nil {
		return err
	}

	// Sort by dot count (parents before children)
	sorted := sortByDotCount(include)

	switch ents := entities.(type) {
	case []*model.VM:
		return e.enrichVMFromPaths(ctx, ents, sorted, registry)
	case []*model.Network:
		return e.enrichNetworkFromPaths(ctx, ents, sorted, registry)
	case []*model.ImageItem:
		return e.enrichImageFromPaths(ctx, ents, sorted, registry)
	case []*model.KernelItem:
		return e.enrichKernelFromPaths(ctx, ents, sorted, registry)
	case []*model.BinaryItem:
		return e.enrichBinaryFromPaths(ctx, ents, sorted, registry)
	case []*model.VolumeItem:
		return e.enrichVolumeFromPaths(ctx, ents, sorted, registry)
	default:
		// Unknown entity type — nothing to do (matches Python's no-op for unknown types)
		return nil
	}
}

// validatePaths checks that all include paths exist in the registry.
// Matches Python's RelationEnricher._validate_paths() exactly.
func validatePaths(include []string, registry map[string]RelationSpec) error {
	for _, path := range include {
		if _, ok := registry[path]; !ok {
			available := make([]string, 0, len(registry))
			for k := range registry {
				available = append(available, k)
			}
			sort.Strings(available)
			return fmt.Errorf(
				"Unknown relation '%s'. Available: %s",
				path, strings.Join(available, ", "),
			)
		}
	}
	return nil
}

// ── Enrichment soft-fail helpers ────────────────────────────────────────────
// These match Python's logger.debug format strings exactly.

// enrichSoftFail logs a soft failure for a forward relation.
// Matches Python:
//
//	logger.debug("Enrichment soft-fail: %s %s not found for FK %r",
//	             spec.resolver, spec.method, fk_val)
func enrichSoftFail(resolver, method, fkVal string) {
	slog.Debug(fmt.Sprintf("Enrichment soft-fail: %s %s not found for FK '%s'", resolver, method, fkVal))
}

// enrichSoftFailReverse logs a soft failure for a reverse relation.
// Matches Python:
//
//	logger.debug("Enrichment soft-fail: reverse %s %s not found for %r",
//	             spec.resolver, spec.method, sid)
func enrichSoftFailReverse(resolver, method, sid string) {
	slog.Debug(fmt.Sprintf("Enrichment soft-fail: reverse %s %s not found for '%s'", resolver, method, sid))
}

// enrichSoftFailNested logs a soft failure for a nested relation.
// Matches Python:
//
//	logger.debug("Enrichment soft-fail: nested %s %s not found for %r",
//	             spec.resolver, spec.method, parent_id)
func enrichSoftFailNested(resolver, method, parentID string) {
	slog.Debug(fmt.Sprintf("Enrichment soft-fail: nested %s %s not found for '%s'", resolver, method, parentID))
}

// ── Enrichment error handling ──────────────────────────────────────────────

// isEnrichmentError checks whether err should be soft-failed rather than
// propagated. Matches Python's "except MVMError" which catches the entire
// MVMError hierarchy.
//
// In Go, DomainError is the replacement for MVMError. Every error type that
// was an MVMError subclass in Python is now a *DomainError in Go (via the
// error factory functions in internal/infra/errors/domain.go). Therefore,
// ANY *DomainError should be soft-failed during enrichment.
//
// Non-DomainError errors (e.g., database connection errors, context deadlines)
// are real failures that must propagate.
func isEnrichmentError(err error) bool {
	if err == nil {
		return false
	}
	var de *errs.DomainError
	if errors.As(err, &de) {
		return true
	}
	return false
}

// ── VM enrichment ──────────────────────────────────────────────────────────

// EnrichVM populates resolved relations on VM instances.
// include must specify which relations to load (e.g., "kernel", "image", "binary", "network", "volumes").
func (e *Enricher) EnrichVM(ctx context.Context, vms []*model.VM, include ...string) error {
	if len(vms) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, VMRelations)
	if err != nil {
		return err
	}
	return e.enrichVMFromPaths(ctx, vms, paths, VMRelations)
}

// enrichVMFromPaths enriches VMs for the given sorted paths.
func (e *Enricher) enrichVMFromPaths(
	ctx context.Context,
	vms []*model.VM,
	paths []string,
	registry map[string]RelationSpec,
) error {
	for _, path := range paths {
		spec := registry[path]
		switch path {
		case "kernel":
			if err := e.enrichVMKernel(ctx, vms, spec); err != nil {
				return err
			}
		case "image":
			if err := e.enrichVMImage(ctx, vms, spec); err != nil {
				return err
			}
		case "binary":
			if err := e.enrichVMBinary(ctx, vms, spec); err != nil {
				return err
			}
		case "network":
			if err := e.enrichVMNetwork(ctx, vms, spec); err != nil {
				return err
			}
		case "network.leases":
			if err := e.enrichVMNetworkLeases(ctx, vms, spec); err != nil {
				return err
			}
		case "volumes":
			if err := e.enrichVMVolumes(ctx, vms, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichVMKernel resolves VM kernel references via batch kernel ID lookup.
// Matches Python's _resolve_forward for "kernel" relation.
func (e *Enricher) enrichVMKernel(ctx context.Context, vms []*model.VM, spec RelationSpec) error {
	ids := collectUniqueVMStrings(vms, func(vm *model.VM) string { return vm.KernelID })
	if len(ids) == 0 {
		return nil
	}
	kernels := make(map[string]*model.KernelItem, len(ids))
	for _, id := range ids {
		krn, err := e.kernelRepo.Get(ctx, id)
		if err == nil && krn != nil {
			kernels[id] = krn
		} else if err != nil {
			if isEnrichmentError(err) {
				enrichSoftFail(spec.Resolver, spec.Method, id)
			} else {
				return err
			}
		}
	}
	for _, vm := range vms {
		if vm.KernelID != "" {
			vm.Kernel = kernels[vm.KernelID]
		}
	}
	return nil
}

// enrichVMImage resolves VM image references via batch image ID lookup.
// Matches Python's _resolve_forward for "image" relation.
func (e *Enricher) enrichVMImage(ctx context.Context, vms []*model.VM, spec RelationSpec) error {
	ids := collectUniqueVMStrings(vms, func(vm *model.VM) string { return vm.ImageID })
	if len(ids) == 0 {
		return nil
	}
	images := make(map[string]*model.ImageItem, len(ids))
	for _, id := range ids {
		img, err := e.imageRepo.Get(ctx, id)
		if err == nil && img != nil {
			images[id] = img
		} else if err != nil {
			if isEnrichmentError(err) {
				enrichSoftFail(spec.Resolver, spec.Method, id)
			} else {
				return err
			}
		}
	}
	for _, vm := range vms {
		if vm.ImageID != "" {
			vm.Image = images[vm.ImageID]
		}
	}
	return nil
}

// enrichVMBinary resolves VM binary references via batch binary ID lookup.
// Matches Python's _resolve_forward for "binary" relation.
func (e *Enricher) enrichVMBinary(ctx context.Context, vms []*model.VM, spec RelationSpec) error {
	ids := collectUniqueVMStrings(vms, func(vm *model.VM) string { return vm.BinaryID })
	if len(ids) == 0 {
		return nil
	}
	binaries := make(map[string]*model.BinaryItem, len(ids))
	for _, id := range ids {
		bin, err := e.binaryRepo.Get(ctx, id)
		if err == nil && bin != nil {
			binaries[id] = bin
		} else if err != nil {
			if isEnrichmentError(err) {
				enrichSoftFail(spec.Resolver, spec.Method, id)
			} else {
				return err
			}
		}
	}
	for _, vm := range vms {
		if vm.BinaryID != "" {
			vm.Binary = binaries[vm.BinaryID]
		}
	}
	return nil
}

// enrichVMNetwork resolves VM network references via batch network ID lookup.
// Matches Python's _resolve_forward for "network" relation.
func (e *Enricher) enrichVMNetwork(ctx context.Context, vms []*model.VM, spec RelationSpec) error {
	ids := collectUniqueVMStrings(vms, func(vm *model.VM) string { return vm.NetworkID })
	if len(ids) == 0 {
		return nil
	}
	networks := make(map[string]*model.Network, len(ids))
	for _, id := range ids {
		net, err := e.networkRepo.Get(ctx, id)
		if err == nil && net != nil {
			networks[id] = net
		} else if err != nil {
			if isEnrichmentError(err) {
				enrichSoftFail(spec.Resolver, spec.Method, id)
			} else {
				return err
			}
		}
	}
	for _, vm := range vms {
		if vm.NetworkID != "" {
			vm.Network = networks[vm.NetworkID]
		}
	}
	return nil
}

// enrichVMNetworkLeases resolves leases onto each VM's resolved network.
// Must be called AFTER enrichVMNetwork to ensure vm.Network is populated.
// Matches Python's _resolve_nested for "network.leases" relation.
func (e *Enricher) enrichVMNetworkLeases(ctx context.Context, vms []*model.VM, spec RelationSpec) error {
	netIDs := make(map[string]bool)
	for _, vm := range vms {
		parent, err := safeCastNetwork(vm.Network)
		if err == nil && parent != nil && parent.ID != "" {
			netIDs[parent.ID] = true
		}
	}
	if len(netIDs) == 0 {
		return nil
	}
	uniqueNetIDs := make([]string, 0, len(netIDs))
	for id := range netIDs {
		uniqueNetIDs = append(uniqueNetIDs, id)
	}

	// Batch resolve leases by network IDs.
	leasesByNetID, err := e.leaseRepo.ListAllBatch(ctx, uniqueNetIDs)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailNested(spec.Resolver, spec.Method, strings.Join(uniqueNetIDs, ","))
			return nil
		}
		return err
	}

	// Group leases by network ID.
	leasesMap := make(map[string][]*model.NetworkLeaseItem)
	for _, lease := range leasesByNetID {
		leasesMap[lease.NetworkID] = append(leasesMap[lease.NetworkID], lease)
	}

	// Assign leases to each VM's network.
	for _, vm := range vms {
		parent, err := safeCastNetwork(vm.Network)
		if err != nil || parent == nil {
			continue
		}
		if l := leasesMap[parent.ID]; l != nil {
			parent.Leases = l
		} else {
			parent.Leases = []*model.NetworkLeaseItem{}
		}
	}
	return nil
}

// safeCastNetwork safely type-asserts a resolved Network from any.
// Returns nil if the value is nil or of unexpected type.
func safeCastNetwork(v any) (*model.Network, error) {
	if v == nil {
		return nil, nil
	}
	net, ok := v.(*model.Network)
	if !ok {
		return nil, fmt.Errorf("unexpected network type: %T", v)
	}
	return net, nil
}

// resolveByVMVolumeIDs resolves volumes by VM volume ID lists.
// Takes a list of JSON-encoded volume ID arrays (one per VM) and returns
// a map from the JSON string key to the resolved VolumeItem list.
//
// Matches Python's VolumeResolver.resolve_by_vm_volume_ids() method.
func (e *Enricher) resolveByVMVolumeIDs(
	ctx context.Context,
	jsonIDKeys []string,
) (map[string][]*model.VolumeItem, error) {
	// Collect all unique volume IDs across all JSON keys.
	allVolumeIDs := make(map[string]bool)
	keyToIDs := make(map[string][]string, len(jsonIDKeys))
	for _, key := range jsonIDKeys {
		var ids []string
		if err := json.Unmarshal([]byte(key), &ids); err != nil {
			continue
		}
		keyToIDs[key] = ids
		for _, vid := range ids {
			allVolumeIDs[vid] = true
		}
	}

	if len(allVolumeIDs) == 0 {
		return make(map[string][]*model.VolumeItem), nil
	}

	uniqueIDs := make([]string, 0, len(allVolumeIDs))
	for id := range allVolumeIDs {
		uniqueIDs = append(uniqueIDs, id)
	}

	// Batch resolve all referenced volume IDs.
	vols, err := e.volumeRepo.FindByIDs(ctx, uniqueIDs)
	if err != nil {
		return nil, err
	}

	volByID := make(map[string]*model.VolumeItem, len(vols))
	for _, v := range vols {
		volByID[v.ID] = v
	}

	// Build results dict mapping JSON string key -> resolved Volume list.
	results := make(map[string][]*model.VolumeItem, len(jsonIDKeys))
	for _, key := range jsonIDKeys {
		ids, ok := keyToIDs[key]
		if !ok {
			results[key] = []*model.VolumeItem{}
			continue
		}
		matched := make([]*model.VolumeItem, 0, len(ids))
		for _, vid := range ids {
			if v := volByID[vid]; v != nil {
				matched = append(matched, v)
			}
		}
		if len(matched) > 0 {
			results[key] = matched
		} else {
			results[key] = []*model.VolumeItem{}
		}
	}

	return results, nil
}

// enrichVMVolumes resolves volume references onto VMs.
// Matches Python's _resolve_forward for "volumes" relation with
// batch_method="resolve_by_vm_volume_ids".
//
// Delegates to resolveByVMVolumeIDs for the actual resolution,
// matching Python's VolumeResolver.resolve_by_vm_volume_ids().
func (e *Enricher) enrichVMVolumes(ctx context.Context, vms []*model.VM, spec RelationSpec) error {
	// Collect unique volume ID lists (as JSON strings for stable keys).
	var jsonKeys []string
	seenJSON := make(map[string]bool)
	for _, vm := range vms {
		if len(vm.VolumeIDs) == 0 {
			continue
		}
		// Normalize list to stable JSON string (matching Python's json.dumps with sort_keys=True).
		data, err := json.Marshal(vm.VolumeIDs)
		if err != nil {
			continue
		}
		jsonStr := string(data)
		if seenJSON[jsonStr] {
			continue
		}
		seenJSON[jsonStr] = true
		jsonKeys = append(jsonKeys, jsonStr)
	}

	if len(jsonKeys) == 0 {
		return nil
	}

	results, err := e.resolveByVMVolumeIDs(ctx, jsonKeys)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFail(spec.Resolver, spec.BatchMethod, strings.Join(jsonKeys, ","))
			return nil
		}
		return err
	}

	// Build VM -> jsonStr map to avoid re-marshaling per VM.
	vmToKey := make(map[string]string)
	for _, vm := range vms {
		if len(vm.VolumeIDs) == 0 {
			continue
		}
		data, err := json.Marshal(vm.VolumeIDs)
		if err != nil {
			continue
		}
		vmToKey[vm.ID] = string(data)
	}

	// Assign volumes back to each VM.
	for _, vm := range vms {
		if len(vm.VolumeIDs) == 0 {
			continue
		}
		jsonStr := vmToKey[vm.ID]
		matchedVols := results[jsonStr]
		if matchedVols == nil {
			matchedVols = []*model.VolumeItem{}
		}
		anyVols := make([]*model.VolumeItem, len(matchedVols))
		for i, v := range matchedVols {
			anyVols[i] = v
		}
		vm.Volumes = anyVols
	}
	return nil
}

// ── Network enrichment ─────────────────────────────────────────────────────

// EnrichNetwork populates resolved relations on Network instances.
// If include is empty/nil, enriches all known Network relations (backward compat).
//
// Matches Python's Resolver.enrich() with the specified include list.
func (e *Enricher) EnrichNetwork(ctx context.Context, networks []*model.Network, include ...string) error {
	if len(networks) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, NetworkRelations)
	if err != nil {
		return err
	}
	return e.enrichNetworkFromPaths(ctx, networks, paths, NetworkRelations)
}

// enrichNetworkFromPaths enriches Networks for the given sorted paths.
func (e *Enricher) enrichNetworkFromPaths(
	ctx context.Context,
	networks []*model.Network,
	paths []string,
	registry map[string]RelationSpec,
) error {
	for _, path := range paths {
		spec := registry[path]
		switch path {
		case "leases":
			if err := e.enrichNetworkLeases(ctx, networks, spec); err != nil {
				return err
			}
		case "vm":
			if err := e.enrichNetworkVMs(ctx, networks, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichNetworkLeases resolves leases for each network via batch lease lookup.
// Matches Python's _resolve_reverse for "leases" relation with batch_method.
func (e *Enricher) enrichNetworkLeases(ctx context.Context, networks []*model.Network, spec RelationSpec) error {
	netIDs := extractNetworkIDs(networks)
	if len(netIDs) == 0 {
		return nil
	}

	leasesByNetID, err := e.leaseRepo.ListAllBatch(ctx, netIDs)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailReverse(spec.Resolver, spec.Method, strings.Join(netIDs, ","))
			return nil
		}
		return err
	}

	leasesMap := make(map[string][]*model.NetworkLeaseItem)
	for _, lease := range leasesByNetID {
		leasesMap[lease.NetworkID] = append(leasesMap[lease.NetworkID], lease)
	}

	for _, net := range networks {
		if l := leasesMap[net.ID]; l != nil {
			net.Leases = l
		} else {
			net.Leases = []*model.NetworkLeaseItem{}
		}
	}
	return nil
}

// enrichNetworkVMs resolves VMs referencing each network.
// Matches Python's _resolve_reverse for "vm" relation with batch_method.
func (e *Enricher) enrichNetworkVMs(ctx context.Context, networks []*model.Network, spec RelationSpec) error {
	netIDs := extractNetworkIDs(networks)
	if len(netIDs) == 0 {
		return nil
	}

	vms, err := e.vmRepo.GetByNetworkIDs(ctx, netIDs)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailReverse(spec.Resolver, spec.Method, strings.Join(netIDs, ","))
			return nil
		}
		return err
	}

	vmsByNetID := make(map[string][]*model.VM)
	for _, vm := range vms {
		vmsByNetID[vm.NetworkID] = append(vmsByNetID[vm.NetworkID], vm)
	}

	for _, net := range networks {
		matchedVMs := vmsByNetID[net.ID]
		anyVMs := make([]*model.VM, len(matchedVMs))
		for i, vm := range matchedVMs {
			anyVMs[i] = vm
		}
		net.VMs = anyVMs
	}
	return nil
}

// ── Image enrichment ───────────────────────────────────────────────────────

// EnrichImage populates resolved relations on Image items.
// If include is empty/nil, enriches all known Image relations (backward compat).
//
// Matches Python's Resolver.enrich() with the specified include list.
func (e *Enricher) EnrichImage(ctx context.Context, images []*model.ImageItem, include ...string) error {
	if len(images) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, ImageRelations)
	if err != nil {
		return err
	}
	return e.enrichImageFromPaths(ctx, images, paths, ImageRelations)
}

// enrichImageFromPaths enriches Images for the given sorted paths.
func (e *Enricher) enrichImageFromPaths(
	ctx context.Context,
	images []*model.ImageItem,
	paths []string,
	registry map[string]RelationSpec,
) error {
	for _, path := range paths {
		spec := registry[path]
		switch path {
		case "vm":
			if err := e.enrichImageVMs(ctx, images, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichImageVMs resolves VMs that reference each image.
// Matches Python's _resolve_reverse for "vm" relation with batch_method.
func (e *Enricher) enrichImageVMs(ctx context.Context, images []*model.ImageItem, spec RelationSpec) error {
	ids := collectImageIDs(images)
	if len(ids) == 0 {
		return nil
	}

	vms, err := e.vmRepo.GetByImageIDs(ctx, ids)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailReverse(spec.Resolver, spec.Method, strings.Join(ids, ","))
			return nil
		}
		return err
	}

	vmsByImgID := make(map[string][]*model.VM)
	for _, vm := range vms {
		vmsByImgID[vm.ImageID] = append(vmsByImgID[vm.ImageID], vm)
	}

	for _, img := range images {
		if img == nil {
			continue
		}
		matchedVMs := vmsByImgID[img.ID]
		anyVMs := make([]*model.VM, len(matchedVMs))
		for i, vm := range matchedVMs {
			anyVMs[i] = vm
		}
		img.VMs = anyVMs
	}

	return nil
}

// ── Kernel enrichment ──────────────────────────────────────────────────────

// EnrichKernel populates resolved relations on Kernel items.
// If include is empty/nil, enriches all known Kernel relations (backward compat).
//
// Matches Python's Resolver.enrich() with the specified include list.
func (e *Enricher) EnrichKernel(ctx context.Context, kernels []*model.KernelItem, include ...string) error {
	if len(kernels) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, KernelRelations)
	if err != nil {
		return err
	}
	return e.enrichKernelFromPaths(ctx, kernels, paths, KernelRelations)
}

// enrichKernelFromPaths enriches Kernels for the given sorted paths.
func (e *Enricher) enrichKernelFromPaths(
	ctx context.Context,
	kernels []*model.KernelItem,
	paths []string,
	registry map[string]RelationSpec,
) error {
	for _, path := range paths {
		spec := registry[path]
		switch path {
		case "vm":
			if err := e.enrichKernelVMs(ctx, kernels, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichKernelVMs resolves VMs that reference each kernel.
// Matches Python's _resolve_reverse for "vm" relation with batch_method.
func (e *Enricher) enrichKernelVMs(ctx context.Context, kernels []*model.KernelItem, spec RelationSpec) error {
	ids := collectKernelIDs(kernels)
	if len(ids) == 0 {
		return nil
	}

	vms, err := e.vmRepo.GetByKernelIDs(ctx, ids)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailReverse(spec.Resolver, spec.Method, strings.Join(ids, ","))
			return nil
		}
		return err
	}

	vmsByKrnID := make(map[string][]*model.VM)
	for _, vm := range vms {
		vmsByKrnID[vm.KernelID] = append(vmsByKrnID[vm.KernelID], vm)
	}

	for _, k := range kernels {
		if k == nil {
			continue
		}
		matchedVMs := vmsByKrnID[k.ID]
		anyVMs := make([]*model.VM, len(matchedVMs))
		for i, vm := range matchedVMs {
			anyVMs[i] = vm
		}
		k.VMs = anyVMs
	}

	return nil
}

// ── Binary enrichment ──────────────────────────────────────────────────────

// EnrichBinary populates resolved relations on Binary items.
// If include is empty/nil, enriches all known Binary relations (backward compat).
//
// Matches Python's Resolver.enrich() with the specified include list.
func (e *Enricher) EnrichBinary(ctx context.Context, binaries []*model.BinaryItem, include ...string) error {
	if len(binaries) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, BinaryRelations)
	if err != nil {
		return err
	}
	return e.enrichBinaryFromPaths(ctx, binaries, paths, BinaryRelations)
}

// enrichBinaryFromPaths enriches Binaries for the given sorted paths.
func (e *Enricher) enrichBinaryFromPaths(
	ctx context.Context,
	binaries []*model.BinaryItem,
	paths []string,
	registry map[string]RelationSpec,
) error {
	for _, path := range paths {
		spec := registry[path]
		switch path {
		case "vm":
			if err := e.enrichBinaryVMs(ctx, binaries, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichBinaryVMs resolves VMs that reference each binary.
// Matches Python's _resolve_reverse for "vm" relation with batch_method.
// Sets full *model.VM objects on bin.VMs ([]any), matching Python's VMInstanceItem list.
func (e *Enricher) enrichBinaryVMs(ctx context.Context, binaries []*model.BinaryItem, spec RelationSpec) error {
	ids := collectBinaryIDs(binaries)
	if len(ids) == 0 {
		return nil
	}

	vms, err := e.vmRepo.GetByBinaryIDs(ctx, ids)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailReverse(spec.Resolver, spec.Method, strings.Join(ids, ","))
			return nil
		}
		return err
	}

	vmsByBinID := make(map[string][]*model.VM)
	for _, vm := range vms {
		vmsByBinID[vm.BinaryID] = append(vmsByBinID[vm.BinaryID], vm)
	}

	for _, bin := range binaries {
		if bin == nil {
			continue
		}
		matchedVMs := vmsByBinID[bin.ID]
		// Set full VM objects ([]*model.VM as []any), matching Python's list[VMInstanceItem].
		anyVMs := make([]*model.VM, len(matchedVMs))
		for i, vm := range matchedVMs {
			anyVMs[i] = vm
		}
		bin.VMs = anyVMs
	}

	return nil
}

// ── Volume enrichment ──────────────────────────────────────────────────────

// EnrichVolume populates resolved relations on Volume items.
// If include is empty/nil, enriches all known Volume relations (backward compat).
//
// Matches Python's VolumeResolver.enrich() with the specified include list.
func (e *Enricher) EnrichVolume(ctx context.Context, volumes []*model.VolumeItem, include ...string) error {
	if len(volumes) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, VolumeRelations)
	if err != nil {
		return err
	}
	return e.enrichVolumeFromPaths(ctx, volumes, paths, VolumeRelations)
}

// enrichVolumeFromPaths enriches Volumes for the given sorted paths.
func (e *Enricher) enrichVolumeFromPaths(
	ctx context.Context,
	volumes []*model.VolumeItem,
	paths []string,
	registry map[string]RelationSpec,
) error {
	for _, path := range paths {
		spec := registry[path]
		switch path {
		case "vm":
			if err := e.enrichVolumeVMs(ctx, volumes, spec); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichVolumeVMs resolves VMs that reference each volume.
// Matches Python's _resolve_reverse for "vm" relation with batch_method.
func (e *Enricher) enrichVolumeVMs(ctx context.Context, volumes []*model.VolumeItem, spec RelationSpec) error {
	ids := collectVolumeIDs(volumes)
	if len(ids) == 0 {
		return nil
	}

	vms, err := e.vmRepo.FindByVolumeIDsBatch(ctx, ids)
	if err != nil {
		if isEnrichmentError(err) {
			enrichSoftFailReverse(spec.Resolver, spec.Method, strings.Join(ids, ","))
			return nil
		}
		return err
	}

	vmsByVolID := make(map[string][]*model.VM)
	for _, vm := range vms {
		for _, vid := range vm.VolumeIDs {
			vmsByVolID[vid] = append(vmsByVolID[vid], vm)
		}
	}

	for _, vol := range volumes {
		if vol == nil {
			continue
		}
		matchedVMs := vmsByVolID[vol.ID]
		anyVMs := make([]*model.VM, len(matchedVMs))
		for i, vm := range matchedVMs {
			anyVMs[i] = vm
		}
		vol.VMs = anyVMs
	}

	return nil
}

// ── Key enrichment ─────────────────────────────────────────────────────────

// EnrichKey populates resolved relations on Key items.
// KeyResolver.RELATIONS is empty in Python, so this is a no-op.
// Included for symmetry with other domain methods.
func (e *Enricher) EnrichKey(keys []any) error {
	// No relations defined for keys — matches Python's KeyResolver.RELATIONS = {}.
	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// resolveInclude validates and sorts the include list against the registry.
// include must be non-empty — callers must explicitly specify relations to load.
func resolveInclude(include []string, registry map[string]RelationSpec) ([]string, error) {
	if len(include) == 0 {
		return nil, fmt.Errorf("enrichment include list is required — specify which relations to load")
	}

	if err := validatePaths(include, registry); err != nil {
		return nil, err
	}
	return sortByDotCount(include), nil
}

// collectUniqueVMStrings collects unique non-empty string field values from VMs.
func collectUniqueVMStrings(vms []*model.VM, fn func(*model.VM) string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, vm := range vms {
		val := fn(vm)
		if val != "" && !seen[val] {
			seen[val] = true
			result = append(result, val)
		}
	}
	return result
}

// extractNetworkIDs collects unique network IDs from a slice of Networks.
func extractNetworkIDs(networks []*model.Network) []string {
	seen := make(map[string]bool)
	var result []string
	for _, net := range networks {
		if net == nil || net.ID == "" {
			continue
		}
		if !seen[net.ID] {
			seen[net.ID] = true
			result = append(result, net.ID)
		}
	}
	return result
}

// collectImageIDs collects unique non-empty image IDs from a slice of Images.
func collectImageIDs(images []*model.ImageItem) []string {
	seen := make(map[string]bool)
	var result []string
	for _, img := range images {
		if img == nil || img.ID == "" {
			continue
		}
		if !seen[img.ID] {
			seen[img.ID] = true
			result = append(result, img.ID)
		}
	}
	return result
}

// collectKernelIDs collects unique non-empty kernel IDs from a slice of Kernels.
func collectKernelIDs(kernels []*model.KernelItem) []string {
	seen := make(map[string]bool)
	var result []string
	for _, k := range kernels {
		if k == nil || k.ID == "" {
			continue
		}
		if !seen[k.ID] {
			seen[k.ID] = true
			result = append(result, k.ID)
		}
	}
	return result
}

// collectBinaryIDs collects unique non-empty binary IDs from a slice of Binaries.
func collectBinaryIDs(binaries []*model.BinaryItem) []string {
	seen := make(map[string]bool)
	var result []string
	for _, bin := range binaries {
		if bin == nil || bin.ID == "" {
			continue
		}
		if !seen[bin.ID] {
			seen[bin.ID] = true
			result = append(result, bin.ID)
		}
	}
	return result
}

// collectVolumeIDs collects unique non-empty volume IDs from a slice of Volumes.
func collectVolumeIDs(volumes []*model.VolumeItem) []string {
	seen := make(map[string]bool)
	var result []string
	for _, vol := range volumes {
		if vol == nil || vol.ID == "" {
			continue
		}
		if !seen[vol.ID] {
			seen[vol.ID] = true
			result = append(result, vol.ID)
		}
	}
	return result
}
