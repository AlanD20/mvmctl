// Package cli provides CLI display helpers.
package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"mvmctl/internal/infra"
)

// checkNameArg guards for positional name arg: shows help on "help" or empty,
// matching Python's MVMCli.check_name_arg() in utils/cli.py.
// Returns the validated name or an error.
// Python prints help to stdout via typer.echo(); Cobra's Help() defaults to stderr,
// so we redirect to stdout before calling Help().
func checkNameArg(cmd *cobra.Command, name string) (string, error) {
	if name == "help" {
		cmd.SetOut(os.Stdout)
		cmd.Help()
		return "", nil // nil error = help shown, caller should return nil
	}
	if name == "" {
		cmd.SetOut(os.Stdout)
		cmd.Help()
		return "", fmt.Errorf("name required")
	}
	return name, nil
}

// confirmPrompt shows a y/n prompt on stderr and returns true if the user confirms.
// Matches Python's typer.confirm(text) behavior: defaults to True (Enter = accept).
func confirmPrompt(prompt string) bool {
	return promptConfirm(prompt, true)
}

func confirmPromptNoDefault(prompt string) bool {
	return promptConfirm(prompt, false)
}

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
		if net.Name != "" && hasPrefix(net.Name, toComplete) && !contains(results, net.Name) {
			results = append(results, net.Name)
		}
		short := net.ID
		if len(short) > 6 { short = short[:6] }
		if hasPrefix(short, toComplete) && !contains(results, short) {
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
	images, _, _ := opRef.ImageListAll(cmd.Context(), false, "", nil, false)
	var results []string
	for _, img := range images {
		short := img.ID
		if len(short) > 6 { short = short[:6] }
		if hasPrefix(short, toComplete) && !contains(results, short) {
			results = append(results, short)
		}
		if img.Type != "" && hasPrefix(img.Type, toComplete) && !contains(results, img.Type) {
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
	kernels, _, _ := opRef.KernelList(cmd.Context(), false, false)
	var results []string
	for _, k := range kernels {
		if k.Type != "" && k.Version != "" {
			combo := k.Type + ":" + k.Version
			if hasPrefix(combo, toComplete) && !contains(results, combo) {
				results = append(results, combo)
			}
		}
		short := k.ID
		if len(short) > 6 { short = short[:6] }
		if hasPrefix(short, toComplete) && !contains(results, short) {
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
	binaries, _, _ := opRef.BinaryList(cmd.Context(), false, nil)
	var results []string
	for _, b := range binaries {
		if b.Name != "" && hasPrefix(b.Name, toComplete) && !contains(results, b.Name) {
			results = append(results, b.Name)
		}
		if b.Version != "" && hasPrefix(b.Version, toComplete) && !contains(results, b.Version) {
			results = append(results, b.Version)
		}
		short := b.ID
		if len(short) > 6 { short = short[:6] }
		if hasPrefix(short, toComplete) && !contains(results, short) {
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
		if k.Name != "" && hasPrefix(k.Name, toComplete) && !contains(results, k.Name) {
			results = append(results, k.Name)
		}
		if k.Fingerprint != "" && hasPrefix(k.Fingerprint, toComplete) && !contains(results, k.Fingerprint) {
			results = append(results, k.Fingerprint)
		}
		if k.Fingerprint != "" {
			bare := strings.TrimPrefix(k.Fingerprint, "SHA256:")
			if bare != k.Fingerprint && hasPrefix(bare, toComplete) && !contains(results, bare) {
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
		if v.Name != "" && hasPrefix(v.Name, toComplete) && !contains(results, v.Name) {
			results = append(results, v.Name)
		}
		short := v.ID
		if len(short) > 6 { short = short[:6] }
		if hasPrefix(short, toComplete) && !contains(results, short) {
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
		if hasPrefix(r, toComplete) {
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
			if hasPrefix(k, toComplete) {
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
			if hasPrefix(k, toComplete) {
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
		if hasPrefix(cat, toComplete) {
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
	_, _, _ = opRef.ImageListAll(cmd.Context(), true, "", nil, false)
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
		if vm.Name != "" && hasPrefix(vm.Name, toComplete) && !contains(results, vm.Name) {
			results = append(results, vm.Name)
		}
		if vm.ID != "" {
			short := vm.ID
			if len(short) > 6 {
				short = short[:6]
			}
			if hasPrefix(short, toComplete) && !contains(results, short) {
				results = append(results, short)
			}
		}
		if vm.IPv4 != "" && hasPrefix(vm.IPv4, toComplete) && !contains(results, vm.IPv4) {
			results = append(results, vm.IPv4)
		}
		if vm.MAC != "" && hasPrefix(vm.MAC, toComplete) && !contains(results, vm.MAC) {
			results = append(results, vm.MAC)
		}
	}
	return results, cobra.ShellCompDirectiveNoFileComp
}

// contains checks if a string is in a slice.
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// hasPrefix checks if s starts with prefix.
func hasPrefix(s, prefix string) bool {
	return strings.HasPrefix(s, prefix)
}
