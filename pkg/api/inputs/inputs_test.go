package inputs

// Internal package: tests both exported and unexported functions.
// Unexported functions (e.g., resolveDisabledDetectors, parseKernelFilename)
// cannot be accessed from an external test package.

import (
	"context"
	"sort"
	"testing"

	"mvmctl/internal/infra"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ParseVMPath ---
// Rationale: ParseVMPath splits "vm:path" references used by CPInput.Resolve()
// for scp-style copy targets. A bug here would misroute file copies
// to the wrong VM or silently treat VM-less paths as local.

func TestParseVMPath(t *testing.T) {
	tests := map[string]struct {
		input    string
		wantVM   string
		wantPath string
	}{
		// Error / edge cases (colon not found means path-only)
		"no colon":               {input: "plainpath", wantVM: "", wantPath: "plainpath"},
		"empty string":           {input: "", wantVM: "", wantPath: ""},
		"empty vm identifier":    {input: ":/remote/path", wantVM: "", wantPath: "/remote/path"},
		"empty path after colon": {input: "vmname:", wantVM: "vmname", wantPath: ""},

		// Happy paths
		"vm colon absolute path":    {input: "my-vm:/etc/hosts", wantVM: "my-vm", wantPath: "/etc/hosts"},
		"path with multiple colons": {input: "vm:/path:with:colons", wantVM: "vm", wantPath: "/path:with:colons"},
		"root path":                 {input: "vm:/", wantVM: "vm", wantPath: "/"},
		"vm with dots":              {input: "my.vm.name:/data/file", wantVM: "my.vm.name", wantPath: "/data/file"},
		"vm with hyphens": {
			input:    "test-vm-42:/var/log/syslog",
			wantVM:   "test-vm-42",
			wantPath: "/var/log/syslog",
		},
		"relative path": {input: "vm:relative/path", wantVM: "vm", wantPath: "relative/path"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotVM, gotPath := infra.ParseVMPath(tc.input)

			if diff := cmp.Diff(tc.wantVM, gotVM); diff != "" {
				t.Errorf("ParseVMPath() VM (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPath, gotPath); diff != "" {
				t.Errorf("ParseVMPath() path (-want +got):\n%s", diff)
			}
		})
	}
}

// --- resolveDisabledDetectors ---
// Rationale: resolveDisabledDetectors translates CLI detector names to
// internal codes or rejects unknown names. A bug here would allow
// typos to silently pass through, or disable the wrong detector.

func TestResolveDisabledDetectors(t *testing.T) {
	// Expected values for the "all" shortcut — sorted for deterministic compare.
	allInternal := sortedValues(CLI_TO_INTERNAL_DETECTOR)

	tests := map[string]struct {
		input   []string
		want    []string
		wantErr string
	}{
		// Error paths first
		"unknown detector name":    {input: []string{"nonexistent"}, wantErr: "Unknown detector"},
		"mix of valid and invalid": {input: []string{"type", "bogus"}, wantErr: "Unknown detector"},

		// Happy paths
		"empty list":            {input: []string{}, want: nil},
		"single valid detector": {input: []string{"type"}, want: []string{"type_code"}},
		"multiple valid detectors": {
			input: []string{"type", "label", "size"},
			want:  []string{"type_code", "label", "size"},
		},
		"identity mapping for filesystem":  {input: []string{"filesystem"}, want: []string{"filesystem"}},
		"all shortcut disables everything": {input: []string{"all"}, want: allInternal},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := resolveDisabledDetectors(tc.input)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)

			// Normalize nil to empty slice for comparison
			if got == nil {
				got = []string{}
			}
			want := tc.want
			if want == nil {
				want = []string{}
			}

			// Sort for deterministic comparison (map iteration order is undefined)
			sort.Strings(got)
			wantSorted := append([]string{}, want...)
			sort.Strings(wantSorted)

			if diff := cmp.Diff(wantSorted, got); diff != "" {
				t.Errorf("resolveDisabledDetectors() (-want +got):\n%s", diff)
			}
		})
	}
}

// sortedValues returns the values of m sorted lexicographically.
func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// --- parseKernelFilename ---
// Rationale: parseKernelFilename extracts version and architecture from
// kernel filenames. Bugs here would mis-identify kernel versions,
// causing version resolution to fall back to "unknown".

