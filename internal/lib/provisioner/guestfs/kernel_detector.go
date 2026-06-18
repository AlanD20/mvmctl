package guestfs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

// KernelDetector detects the best host kernel for libguestfs appliance builds.
type KernelDetector struct{}

var (
	kernelNames      = []string{"vmlinuz", "bzImage", "kernel"}
	driverExtensions = []string{".ko", ".ko.zst", ".ko.xz"}
	virtioNetPaths   = []string{"kernel/drivers/net"}

	// cached result for findBestKernel
	cachedKernelOnce sync.Once
	cachedKernelPath string
	cachedModulesDir string
	cachedKernelErr  error

	// Compiled regexes for kernel version extraction and scoring
	versionRegex    = regexp.MustCompile(`version\s+(\S+)`)
	versionStart    = regexp.MustCompile(`^\d`)                // version starts with digit
	customSuffixRe  = regexp.MustCompile(`-(g14|custom)$`)     // explicit custom suffix
	cleanVersionRe  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)    // clean "6.9.3"
	distroVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+[-.]`) // distro "6.9.3-1"
)

// FindBestKernel finds the best host kernel for libguestfs.
// Returns (kernelPath, modulesDir, error).
// Scans /boot for kernel images, extracts version strings, and scores
// candidates based on virtio module availability and custom build penalty.
func (kd *KernelDetector) FindBestKernel(ctx context.Context) (string, string, error) {
	cachedKernelOnce.Do(func() {
		cachedKernelPath, cachedModulesDir, cachedKernelErr = kd.findBestKernelUncached(ctx)
	})
	return cachedKernelPath, cachedModulesDir, cachedKernelErr
}

func (kd *KernelDetector) findBestKernelUncached(ctx context.Context) (string, string, error) {
	candidates, err := kd.scanBootDirectory(ctx)
	if err != nil {
		return "", "", err
	}
	if len(candidates) == 0 {
		return "", "", nil
	}

	type scored struct {
		kernelPath string
		modulesDir string
		score      int
	}

	var scoredCandidates []scored

	for _, c := range candidates {
		kernelPath, version := c.kernelPath, c.version
		modulesDir := filepath.Join("/lib/modules", version)
		info, err := os.Stat(modulesDir)
		if err != nil || !info.IsDir() {
			slog.Debug("Modules directory missing for kernel",
				"kernel", kernelPath,
				"modules", modulesDir,
			)
			continue
		}

		virtioNetBonus := kd.countVirtioNet(modulesDir) * 2
		virtioCount := kd.countVirtioDrivers(modulesDir)
		customPenalty := kd.customSuffixPenalty(version)
		score := virtioNetBonus + virtioCount - customPenalty

		scoredCandidates = append(scoredCandidates, scored{
			kernelPath: kernelPath,
			modulesDir: modulesDir,
			score:      score,
		})

		slog.Debug("Kernel scored",
			"name", filepath.Base(kernelPath),
			"virtio_net", virtioNetBonus/2,
			"total", virtioCount,
			"penalty", customPenalty,
			"score", score,
		)
	}

	if len(scoredCandidates) == 0 {
		return "", "", nil
	}

	sort.Slice(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].score > scoredCandidates[j].score
	})

	best := scoredCandidates[0]
	slog.Debug("Selected kernel",
		"kernel", best.kernelPath,
		"modules", best.modulesDir,
	)
	return best.kernelPath, best.modulesDir, nil
}

type kernelCandidate struct {
	kernelPath string
	version    string
}

func (kd *KernelDetector) scanBootDirectory(ctx context.Context) ([]kernelCandidate, error) {
	bootDir := "/boot"
	info, err := os.Stat(bootDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}

	var paths []string
	for _, name := range kernelNames {
		matches, err := filepath.Glob(filepath.Join(bootDir, name+"*"))
		if err != nil {
			continue
		}
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err == nil && !fi.IsDir() {
				paths = append(paths, m)
			}
		}
	}

	// Deduplicate paths while preserving order
	unique := infra.Dedup(paths)

	var candidates []kernelCandidate
	for _, p := range unique {
		version, verErr := kd.extractVersion(ctx, p)
		if verErr != nil {
			return nil, verErr
		}
		if version != "" {
			candidates = append(candidates, kernelCandidate{
				kernelPath: p,
				version:    version,
			})
		}
	}
	return candidates, nil
}

func (kd *KernelDetector) extractVersion(ctx context.Context, kernelPath string) (string, error) {
	// Try 'file' command first
	// On timeout, return a specific error; other errors silently fall through.
	result, err := system.DefaultRunner.Run(ctx, []string{"file", kernelPath}, system.RunCmdOpts{
		Capture: true,
		Check:   false,
		Timeout: 5000,
	})
	if err != nil {
		// Check for timeout — return a specific error message.
		if strings.Contains(err.Error(), "timed out") {
			return "", fmt.Errorf("'file' command timed out for %s", kernelPath)
		}
		// Other errors: silently fall through to filename fallback
	} else if result.Stdout != "" {
		matches := versionRegex.FindStringSubmatch(result.Stdout)
		if len(matches) >= 2 {
			return matches[1], nil
		}
	}

	// Fallback: extract from filename for vmlinuz-X.Y.Z...
	name := filepath.Base(kernelPath)
	for _, prefix := range kernelNames {
		if strings.HasPrefix(name, prefix) {
			remainder := name[len(prefix):]
			if strings.HasPrefix(remainder, "-") {
				version := remainder[1:]
				if version != "" && versionStart.MatchString(version) {
					return version, nil
				}
			}
			break
		}
	}

	return "", nil
}

func (kd *KernelDetector) countVirtioNet(modulesDir string) int {
	count := 0
	for _, relPath := range virtioNetPaths {
		searchPath := filepath.Join(modulesDir, relPath)
		info, err := os.Stat(searchPath)
		if err != nil || !info.IsDir() {
			continue
		}
		for _, ext := range driverExtensions {
			pattern := filepath.Join(searchPath, "virtio_net"+ext)
			matches, _ := filepath.Glob(pattern)
			count += len(matches)
		}
	}
	return count
}

func (kd *KernelDetector) countVirtioDrivers(modulesDir string) int {
	driversDir := filepath.Join(modulesDir, "kernel/drivers")
	info, err := os.Stat(driversDir)
	if err != nil || !info.IsDir() {
		return 0
	}

	// Build set of matching extensions for fast lookup
	extSet := make(map[string]bool)
	for _, ext := range driverExtensions {
		extSet[ext] = true
	}

	count := 0
	filepath.WalkDir(driversDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		ext := filepath.Ext(name)
		if !extSet[ext] {
			return nil
		}
		// Check if name starts with "virtio_"
		baseName := name[:len(name)-len(ext)]
		if strings.HasPrefix(baseName, "virtio_") {
			count++
		}
		return nil
	})
	return count
}

func (kd *KernelDetector) customSuffixPenalty(version string) int {
	if customSuffixRe.MatchString(version) {
		return 5
	}
	if cleanVersionRe.MatchString(version) {
		return 0
	}
	if distroVersionRe.MatchString(version) {
		return 1
	}
	return 5
}

// ResetCache clears the cached kernel find result. Used for testing.
func (kd *KernelDetector) ResetCache() {
	cachedKernelOnce = sync.Once{}
	cachedKernelPath = ""
	cachedModulesDir = ""
	cachedKernelErr = nil
}

// Ensure kernelDetector satisfies the interface.
var _ = (*KernelDetector)(nil)

// FindBestKernel is a package-level helper wrapping KernelDetector.FindBestKernel.
func FindBestKernel(ctx context.Context) (string, string, error) {
	kd := &KernelDetector{}
	return kd.FindBestKernel(ctx)
}

func ScanBootDirectory(ctx context.Context) ([]string, error) {
	kd := &KernelDetector{}
	candidates, err := kd.scanBootDirectory(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(candidates))
	for i, c := range candidates {
		result[i] = c.kernelPath
	}
	return result, nil
}
