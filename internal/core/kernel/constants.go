package kernel

// --- Build dependencies ---

// KernelBuildCommands lists required executables for kernel builds.
var KernelBuildCommands = []string{
	"git", "curl", "make", "gcc", "flex", "bison", "bc", "pahole", "ld",
}

// KernelBuildLibraries lists required pkg-config libraries for kernel builds.
var KernelBuildLibraries = []struct{ Pkg, Display string }{
	{"libelf", "libelf"},
	{"openssl", "libssl-dev"},
}

// --- Make targets ---

const (
	KernelMakeTarget         = "vmlinux"
	KernelDefconfigTarget    = "defconfig"
	KernelOlddefconfigTarget = "olddefconfig"
	KernelConfigScript       = "scripts/config"
)

// --- File patterns ---

const (
	KernelBuildLogSuffix = ".build.log"
	KernelTarballPattern = "linux-%s.tar.xz"
	KernelSrcDirPattern  = "linux-%s-%s"
	KernelCacheMarker    = "kernel-cache-%s.marker"
	KernelCachePath      = "kernel-cache-%s.vmlinux"
	KernelOutputPattern  = "%s-%s-%s"
	KernelMakeCmd        = "make"
)

// --- Valid kernel types ---

// KernelValidTypes lists the accepted kernel type identifiers.
var KernelValidTypes = map[string]bool{"firecracker": true, "official": true}

// KernelValidFeatures lists the accepted kernel feature names for official builds.
var KernelValidFeatures = map[string]bool{
	"kvm":      true,
	"nftables": true,
	"tuntap":   true,
	"btrfs":    true,
}

// --- Firecracker S3 ---

const (
	KernelS3KeyPattern   = "firecracker-ci/%s/%s/vmlinux-%s"
	KernelS3XMLPattern   = `<Key>(firecracker-ci/%s/%s/vmlinux-[\d.]+)</Key>`
	KernelS3SHA256Suffix = ".sha256"
)
