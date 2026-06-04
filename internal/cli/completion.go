// Package cli provides CLI display helpers.
package cli

import (
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
)

// ── Shell completion functions ──
// Matches Python's cli/_completion.py functions.

// completeNetworkNames completes with network names and short IDs.
func completeNetworkNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	networks, _ := opRef.NetworkListAll(cmd.Context())
	var results []string
	for _, net := range networks {
		if net.Name != "" && strings.HasPrefix(net.Name, toComplete) && !slices.Contains(results, net.Name) {
			results = append(results, net.Name)
		}
		short := crypto.Truncate(net.ID, 6)
		if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
			results = append(results, short)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeImageIDs completes with local image types and short IDs.
func completeImageIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	images, _, _ := opRef.ImageListAll(cmd.Context(), false, "", false, nil)
	var results []string
	for _, img := range images {
		short := crypto.Truncate(img.ID, 6)
		if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
			results = append(results, short)
		}
		if img.Type != "" && strings.HasPrefix(img.Type, toComplete) && !slices.Contains(results, img.Type) {
			results = append(results, img.Type)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeKernelIDs completes with kernel type:version combos and short IDs.
func completeKernelIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	kernels, _, _ := opRef.KernelList(cmd.Context(), false, false, nil)
	var results []string
	for _, k := range kernels {
		if k.Type != "" && k.Version != "" {
			combo := k.Type + ":" + k.Version
			if strings.HasPrefix(combo, toComplete) && !slices.Contains(results, combo) {
				results = append(results, combo)
			}
		}
		short := crypto.Truncate(k.ID, 6)
		if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
			results = append(results, short)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeBinaryVersions completes with binary names, versions, and short IDs.
func completeBinaryVersions(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	binaries, _, _ := opRef.BinaryList(cmd.Context(), false, nil, nil)
	var results []string
	for _, b := range binaries {
		if b.Name != "" && strings.HasPrefix(b.Name, toComplete) && !slices.Contains(results, b.Name) {
			results = append(results, b.Name)
		}
		if b.Version != "" && strings.HasPrefix(b.Version, toComplete) && !slices.Contains(results, b.Version) {
			results = append(results, b.Version)
		}
		short := crypto.Truncate(b.ID, 6)
		if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
			results = append(results, short)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeKeyNames completes with key names, fingerprints (with and without SHA256: prefix).
func completeKeyNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	keys, _ := opRef.KeyListAll(cmd.Context())
	var results []string
	for _, k := range keys {
		if k.Name != "" && strings.HasPrefix(k.Name, toComplete) && !slices.Contains(results, k.Name) {
			results = append(results, k.Name)
		}
		if k.Fingerprint != "" && strings.HasPrefix(k.Fingerprint, toComplete) &&
			!slices.Contains(results, k.Fingerprint) {
			results = append(results, k.Fingerprint)
		}
		if k.Fingerprint != "" {
			bare := strings.TrimPrefix(k.Fingerprint, "SHA256:")
			if bare != k.Fingerprint && strings.HasPrefix(bare, toComplete) && !slices.Contains(results, bare) {
				results = append(results, bare)
			}
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeVolumeNames completes with volume names and short IDs.
func completeVolumeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	volumes := opRef.VolumeListAll(cmd.Context())
	var results []string
	for _, v := range volumes {
		if v.Name != "" && strings.HasPrefix(v.Name, toComplete) && !slices.Contains(results, v.Name) {
			results = append(results, v.Name)
		}
		short := crypto.Truncate(v.ID, 6)
		if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
			results = append(results, short)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeCacheResources completes with cache resource types (static list matching Python's _complete_cache_resources).
func completeCacheResources(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	resources := []string{"vm", "network", "image", "kernel", "binary", "misc"}
	var results []string
	for _, r := range resources {
		if strings.HasPrefix(r, toComplete) {
			results = append(results, r)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeConfigGet completes config args positionally: cat on arg0, key on arg1+.
func completeConfigGet(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return listCategories(toComplete)
	}
	// Second arg: complete keys from the category in args[0]
	if catKeys, ok := infra.OverridableDefaults[args[0]]; ok {
		var keys []string
		for k := range catKeys {
			if strings.HasPrefix(k, toComplete) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		return keys, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeConfigSet completes config set args: cat on arg0, key on arg1.
func completeConfigSet(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) <= 1 {
		return listCategories(toComplete)
	}
	if catKeys, ok := infra.OverridableDefaults[args[0]]; ok {
		var keys []string
		for k := range catKeys {
			if strings.HasPrefix(k, toComplete) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		return keys, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// listCategories returns matching config categories.
func listCategories(toComplete string) ([]string, cobra.ShellCompDirective) {
	var cats []string
	for cat := range infra.OverridableDefaults {
		if strings.HasPrefix(cat, toComplete) {
			cats = append(cats, cat)
		}
	}
	sort.Strings(cats)
	return cats, cobra.ShellCompDirectiveNoFileComp
}

// completeRemoteImageIDs completes with remote image IDs (via API).
// Matches Python's _complete_remote_image_ids() in cli/_completion.py.
func completeRemoteImageIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// Match Python: ImageOperation.list_all(remote=True) returns list[ImageVersion]
	// ImageVersion has no ID field in either Python or Go, so the Python completion
	// `hasattr(img, "id")` always returns False, yielding zero results. Match that.
	_, _, _ = opRef.ImageListAll(cmd.Context(), true, "", false, nil)
	var results []string
	return results, cobra.ShellCompDirectiveNoFileComp
}

// completeVMNamesEnhanced completes with VM names, short IDs, IPv4 addresses, and MAC addresses.
// Matches Python's _complete_vm_names() in cli/_completion.py with full dedup.
func completeVMNamesEnhanced(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if opRef == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	vms := opRef.VMList(cmd.Context(), nil)
	var results []string
	for _, vm := range vms {
		if vm.Name != "" && strings.HasPrefix(vm.Name, toComplete) && !slices.Contains(results, vm.Name) {
			results = append(results, vm.Name)
		}
		if vm.ID != "" {
			short := crypto.Truncate(vm.ID, 6)
			if strings.HasPrefix(short, toComplete) && !slices.Contains(results, short) {
				results = append(results, short)
			}
		}
		if vm.IPv4 != "" && strings.HasPrefix(vm.IPv4, toComplete) && !slices.Contains(results, vm.IPv4) {
			results = append(results, vm.IPv4)
		}
		if vm.MAC != "" && strings.HasPrefix(vm.MAC, toComplete) && !slices.Contains(results, vm.MAC) {
			results = append(results, vm.MAC)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}
