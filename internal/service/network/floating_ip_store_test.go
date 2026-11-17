package network

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFloatingIPStoreRetirementSurvivesReopenAndRefusesResurrection(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	record := floatingIPContractRecord()
	require.NoError(t, store.persist(t.Context(), record, nil))
	duplicate := record
	duplicate.Name = "different-intent"
	require.Error(t, store.persist(t.Context(), duplicate, nil))
	retired := record
	retired.Retired = true
	retired.Generation = 2
	require.NoError(t, store.persist(t.Context(), retired, &record))

	reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.release(context.Background())) })
	got, _, _, found, err := reopened.read(t.Context(), record.ID)
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(retired, got); diff != "" {
		t.Fatalf("retained retirement (-want +got):\n%s", diff)
	}
	require.Error(t, reopened.persist(t.Context(), record, nil), "an old create must not resurrect a retired identity")
	require.NoError(t, requireRetiredFloatingIPs(t.Context(), fixture.uid))
	info, err := os.Stat(filepath.Join(filepath.Dir(fixture.recordPath), floatingIPDirectory, record.ID+".json"))
	require.NoError(t, err)
	if info.Mode().Perm() != 0600 {
		t.Errorf("record mode = %o, want 600", info.Mode().Perm())
	}
}

func TestFloatingIPStoreRejectsStaleAndReboundReplacements(t *testing.T) {
	cases := map[string]func(*floatingIPRecord){
		"stale generation":   func(r *floatingIPRecord) { r.Generation = 1 },
		"skipped generation": func(r *floatingIPRecord) { r.Generation = 3 },
		"different network":  func(r *floatingIPRecord) { r.NetworkID = strings.Repeat("c", 64) },
		"different address":  func(r *floatingIPRecord) { r.IPv4 = "10.10.0.101" },
		"different name":     func(r *floatingIPRecord) { r.Name = "replacement" },
		"different owner":    func(r *floatingIPRecord) { r.OwnerUID++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, store := floatingIPStoreFixture(t)
			record := floatingIPContractRecord()
			require.NoError(t, store.persist(t.Context(), record, nil))
			replacement := record
			replacement.Generation = 2
			replacement.Retired = true
			mutate(&replacement)
			require.Error(t, store.persist(t.Context(), replacement, &record))
			got, _, _, found, err := store.read(t.Context(), record.ID)
			require.NoError(t, err)
			require.True(t, found)
			if diff := cmp.Diff(record, got); diff != "" {
				t.Fatalf("rejected replacement altered authority (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFloatingIPStoreRefusesAmbiguousInventoryAndResetWithLiveReservation(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	record := floatingIPContractRecord()
	require.NoError(t, store.persist(t.Context(), record, nil))
	require.Error(t, requireRetiredFloatingIPs(t.Context(), fixture.uid))
	addresses, err := floatingIPv4Reservations(t.Context(), fixture.uid, NetworkID(record.NetworkID))
	require.NoError(t, err)
	if len(addresses) != 1 || addresses[0].String() != "10.10.0.100" {
		t.Fatalf("root reservations = %v, want [10.10.0.100]", addresses)
	}

	second := record
	second.ID = strings.Repeat("d", 32)
	second.Name = "duplicate-address"
	raw, err := json.Marshal(second)
	require.NoError(t, err)
	path := filepath.Join(filepath.Dir(fixture.recordPath), floatingIPDirectory, second.ID+".json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	_, err = store.list(t.Context())
	require.Error(t, err, "duplicate root address authority must not be projected as valid inventory")
}

func TestFloatingIPResetInventoryRefusesDisappearedDirectory(t *testing.T) {
	fixture := newNetworkStoreFixture(t)
	require.Error(t, requireRetiredFloatingIPs(t.Context(), fixture.uid),
		"a previously enumerated authority directory cannot silently become absent")
}

func TestFloatingIPStoreRejectsUnsafeFilesWithoutFollowingOrRepair(t *testing.T) {
	cases := []string{"symlink", "hardlink", "public mode", "pending file"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			fixture, store := floatingIPStoreFixture(t)
			directory := filepath.Join(filepath.Dir(fixture.recordPath), floatingIPDirectory)
			record := floatingIPContractRecord()
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			path := filepath.Join(directory, record.ID+".json")
			original := filepath.Join(t.TempDir(), "original")
			require.NoError(t, os.WriteFile(original, raw, 0600))
			switch name {
			case "symlink":
				require.NoError(t, os.Symlink(original, path))
			case "hardlink":
				require.NoError(t, os.Link(original, path))
			case "public mode":
				require.NoError(t, os.WriteFile(path, raw, 0644))
			case "pending file":
				require.NoError(t, os.WriteFile(filepath.Join(directory, ".unfinished"), raw, 0600))
			}
			_, err = store.list(t.Context())
			require.Error(t, err)
			preserved, err := os.ReadFile(original)
			require.NoError(t, err)
			if diff := cmp.Diff(raw, preserved); diff != "" {
				t.Fatalf("original file changed (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFloatingIPStorePreservesPublicationUncertainty(t *testing.T) {
	_, store := floatingIPStoreFixture(t)
	realSync := store.owner.deps.fsync
	store.owner.deps.fsync = func(ctx context.Context, fd int) error {
		if fd == store.owner.stateFD {
			return unix.EIO
		}
		return realSync(ctx, fd)
	}
	err := store.persist(t.Context(), floatingIPContractRecord(), nil)
	require.ErrorIs(t, err, unix.EIO)
	require.True(t, store.ambiguous)
	require.Error(
		t,
		store.persist(t.Context(), floatingIPContractRecord(), nil),
		"uncertain authority cannot accept another mutation",
	)
}

func TestFloatingIPStoreFailedWritePreservesOriginalAndCleansTemporary(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	record := floatingIPContractRecord()
	require.NoError(t, store.persist(t.Context(), record, nil))
	observation := installNetworkStoreWriteFault(t, store.owner, "write", unix.EIO)
	retired := record
	retired.Generation = 2
	retired.Retired = true
	require.ErrorIs(t, store.persist(t.Context(), retired, &record), unix.EIO)
	// Capture before readback can reuse the closed descriptor number for the original record.
	closed := observation.tempCloseCalls.Load()
	got, _, _, found, err := store.read(t.Context(), record.ID)
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(record, got); diff != "" {
		t.Fatalf("failed write altered original (-want +got):\n%s", diff)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(fixture.recordPath), floatingIPDirectory))
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if diff := cmp.Diff([]string{record.ID + ".json"}, names); diff != "" {
		t.Fatalf("failed write left temporary state (-want +got):\n%s", diff)
	}
	if closed != 1 {
		t.Fatal("temporary descriptor was not closed exactly once")
	}
}

func TestFloatingIPReservationRetiresWithoutRecreatingPriorBootNetwork(t *testing.T) {
	tx := &managedNetworkTransaction{found: true, priorBoot: &networkAuthorityRecord{}}
	require.NoError(
		t,
		admitFloatingIPRetirement(t.Context(), tx),
		"reservation-only retirement requires no kernel recreation",
	)
	fixture, store := floatingIPStoreFixture(t)
	record := floatingIPContractRecord()
	require.NoError(t, store.persist(t.Context(), record, nil))
	retired := record
	retired.Retired, retired.Generation = true, 2
	require.NoError(t, store.persist(t.Context(), retired, &record))
	require.NoError(t, requireRetiredFloatingIPs(t.Context(), fixture.uid))
}

func TestFloatingIPStoreRefusesChangedReplacementEvidence(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	record := floatingIPContractRecord()
	require.NoError(t, store.persist(t.Context(), record, nil))
	foreign := record
	foreign.Name = "changed-authority"
	foreignRaw, err := json.Marshal(foreign)
	require.NoError(t, err)
	path := filepath.Join(filepath.Dir(fixture.recordPath), floatingIPDirectory, record.ID+".json")
	realSync := store.owner.deps.fsync
	changed := false
	store.owner.deps.fsync = func(ctx context.Context, fd int) error {
		if err := realSync(ctx, fd); err != nil {
			return err
		}
		if fd != store.owner.stateFD && !changed {
			changed = true
			return os.WriteFile(path, foreignRaw, 0600)
		}
		return nil
	}
	retired := record
	retired.Generation, retired.Retired = 2, true
	require.Error(t, store.persist(t.Context(), retired, &record))
	got, _, _, found, err := store.read(t.Context(), record.ID)
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(foreign, got); diff != "" {
		t.Fatalf("replacement overwrote changed authority (-want +got):\n%s", diff)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	if len(entries) != 1 || entries[0].Name() != record.ID+".json" {
		t.Fatalf("rejected replacement left unexpected entries: %v", entries)
	}
}

func TestFloatingIPReservationBlocksPrimaryAdmissionUntilRetired(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	record := floatingIPContractRecord()
	require.NoError(t, store.persist(t.Context(), record, nil))
	key, err := newTopologyKey(testOwnerUID, strings.Repeat("e", 32), testNetworkID)
	require.NoError(t, err)
	network, err := admitManagedNetworkL3(managedNetworkL3Input{prefix: netip.MustParsePrefix("10.10.0.0/24")})
	require.NoError(t, err)
	endpoint, err := deriveEndpointL3(key, network, endpointL3Input{guestAddress: netip.MustParseAddr("10.10.0.100")})
	require.NoError(t, err)
	require.Error(t, inspectOwnerEndpointReservations(t.Context(), fixture.uid, key, endpoint))

	retired := record
	retired.Retired = true
	retired.Generation = 2
	require.NoError(t, store.persist(t.Context(), retired, &record))
	require.NoError(t, inspectOwnerEndpointReservations(t.Context(), fixture.uid, key, endpoint))
}

func TestFloatingIPRecordRefusesNoncanonicalState(t *testing.T) {
	raw, err := encodeFloatingIPRecord(floatingIPContractRecord())
	require.NoError(t, err)
	cases := map[string][]byte{
		"duplicate generation": []byte(
			strings.Replace(string(raw), `"generation":1`, `"generation":2,"generation":1`, 1),
		),
		"null retired":    []byte(strings.Replace(string(raw), `"retired":false`, `"retired":null`, 1)),
		"missing retired": []byte(strings.Replace(string(raw), `,"retired":false`, ``, 1)),
		"unknown field": []byte(
			strings.Replace(string(raw), `"schema":1`, `"schema":1,"path":"/etc/passwd"`, 1),
		),
		"trailing value": append(append([]byte{}, raw...), []byte(`{}`)...),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeFloatingIPRecord(value)
			require.Error(t, err)
		})
	}
}

func floatingIPContractRecord() floatingIPRecord {
	return floatingIPRecord{
		Schema: 1, OwnerUID: testOwnerUID, ID: strings.Repeat("b", 32), NetworkID: testNetworkID,
		Name: "api", IPv4: "10.10.0.100", Generation: 1,
	}
}

func floatingIPStoreFixture(t *testing.T) (*networkStoreFixture, *floatingIPStore) {
	t.Helper()
	fixture := newNetworkStoreFixture(t)
	store, err := openFloatingIPStore(t.Context(), fixture.uid, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.release(context.Background())) })
	return fixture, store
}
