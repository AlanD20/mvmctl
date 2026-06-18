package enricher

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

var ctx = context.Background()

func newEnricher() *Enricher {
	return New(
		testutil.NewVMRepo(),
		testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(),
		testutil.NewImageRepo(),
		testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)
}

// --- EnrichVM: Forward relations ---
// Rationale: EnrichVM resolves kernel, image, binary, and network via
// forward FK lookups. Each must correctly batch-resolve and assign.

func TestEnrichVM_Kernel(t *testing.T) {
	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", KernelID: "k-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))

	require.NotNil(t, vms[0].Kernel)
	assert.Equal(t, "6.1", vms[0].Kernel.Version)
}

func TestEnrichVM_Image(t *testing.T) {
	img := testutil.NewImageRepo()
	require.NoError(t, img.Upsert(ctx, &model.ImageItem{ID: "img-1", Name: "alpine-3.21"}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), img,
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", ImageID: "img-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "image"))

	require.NotNil(t, vms[0].Image)
	assert.Equal(t, "alpine-3.21", vms[0].Image.Name)
}

func TestEnrichVM_Binary(t *testing.T) {
	bin := testutil.NewBinaryRepo()
	require.NoError(t, bin.Upsert(ctx, &model.BinaryItem{ID: "b-1", Version: "1.15", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), bin, testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", BinaryID: "b-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "binary"))

	require.NotNil(t, vms[0].Binary)
	assert.Equal(t, "1.15", vms[0].Binary.Version)
}

func TestEnrichVM_Network(t *testing.T) {
	net := testutil.NewNetworkRepo()
	require.NoError(t, net.Upsert(ctx, &model.NetworkItem{ID: "net-1", Name: "default", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), net, testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", NetworkID: "net-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "network"))

	require.NotNil(t, vms[0].Network)
	assert.Equal(t, "default", vms[0].Network.Name)
}

// --- EnrichVM: All forward relations combined ---
// Rationale: Enrichment with multiple paths must resolve all correctly.

