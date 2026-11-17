package network

import (
	"context"
	"net/netip"

	"mvmctl/pkg/errs"
)

// FloatingIPServer owns durable address reservations under managed-network mutation authority.
// It is not a guest RPC endpoint; ownerUID comes from the authenticated privileged-dispatch peer.
type FloatingIPServer struct{}

// NewFloatingIPServer constructs the root-side resource service.
func NewFloatingIPServer() *FloatingIPServer { return &FloatingIPServer{} }

// CreateFloatingIP reserves an address before projecting any operator-owned metadata.
func (server *FloatingIPServer) CreateFloatingIP(
	ctx context.Context, ownerUID uint32, request FloatingIPCreateRequest,
) (_ FloatingIPResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID))
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if err := admitFloatingIPNetwork(ctx, tx); err != nil {
		return FloatingIPResult{}, err
	}
	store, err := openFloatingIPStore(ctx, tx.store, true)
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"release floating-IP store",
			store.release(context.WithoutCancel(ctx)),
		)
	}()
	records, err := store.list(ctx)
	if err != nil {
		return FloatingIPResult{}, err
	}
	reserved, err := primaryIPv4Reservations(ctx, tx.store, tx.key.networkID)
	if err != nil {
		return FloatingIPResult{}, err
	}
	stopped, err := tx.firewall.ReservedNetworkIPv4(ctx)
	if err != nil {
		return FloatingIPResult{}, err
	}
	reserved = append(reserved, stopped...)
	var existing *floatingIPRecord
	for _, record := range records {
		if record.ID == request.ID {
			if record.Retired || record.NetworkID != request.NetworkID || record.Name != request.Name ||
				(request.IPv4 != "" && record.IPv4 != request.IPv4) {
				return FloatingIPResult{}, errs.New(
					errs.CodeNetworkAlreadyExists,
					"floating-IP identity is retired or has different intent",
				)
			}
			existing = &record
			continue
		}
		if record.Retired {
			continue
		}
		if record.Name == request.Name {
			return FloatingIPResult{}, errs.New(errs.CodeNetworkAlreadyExists, "floating-IP name is already reserved")
		}
		if record.NetworkID == request.NetworkID {
			reserved = append(reserved, netip.MustParseAddr(record.IPv4))
		}
	}
	if existing != nil {
		if _, err := selectFloatingIPv4(tx.record.configuration.l3, existing.IPv4, reserved); err != nil {
			return FloatingIPResult{}, err
		}
		return existing.result(), nil
	}
	address, err := selectFloatingIPv4(tx.record.configuration.l3, request.IPv4, reserved)
	if err != nil {
		return FloatingIPResult{}, err
	}
	record := floatingIPRecord{
		Schema: floatingIPRecordSchema, OwnerUID: ownerUID, ID: request.ID, NetworkID: request.NetworkID,
		Name: request.Name, IPv4: address.value.String(), Generation: 1,
	}
	if err := store.persist(ctx, record, nil); err != nil {
		return FloatingIPResult{}, err
	}
	return record.result(), nil
}

// RemoveFloatingIP retires a reservation. A retained tombstone prevents stale create replay.
func (server *FloatingIPServer) RemoveFloatingIP(
	ctx context.Context, ownerUID uint32, request FloatingIPRemoveRequest,
) (_ FloatingIPResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID))
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if err := admitFloatingIPRetirement(ctx, tx); err != nil {
		return FloatingIPResult{}, err
	}
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"release floating-IP store",
			store.release(context.WithoutCancel(ctx)),
		)
	}()
	if store == nil {
		return FloatingIPResult{}, errs.NotFound(errs.CodeNetworkNotFound, request.ID)
	}
	record, _, _, found, err := store.read(ctx, request.ID)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if !found || record.NetworkID != request.NetworkID {
		return FloatingIPResult{}, errs.NotFound(errs.CodeNetworkNotFound, request.ID)
	}
	if record.Retired {
		if request.ExpectedGeneration != record.Generation &&
			(request.ExpectedGeneration == ^uint64(0) || request.ExpectedGeneration+1 != record.Generation) {
			return FloatingIPResult{}, networkAuthorityValidationError("floating-IP removal generation is stale")
		}
		return record.result(), nil
	}
	if record.Generation != request.ExpectedGeneration || record.Generation == ^uint64(0) {
		return FloatingIPResult{}, networkAuthorityValidationError(
			"floating-IP removal generation is stale or exhausted",
		)
	}
	previous := record
	record.Retired = true
	record.Generation++
	if err := store.persist(ctx, record, &previous); err != nil {
		return FloatingIPResult{}, err
	}
	return record.result(), nil
}

func admitFloatingIPRetirement(ctx context.Context, tx *managedNetworkTransaction) error {
	if tx.found && tx.priorBoot != nil && !tx.removed && !tx.cancelled {
		// Schema 1 stores reservations only: it cannot authorize packets or retain a guest owner.
		// The retained index/network locks suffice for logical retirement after reboot. Recreating
		// a bridge or firewall solely to remove this reservation would obstruct clean teardown.
		return nil
	}
	return admitFloatingIPNetwork(ctx, tx)
}

func admitFloatingIPNetwork(ctx context.Context, tx *managedNetworkTransaction) error {
	if !tx.found || tx.removed || tx.cancelled {
		return errs.NotFound(errs.CodeNetworkNotFound, string(tx.key.networkID))
	}
	if tx.priorBoot != nil {
		return staleNetworkAuthorityError(tx.key.networkID)
	}
	if _, err := tx.inspectBridge(ctx, false); err != nil {
		return err
	}
	return tx.beginFirewall(ctx)
}
