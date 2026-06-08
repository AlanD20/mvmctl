package enricher_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/enricher"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

var ctx = context.Background()

func newEnricher() *enricher.Enricher {
	return enricher.New(
		testutil.NewVMRepo(),
		testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(),
		testutil.NewImageRepo(),
		testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(),
	)
}

// ─── EnrichVM: Forward relations ────────────────────────────────────────────
// Rationale: EnrichVM resolves kernel, image, binary, and network via
// forward FK lookups. Each must correctly batch-resolve and assign.

func TestEnrichVM_Kernel(t *testing.T) {
	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{ID: "vm-1", KernelID: "k-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))

	require.NotNil(t, vms[0].Kernel)
	assert.Equal(t, "6.1", vms[0].Kernel.Version)
}

func TestEnrichVM_Image(t *testing.T) {
	img := testutil.NewImageRepo()
	require.NoError(t, img.Upsert(ctx, &model.ImageItem{ID: "img-1", Name: "alpine-3.21"}))

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), img,
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(),
		testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{ID: "vm-1", ImageID: "img-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "image"))

	require.NotNil(t, vms[0].Image)
	assert.Equal(t, "alpine-3.21", vms[0].Image.Name)
}

func TestEnrichVM_Binary(t *testing.T) {
	bin := testutil.NewBinaryRepo()
	require.NoError(t, bin.Upsert(ctx, &model.BinaryItem{ID: "b-1", Version: "1.15", IsPresent: true}))

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), bin, testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{ID: "vm-1", BinaryID: "b-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "binary"))

	require.NotNil(t, vms[0].Binary)
	assert.Equal(t, "1.15", vms[0].Binary.Version)
}

func TestEnrichVM_Network(t *testing.T) {
	net := testutil.NewNetworkRepo()
	require.NoError(t, net.Upsert(ctx, &model.Network{ID: "net-1", Name: "default", IsPresent: true}))

	e := enricher.New(
		testutil.NewVMRepo(), net, testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{ID: "vm-1", NetworkID: "net-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "network"))

	require.NotNil(t, vms[0].Network)
	assert.Equal(t, "default", vms[0].Network.Name)
}

// ─── EnrichVM: All forward relations combined ───────────────────────────────
// Rationale: Enrichment with multiple paths must resolve all correctly.

func TestEnrichVM_AllForward(t *testing.T) {
	krn := testutil.NewKernelRepo()
	img := testutil.NewImageRepo()
	bin := testutil.NewBinaryRepo()
	net := testutil.NewNetworkRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", IsPresent: true}))
	require.NoError(t, img.Upsert(ctx, &model.ImageItem{ID: "i-1", IsPresent: true}))
	require.NoError(t, bin.Upsert(ctx, &model.BinaryItem{ID: "b-1", IsPresent: true}))
	require.NoError(t, net.Upsert(ctx, &model.Network{ID: "n-1", IsPresent: true}))

	e := enricher.New(
		testutil.NewVMRepo(), net, testutil.NewLeaseRepo(),
		img, krn, bin, testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{
		ID: "vm-1", KernelID: "k-1", ImageID: "i-1",
		BinaryID: "b-1", NetworkID: "n-1",
	}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel", "image", "binary", "network"))

	assert.NotNil(t, vms[0].Kernel, "kernel")
	assert.NotNil(t, vms[0].Image, "image")
	assert.NotNil(t, vms[0].Binary, "binary")
	assert.NotNil(t, vms[0].Network, "network")
}

// ─── EnrichVM: Missing FK ───────────────────────────────────────────────────
// Rationale: When a VM references a nonexistent kernel/image/binary,
// enrichment should not error — the field remains nil (soft-fail).

func TestEnrichVM_MissingFK_leavesNil(t *testing.T) {
	e := newEnricher()
	vms := []*model.VM{{ID: "vm-1", KernelID: "nonexistent"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))
	assert.Nil(t, vms[0].Kernel)
}

func TestEnrichVM_MissingNetwork_leavesNil(t *testing.T) {
	e := newEnricher()
	vms := []*model.VM{{ID: "vm-1", NetworkID: "nonexistent"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "network"))
	assert.Nil(t, vms[0].Network)
}

// ─── EnrichVM: Empty / nil input ────────────────────────────────────────────
// Rationale: Empty input must not error or panic.

func TestEnrichVM_EmptyInput(t *testing.T) {
	e := newEnricher()
	require.NoError(t, e.EnrichVM(ctx, nil, "kernel"))
	require.NoError(t, e.EnrichVM(ctx, []*model.VM{}, "kernel"))
}

// ─── EnrichVM: Volumes ──────────────────────────────────────────────────────
// Rationale: Volume enrichment uses JSON-array-to-list resolution.
// Must correctly match each VM to its volumes.

func TestEnrichVM_Volumes(t *testing.T) {
	vol := testutil.NewVolumeRepo()
	require.NoError(t, vol.Upsert(ctx, &model.VolumeItem{ID: "vol-1", Name: "data"}))
	require.NoError(t, vol.Upsert(ctx, &model.VolumeItem{ID: "vol-2", Name: "log"}))

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		testutil.NewKernelRepo(), testutil.NewBinaryRepo(), vol,
	)

	vms := []*model.VM{
		{ID: "vm-1", VolumeIDs: []string{"vol-1", "vol-2"}},
		{ID: "vm-2", VolumeIDs: []string{"vol-1"}},
	}
	require.NoError(t, e.EnrichVM(ctx, vms, "volumes"))

	assert.Len(t, vms[0].Volumes, 2)
	assert.Len(t, vms[1].Volumes, 1)
}

func TestEnrichVM_Volumes_noneAssigned(t *testing.T) {
	e := newEnricher()
	vms := []*model.VM{{ID: "vm-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "volumes"))
	assert.Nil(t, vms[0].Volumes)
}

// ─── EnrichVM: Network Leases (nested) ──────────────────────────────────────
// Rationale: network.leases is a nested relation. "network" must resolve
// before "network.leases" — the sortByDotCount helper ensures this.

func TestEnrichVM_NetworkLeases(t *testing.T) {
	net := testutil.NewNetworkRepo()
	lease := testutil.NewLeaseRepo()
	require.NoError(t, net.Upsert(ctx, &model.Network{ID: "net-1", Name: "default", IsPresent: true}))
	lease.SetNetwork(10, true)
	_, err := lease.Acquire(ctx, "net-1", "vm-1", nil)
	require.NoError(t, err)

	e := enricher.New(
		testutil.NewVMRepo(), net, lease,
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{ID: "vm-1", NetworkID: "net-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "network", "network.leases"))

	require.NotNil(t, vms[0].Network)
	require.NotNil(t, vms[0].Network.Leases)
	assert.Len(t, vms[0].Network.Leases, 1)
}

// ─── EnrichNetwork ──────────────────────────────────────────────────────────
// Rationale: Network enrichment resolves leases and referencing VMs.

func TestEnrichNetwork_Leases(t *testing.T) {
	lease := testutil.NewLeaseRepo()
	lease.SetNetwork(10, true)
	_, err := lease.Acquire(ctx, "net-1", "vm-1", nil)
	require.NoError(t, err)

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(), lease,
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	nets := []*model.Network{{ID: "net-1", Name: "test"}}
	require.NoError(t, e.EnrichNetwork(ctx, nets, "leases"))
	require.NotNil(t, nets[0].Leases)
	assert.Len(t, nets[0].Leases, 1)
}

func TestEnrichNetwork_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VM{ID: "vm-1", NetworkID: "net-1"}))

	e := enricher.New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	nets := []*model.Network{{ID: "net-1", Name: "test"}}
	require.NoError(t, e.EnrichNetwork(ctx, nets, "vm"))
	require.NotNil(t, nets[0].VMs)
	assert.Len(t, nets[0].VMs, 1)
}

// ─── EnrichImage ────────────────────────────────────────────────────────────
// Rationale: Image enrichment resolves VMs referencing each image.

func TestEnrichImage_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VM{ID: "vm-1", ImageID: "img-1"}))

	e := enricher.New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	images := []*model.ImageItem{{ID: "img-1", Name: "alpine"}}
	require.NoError(t, e.EnrichImage(ctx, images, "vm"))

	require.NotNil(t, images[0].VMs)
	assert.Len(t, images[0].VMs, 1)
	assert.Equal(t, "vm-1", images[0].VMs[0].ID)
}

// ─── EnrichKernel ───────────────────────────────────────────────────────────
// Rationale: Kernel enrichment resolves VMs referencing each kernel.

func TestEnrichKernel_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VM{ID: "vm-1", KernelID: "k-1"}))

	e := enricher.New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	kernels := []*model.KernelItem{{ID: "k-1", Version: "6.1"}}
	require.NoError(t, e.EnrichKernel(ctx, kernels, "vm"))

	require.NotNil(t, kernels[0].VMs)
	assert.Len(t, kernels[0].VMs, 1)
}