func TestEnrichVM_AllForward(t *testing.T) {
	krn := testutil.NewKernelRepo()
	img := testutil.NewImageRepo()
	bin := testutil.NewBinaryRepo()
	net := testutil.NewNetworkRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", IsPresent: true}))
	require.NoError(t, img.Upsert(ctx, &model.ImageItem{ID: "i-1", IsPresent: true}))
	require.NoError(t, bin.Upsert(ctx, &model.BinaryItem{ID: "b-1", IsPresent: true}))
	require.NoError(t, net.Upsert(ctx, &model.NetworkItem{ID: "n-1", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), net, testutil.NewLeaseRepo(),
		img, krn, bin, testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{
		ID: "vm-1", KernelID: "k-1", ImageID: "i-1",
		BinaryID: "b-1", NetworkID: "n-1",
	}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel", "image", "binary", "network"))

	assert.NotNil(t, vms[0].Kernel, "kernel")
	assert.NotNil(t, vms[0].Image, "image")
	assert.NotNil(t, vms[0].Binary, "binary")
	assert.NotNil(t, vms[0].Network, "network")
}

// --- EnrichVM: Missing FK ---
// Rationale: When a VM references a nonexistent kernel/image/binary,
// enrichment should not error — the field remains nil (soft-fail).

func TestEnrichVM_MissingFK_leavesNil(t *testing.T) {
	e := newEnricher()
	vms := []*model.VMItem{{ID: "vm-1", KernelID: "nonexistent"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))
	assert.Nil(t, vms[0].Kernel)
}

func TestEnrichVM_MissingNetwork_leavesNil(t *testing.T) {
	e := newEnricher()
	vms := []*model.VMItem{{ID: "vm-1", NetworkID: "nonexistent"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "network"))
	assert.Nil(t, vms[0].Network)
}

// --- EnrichVM: Empty / nil input ---
// Rationale: Empty input must not error or panic.

func TestEnrichVM_EmptyInput(t *testing.T) {
	e := newEnricher()
	require.NoError(t, e.EnrichVM(ctx, nil, "kernel"))
	require.NoError(t, e.EnrichVM(ctx, []*model.VMItem{}, "kernel"))
}

// --- EnrichVM: Volumes ---
// Rationale: Volume enrichment uses JSON-array-to-list resolution.
// Must correctly match each VM to its volumes.

func TestEnrichVM_Volumes(t *testing.T) {
	vol := testutil.NewVolumeRepo()
	require.NoError(t, vol.Upsert(ctx, &model.VolumeItem{ID: "vol-1", Name: "data"}))
	require.NoError(t, vol.Upsert(ctx, &model.VolumeItem{ID: "vol-2", Name: "log"}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(), vol, testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{
		{ID: "vm-1", VolumeIDs: []string{"vol-1", "vol-2"}},
		{ID: "vm-2", VolumeIDs: []string{"vol-1"}},
	}
	require.NoError(t, e.EnrichVM(ctx, vms, "volumes"))

	assert.Len(t, vms[0].Volumes, 2)
	assert.Len(t, vms[1].Volumes, 1)
}

func TestEnrichVM_Volumes_noneAssigned(t *testing.T) {
	e := newEnricher()
	vms := []*model.VMItem{{ID: "vm-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "volumes"))
	assert.Nil(t, vms[0].Volumes)
}

// --- EnrichVM: Network Leases (nested) ---
// Rationale: network.leases is a nested relation. "network" must resolve
// before "network.leases" — the sortByDotCount helper ensures this.

func TestEnrichVM_NetworkLeases(t *testing.T) {
	net := testutil.NewNetworkRepo()
	lease := testutil.NewLeaseRepo()
	require.NoError(t, net.Upsert(ctx, &model.NetworkItem{ID: "net-1", Name: "default", IsPresent: true}))
	lease.SetNetwork(10, true)
	_, err := lease.Acquire(ctx, "net-1", "vm-1", nil)
	require.NoError(t, err)

	e := New(
		testutil.NewVMRepo(), net, lease,
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", NetworkID: "net-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "network", "network.leases"))

	require.NotNil(t, vms[0].Network)
	require.NotNil(t, vms[0].Network.Leases)
	assert.Len(t, vms[0].Network.Leases, 1)
}

// --- EnrichNetwork ---
// Rationale: Network enrichment resolves leases and referencing VMs.

func TestEnrichNetwork_Leases(t *testing.T) {
	lease := testutil.NewLeaseRepo()
	lease.SetNetwork(10, true)
	_, err := lease.Acquire(ctx, "net-1", "vm-1", nil)
	require.NoError(t, err)

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(), lease,
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	nets := []*model.NetworkItem{{ID: "net-1", Name: "test"}}
	require.NoError(t, e.EnrichNetwork(ctx, nets, "leases"))
	require.NotNil(t, nets[0].Leases)
	assert.Len(t, nets[0].Leases, 1)
}

func TestEnrichNetwork_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VMItem{ID: "vm-1", NetworkID: "net-1"}))

	e := New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	nets := []*model.NetworkItem{{ID: "net-1", Name: "test"}}
	require.NoError(t, e.EnrichNetwork(ctx, nets, "vm"))
	require.NotNil(t, nets[0].VMs)
	assert.Len(t, nets[0].VMs, 1)
}

// --- EnrichImage ---
// Rationale: Image enrichment resolves VMs referencing each image.

func TestEnrichImage_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VMItem{ID: "vm-1", ImageID: "img-1"}))

	e := New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	images := []*model.ImageItem{{ID: "img-1", Name: "alpine"}}
	require.NoError(t, e.EnrichImage(ctx, images, "vm"))

	require.NotNil(t, images[0].VMs)
	assert.Len(t, images[0].VMs, 1)
	assert.Equal(t, "vm-1", images[0].VMs[0].ID)
}

// --- EnrichKernel ---
// Rationale: Kernel enrichment resolves VMs referencing each kernel.

func TestEnrichKernel_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VMItem{ID: "vm-1", KernelID: "k-1"}))

	e := New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	kernels := []*model.KernelItem{{ID: "k-1", Version: "6.1"}}
	require.NoError(t, e.EnrichKernel(ctx, kernels, "vm"))

	require.NotNil(t, kernels[0].VMs)
	assert.Len(t, kernels[0].VMs, 1)
}

// --- EnrichBinary ---
// Rationale: Binary enrichment resolves VMs referencing each binary.

func TestEnrichBinary_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VMItem{ID: "vm-1", BinaryID: "b-1"}))

	e := New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	binaries := []*model.BinaryItem{{ID: "b-1", Version: "1.15"}}
	require.NoError(t, e.EnrichBinary(ctx, binaries, "vm"))

	require.NotNil(t, binaries[0].VMs)
	assert.Len(t, binaries[0].VMs, 1)
}

