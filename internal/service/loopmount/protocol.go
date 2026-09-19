package loopmount

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

const (
	// VMProvisionAction selects provisioning of one receiver-derived VM rootfs.
	VMProvisionAction = "loopmount-provision-vm"
	// ImageInspectAction selects OS inspection of one receiver-derived image staging rootfs.
	ImageInspectAction = "loopmount-inspect-image"
	// ImageOptimizeAction selects fixed optimization of one receiver-derived image staging rootfs.
	ImageOptimizeAction = "loopmount-optimize-image"

	minimumRootfsBytes    uint64 = 128 << 20
	maximumRootfsBytes    uint64 = 16 << 40
	minimumImageBytes     uint64 = 1
	maximumImageBytes     uint64 = 16 << 40
	maxSSHKeys                   = 64
	maxSSHKeyBytes               = 16 << 10
	maxSSHKeysBytes              = 48 << 10
	maxGuestUsernameBytes        = 32
	maxResultCount               = 1 << 20
)

// Filesystem is a closed filesystem type admitted by loopmount operations.
type Filesystem string

const (
	// FilesystemExt2 selects an ext2 root filesystem.
	FilesystemExt2 Filesystem = "ext2"
	// FilesystemExt3 selects an ext3 root filesystem.
	FilesystemExt3 Filesystem = "ext3"
	// FilesystemExt4 selects an ext4 root filesystem.
	FilesystemExt4 Filesystem = "ext4"
	// FilesystemBtrfs selects a Btrfs root filesystem.
	FilesystemBtrfs Filesystem = "btrfs"
	// FilesystemXFS selects an XFS root filesystem; offline shrink is not supported.
	FilesystemXFS Filesystem = "xfs"
)

// VMProvisionRequest is the complete intent for one managed VM rootfs.
// CRITICAL: CacheLocator locates only the caller's managed namespace. VMID selects the fixed
// vms/<vm-id>/rootfs.img leaf; the request cannot choose a resource path, command, shell, or mount target.
type VMProvisionRequest struct {
	CacheLocator        string          `json:"cache_locator"`
	VMID                string          `json:"vm_id"`
	ExpectedRootfsBytes uint64          `json:"expected_rootfs_bytes"`
	Filesystem          Filesystem      `json:"filesystem"`
	Plan                VMProvisionPlan `json:"plan"`
}

// VMProvisionPlan contains only observed, named VM provisioning intent.
type VMProvisionPlan struct {
	ResizeBytes     uint64      `json:"resize_bytes"`
	Hostname        *string     `json:"hostname,omitempty"`
	DNS             *string     `json:"dns,omitempty"`
	SSH             *SSHSetup   `json:"ssh,omitempty"`
	SudoUser        *string     `json:"sudo_user,omitempty"`
	InjectCloudInit bool        `json:"inject_cloud_init"`
	DeblobOS        *string     `json:"deblob_os,omitempty"`
	FixFstab        bool        `json:"fix_fstab"`
	Agent           *AgentSetup `json:"agent,omitempty"`
}

// SSHSetup is exact guest SSH setup intent.
type SSHSetup struct {
	User       string   `json:"user"`
	PublicKeys []string `json:"public_keys"`
}

// AgentSetup requests injection of the receiver binary's embedded guest agent.
// The caller supplies no executable bytes or executable path.
type AgentSetup struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// VMProvisionResult is the complete authoritative result of VM rootfs provisioning.
type VMProvisionResult struct {
	VMID         string     `json:"vm_id"`
	Filesystem   Filesystem `json:"filesystem"`
	RootfsBytes  uint64     `json:"rootfs_bytes"`
	FilesWritten uint32     `json:"files_written"`
	CommandsRun  uint32     `json:"commands_run"`
}