// ─── EnrichBinary ───────────────────────────────────────────────────────────
// Rationale: Binary enrichment resolves VMs referencing each binary.

func TestEnrichBinary_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VM{ID: "vm-1", BinaryID: "b-1"}))

	e := enricher.New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	binaries := []*model.BinaryItem{{ID: "b-1", Version: "1.15"}}
	require.NoError(t, e.EnrichBinary(ctx, binaries, "vm"))

	require.NotNil(t, binaries[0].VMs)
	assert.Len(t, binaries[0].VMs, 1)
}

// ─── EnrichVolume ───────────────────────────────────────────────────────────
// Rationale: Volume enrichment resolves VMs referencing each volume.

func TestEnrichVolume_VMs(t *testing.T) {
	vm := testutil.NewVMRepo()
	require.NoError(t, vm.Upsert(ctx, &model.VM{ID: "vm-1", VolumeIDs: []string{"vol-1"}}))

	e := enricher.New(
		vm, testutil.NewNetworkRepo(), testutil.NewLeaseRepo(),
		testutil.NewImageRepo(), testutil.NewKernelRepo(),
		testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	vols := []*model.VolumeItem{{ID: "vol-1", Name: "data"}}
	require.NoError(t, e.EnrichVolume(ctx, vols, "vm"))

	require.NotNil(t, vols[0].VMs)
	assert.Len(t, vols[0].VMs, 1)
}

// ─── Error handling: invalid include path ───────────────────────────────────
// Rationale: Enrichment with an unknown path must error with available options.

func TestEnrichVM_unknownPath(t *testing.T) {
	e := newEnricher()
	vms := []*model.VM{{ID: "vm-1"}}
	err := e.EnrichVM(ctx, vms, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unknown relation")
	assert.Contains(t, err.Error(), "kernel") // available options in error
}

func TestEnrichVM_emptyInclude(t *testing.T) {
	e := newEnricher()
	vms := []*model.VM{{ID: "vm-1"}}
	err := e.EnrichVM(ctx, vms)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "include list is required")
}

// ─── Duplicate enrichment ───────────────────────────────────────────────────
// Rationale: Enriching the same relation twice must not duplicate data.

func TestEnrichVM_DuplicateCall(t *testing.T) {
	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	vms := []*model.VM{{ID: "vm-1", KernelID: "k-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))
	require.NoError(t, e.EnrichVM(ctx, vms, "kernel"))

	assert.NotNil(t, vms[0].Kernel)
}

// ─── Concurrent enrichment ──────────────────────────────────────────────────
// Rationale: Enrichment is called from concurrent contexts (batch resolve).
// No data races or panics.

func TestEnrichVM_Concurrent(t *testing.T) {
	krn := testutil.NewKernelRepo()
	require.NoError(t, krn.Upsert(ctx, &model.KernelItem{ID: "k-1", Version: "6.1", IsPresent: true}))

	e := enricher.New(
		testutil.NewVMRepo(), testutil.NewNetworkRepo(),
		testutil.NewLeaseRepo(), testutil.NewImageRepo(),
		krn, testutil.NewBinaryRepo(), testutil.NewVolumeRepo(),
	)

	done := make(chan struct{})
	go func() {
		vms := []*model.VM{{ID: "vm-1", KernelID: "k-1"}}
		_ = e.EnrichVM(ctx, vms, "kernel")
		done <- struct{}{}
	}()
	vms2 := []*model.VM{{ID: "vm-2", KernelID: "k-1"}}
	require.NoError(t, e.EnrichVM(ctx, vms2, "kernel"))
	<-done
}