// --- EnrichVolume ---
// Rationale: Volume enrichment resolves VMs referencing each volume.

func TestEnrichVolume_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VMItem{ID: "vm-1", VolumeIDs: []string{"vol-1"}}))

	e := New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vols := []*model.VolumeItem{{ID: "vol-1", Name: "data"}}
	require.NoError(t, e.EnrichVolume(ctx, vols, "vm"))

	require.NotNil(t, vols[0].VMs)
	assert.Len(t, vols[0].VMs, 1)
}

// --- Error handling: invalid include path ---
// Rationale: Enrichment with an unknown path must error with available options.

func TestEnrichVM_unknownPath(t *testing.T) {
	e := newEnricher()
	vms := []*model.VMItem{{ID: "vm-1"}}
	err := e.EnrichVM(ctx, vms, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unknown relation")
	assert.Contains(t, err.Error(), "kernel") // available options in error
}

func TestEnrichVM_emptyInclude(t *testing.T) {
	e := newEnricher()
	vms := []*model.VMItem{{ID: "vm-1"}}
	err := e.EnrichVM(ctx, vms)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "include list is required")
}

// --- Duplicate enrichment ---
// Rationale: Enriching the same relation twice must not duplicate data.

func TestEnrichVM_DuplicateCall(t *testing.T) {
	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", KernelID: "k-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))

	assert.NotNil(t, vms[0].Kernel)
}

// --- Concurrent enrichment ---
// Rationale: Enrichment is called from concurrent contexts (batch resolve).
// No data races or panics.

func TestEnrichVM_Concurrent(t *testing.T) {
	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	done := make(chan struct{})
	go func() {
		vms := []*model.VMItem{{ID: "vm-1", KernelID: "k-1"}}
		_ = e.EnrichVM(ctx, vms, "kernel")
		done <- struct{}{}
	}()
	vms2 := []*model.VMItem{{ID: "vm-2", KernelID: "k-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms2, "kernel"))
	<-done
}

// --- Context cancellation (enrichment methods) ---
// Rationale: All Enrich* methods take context.Context. With in-memory repos
// the context is not checked (no DB or HTTP I/O), so cancellation must not
// cause panic or hang — it returns successfully with populated relations.

func TestEnrichVM_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)

	vms := []*model.VMItem{{ID: "vm-1", KernelID: "k-1"}}
	err := e.EnrichVM(ctx, vms, "kernel")
	require.NoError(t, err, "cancelled context must not block enrichment with in-memory repos")
	require.NotNil(t, vms[0].Kernel, "enrichment must still populate relations")
	assert.Equal(t, "6.1", vms[0].Kernel.Version)
}

// --- Context cancellation — remaining Enrich* method ---
// Rationale: EnrichNetwork, EnrichImage, EnrichKernel, EnrichBinary,
// EnrichVolume all take context.Context. With in-memory repos the context
// is not checked (no DB or HTTP I/O), so cancellation must not cause panic
// or hang — each returns successfully with populated relations.

func TestEnrichNetwork_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)
	nets := []*model.NetworkItem{{ID: "net-1", Name: "default"}}
	err := e.EnrichNetwork(ctx, nets, "leases")
	require.NoError(t, err, "cancelled context must not block enrichment with in-memory repos")
}

func TestEnrichImage_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)
	imgs := []*model.ImageItem{{ID: "img-1"}}
	err := e.EnrichImage(ctx, imgs, "vm")
	require.NoError(t, err, "cancelled context must not block enrichment with in-memory repos")
}

func TestEnrichKernel_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)
	kernels := []*model.KernelItem{{ID: "k-1"}}
	err := e.EnrichKernel(ctx, kernels, "vm")
	require.NoError(t, err, "cancelled context must not block enrichment with in-memory repos")
}

func TestEnrichBinary_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)
	bins := []*model.BinaryItem{{ID: "b-1"}}
	err := e.EnrichBinary(ctx, bins, "vm")
	require.NoError(t, err, "cancelled context must not block enrichment with in-memory repos")
}

func TestEnrichVolume_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), testutil.NewVsockRepo(),
	)
	vols := []*model.VolumeItem{{ID: "vol-1"}}
	err := e.EnrichVolume(ctx, vols, "vm")
	require.NoError(t, err, "cancelled context must not block enrichment with in-memory repos")
}

