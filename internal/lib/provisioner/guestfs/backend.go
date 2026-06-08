package guestfs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// GuestfsBackend implements the Backend interface using guestfish CLI.
// Builder pattern: queue operations, call Run() to execute.
type GuestfsBackend struct {
	rootfsPath string
	rootUID    int
	rootGID    int
	userUID    int
	userGID    int

	// Builder state
	targetSize   int64
	hostname     string
	user         string
	sshPubkeys   []string
	cloudInitDir string
	dnsServer    string
	ops          []string
}

// NewGuestfsBackend creates a new GuestfsBackend.
func NewGuestfsBackend(rootfsPath string, rootUID, rootGID, userUID, userGID int) *GuestfsBackend {
	return &GuestfsBackend{
		rootfsPath: rootfsPath,
		rootUID:    rootUID,
		rootGID:    rootGID,
		userUID:    userUID,
		userGID:    userGID,
	}
}

// ═════════════════════════════════════════════════════════════════════════════
// Builder methods — queue operations
// ═════════════════════════════════════════════════════════════════════════════

func (b *GuestfsBackend) Resize(ctx context.Context, targetSizeBytes int64) error {
	b.targetSize = targetSizeBytes
	return nil
}

func (b *GuestfsBackend) SetHostname(ctx context.Context, hostname string) error {
	b.hostname = hostname
	b.ops = append(b.ops, "set_hostname")
	return nil
}

func (b *GuestfsBackend) InjectDNS(ctx context.Context, dnsServer string) error {
	b.dnsServer = dnsServer
	b.ops = append(b.ops, "inject_dns")
	return nil
}

func (b *GuestfsBackend) SetupSSH(ctx context.Context, user string, sshPubkeys []string) error {
	b.user = user
	b.sshPubkeys = sshPubkeys
	b.ops = append(b.ops, "setup_ssh")
	return nil
}

func (b *GuestfsBackend) InjectCloudInit(ctx context.Context, cloudInitDir string) error {
	b.cloudInitDir = cloudInitDir
	b.ops = append(b.ops, "inject_cloud_init")
	return nil
}

func (b *GuestfsBackend) DisableCloudInit(ctx context.Context) error {
	b.ops = append(b.ops, "disable_cloud_init")
	return nil
}

func (b *GuestfsBackend) Shrink(ctx context.Context) error {
	b.ops = append(b.ops, "shrink")
	return nil
}

func (b *GuestfsBackend) Deblob(ctx context.Context, osType *string) error {
	b.ops = append(b.ops, "deblob")
	return nil
}

func (b *GuestfsBackend) FixFstab(ctx context.Context) error {
	b.ops = append(b.ops, "fix_fstab")
	return nil
}

// ═════════════════════════════════════════════════════════════════════════════
// Execution
// ═════════════════════════════════════════════════════════════════════════════

// Run converts builder state to ProvisioningConfig and delegates to RunDeferred.
func (b *GuestfsBackend) Run(ctx context.Context) error {
	cfg := ProvisioningConfig{
		RootfsPath:       b.rootfsPath,
		RootUID:          b.rootUID,
		RootGID:          b.rootGID,
		UserUID:          b.userUID,
		UserGID:          b.userGID,
		TargetSize:       b.targetSize,
		Hostname:         b.hostname,
		User:             b.user,
		SSHPubkeys:       b.sshPubkeys,
		CloudInitDir:     b.cloudInitDir,
		DNSServer:        b.dnsServer,
		DisableCloudInit: false,
		Shrink:           false,
		Deblob:           false,
	}

	for _, op := range b.ops {
		switch op {
		case "disable_cloud_init":
			cfg.DisableCloudInit = true
		case "shrink":
			cfg.Shrink = true
		case "deblob", "fix_fstab":
			cfg.Deblob = true
		}
	}

	return RunDeferred(ctx, cfg)
}

// ═════════════════════════════════════════════════════════════════════════════
// Other Backend interface methods
// ═════════════════════════════════════════════════════════════════════════════

// DetectOS reads /etc/os-release via guestfish to identify the OS.
func (b *GuestfsBackend) DetectOS(ctx context.Context) (string, error) {
	handle, err := NewHandle(b.rootfsPath, true)
	if err != nil {
		slog.Debug("OS detection via guestfs failed (handle init), falling back to 'linux'", "error", err)
		return "linux", nil
	}

	osReleaseContent := ""
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		out, err := handle.ReadFile(ctx, path)
		if err == nil {
			osReleaseContent = out
			break
		}
	}

	if osReleaseContent == "" {
		slog.Debug("OS detection via guestfs failed, falling back to 'linux'")
		return "linux", nil
	}

	idVal := ""
	for _, line := range strings.Split(osReleaseContent, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ID=") {
			idVal = strings.Trim(strings.TrimPrefix(line, "ID="), "\"'")
			idVal = strings.ToLower(idVal)
			break
		}
	}

	if idVal != "" {
		return idVal, nil
	}
	return "linux", nil
}

// ExtractPartition extracts root partition from a raw disk image.
func (b *GuestfsBackend) ExtractPartition(
	ctx context.Context,
	rawPath, outputPath string,
	partition int,
	disabledDetectors []string,
) (string, error) {
	result, err := ExtractPartition(ctx, rawPath, outputPath, partition)
	if err != nil {
		return "", fmt.Errorf("guestfs partition extraction failed: %w", err)
	}
	if result == "" {
		return "", fmt.Errorf("guestfs partition extraction failed")
	}
	return result, nil
}

// ConvertTo delegates to the package-level ConvertTo function.
func (b *GuestfsBackend) ConvertTo(ctx context.Context, targetFS string) error {
	return ConvertTo(ctx, b.rootfsPath, targetFS)
}
