package inputs

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"mvmctl/internal/infra/model"
)

// VMCreateInput matches Python's VMCreateInput dataclass exactly.
type VMCreateInput struct {
	// Required fields (no defaults)
	Name    string
	SSHKeys []string

	// Optional fields with CLI-layer defaults resolved in Build()
	VCPUCount           *int
	MemSizeMib          *string
	User                *string
	PCIEnabled          *bool
	NestedVirt          *bool
	CPUTemplate         *string // file path to CPU template JSON
	CPUConfig           map[string]any
	EnableConsole       *bool
	EnableLogging       *bool
	EnableMetrics       *bool
	FirecrackerBin      *string
	Image               *string
	KernelID            *string
	BinaryID            *string
	DiskSize            *string
	RequestedGuestIP    *string
	SkipCINetworkConfig bool
	BootArgs            *string
	LSMFlags            *string
	NetworkName         *string
	RequestedGuestMAC   *string
	CustomUserDataPath  *string
	CustomUserData      *string // alias for CustomUserDataPath, kept for CLI compat
	CloudInitMode       *string
	CloudInitISOPath    *string
	KeepCloudInitISO    bool
	NocloudNetPort      *int
	NoConsole           bool // inverse of EnableConsole, kept for CLI compat
	SkipCleanup         bool
	SkipDeblob          bool
	Count               *int
	Atomic              bool
	Volumes             []string
}

// CloudInitModeResolved matches Python's CloudInitModeResolved dataclass.
type CloudInitModeResolved struct {
	Mode    model.CloudInitMode
	ISOPath *string
}

// VMCreateResolved matches Python's ResolvedVMCreateInput (immutable resolved inputs).
type VMCreateResolved struct {
	Name                string
	VMID                string
	VMDir               string
	VCPUCount           int
	MemSizeMib          int
	User                string
	DNSServer           string
	RootUID             int
	RootGID             int
	UserUID             int
	UserGID             int
	GuestMACPrefix      string
	Network             *model.Network
	Image               *model.ImageItem
	Kernel              *model.KernelItem
	Binary              *model.BinaryItem
	NetworkPrefixLen    int
	CloudInitMode       model.CloudInitMode
	SkipCINetworkConfig bool
	PCIEnabled          bool
	NestedVirt          bool
	EnableConsole       bool
	EnableLogging       bool
	EnableMetrics       bool
	KeepCloudInitISO    bool
	SkipCleanup         bool
	SkipDeblob          bool
	NetworkNetmask      string
	DiskSizeBytes       int64
	DiskSizeMib         int
	LSMFlags            string

	// Firecracker
	LogLevel              string
	LogFilename           string
	SerialOutputFilename  string
	MetricsFilename       string
	APISocketFilename     string
	PIDFilename           string
	ConfigFilename        string
	ConsoleSocketFilename string
	ConsolePIDFilename    string

	// Cloud-init
	CloudInitISOName      string
	NocloudPortRangeStart int
	NocloudPortRangeEnd   int
	NocloudMaxPortRetries int

	RequestedGuestIP   *string
	RequestedGuestMAC  *string
	NocloudNetPort     *int
	CustomUserDataPath *string
	CloudInitISOPath   *string
	CPUConfig          *model.CpuConfig
	BootArgs           *string
	SSHKeys            []*model.SSHKeyItem
	Provisioner        model.ProvisionerType
	ExtraDrives        []model.DriveConfig
	Volumes            []*model.VolumeItem
}

// deriveFirecrackerVersionFromPath extracts version from firecracker binary path.
// Matches Python: firecracker-v1.15.1 -> "1.15.1", firecracker-dev-abc -> "dev-abc", fallback -> "custom-{hash}"
func deriveFirecrackerVersionFromPath(path string) string {
	stem := filepath.Base(path)
	if len(stem) > len("firecracker-v") && stem[:len("firecracker-v")] == "firecracker-v" {
		return stem[len("firecracker-v"):]
	}
	if len(stem) > len("firecracker-") && stem[:len("firecracker-")] == "firecracker-" {
		return stem[len("firecracker-"):]
	}
	// fallback: custom-{path_hash}
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("custom-%x", h[:6])
}