// --- validatePaths ---
// Rationale: validatePaths guards every enrichment call. If it misses an
// invalid path or rejects a valid one, enrichment silently skips or fails.

func TestValidatePaths(t *testing.T) {
	tests := map[string]struct {
		include []string
		wantErr string
	}{
		// Error paths first
		"invalid_path": {
			include: []string{"nonexistent"},
			wantErr: "Unknown relation 'nonexistent'",
		},
		"multiple_paths_one_invalid": {
			include: []string{"kernel", "network", "invalid"},
			wantErr: "Unknown relation 'invalid'",
		},
		// Happy paths
		"valid_path": {
			include: []string{"kernel"},
			wantErr: "",
		},
		"empty_include_returns_nil": {
			include: []string{},
			wantErr: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := validatePaths(tc.include, VMRelations)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// --- resolveInclude ---
// Rationale: resolveInclude is the entry point for enrichment path resolution.
// It validates and sorts include paths. An incorrect sort would break nested
// relation ordering (parents must resolve before children).

func TestResolveInclude(t *testing.T) {
	tests := map[string]struct {
		include []string
		want    []string
		wantErr string
	}{
		// Error paths first
		"empty_include": {
			include: []string{},
			wantErr: "include list is required",
		},
		"invalid_path": {
			include: []string{"nonexistent"},
			wantErr: "Unknown relation 'nonexistent'",
		},
		// Happy paths
		"returns_sorted_by_dot_count": {
			include: []string{"network.leases", "kernel"},
			want:    []string{"kernel", "network.leases"},
		},
		"single_path": {
			include: []string{"kernel"},
			want:    []string{"kernel"},
		},
		"already_sorted": {
			include: []string{"kernel", "network.leases"},
			want:    []string{"kernel", "network.leases"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := resolveInclude(tc.include, VMRelations)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("resolveInclude() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- isEnrichmentError ---
// Rationale: isEnrichmentError determines whether a repository error should
// soft-fail or propagate. If it returns false for a DomainError, enrichment
// would propagate not-found errors as hard failures instead of populating nil.

func TestIsEnrichmentError(t *testing.T) {
	tests := map[string]struct {
		err  error
		want bool
	}{
		"nil_returns_false": {
			err: nil, want: false,
		},
		"domain_error_returns_true": {
			err: errs.New(errs.CodeVMNotFound, "vm not found"), want: true,
		},
		"plain_error_returns_false": {
			err: errors.New("plain error"), want: false,
		},
		"wrapped_domain_error_returns_true": {
			err: fmt.Errorf("wrapped: %w", errs.New(errs.CodeVMNotFound, "not found")), want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := isEnrichmentError(tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- safeCastNetwork ---
// Rationale: safeCastNetwork is used in nested enrichment (network.leases).
// A panic or wrong-type handling here would crash the enricher on any VM with
// a resolved network.

func TestSafeCastNetwork(t *testing.T) {
	testNet := &model.NetworkItem{ID: "net-1", Name: "test-net"}

	tests := map[string]struct {
		val     any
		want    *model.NetworkItem
		wantErr string
	}{
		"nil_returns_nil_nil": {
			val: nil, want: nil, wantErr: "",
		},
		"network_returns_it": {
			val: testNet, want: testNet, wantErr: "",
		},
		"string_returns_error": {
			val: "not a network", want: nil, wantErr: "unexpected network type",
		},
		"int_returns_error": {
			val: 42, want: nil, wantErr: "unexpected network type",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := safeCastNetwork(tc.val)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("safeCastNetwork() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- collectUniqueVMStrings ---
// Rationale: collectUniqueVMStrings extracts and deduplicates string fields
// from VM instances. If it misses duplicates, the enricher makes redundant
// repository calls. If it includes empty strings, the enricher looks up
// empty IDs.

func TestCollectUniqueVMStrings(t *testing.T) {
	tests := map[string]struct {
		vms  []*model.VMItem
		fn   func(*model.VMItem) string
		want []string
	}{
		"empty_slice": {
			vms:  []*model.VMItem{},
			fn:   func(vm *model.VMItem) string { return vm.ID },
			want: nil,
		},
		"all_unique": {
			vms:  []*model.VMItem{{ID: "vm-1"}, {ID: "vm-2"}},
			fn:   func(vm *model.VMItem) string { return vm.ID },
			want: []string{"vm-1", "vm-2"},
		},
		"duplicates_deduplicated": {
			vms:  []*model.VMItem{{ID: "vm-1"}, {ID: "vm-1"}, {ID: "vm-2"}},
			fn:   func(vm *model.VMItem) string { return vm.ID },
			want: []string{"vm-1", "vm-2"},
		},
		"empty_strings_filtered": {
			vms:  []*model.VMItem{{ID: "vm-1"}, {ID: ""}, {ID: "vm-2"}},
			fn:   func(vm *model.VMItem) string { return vm.ID },
			want: []string{"vm-1", "vm-2"},
		},
		"all_empty_filtered": {
			vms:  []*model.VMItem{{ID: ""}, {ID: ""}},
			fn:   func(vm *model.VMItem) string { return vm.ID },
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := collectUniqueVMStrings(tc.vms, tc.fn)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("collectUniqueVMStrings() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- extractNetworkIDs ---
// Rationale: extractNetworkIDs feeds network enrichment (leases, VMs). Nil
// entries, empty IDs, or duplicates would cause wasted lookups or nil-deref.

func TestExtractNetworkIDs(t *testing.T) {
	tests := map[string]struct {
		nets []*model.NetworkItem
		want []string
	}{
		"empty_slice": {
			nets: []*model.NetworkItem{},
			want: nil,
		},
		"all_unique": {
			nets: []*model.NetworkItem{{ID: "net-1"}, {ID: "net-2"}},
			want: []string{"net-1", "net-2"},
		},
		"nil_entries_skipped": {
			nets: []*model.NetworkItem{nil, {ID: "net-1"}, nil},
			want: []string{"net-1"},
		},
		"empty_id_skipped": {
			nets: []*model.NetworkItem{{ID: ""}, {ID: "net-1"}},
			want: []string{"net-1"},
		},
		"duplicates_deduplicated": {
			nets: []*model.NetworkItem{{ID: "net-1"}, {ID: "net-1"}},
			want: []string{"net-1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := extractNetworkIDs(tc.nets)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("extractNetworkIDs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- collectImageIDs (representative collect*ID helper ---
// Rationale: collectImageIDs, collectKernelIDs, collectBinaryIDs, and
// collectVolumeIDs are structurally identical (nil-skip, empty-skip, dedup).
// Testing one representative validates the pattern for all four.

func TestCollectImageIDs(t *testing.T) {
	tests := map[string]struct {
		images []*model.ImageItem
		want   []string
	}{
		"empty_slice": {
			images: []*model.ImageItem{},
			want:   nil,
		},
		"all_unique": {
			images: []*model.ImageItem{{ID: "img-1"}, {ID: "img-2"}},
			want:   []string{"img-1", "img-2"},
		},
		"nil_entries_skipped": {
			images: []*model.ImageItem{nil, {ID: "img-1"}, nil},
			want:   []string{"img-1"},
		},
		"empty_id_skipped": {
			images: []*model.ImageItem{{ID: ""}, {ID: "img-1"}},
			want:   []string{"img-1"},
		},
		"duplicates_deduplicated": {
			images: []*model.ImageItem{{ID: "img-1"}, {ID: "img-1"}},
			want:   []string{"img-1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := collectImageIDs(tc.images)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("collectImageIDs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- collectKernelIDs ---
// Rationale: Same pattern as collectImageIDs — validates the structural
// clone for KernelItem slices.

func TestCollectKernelIDs(t *testing.T) {
	tests := map[string]struct {
		kernels []*model.KernelItem
		want    []string
	}{
		"empty_slice": {
			kernels: []*model.KernelItem{},
			want:    nil,
		},
		"nil_entries_skipped": {
			kernels: []*model.KernelItem{nil, {ID: "k-1"}, nil},
			want:    []string{"k-1"},
		},
		"duplicates_deduplicated": {
			kernels: []*model.KernelItem{{ID: "k-1"}, {ID: "k-1"}},
			want:    []string{"k-1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := collectKernelIDs(tc.kernels)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("collectKernelIDs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- collectBinaryIDs ---
// Rationale: Same pattern as collectImageIDs — validates the structural
// clone for BinaryItem slices.

func TestCollectBinaryIDs(t *testing.T) {
	tests := map[string]struct {
		binaries []*model.BinaryItem
		want     []string
	}{
		"empty_slice": {
			binaries: []*model.BinaryItem{},
			want:     nil,
		},
		"nil_entries_skipped": {
			binaries: []*model.BinaryItem{nil, {ID: "b-1"}, nil},
			want:     []string{"b-1"},
		},
		"duplicates_deduplicated": {
			binaries: []*model.BinaryItem{{ID: "b-1"}, {ID: "b-1"}},
			want:     []string{"b-1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := collectBinaryIDs(tc.binaries)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("collectBinaryIDs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- collectVolumeIDs ---
// Rationale: Same pattern as collectImageIDs — validates the structural
// clone for VolumeItem slices.

func TestCollectVolumeIDs(t *testing.T) {
	tests := map[string]struct {
		volumes []*model.VolumeItem
		want    []string
	}{
		"empty_slice": {
			volumes: []*model.VolumeItem{},
			want:    nil,
		},
		"nil_entries_skipped": {
			volumes: []*model.VolumeItem{nil, {ID: "vol-1"}, nil},
			want:    []string{"vol-1"},
		},
		"duplicates_deduplicated": {
			volumes: []*model.VolumeItem{{ID: "vol-1"}, {ID: "vol-1"}},
			want:    []string{"vol-1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := collectVolumeIDs(tc.volumes)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("collectVolumeIDs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- sortByDotCount (edge cases missing from batch_tes ---
// Rationale: sortByDotCount uses SliceStable. Equal-dot-count paths must
// preserve their relative input order. This is critical for enrichment
// ordering within the same depth level.

func TestSortByDotCount_EqualDotCount(t *testing.T) {
	tests := map[string]struct {
		input []string
		want  []string
	}{
		"equal_dot_count_preserves_order": {
			input: []string{"vm", "kernel", "image"},
			want:  []string{"vm", "kernel", "image"},
		},
		"all_one_dot_preserves_order": {
			input: []string{"a.b", "c.d", "e.f"},
			want:  []string{"a.b", "c.d", "e.f"},
		},
		"empty": {
			input: []string{},
			want:  []string{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := sortByDotCount(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("sortByDotCount() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- EnrichVM: Vsock ---
// Rationale: Vsock enrichment uses a reverse relation lookup (VsockConfigItem
// references VM by VmID). If the batch resolution or assignment is wrong, VMs
// get incorrect or missing vsock configurations.

func TestEnrichVM_Vsock(t *testing.T) {
	vsockRepo := testutil.NewVsockRepo()
	require.NoError(t, vsockRepo.Upsert(ctx, &model.VsockConfigItem{
		ID: "vsock-1", VmID: "vm-1",
		GuestCID: 3, UDSPath: "/tmp/vm-1.sock", Port: 1024, Token: "tok",
	}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), vsockRepo,
	)

	vms := []*model.VMItem{{ID: "vm-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "vsock"))

	require.NotNil(t, vms[0].Vsock)
	assert.Equal(t, 3, vms[0].Vsock.GuestCID)
	assert.Equal(t, "tok", vms[0].Vsock.Token)
}

// --- EnrichVM: Vsock, no configs ---
// Rationale: When no vsock config exists for any VM, enrichment must succeed
// as a soft-fail (no error). All Vsock fields remain nil.

func TestEnrichVM_Vsock_NoConfigs(t *testing.T) {
	e := newEnricher()

	vms := []*model.VMItem{{ID: "vm-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "vsock"))
	assert.Nil(t, vms[0].Vsock, "Vsock must remain nil when no config exists")
}

// --- EnrichVM: Vsock, mixed VMs ---
// Rationale: When some VMs have vsock configs and some don't, enrichment must
// correctly assign configs only to the VMs that have them, leaving others nil.

func TestEnrichVM_Vsock_Mixed(t *testing.T) {
	vsockRepo := testutil.NewVsockRepo()
	require.NoError(t, vsockRepo.Upsert(ctx, &model.VsockConfigItem{
		ID: "vsock-1", VmID: "vm-1",
		GuestCID: 3, UDSPath: "/tmp/vm-1.sock", Port: 1024, Token: "tok1",
	}))

	e := New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(), vsockRepo,
	)

	vms := []*model.VMItem{
		{ID: "vm-1"},
		{ID: "vm-2"},
	}
	require.NoError(t, e.EnrichVM(ctx, vms, "vsock"))

	require.NotNil(t, vms[0].Vsock, "vm-1 must have vsock config")
	assert.Equal(t, "tok1", vms[0].Vsock.Token)
	assert.Nil(t, vms[1].Vsock, "vm-2 must have nil vsock config")
}
