package vm

import (
	"context"
	"fmt"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ResolveResult holds the result of resolving multiple VM identifiers.
type ResolveResult struct {
	VMs      []*model.VM
	Errors   []string
	ExitCode int
}

// Resolver resolves VM identifiers (name, ID prefix, IP, MAC) to VM objects.
// This is pure resolution — no enrichment. Enrichment is handled by the
// enricher package (internal/enricher) which resolves cross-domain relations.
type Resolver struct {
	repo Repository
}

// NewResolver creates a new VM resolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{repo: repo}
}

// ByID resolves a VM by ID prefix. Returns error if not found or ambiguous.
// Uses prefix matching only — does not try an exact match first.
func (r *Resolver) ByID(ctx context.Context, vmID string) (*model.VM, error) {
	// Only prefix matching
	matches, err := r.repo.FindByPrefix(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("VM not found: %s", vmID))
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		// Error for ambiguous matches
		return nil, errs.NotFound(errs.CodeVMNotFound,
			fmt.Sprintf("ID %s matches multiple VMs: %s", vmID, strings.Join(names, ", ")))
	}
	return matches[0], nil
}

// ByName resolves a VM by exact name.
func (r *Resolver) ByName(ctx context.Context, name string) (*model.VM, error) {
	vm, err := r.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("VM not found: %s", name))
	}
	return vm, nil
}

// ByIP resolves a VM by IP address.
func (r *Resolver) ByIP(ctx context.Context, ip string) (*model.VM, error) {
	vm, err := r.repo.FindByIP(ctx, ip)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("No VM found with IP: %s", ip))
	}
	return vm, nil
}

// ByMAC resolves a VM by MAC address.
func (r *Resolver) ByMAC(ctx context.Context, mac string) (*model.VM, error) {
	vm, err := r.repo.FindByMAC(ctx, mac)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		return nil, errs.NotFound(errs.CodeVMNotFound, fmt.Sprintf("No VM found with MAC: %s", mac))
	}
	return vm, nil
}

// ByImageID resolves VMs by image ID.
func (r *Resolver) ByImageID(ctx context.Context, imageID string) ([]*model.VM, error) {
	return r.repo.GetByImageIDs(ctx, []string{imageID})
}

// ByImageIDBatch resolves VMs by multiple image IDs, returning a map.
func (r *Resolver) ByImageIDBatch(ctx context.Context, imageIDs []string) (map[string][]*model.VM, error) {
	vms, err := r.repo.GetByImageIDs(ctx, imageIDs)
	if err != nil {
		return nil, err
	}
	results := make(map[string][]*model.VM)
	for _, iid := range imageIDs {
		results[iid] = nil
	}
	for _, vm := range vms {
		if _, ok := results[vm.ImageID]; ok {
			results[vm.ImageID] = append(results[vm.ImageID], vm)
		}
	}
	return results, nil
}

// ByNetworkIDBatch resolves VMs by multiple network IDs.
func (r *Resolver) ByNetworkIDBatch(ctx context.Context, networkIDs []string) (map[string][]*model.VM, error) {
	vms, err := r.repo.GetByNetworkIDs(ctx, networkIDs)
	if err != nil {
		return nil, err
	}
	results := make(map[string][]*model.VM)
	for _, nid := range networkIDs {
		results[nid] = nil
	}
	for _, vm := range vms {
		if _, ok := results[vm.NetworkID]; ok {
			results[vm.NetworkID] = append(results[vm.NetworkID], vm)
		}
	}
	return results, nil
}

// ByKernelIDBatch resolves VMs by multiple kernel IDs.
func (r *Resolver) ByKernelIDBatch(ctx context.Context, kernelIDs []string) (map[string][]*model.VM, error) {
	vms, err := r.repo.GetByKernelIDs(ctx, kernelIDs)
	if err != nil {
		return nil, err
	}
	results := make(map[string][]*model.VM)
	for _, kid := range kernelIDs {
		results[kid] = nil
	}
	for _, vm := range vms {
		if _, ok := results[vm.KernelID]; ok {
			results[vm.KernelID] = append(results[vm.KernelID], vm)
		}
	}
	return results, nil
}

// ByBinaryIDBatch resolves VMs by multiple binary IDs.
func (r *Resolver) ByBinaryIDBatch(ctx context.Context, binaryIDs []string) (map[string][]*model.VM, error) {
	vms, err := r.repo.GetByBinaryIDs(ctx, binaryIDs)
	if err != nil {
		return nil, err
	}
	results := make(map[string][]*model.VM)
	for _, bid := range binaryIDs {
		results[bid] = nil
	}
	for _, vm := range vms {
		if _, ok := results[vm.BinaryID]; ok {
			results[vm.BinaryID] = append(results[vm.BinaryID], vm)
		}
	}
	return results, nil
}

// ByVolumeIDBatch resolves VMs by multiple volume IDs.
func (r *Resolver) ByVolumeIDBatch(ctx context.Context, volumeIDs []string) (map[string][]*model.VM, error) {
	vms, err := r.repo.FindByVolumeIDsBatch(ctx, volumeIDs)
	if err != nil {
		return nil, err
	}
	results := make(map[string][]*model.VM)
	for _, vid := range volumeIDs {
		results[vid] = nil
	}
	for _, vm := range vms {
		if vm.VolumeIDs != nil {
			for _, vid := range vm.VolumeIDs {
				if _, ok := results[vid]; ok {
					results[vid] = append(results[vid], vm)
				}
			}
		}
	}
	return results, nil
}

// Resolve resolves a VM by name, IP, MAC, or ID prefix (in that order).
// - Tries by_name first
// - If name fails, checks if identifier contains "." -> by_ip (immediate return on failure)
// - If identifier contains ":" -> by_mac (immediate return on failure)
// - Falls back to by_id
//
// ByIP and ByMAC fail immediately on error (no fall-through) —
// propagate any error (including IsNotFound) before falling through to by_name/by_id.
func (r *Resolver) Resolve(ctx context.Context, identifier string) (*model.VM, error) {
	vm, err := r.ByName(ctx, identifier)
	if err == nil {
		return vm, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err
	}

	// Contains "." -> likely an IP address; raises immediately on failure (no fall-through)
	if strings.Contains(identifier, ".") {
		return r.ByIP(ctx, identifier)
	}

	// Contains ":" -> likely a MAC address; raises immediately on failure (no fall-through)
	if strings.Contains(identifier, ":") {
		return r.ByMAC(ctx, identifier)
	}

	// Fall back to ID prefix
	return r.ByID(ctx, identifier)
}

// ResolveMany resolves multiple VM identifiers, deduplicating results.
// exit_code is 1 if errors and not items, else 0.
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) *ResolveResult {
	// Deduplicate identifiers while preserving order
	uniqueIDs := infra.Dedup(identifiers)

	var vms []*model.VM
	var errsList []string
	resolvedVMIDs := make(map[string]bool)

	for _, ident := range uniqueIDs {
		vm, err := r.Resolve(ctx, ident)
		if err != nil {
			errsList = append(errsList, err.Error())
			continue
		}
		if !resolvedVMIDs[vm.ID] {
			resolvedVMIDs[vm.ID] = true
			vms = append(vms, vm)
		}
	}

	// exit_code = 1 if errors and not items else 0
	exitCode := 0
	if len(errsList) > 0 && len(vms) == 0 {
		exitCode = 1
	}
	return &ResolveResult{VMs: vms, Errors: errsList, ExitCode: exitCode}
}