// ImageInspectRequest identifies one fixed images/staging/<image-id>/rootfs.img leaf.
type ImageInspectRequest struct {
	CacheLocator       string     `json:"cache_locator"`
	ImageID            string     `json:"image_id"`
	ExpectedImageBytes uint64     `json:"expected_image_bytes"`
	Filesystem         Filesystem `json:"filesystem"`
}

// ImageInspectResult is the bounded detected OS identity for one staging image.
type ImageInspectResult struct {
	ImageID string `json:"image_id"`
	OSType  string `json:"os_type"`
}

// ImageOptimizeRequest applies a receiver-ordered plan to one fixed image staging rootfs.
type ImageOptimizeRequest struct {
	CacheLocator       string            `json:"cache_locator"`
	ImageID            string            `json:"image_id"`
	ExpectedImageBytes uint64            `json:"expected_image_bytes"`
	Filesystem         Filesystem        `json:"filesystem"`
	Plan               ImageOptimizePlan `json:"plan"`
}

// ImageOptimizePlan contains only the observed convert, deblob, fstab, and shrink operations.
type ImageOptimizePlan struct {
	ConvertTo *Filesystem `json:"convert_to,omitempty"`
	DeblobOS  *string     `json:"deblob_os,omitempty"`
	FixFstab  bool        `json:"fix_fstab"`
	Shrink    bool        `json:"shrink"`
}

// ImageOptimizeResult is the complete authoritative staging-image result.
type ImageOptimizeResult struct {
	ImageID    string     `json:"image_id"`
	Filesystem Filesystem `json:"filesystem"`
	ImageBytes uint64     `json:"image_bytes"`
}

// Validate enforces the receiver trust-boundary contract for VM provisioning.
func (request VMProvisionRequest) Validate() error {
	if err := validateCacheLocator(request.CacheLocator); err != nil {
		return err
	}
	if !validLowerHexID(request.VMID, 32) {
		return validationError("managed VM ID must be 32 lowercase hexadecimal characters")
	}
	if request.ExpectedRootfsBytes < minimumRootfsBytes || request.ExpectedRootfsBytes > maximumRootfsBytes {
		return validationError("managed VM rootfs size is outside the supported range")
	}
	if err := request.Filesystem.Validate(); err != nil {
		return err
	}
	if request.Filesystem == FilesystemXFS && request.Plan.ResizeBytes != 0 {
		return validationError("managed XFS resize is unsupported; use the existing filesystem capacity")
	}
	return request.Plan.Validate(request.ExpectedRootfsBytes)
}

// Validate enforces the closed VM provisioning plan relative to the admitted rootfs size.
func (plan VMProvisionPlan) Validate(expectedRootfsBytes uint64) error {
	hasOperation := false
	if plan.ResizeBytes != 0 {
		hasOperation = true
		if plan.ResizeBytes < minimumRootfsBytes || plan.ResizeBytes > maximumRootfsBytes {
			return validationError("managed VM resize is outside the supported range")
		}
		if plan.ResizeBytes < expectedRootfsBytes {
			return validationError("managed VM resize cannot shrink the rootfs")
		}
	}
	if plan.Hostname != nil {
		hasOperation = true
		if err := validators.VMName(*plan.Hostname); err != nil {
			return validationError("managed VM hostname is invalid: " + err.Error())
		}
	}
	if plan.DNS != nil {
		hasOperation = true
		if net.ParseIP(*plan.DNS) == nil {
			return validationError("managed VM DNS must be one IP address")
		}
	}
	if plan.SSH != nil {
		hasOperation = true
		if err := plan.SSH.Validate(); err != nil {
			return err
		}
	}
	if plan.SudoUser != nil {
		hasOperation = true
		if len(*plan.SudoUser) > maxGuestUsernameBytes {
			return validationError(fmt.Sprintf(
				"managed VM sudo username exceeds %d-byte limit",
				maxGuestUsernameBytes,
			))
		}
		if err := validators.SSHUsername(*plan.SudoUser); err != nil {
			return validationError(err.Error())
		}
	}
	if plan.InjectCloudInit {
		hasOperation = true
	}
	if plan.DeblobOS != nil {
		hasOperation = true
		if !validOSIdentifier(*plan.DeblobOS) {
			return validationError("managed VM OS identifier is invalid")
		}
	}
	if plan.FixFstab {
		hasOperation = true
	}
	if plan.Agent != nil {
		hasOperation = true
		if err := plan.Agent.Validate(); err != nil {
			return err
		}
	}
	if !hasOperation {
		return validationError("managed VM provision plan requires at least one operation")
	}
	return nil
}

