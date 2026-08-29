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
	testVMID    = "0123456789abcdef0123456789abcdef"
	testImageID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
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
		"agent privileged port": {
			mutate: func(request *VMProvisionRequest) {
				request.Plan.Agent = &AgentSetup{Port: 22, Token: "01234567-89ab-4def-8123-456789abcdef"}
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
				Plan:               ImageOptimizePlan{DeblobOS: func() *string { value := distro + "$(id)"; return &value }()},
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
