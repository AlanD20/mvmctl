package loopmount

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/privilegedwire"
)

const (
	testVMID                  = "0123456789abcdef0123456789abcdef"
	testImageID               = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testMaxGuestUsernameBytes = 32
)

func TestVMProvisionRequestValidate(t *testing.T) {
	hostname := "build-vm"
	dns := "1.1.1.1"
	user := "builder"
	distro := "ubuntu"
	request := VMProvisionRequest{
		CacheLocator:        "/srv/mvm-cache",
		VMID:                testVMID,
		ExpectedRootfsBytes: 512 << 20,
		Filesystem:          FilesystemExt4,
		Plan: VMProvisionPlan{
			ResizeBytes: 1 << 30,
			Hostname:    &hostname,
			DNS:         &dns,
			SSH: &SSHSetup{
				User:       user,
				PublicKeys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEexample builder@test"},
			},
			SudoUser:        &user,
			InjectCloudInit: true,
			DeblobOS:        &distro,
			FixFstab:        true,
			Agent: &AgentSetup{
				Port:  52,
				Token: "01234567-89ab-4def-8123-456789abcdef",
			},
		},
	}

	require.NoError(t, request.Validate())

	var frame bytes.Buffer
	require.NoError(t, privilegedwire.EncodeRequest(
		context.Background(),
		&frame,
		VMProvisionAction,
		request,
		0,
	))
	decoded, err := privilegedwire.DecodeRequest[VMProvisionRequest](
		context.Background(),
		bytes.NewReader(frame.Bytes()),
		VMProvisionAction,
		0,
	)
	require.NoError(t, err)
	require.NoError(t, decoded.Body.Validate())
	if diff := cmp.Diff(request, decoded.Body); diff != "" {
		t.Errorf("VM provision request mismatch (-want +got):\n%s", diff)
	}

	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	for _, forbidden := range []string{`"image"`, `"path"`, `"command"`, `"shell"`, "/tmp/", "/proc/self/fd"} {
		assert.NotContains(t, string(encoded), forbidden)
	}
}