func TestParseKernelFilename(t *testing.T) {
	tests := map[string]struct {
		input    string
		wantVer  string
		wantArch string
	}{
		// Edge cases
		"empty filename":       {input: "", wantVer: "-", wantArch: "-"},
		"no arch no version":   {input: "vmlinux", wantVer: "-", wantArch: "-"},
		"only arch no version": {input: "vmlinux-x86_64", wantVer: "-", wantArch: "x86_64"},
		"only version no arch": {input: "vmlinux-6.1", wantVer: "6.1", wantArch: "-"},

		// Happy paths
		"full semver with arch": {input: "vmlinux-6.1.0-x86_64", wantVer: "6.1.0", wantArch: "x86_64"},
		"two-part version":      {input: "vmlinux-5.10-arm64", wantVer: "5.10", wantArch: "arm64"},
		"v prefix stripped":     {input: "vmlinux-v6.1.0-arm64", wantVer: "6.1.0", wantArch: "arm64"},
		"amd64 arch":            {input: "vmlinux-5.15.3-amd64", wantVer: "5.15.3", wantArch: "amd64"},
		"aarch64 arch":          {input: "bzImage-5.4-aarch64", wantVer: "5.4", wantArch: "aarch64"},
		"rc version suffix":     {input: "vmlinux-5.15.0-rc3-arm64", wantVer: "5.15.0", wantArch: "arm64"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			gotVer, gotArch := parseKernelFilename(tc.input)

			if diff := cmp.Diff(tc.wantVer, gotVer); diff != "" {
				t.Errorf("parseKernelFilename() version (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantArch, gotArch); diff != "" {
				t.Errorf("parseKernelFilename() arch (-want +got):\n%s", diff)
			}
		})
	}
}



// --- KeyCreateInput.Resolve ---
// Rationale: KeyCreateInput.Resolve validates key names and
// algorithms before creating SSH keys. A bug here would let invalid
// key names through, causing filesystem or SSH failures.