// Validate enforces bounded SSH setup values before fixed receiver templates use them.
func (setup SSHSetup) Validate() error {
	if len(setup.User) > maxGuestUsernameBytes {
		return validationError(fmt.Sprintf(
			"managed VM SSH username exceeds %d-byte limit",
			maxGuestUsernameBytes,
		))
	}
	if err := validators.SSHUsername(setup.User); err != nil {
		return validationError(err.Error())
	}
	if len(setup.PublicKeys) > maxSSHKeys {
		return validationError("managed VM SSH public key count exceeds limit")
	}
	total := 0
	for _, key := range setup.PublicKeys {
		if !validSSHPublicKey(key) {
			return validationError("managed VM SSH public key is invalid")
		}
		total += len(key)
		if total > maxSSHKeysBytes {
			return validationError("managed VM SSH public keys exceed total size limit")
		}
	}
	return nil
}

// Validate enforces the bounded guest-agent configuration.
func (setup AgentSetup) Validate() error {
	if setup.Port < 1 || setup.Port > 65535 {
		return validationError("managed VM agent port is outside the supported range")
	}
	if !validUUIDV4(setup.Token) {
		return validationError("managed VM agent token must be a canonical UUIDv4")
	}
	return nil
}

// Validate enforces the receiver trust-boundary contract for image inspection.
func (request ImageInspectRequest) Validate() error {
	return validateImageTarget(
		request.CacheLocator,
		request.ImageID,
		request.ExpectedImageBytes,
		request.Filesystem,
	)
}

// Validate enforces the receiver trust-boundary contract for image optimization.
func (request ImageOptimizeRequest) Validate() error {
	if err := validateImageTarget(
		request.CacheLocator,
		request.ImageID,
		request.ExpectedImageBytes,
		request.Filesystem,
	); err != nil {
		return err
	}
	return request.Plan.Validate(request.Filesystem)
}

// Validate enforces the closed image optimization plan.
func (plan ImageOptimizePlan) Validate(current Filesystem) error {
	operations := 0
	if plan.ConvertTo != nil {
		operations++
		if *plan.ConvertTo != FilesystemExt4 {
			return validationError("managed image conversion target must be ext4")
		}
		if current == FilesystemExt4 {
			return validationError("managed image conversion target already matches current filesystem")
		}
	}
	if plan.DeblobOS != nil {
		operations++
		if !validOSIdentifier(*plan.DeblobOS) {
			return validationError("managed image OS identifier is invalid")
		}
	}
	if plan.FixFstab {
		operations++
	}
	if plan.Shrink {
		operations++
	}
	if operations == 0 {
		return validationError("managed image optimization plan requires at least one operation")
	}
	return nil
}

// Validate enforces the closed filesystem set.
func (filesystem Filesystem) Validate() error {
	switch filesystem {
	case FilesystemExt2, FilesystemExt3, FilesystemExt4, FilesystemBtrfs, FilesystemXFS:
		return nil
	default:
		return validationError("managed rootfs filesystem is unsupported")
	}
}