func TestVMProvisionRequestRejectsUntrustedAuthorityAndUnboundedValues(t *testing.T) {
	// Rationale: Every value crossing into the privileged receiver must be either a closed enum or independently
	// bounded before it can influence guest filesystem mutation.
	valid := func() VMProvisionRequest {
		return VMProvisionRequest{
			CacheLocator:        "/srv/mvm-cache",
			VMID:                testVMID,
			ExpectedRootfsBytes: 512 << 20,
			Filesystem:          FilesystemExt4,
			Plan:                VMProvisionPlan{ResizeBytes: 1 << 30},
		}
	}
	overlongKeys := make([]string, maxSSHKeys+1)
	for index := range overlongKeys {
		overlongKeys[index] = "ssh-ed25519 AAAA"
	}
	largeKey := "ssh-ed25519 " + strings.Repeat("A", maxSSHKeyBytes)
	overlongUsername := strings.Repeat("a", testMaxGuestUsernameBytes+1)

	tests := map[string]struct {
		mutate  func(*VMProvisionRequest)
		wantErr string
	}{
		"relative cache locator": {
			mutate:  func(request *VMProvisionRequest) { request.CacheLocator = "cache" },
			wantErr: "cache locator must be absolute",
		},
		"noncanonical cache locator": {
			mutate:  func(request *VMProvisionRequest) { request.CacheLocator = "/srv/../cache" },
			wantErr: "cache locator must be canonical",
		},
		"filesystem root cache locator": {
			mutate:  func(request *VMProvisionRequest) { request.CacheLocator = "/" },
			wantErr: "filesystem root",
		},
		"VM ID traversal": {
			mutate:  func(request *VMProvisionRequest) { request.VMID = "../../etc/shadow" },
			wantErr: "VM ID",
		},
		"unsupported filesystem": {
			mutate:  func(request *VMProvisionRequest) { request.Filesystem = Filesystem("xfs") },
			wantErr: "filesystem",
		},
		"rootfs below hard minimum": {
			mutate:  func(request *VMProvisionRequest) { request.ExpectedRootfsBytes = minimumRootfsBytes - 1 },
			wantErr: "rootfs size",
		},
		"resize above hard maximum": {
			mutate:  func(request *VMProvisionRequest) { request.Plan.ResizeBytes = maximumRootfsBytes + 1 },
			wantErr: "resize",
		},
		"empty plan": {
			mutate:  func(request *VMProvisionRequest) { request.Plan = VMProvisionPlan{} },
			wantErr: "at least one operation",
		},
		"hostname shell syntax": {
			mutate: func(request *VMProvisionRequest) {
				hostname := "vm;touch-pwned"
				request.Plan.Hostname = &hostname
			},
			wantErr: "hostname",
		},
		"DNS command substitution": {
			mutate: func(request *VMProvisionRequest) {
				dns := "$(id)"
				request.Plan.DNS = &dns
			},
			wantErr: "DNS",
		},
		"SSH user shell syntax": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.SSH = &SSHSetup{User: "root;id"}
			},
			wantErr: "SSH username",
		},
		"SSH username exceeds receiver limit": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.SSH = &SSHSetup{User: overlongUsername}
			},
			wantErr: "SSH username exceeds",
		},
		"too many SSH keys": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.SSH = &SSHSetup{User: "root", PublicKeys: overlongKeys}
			},
			wantErr: "SSH public key count",
		},
		"oversized SSH key": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.SSH = &SSHSetup{User: "root", PublicKeys: []string{largeKey}}
			},
			wantErr: "SSH public key",
		},
		"multiline SSH key": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.SSH = &SSHSetup{User: "root", PublicKeys: []string{"ssh-ed25519 AAAA\nforged"}}
			},
			wantErr: "SSH public key",
		},
		"sudo user shell syntax": {
			mutate: func(request *VMProvisionRequest) {
				user := "root$(id)"
				request.Plan.SudoUser = &user
			},
			wantErr: "SSH username",
		},
		"sudo username exceeds receiver limit": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.SudoUser = &overlongUsername
			},
			wantErr: "sudo username exceeds",
		},
		"agent port below closed range": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.Agent = &AgentSetup{Port: 0, Token: "01234567-89ab-4def-8123-456789abcdef"}
			},
			wantErr: "agent port",
		},
		"agent port above closed range": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.Agent = &AgentSetup{Port: 65536, Token: "01234567-89ab-4def-8123-456789abcdef"}
			},
			wantErr: "agent port",
		},
		"agent token shell syntax": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.Agent = &AgentSetup{Port: 52, Token: "$(id)"}
			},
			wantErr: "agent token",
		},
		"distro shell syntax": {
			mutate: func(request *VMProvisionRequest) {
				distro := "ubuntu;id"
				request.Plan.DeblobOS = &distro
			},
			wantErr: "OS identifier",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid()
			tc.mutate(&request)
			err := request.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Rationale: Resize is a grow-only privileged effect. The request's admitted rootfs size is the authority boundary;
// an equal size is an idempotent resize, while even one byte below it would authorize an undeclared shrink.
func TestVMProvisionRequestResizeIntentIsGrowOnly(t *testing.T) {
	const expectedRootfsBytes = uint64(512 << 20)
	tests := map[string]struct {
		resizeBytes uint64
		wantErr     string
	}{
		"one byte below admitted size": {
			resizeBytes: expectedRootfsBytes - 1,
			wantErr:     "cannot shrink",
		},
		"equal to admitted size": {
			resizeBytes: expectedRootfsBytes,
		},
		"one byte above admitted size": {
			resizeBytes: expectedRootfsBytes + 1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := VMProvisionRequest{
				CacheLocator:        "/srv/mvm-cache",
				VMID:                testVMID,
				ExpectedRootfsBytes: expectedRootfsBytes,
				Filesystem:          FilesystemExt4,
				Plan:                VMProvisionPlan{ResizeBytes: test.resizeBytes},
			}

			err := request.Validate()

			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// Rationale: The shared username validator constrains characters but not length. The privileged protocol owns an
// explicit byte boundary and must accept its exact maximum for both guest-account fields.
func TestVMProvisionRequestAcceptsGuestUsernameBoundary(t *testing.T) {
	username := strings.Repeat("a", testMaxGuestUsernameBytes)
	request := VMProvisionRequest{
		CacheLocator:        "/srv/mvm-cache",
		VMID:                testVMID,
		ExpectedRootfsBytes: 512 << 20,
		Filesystem:          FilesystemExt4,
		Plan: VMProvisionPlan{
			SSH:      &SSHSetup{User: username},
			SudoUser: &username,
		},
	}

	require.NoError(t, request.Validate())
}

// Rationale: AF_VSOCK ports do not have TCP's privileged-port boundary. The closed product range deliberately admits
// every value from 1 through 65535, including port 22.
func TestAgentSetupAcceptsFullVsockPortRange(t *testing.T) {
	tests := map[string]int{
		"minimum":             1,
		"TCP privileged port": 22,
		"maximum":             65535,
	}

	for name, port := range tests {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, (AgentSetup{
				Port:  port,
				Token: "01234567-89ab-4def-8123-456789abcdef",
			}).Validate())
		})
	}
}

func TestImageRequestsValidateClosedStagingIdentity(t *testing.T) {
	distro := "alpine"
	targetFilesystem := FilesystemExt4
	tests := map[string]struct {
		validate func() error
	}{
		"inspect": {
			validate: func() error {
				return (ImageInspectRequest{
					CacheLocator:       "/srv/mvm-cache",
					ImageID:            testImageID,
					ExpectedImageBytes: 256 << 20,
					Filesystem:         FilesystemExt4,
				}).Validate()
			},
		},
		"optimize": {
			validate: func() error {
				return (ImageOptimizeRequest{
					CacheLocator:       "/srv/mvm-cache",
					ImageID:            testImageID,
					ExpectedImageBytes: 256 << 20,
					Filesystem:         FilesystemBtrfs,
					Plan: ImageOptimizePlan{
						ConvertTo: &targetFilesystem,
						DeblobOS:  &distro,
						FixFstab:  true,
						Shrink:    true,
					},
				}).Validate()
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, tc.validate())
		})
	}
}