func TestKeyCreateInput_Resolve(t *testing.T) {
	tests := map[string]struct {
		input   KeyCreateInput
		want    *ResolvedKeyCreateInput
		wantErr string
	}{
		// Error paths first
		"empty name": {
			input:   KeyCreateInput{Name: "", OutputDir: "/tmp", Overwrite: true},
			wantErr: "key name is required",
		},
		"invalid algorithm": {
			input:   KeyCreateInput{Name: "mykey", Algorithm: "dsa", OutputDir: "/tmp", Overwrite: true},
			wantErr: "invalid algorithm",
		},
		"name with shell metacharacters": {
			input:   KeyCreateInput{Name: "key;rm -rf /", OutputDir: "/tmp", Overwrite: true},
			wantErr: "invalid key name",
		},

		// Happy paths
		"default algorithm ed25519": {
			input: KeyCreateInput{Name: "mykey", OutputDir: "/tmp", Overwrite: true},
			want: &ResolvedKeyCreateInput{
				Name:      "mykey",
				Algorithm: "ed25519",
				OutputDir: "/tmp",
				Overwrite: true,
			},
		},
		"explicit rsa algorithm": {
			input: KeyCreateInput{Name: "rsa-key", Algorithm: "rsa", Bits: 4096, OutputDir: "/tmp", Overwrite: true},
			want: &ResolvedKeyCreateInput{
				Name:      "rsa-key",
				Algorithm: "rsa",
				Bits:      intPtr(4096),
				OutputDir: "/tmp",
				Overwrite: true,
			},
		},
		"custom comment": {
			input: KeyCreateInput{Name: "mykey", Comment: "ci@builder", OutputDir: "/tmp", Overwrite: true},
			want: &ResolvedKeyCreateInput{
				Name:      "mykey",
				Algorithm: "ed25519",
				Comment:   "ci@builder",
				OutputDir: "/tmp",
				Overwrite: true,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tc.input.Resolve()

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)

			// Zero out Comment for default-comment assertions
			// (Comment includes hostname from os.Hostname(), which varies)
			if tc.want.Comment == "" {
				tc.want.Comment = got.Comment
			}
			// Zero out SetDefault (not tested here)
			tc.want.SetDefault = got.SetDefault

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Resolve() (-want +got):\n%s", diff)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

// --- BinaryPullInput.Resolve ---
// Rationale: BinaryPullInput.Resolve validates and resolves defaults for
// binary pulls. A bug here could let invalid types through.

func TestBinaryPullInput_Resolve(t *testing.T) {
	tests := map[string]struct {
		input   BinaryPullInput
		wantErr string
	}{
		// Error paths
		"unsupported binary type": {
			input:   BinaryPullInput{Type: "kernel", Version: "1.0.0"},
			wantErr: "Unsupported binary",
		},

		// Happy paths
		"firecracker type passes": {
			input: BinaryPullInput{Type: "firecracker", Version: "1.0.0"},
		},
		"empty type defaults to firecracker": {
			input: BinaryPullInput{Version: "1.0.0"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := tc.input.Resolve()

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// --- VMCreateRequest.ensureValidate (VCPU range) ---
// Rationale: VCPU range validation prevents creating VMs with
// impossible or wasteful vCPU counts. A bug here could allow 0 or
// 999 vCPUs, causing Firecracker startup failure or resource exhaustion.

func TestVMCreateEnsureValidate_VCPURange(t *testing.T) {
	tests := map[string]struct {
		vcpu    int
		wantErr string
	}{
		// Error paths — out of range (checked before I/O, no deps needed)
		"zero vcpu":            {vcpu: 0, wantErr: "Invalid vcpu"},
		"negative vcpu":        {vcpu: -1, wantErr: "Invalid vcpu"},
		"below minimum by one": {vcpu: infra.VCPUMin - 1, wantErr: "Invalid vcpu"},
		"above maximum by one": {vcpu: infra.VCPUMax + 1, wantErr: "Invalid vcpu"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := &VMCreateRequest{}
			result := &ResolvedVMCreateInput{
				VCPUCount: tc.vcpu,
				// All other fields zero — early check returns before I/O
			}
			err := r.ensureValidate(context.Background(), result)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			return
		})
	}

	// Context cancellation test
	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &VMCreateRequest{}
		result := &ResolvedVMCreateInput{VCPUCount: 2}
		err := r.ensureValidate(ctx, result)
		assert.Error(t, err)
	})
}

// --- VMCreateRequest.ensureValidate (memory range) ---
// Rationale: Memory range validation prevents OOM or insufficient-memory
// VM configurations. A bug could let 0 MiB or 99999 MiB through.

func TestVMCreateEnsureValidate_MemoryRange(t *testing.T) {
	tests := map[string]struct {
		memMib  int
		wantErr string
	}{
		// Error paths — out of range (checked before I/O, no deps needed)
		"zero memory":          {memMib: 0, wantErr: "Invalid mem_size_mib"},
		"below minimum by one": {memMib: infra.MemMinMB - 1, wantErr: "Invalid mem_size_mib"},
		"above maximum by one": {memMib: infra.MemMaxMB + 1, wantErr: "Invalid mem_size_mib"},
		"negative memory":      {memMib: -1, wantErr: "Invalid mem_size_mib"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := &VMCreateRequest{}
			result := &ResolvedVMCreateInput{
				VCPUCount:  2, // valid, passes VCPU check first
				MemSizeMib: tc.memMib,
			}
			err := r.ensureValidate(context.Background(), result)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			return
		})
	}

	// Context cancellation test
	t.Run("context_cancelled_returns_error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := &VMCreateRequest{}
		// Use invalid VCPU to fail validation before reaching Kernel/Binary access
		result := &ResolvedVMCreateInput{VCPUCount: 0, MemSizeMib: 512}
		err := r.ensureValidate(ctx, result)
		assert.Error(t, err)
	})
}

// --- NetworkCreateInput.Validate ---
// Rationale: NetworkCreateInput.Validate checks basic input fields.
// A bug would allow empty names or subnets through.

func TestNetworkCreateInput_Validate(t *testing.T) {
	tests := map[string]struct {
		input   NetworkCreateInput
		wantErr string
	}{
		"empty name": {
			input:   NetworkCreateInput{Subnet: "10.0.0.0/24"},
			wantErr: "Network name is required",
		},
		"empty subnet": {
			input:   NetworkCreateInput{Name: "testnet"},
			wantErr: "Subnet is required",
		},
		"valid input": {
			input: NetworkCreateInput{Name: "testnet", Subnet: "10.0.0.0/24"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.input.Validate()
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
