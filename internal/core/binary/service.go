package binary

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/download"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
)

// Service is the stateless intra-domain orchestrator for binary operations.
type Service struct {
	repo         Repository
	architecture string
}

func NewService(repo Repository, architecture string) *Service {
	return &Service{
		repo:         repo,
		architecture: architecture,
	}
}

// ListAll returns the architecture-scoped display projection.
func (s *Service) ListAll(ctx context.Context) ([]*model.BinaryItem, error) {
	return s.repo.ListAll(ctx, s.architecture)
}

// GetDefaultFirecracker returns the default firecracker binary, or nil if not set.
func (s *Service) GetDefaultFirecracker(ctx context.Context) (*model.BinaryItem, error) {
	return s.repo.GetDefault(ctx, "firecracker", s.architecture)
}

// ListRemote fetches Firecracker release versions from GitHub.
func (s *Service) ListRemote(ctx context.Context, limit int) ([]model.VersionInfo, error) {
	gh := download.NewGitHub(infra.FirecrackerGitHubRepo)
	releases, err := gh.ListReleases(ctx, limit)
	if err != nil {
		return nil, binaryError(errs.CodeDownloadFailed,
			fmt.Sprintf("Failed to fetch Firecracker releases from GitHub: %v", err))
	}

	versions := make([]model.VersionInfo, 0, len(releases))
	for _, rel := range releases {
		if rel.TagName != "" {
			versions = append(versions, model.VersionInfo{
				Version: NormalizeVersion(rel.TagName),
				Type:    "bin",
				Name:    "firecracker",
			})
		}
	}

	sort.Slice(versions, func(i, j int) bool {
		return version.Compare(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

// ResolvePair resolves and validates the canonical exact-version launch pair.
func (s *Service) ResolvePair(ctx context.Context, firecracker *model.BinaryItem) (*model.BinaryPair, error) {
	if firecracker == nil || firecracker.Type != "firecracker" || firecracker.Version == "" ||
		firecracker.Architecture != s.architecture {
		return nil, binaryError(errs.CodeBinaryPairInvalid, "invalid Firecracker binary selection")
	}
	pairedFirecracker, err := s.repo.GetByTypeAndVersion(
		ctx,
		"firecracker",
		s.architecture,
		firecracker.Version,
	)
	if err != nil {
		return nil, binaryError(errs.CodeBinaryPairInvalid,
			fmt.Sprintf("failed to resolve Firecracker binary: %v", err))
	}
	pairedJailer, err := s.repo.GetByTypeAndVersion(ctx, "jailer", s.architecture, firecracker.Version)
	if err != nil {
		return nil, binaryError(errs.CodeBinaryPairInvalid,
			fmt.Sprintf("failed to resolve Jailer binary: %v", err))
	}
	pair := &model.BinaryPair{Firecracker: pairedFirecracker, Jailer: pairedJailer}
	if pair.Firecracker == nil || pair.Jailer == nil || pair.Firecracker.ID != firecracker.ID ||
		pair.Firecracker.Version != pair.Jailer.Version {
		return nil, binaryError(errs.CodeBinaryPairInvalid,
			fmt.Sprintf("exact Firecracker/Jailer pair is missing or mismatched for version %s", firecracker.Version))
	}
	for _, item := range []*model.BinaryItem{pair.Firecracker, pair.Jailer} {
		if !item.IsPresent {
			return nil, binaryError(errs.CodeBinaryPairInvalid,
				fmt.Sprintf("%s %s is marked missing", item.Type, item.Version))
		}
		if err := validateTrustedBinaryProjection(item); err != nil {
			return nil, binaryError(errs.CodeBinaryUntrusted, err.Error())
		}
	}
	return pair, nil
}

// validateTrustedBinaryProjection checks the repository locator without
// traversing the root-only release store. Root launch admission pins the exact
// installed slot and verifies its executable descriptors before any effects.
func validateTrustedBinaryProjection(item *model.BinaryItem) error {
	want := filepath.Join(infra.TrustedBinaryRoot, item.Architecture, item.Version, item.Type)
	if item.Path != want || filepath.Clean(item.Path) != item.Path {
		return fmt.Errorf(
			"%s %s is not installed in the trusted root at its canonical slot; source-built pairs are unsupported for canonical jailed launch",
			item.Type,
			item.Version,
		)
	}
	return nil
}

// ResolveVersion resolves a version spec (latest, partial, or exact) to a concrete version.
// typ is the binary type (e.g. "firecracker").
func (s *Service) ResolveVersion(ctx context.Context, typ string, versionSpec string) (string, error) {
	spec, err := version.ParseSpec(versionSpec)
	if err != nil {
		return "", binaryError(errs.CodeBinaryVersionGate,
			fmt.Sprintf("Invalid version spec %q: %s", versionSpec, err))
	}

	if !spec.IsPartial() {
		return strings.TrimPrefix(versionSpec, "v"), nil
	}

	remoteVersions, err := s.ListRemote(ctx, 20)
	if err != nil {
		return "", binaryError(errs.CodeBinaryVersionGate,
			fmt.Sprintf("Failed to list remote versions: %s", err))
	}

	allVersions := make([]string, 0, len(remoteVersions))
	for _, remoteVersion := range remoteVersions {
		allVersions = append(allVersions, remoteVersion.Version)
	}
	if len(allVersions) == 0 {
		return "", binaryError(errs.CodeBinaryVersionGate, "No remote Firecracker versions found")
	}

	resolved, err := version.Resolve(allVersions, spec)
	if err != nil {
		return "", binaryError(errs.CodeBinaryVersionGate,
			fmt.Sprintf("Cannot resolve version %q for %s: %s", versionSpec, typ, err))
	}
	return resolved, nil
}
