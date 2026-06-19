package enricher

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
)

// SnapshotRelations defines resolvable snapshot relations.
var SnapshotRelations = map[string]model.RelationSpec{
	"image": {
		FKField: "image_id", Resolver: "image", Method: "get_image",
		RelationName: "image",
	},
	"kernel": {
		FKField: "kernel_id", Resolver: "kernel", Method: "get_kernel",
		RelationName: "kernel",
	},
	"network": {
		FKField: "network_id", Resolver: "network", Method: "get_network",
		RelationName: "network",
	},
	"binary": {
		FKField: "binary_id", Resolver: "binary", Method: "get_binary",
		RelationName: "binary",
	},
}

// EnrichSnapshot populates resolved relations on Snapshot items.
func (e *Enricher) EnrichSnapshot(ctx context.Context, snapshots []*model.SnapshotItem, include ...string) error {
	if len(snapshots) == 0 {
		return nil
	}
	paths, err := resolveInclude(include, SnapshotRelations)
	if err != nil {
		return err
	}
	return e.enrichSnapshotFromPaths(ctx, snapshots, paths)
}

// enrichSnapshotFromPaths enriches snapshots for the given sorted paths.
func (e *Enricher) enrichSnapshotFromPaths(
	ctx context.Context,
	snapshots []*model.SnapshotItem,
	paths []string,
) error {
	for _, path := range paths {
		switch path {
		case "kernel":
			if err := e.enrichSnapshotKernel(ctx, snapshots); err != nil {
				return err
			}
		case "network":
			if err := e.enrichSnapshotNetwork(ctx, snapshots); err != nil {
				return err
			}
		case "image":
			if err := e.enrichSnapshotImage(ctx, snapshots); err != nil {
				return err
			}
		case "binary":
			if err := e.enrichSnapshotBinary(ctx, snapshots); err != nil {
				return err
			}
		default:
			slog.Debug("Enrichment soft-fail: unknown snapshot relation path", "path", path)
		}
	}
	return nil
}

// enrichSnapshotKernel resolves kernel references for snapshots.
func (e *Enricher) enrichSnapshotKernel(ctx context.Context, snapshots []*model.SnapshotItem) error {
	ids := collectUniqueSnapshotStrings(snapshots, func(s *model.SnapshotItem) string { return s.KernelID })
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
				enrichSoftFail("kernel", "get_kernel", id)
			} else {
				return err
			}
		}
	}
	for _, snap := range snapshots {
		if snap.KernelID != "" {
			snap.Kernel = kernels[snap.KernelID]
		}
	}
	return nil
}

// enrichSnapshotNetwork resolves network references for snapshots.
func (e *Enricher) enrichSnapshotNetwork(ctx context.Context, snapshots []*model.SnapshotItem) error {
	ids := collectUniqueSnapshotStrings(snapshots, func(s *model.SnapshotItem) string { return s.NetworkID })
	if len(ids) == 0 {
		return nil
	}
	networks := make(map[string]*model.NetworkItem, len(ids))
	for _, id := range ids {
		net, err := e.networkRepo.Get(ctx, id)
		if err == nil && net != nil {
			networks[id] = net
		} else if err != nil {
			if isEnrichmentError(err) {
				enrichSoftFail("network", "get_network", id)
			} else {
				return err
			}
		}
	}
	for _, snap := range snapshots {
		if snap.NetworkID != "" {
			snap.Network = networks[snap.NetworkID]
		}
	}
	return nil
}

// enrichSnapshotBinary resolves binary references for snapshots.
func (e *Enricher) enrichSnapshotBinary(ctx context.Context, snapshots []*model.SnapshotItem) error {
	ids := collectUniqueSnapshotStrings(snapshots, func(s *model.SnapshotItem) string { return s.BinaryID })
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
				enrichSoftFail("binary", "get_binary", id)
			} else {
				return err
			}
		}
	}
	for _, snap := range snapshots {
		if snap.BinaryID != "" {
			snap.Binary = binaries[snap.BinaryID]
		}
	}
	return nil
}

// enrichSnapshotImage resolves image references for snapshots.
func (e *Enricher) enrichSnapshotImage(ctx context.Context, snapshots []*model.SnapshotItem) error {
	ids := collectUniqueSnapshotStrings(snapshots, func(s *model.SnapshotItem) string { return s.ImageID })
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
				enrichSoftFail("image", "get_image", id)
			} else {
				return err
			}
		}
	}
	for _, snap := range snapshots {
		if snap.ImageID != "" {
			snap.Image = images[snap.ImageID]
		}
	}
	return nil
}

// collectUniqueSnapshotStrings collects unique non-empty string field values from snapshots.
func collectUniqueSnapshotStrings(snapshots []*model.SnapshotItem, fn func(*model.SnapshotItem) string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range snapshots {
		val := fn(s)
		if val != "" && !seen[val] {
			seen[val] = true
			result = append(result, val)
		}
	}
	return result
}
