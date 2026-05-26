package disk

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"mvmctl/internal/infra/errs"
)

// ── Disk-specific constants (moved from infra/constants.go) ──
const (
	MinRootSizeMB    = 500
	SizeTooSmallMB   = 100
	SectorSizeBytes  = 512
	MebibyteBytes    = 1024 * 1024
)

// DetectorWeights holds scoring weights for partition root detection.
var DetectorWeights = map[string]float64{
	"type_code":  1.0,
	"label":      0.8,
	"size":       0.5,
	"filesystem": 0.7,
}

// DetectorScores holds scoring values for partition root detection.
var DetectorScores = map[string]float64{
	"root_score":             1.0,
	"exclude_score":         -1.0,
	"neutral_score":          0.0,
	"mbr_linux_score":       0.5,
	"label_root_score":      1.0,
	"label_exclude_score":  -0.5,
	"size_largest_score":    0.5,
	"size_root_score":       0.3,
	"size_too_small_score": -0.5,
}

// ── Size multipliers (IEC binary units) ──

var sizeMultipliers = map[string]int64{
	"B":  1,
	"K":  1024,
	"KB": 1024,
	"M":  1024 * 1024,
	"MB": 1024 * 1024,
	"G":  1024 * 1024 * 1024,
	"GB": 1024 * 1024 * 1024,
	"T":  1024 * 1024 * 1024 * 1024,
	"TB": 1024 * 1024 * 1024 * 1024,
}

// output units for FormatDiskSize — built from sizeMultipliers map, excluding short forms.
// Mirrors Python's sorted(_SIZE_MULTIPLIERS.items(), key=lambda x: x[1], reverse=True) with skip of B/KB/MB/GB/TB.
// Initialised lazily via getFormatDiskUnits().
// TODO: call InitFormatDiskUnits() from app/app.go explicitly
var (
	formatDiskUnits []struct {
		suffix string
		size   int64
	}
	formatDiskUnitsOnce sync.Once
)

// getFormatDiskUnits builds and caches the sorted formatDiskUnits slice on first call.
func getFormatDiskUnits() []struct {
	suffix string
	size   int64
} {
	formatDiskUnitsOnce.Do(func() {
		shortForms := map[string]bool{"B": true, "KB": true, "MB": true, "GB": true, "TB": true}
		for suffix, size := range sizeMultipliers {
			if !shortForms[suffix] {
				formatDiskUnits = append(formatDiskUnits, struct {
					suffix string
					size   int64
				}{suffix, size})
			}
		}
		sort.Slice(formatDiskUnits, func(i, j int) bool {
			return formatDiskUnits[i].size > formatDiskUnits[j].size
		})
	})
	return formatDiskUnits
}

var sizePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMGT]?B?|[kmgt]?b?)?$`)

// ParseDiskSizeToBytes parses a disk size string like "512M", "1G", "2.5GB", "1024" into bytes.
// Matches Python's DiskUtils.parse_disk_size_to_bytes() exactly, including error messages.
func ParseDiskSizeToBytes(s string) (int64, error) {
	return ParseDiskSize(s)
}

// ParseDiskSize parses a disk size string into bytes.
// Matches Python's DiskUtils.parse_disk_size_to_bytes() exactly.
// Python raises MVMError on format errs.
func ParseDiskSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	match := sizePattern.FindStringSubmatch(upper)
	if match == nil {
		return 0, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Class:   errs.ClassValidation,
			Message: fmt.Sprintf("Invalid disk size format: '%s'. Expected format: <number><unit> where unit is B, K, KB, M, MB, G, GB, T, TB", upper),
		}
	}

	numberStr, unit := match[1], match[2]
	if unit == "" {
		unit = "B" // Default to bytes if no unit
	}

	number, err := strconv.ParseFloat(numberStr, 64)
	if err != nil {
		return 0, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Class:   errs.ClassValidation,
			Message: fmt.Sprintf("Invalid number in disk size: '%s'", numberStr),
		}
	}

	multiplier, ok := sizeMultipliers[unit]
	if !ok {
		return 0, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Class:   errs.ClassValidation,
			Message: fmt.Sprintf("Unknown size unit: '%s'. Valid: B, K, KB, M, MB, G, GB, T, TB", unit),
		}
	}

	bytesCount := int64(number * float64(multiplier))
	if bytesCount < 0 {
		return 0, &errs.DomainError{
			Code:    errs.CodeValidationFailed,
			Class:   errs.ClassValidation,
			Message: fmt.Sprintf("Disk size cannot be negative: %s", upper),
		}
	}

	return bytesCount, nil
}

func FormatSectorsHumanReadable(sizeSectors int64, sectorSize int64) string {
	if sectorSize == 0 {
		sectorSize = 512
	}
	sizeBytes := sizeSectors * sectorSize
	sizeMib := float64(sizeBytes) / (1024 * 1024)
	if sizeMib >= 1024 {
		return fmt.Sprintf("%.1f GiB", sizeMib/1024)
	}
	return fmt.Sprintf("%.1f MiB", sizeMib)
}

func FormatDiskSize(bytesCount int64) string {
	if bytesCount == 0 {
		return "0B"
	}
	for _, u := range getFormatDiskUnits() {
		if bytesCount >= u.size {
			value := float64(bytesCount) / float64(u.size)
			if value == float64(int64(value)) {
				return fmt.Sprintf("%d%s", int64(value), u.suffix)
			}
			return fmt.Sprintf("%.1f%s", value, u.suffix)
		}
	}
	return fmt.Sprintf("%dB", bytesCount)
}

// ══════════════════════════════════════════════════════════════════════════════
// Root partition detection
// ══════════════════════════════════════════════════════════════════════════════

// Partition represents a detected partition.
type Partition struct {
	Node   string
	Size   int64 // in sectors
	Type   string
	Name   string
	Label  string
	Fstype string
	Start  int64
}

// PartitionDetector is the interface for partition detectors.
type PartitionDetector interface {
	Name() string
	Weight() float64
	Score(partition Partition, allPartitions []Partition) float64
}

// ── TypeCodeDetector ──

type TypeCodeDetector struct{}

func (TypeCodeDetector) Name() string { return "type_code" }

func (TypeCodeDetector) Weight() float64 {
	return getDetectorWeight("type_code", 0.25)
}

// GPT type GUIDs
const (
	GPTRootX8664   = "44479540-f297-41b2-9af7-d131d5f0458a"
	GPTRootAarch64 = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709"
	GPTESP         = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	GPTSwap        = "0657fd6d-a4ab-43c4-84e5-0933c84b4f4f"
)

// MBR type codes
const (
	MBRLinux    = "83"
	MBREFI      = "ef"
	MBRSwap     = "82"
	MBRExtended = "85"
	MBRLVM      = "8e"
)

func (TypeCodeDetector) Score(partition Partition, allPartitions []Partition) float64 {
	partitionType := strings.ToLower(partition.Type)

	// Root partitions get highest score
	if partitionType == strings.ToLower(GPTRootX8664) ||
		partitionType == strings.ToLower(GPTRootAarch64) {
		return getDetectorScore("root_score", 1.0)
	}

	// Linux MBR type gets medium score
	if partitionType == strings.ToLower(MBRLinux) {
		return getDetectorScore("mbr_linux_score", 0.5)
	}

	// Exclude partitions (ESP, swap, LVM, extended) get negative score
	if partitionType == strings.ToLower(GPTESP) ||
		partitionType == strings.ToLower(GPTSwap) ||
		partitionType == strings.ToLower(MBREFI) ||
		partitionType == strings.ToLower(MBRSwap) ||
		partitionType == strings.ToLower(MBRExtended) ||
		partitionType == strings.ToLower(MBRLVM) {
		return getDetectorScore("exclude_score", -1.0)
	}

	return getDetectorScore("neutral_score", 0.0)
}

// ── LabelDetector ──

type LabelDetector struct{}

func (LabelDetector) Name() string { return "label" }

func (LabelDetector) Weight() float64 {
	return getDetectorWeight("label", 0.25)
}

func (LabelDetector) Score(partition Partition, allPartitions []Partition) float64 {
	label := partition.Name
	if label == "" {
		label = partition.Label
	}
	label = strings.ToLower(label)

	rootIndicators := []string{"root", "cloudimg", "rootfs"}
	for _, indicator := range rootIndicators {
		if strings.Contains(label, indicator) {
			return getDetectorScore("label_root_score", 1.0)
		}
	}

	excludeIndicators := []string{"esp", "efi", "boot", "swap"}
	for _, indicator := range excludeIndicators {
		if strings.Contains(label, indicator) {
			return getDetectorScore("label_exclude_score", -0.5)
		}
	}

	return getDetectorScore("neutral_score", 0.0)
}

// ── SizeDetector ──

type SizeDetector struct{}

func (SizeDetector) Name() string { return "size" }

func (SizeDetector) Weight() float64 {
	return getDetectorWeight("size", 0.25)
}

func (SizeDetector) Score(partition Partition, allPartitions []Partition) float64 {
	sectorBytes := int64(SectorSizeBytes)
	mebibyteBytes := int64(MebibyteBytes)
	sizeMB := float64(partition.Size) * float64(sectorBytes) / float64(mebibyteBytes)

	// Find the largest partition
	maxSizeMB := float64(0)
	for _, p := range allPartitions {
		if p.Size > 0 {
			pSizeMB := float64(p.Size) * float64(sectorBytes) / float64(mebibyteBytes)
			if pSizeMB > maxSizeMB {
				maxSizeMB = pSizeMB
			}
		}
	}

	minRootSizeMB := float64(MinRootSizeMB)
	tooSmallMB := float64(SizeTooSmallMB)

	if sizeMB < tooSmallMB {
		return getDetectorScore("size_too_small_score", -0.5)
	}

	if sizeMB >= minRootSizeMB {
		if sizeMB >= maxSizeMB {
			return getDetectorScore("size_largest_score", 0.5)
		}
		return getDetectorScore("size_root_score", 0.3)
	}

	return getDetectorScore("neutral_score", 0.0)
}

// ── FilesystemDetector ──

type FilesystemDetector struct{}

func (FilesystemDetector) Name() string { return "filesystem" }

func (FilesystemDetector) Weight() float64 {
	return getDetectorWeight("filesystem", 0.25)
}

func (FilesystemDetector) Score(partition Partition, allPartitions []Partition) float64 {
	fstype := strings.ToLower(partition.Fstype)

	rootFilesystems := map[string]bool{"ext4": true, "btrfs": true, "xfs": true, "f2fs": true}
	if rootFilesystems[fstype] {
		return getDetectorScore("filesystem_root_score", 0.5)
	}
	if fstype == "vfat" {
		return getDetectorScore("filesystem_vfat_score", -0.8)
	}
	if fstype == "crypto_luks" || fstype == "" {
		return getDetectorScore("neutral_score", 0.0)
	}

	return getDetectorScore("neutral_score", 0.0)
}

// ── RootPartitionDetector ──

type RootPartitionDetector struct {
	detectors []PartitionDetector
	disabled  map[string]bool
}

func NewRootPartitionDetector(disabledDetectors []string) *RootPartitionDetector {
	d := &RootPartitionDetector{
		detectors: []PartitionDetector{
			TypeCodeDetector{},
			LabelDetector{},
			SizeDetector{},
			FilesystemDetector{},
		},
		disabled: make(map[string]bool),
	}
	for _, name := range disabledDetectors {
		d.disabled[name] = true
	}
	return d
}

func (d *RootPartitionDetector) Register(detector PartitionDetector) {
	d.detectors = append(d.detectors, detector)
}

func (d *RootPartitionDetector) Detect(partitions []Partition) (int, error) {
	if len(partitions) == 0 {
		return 0, errs.RootPartitionDetectionError(nil, "no partitions to evaluate")
	}
	if len(partitions) == 1 {
		return 1, nil
	}

	type scoredPartition struct {
		index int
		score float64
	}

	var scores []scoredPartition
	for i, partition := range partitions {
		total := 0.0
		for _, detector := range d.detectors {
			if d.disabled[detector.Name()] {
				continue
			}
			total += detector.Weight() * detector.Score(partition, partitions)
		}
		scores = append(scores, scoredPartition{index: i + 1, score: total})
	}

	bestScore := math.Inf(-1)
	for _, sp := range scores {
		if sp.score > bestScore {
			bestScore = sp.score
		}
	}

	var bestPartitions []int
	for _, sp := range scores {
		if sp.score == bestScore {
			bestPartitions = append(bestPartitions, sp.index)
		}
	}

	if len(bestPartitions) > 1 {
		tiedParts := make([]string, len(bestPartitions))
		for i, idx := range bestPartitions {
			tiedParts[i] = strconv.Itoa(idx)
		}
		return 0, errs.TieDetectedError(tiedParts, fmt.Sprintf("tie score %f", bestScore), nil)
	}

	if bestScore < 0 {
		return 0, errs.RootPartitionDetectionError(
			partitionsToMaps(partitions),
			fmt.Sprintf("Best score %f < 0, no suitable root partition found", bestScore),
		)
	}

	return bestPartitions[0], nil
}

// partitionsToMaps converts a slice of Partition structs to the []map[string]any
// format expected by RootPartitionDetectionError, matching Python's partition dicts.
func partitionsToMaps(partitions []Partition) []map[string]any {
	result := make([]map[string]any, len(partitions))
	for i, p := range partitions {
		result[i] = map[string]any{
			"node":    p.Node,
			"size":    p.Size,
			"type":    p.Type,
			"name":    p.Name,
			"label":   p.Label,
			"fstype":  p.Fstype,
			"start":   p.Start,
		}
	}
	return result
}

func getDetectorScore(key string, defaultVal float64) float64 {
	if val, ok := DetectorScores[key]; ok {
		return val
	}
	return defaultVal
}

func getDetectorWeight(key string, defaultVal float64) float64 {
	if val, ok := DetectorWeights[key]; ok {
		return val
	}
	return defaultVal
}