func TestImageRequestsRejectPathsAndGenericOperations(t *testing.T) {
	distro := "ubuntu"
	tests := map[string]struct {
		request ImageOptimizeRequest
		wantErr string
	}{
		"image ID is not a path": {
			request: ImageOptimizeRequest{
				CacheLocator:       "/srv/mvm-cache",
				ImageID:            "/tmp/rootfs.img",
				ExpectedImageBytes: 256 << 20,
				Filesystem:         FilesystemExt4,
				Plan:               ImageOptimizePlan{Shrink: true},
			},
			wantErr: "image ID",
		},
		"plan must be nonempty": {
			request: ImageOptimizeRequest{
				CacheLocator:       "/srv/mvm-cache",
				ImageID:            testImageID,
				ExpectedImageBytes: 256 << 20,
				Filesystem:         FilesystemExt4,
			},
			wantErr: "at least one operation",
		},
		"conversion is closed to ext4": {
			request: ImageOptimizeRequest{
				CacheLocator:       "/srv/mvm-cache",
				ImageID:            testImageID,
				ExpectedImageBytes: 256 << 20,
				Filesystem:         FilesystemExt4,
				Plan: ImageOptimizePlan{
					ConvertTo: func() *Filesystem { value := Filesystem("xfs"); return &value }(),
				},
			},
			wantErr: "conversion target",
		},
		"distro is bounded typed data": {
			request: ImageOptimizeRequest{
				CacheLocator:       "/srv/mvm-cache",
				ImageID:            testImageID,
				ExpectedImageBytes: 256 << 20,
				Filesystem:         FilesystemExt4,
				Plan: ImageOptimizePlan{
					DeblobOS: func() *string { value := distro + "$(id)"; return &value }(),
				},
			},
			wantErr: "OS identifier",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.request.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestLoopMountResultsBindToRequestIdentity(t *testing.T) {
	vmRequest := VMProvisionRequest{
		CacheLocator:        "/srv/mvm-cache",
		VMID:                testVMID,
		ExpectedRootfsBytes: 512 << 20,
		Filesystem:          FilesystemExt4,
		Plan:                VMProvisionPlan{ResizeBytes: 1 << 30},
	}
	imageRequest := ImageOptimizeRequest{
		CacheLocator:       "/srv/mvm-cache",
		ImageID:            testImageID,
		ExpectedImageBytes: 256 << 20,
		Filesystem:         FilesystemExt4,
		Plan:               ImageOptimizePlan{Shrink: true},
	}

	require.NoError(t, (VMProvisionResult{
		VMID:         testVMID,
		Filesystem:   FilesystemExt4,
		RootfsBytes:  1 << 30,
		FilesWritten: 4,
		CommandsRun:  3,
	}).Validate(vmRequest))
	require.NoError(t, (ImageInspectResult{ImageID: testImageID, OSType: "alpine"}).Validate(ImageInspectRequest{
		CacheLocator:       "/srv/mvm-cache",
		ImageID:            testImageID,
		ExpectedImageBytes: 256 << 20,
		Filesystem:         FilesystemExt4,
	}))
	require.NoError(t, (ImageOptimizeResult{
		ImageID:    testImageID,
		Filesystem: FilesystemExt4,
		ImageBytes: 192 << 20,
	}).Validate(imageRequest))

	wrongVM := VMProvisionResult{VMID: strings.Repeat("f", 32), Filesystem: FilesystemExt4, RootfsBytes: 1 << 30}
	assert.Error(t, wrongVM.Validate(vmRequest))
	wrongImage := ImageOptimizeResult{
		ImageID:    strings.Repeat("f", 64),
		Filesystem: FilesystemExt4,
		ImageBytes: 192 << 20,
	}
	assert.Error(t, wrongImage.Validate(imageRequest))
}

// Rationale: Without a resize operation, a changed rootfs size is not an authoritative result of the closed plan.
// Requiring the admitted pre-operation size prevents an unrelated or compromised receiver mutation from being accepted.
func TestVMProvisionResultRequiresUnchangedSizeWithoutResize(t *testing.T) {
	request := VMProvisionRequest{
		CacheLocator:        "/srv/mvm-cache",
		VMID:                testVMID,
		ExpectedRootfsBytes: 512 << 20,
		Filesystem:          FilesystemExt4,
		Plan:                VMProvisionPlan{FixFstab: true},
	}
	valid := VMProvisionResult{
		VMID:        testVMID,
		Filesystem:  FilesystemExt4,
		RootfsBytes: request.ExpectedRootfsBytes,
	}

	require.NoError(t, valid.Validate(request))

	changed := valid
	changed.RootfsBytes++
	err := changed.Validate(request)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size does not match")
}

// Rationale: Result admission must use the same request-relative boundary as request admission. Equal and growing
// resize results are authoritative only at the exact requested size; returning the old size after a grow is invalid.
func TestVMProvisionResultBindsGrowOnlyResize(t *testing.T) {
	const expectedRootfsBytes = uint64(512 << 20)
	tests := map[string]struct {
		resizeBytes uint64
		resultBytes uint64
		wantErr     string
	}{
		"equal resize result": {
			resizeBytes: expectedRootfsBytes,
			resultBytes: expectedRootfsBytes,
		},
		"growing resize result": {
			resizeBytes: expectedRootfsBytes + 1,
			resultBytes: expectedRootfsBytes + 1,
		},
		"grow returned old size": {
			resizeBytes: expectedRootfsBytes + 1,
			resultBytes: expectedRootfsBytes,
			wantErr:     "size does not match",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := VMProvisionRequest{
				CacheLocator:        "/srv/mvm-cache",
				VMID:                testVMID,
				ExpectedRootfsBytes: expectedRootfsBytes,
				Filesystem:          FilesystemExt4,
				Plan:                VMProvisionPlan{ResizeBytes: test.resizeBytes},
			}
			result := VMProvisionResult{
				VMID:        testVMID,
				Filesystem:  FilesystemExt4,
				RootfsBytes: test.resultBytes,
			}

			err := result.Validate(request)

			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