// Validate checks that a VM result is bound to the request and within hard bounds.
func (result VMProvisionResult) Validate(request VMProvisionRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMID != request.VMID {
		return validationError("managed VM provision result ID does not match request")
	}
	if result.Filesystem != request.Filesystem {
		return validationError("managed VM provision result filesystem does not match request")
	}
	if result.RootfsBytes < minimumRootfsBytes || result.RootfsBytes > maximumRootfsBytes {
		return validationError("managed VM provision result size is outside policy")
	}
	expectedRootfsBytes := request.ExpectedRootfsBytes
	if request.Plan.ResizeBytes != 0 {
		expectedRootfsBytes = request.Plan.ResizeBytes
	}
	if result.RootfsBytes != expectedRootfsBytes {
		return validationError("managed VM provision result size does not match request")
	}
	if result.FilesWritten > maxResultCount || result.CommandsRun > maxResultCount {
		return validationError("managed VM provision result count exceeds policy")
	}
	return nil
}

// Validate checks that an image-inspection result is bound to the request.
func (result ImageInspectResult) Validate(request ImageInspectRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.ImageID != request.ImageID {
		return validationError("managed image inspection result ID does not match request")
	}
	if !validOSIdentifier(result.OSType) {
		return validationError("managed image inspection OS identifier is invalid")
	}
	return nil
}

// Validate checks that an image-optimization result is bound to the request.
func (result ImageOptimizeResult) Validate(request ImageOptimizeRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.ImageID != request.ImageID {
		return validationError("managed image optimization result ID does not match request")
	}
	expectedFilesystem := request.Filesystem
	if request.Plan.ConvertTo != nil {
		expectedFilesystem = *request.Plan.ConvertTo
	}
	if result.Filesystem != expectedFilesystem {
		return validationError("managed image optimization result filesystem does not match request")
	}
	if result.ImageBytes < minimumImageBytes || result.ImageBytes > maximumImageBytes {
		return validationError("managed image optimization result size is outside policy")
	}
	if request.Plan.Shrink && result.ImageBytes > request.ExpectedImageBytes {
		return validationError("managed image optimization result grew during shrink")
	}
	if !request.Plan.Shrink && request.Plan.ConvertTo == nil && result.ImageBytes != request.ExpectedImageBytes {
		return validationError("managed image optimization result size changed without a size operation")
	}
	return nil
}

func validateImageTarget(cacheLocator, imageID string, expectedBytes uint64, filesystem Filesystem) error {
	if err := validateCacheLocator(cacheLocator); err != nil {
		return err
	}
	if !validLowerHexID(imageID, 64) {
		return validationError("managed image ID must be 64 lowercase hexadecimal characters")
	}
	if expectedBytes < minimumImageBytes || expectedBytes > maximumImageBytes {
		return validationError("managed image size is outside the supported range")
	}
	return filesystem.Validate()
}

func validateCacheLocator(locator string) error {
	if strings.ContainsRune(locator, '\x00') {
		return validationError("managed cache locator contains NUL")
	}
	if !filepath.IsAbs(locator) {
		return validationError("managed cache locator must be absolute")
	}
	if len(locator) >= unix.PathMax {
		return validationError("managed cache locator exceeds path length limit")
	}
	if filepath.Clean(locator) != locator {
		return validationError("managed cache locator must be canonical")
	}
	if locator == string(filepath.Separator) {
		return validationError("filesystem root is not a managed cache locator")
	}
	return nil
}

func validLowerHexID(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validOSIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && (character == '-' || character == '_') {
			continue
		}
		return false
	}
	return true
}

func validSSHPublicKey(value string) bool {
	if len(value) == 0 || len(value) > maxSSHKeyBytes || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, prefix := range []string{
		"ssh-ed25519 ",
		"ssh-rsa ",
		"ecdsa-sha2-nistp256 ",
		"ecdsa-sha2-nistp384 ",
		"ecdsa-sha2-nistp521 ",
		"sk-ssh-ed25519@openssh.com ",
		"sk-ecdsa-sha2-nistp256@openssh.com ",
	} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func validUUIDV4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	if value[14] != '4' || !strings.ContainsRune("89ab", rune(value[19])) {
		return false
	}
	for index := range len(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		character := value[index]
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, fmt.Sprintf("loopmount: %s", message))
}
