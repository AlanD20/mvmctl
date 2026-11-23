package network

import (
	"context"
	"errors"

	"mvmctl/pkg/errs"
)

// FloatingIPListRequest selects one owned network's floating-IP inventory.
type FloatingIPListRequest struct {
	NetworkID string `json:"network_id"`
	AfterID   string `json:"after_id"`
}

// FloatingIPPage fits the fixed privileged response bound without limiting resource count.
type FloatingIPPage struct {
	Items  []FloatingIPResult `json:"items"`
	NextID string             `json:"next_id"`
}

// At the maximum field lengths, 64 records fit comfortably within the 64-KiB privileged frame.
const floatingIPPageSize = 64

// Validate requires an exact network identity, not a name or prefix.
func (request FloatingIPListRequest) Validate() error {
	if !networkIDPattern.MatchString(request.NetworkID) ||
		(request.AfterID != "" && !vmIDPattern.MatchString(request.AfterID)) {
		return networkAuthorityValidationError("floating-IP inventory requires a complete network identity")
	}
	return nil
}

// FloatingIPGetRequest identifies an exact resource, including a retained retirement record.
type FloatingIPGetRequest struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
}

// Validate rejects incomplete resource and network identities.
func (request FloatingIPGetRequest) Validate() error {
	if !vmIDPattern.MatchString(request.ID) || !networkIDPattern.MatchString(request.NetworkID) {
		return networkAuthorityValidationError(
			"floating-IP inspection requires complete resource and network identities",
		)
	}
	return nil
}

// ListFloatingIPs returns live logical reservations even when the network needs reboot recovery.
// Listing does not activate a network, authorize guest packets or infer a live lease.
func (server *FloatingIPServer) ListFloatingIPs(
	ctx context.Context, ownerUID uint32, request FloatingIPListRequest,
) (FloatingIPPage, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPPage{}, err
	}
	records, err := readFloatingIPNetwork(ctx, ownerUID, request.NetworkID)
	if err != nil {
		return FloatingIPPage{}, err
	}
	return floatingIPRecordPage(records, request.AfterID), nil
}

func floatingIPRecordPage(records []floatingIPRecord, afterID string) FloatingIPPage {
	page := FloatingIPPage{Items: make([]FloatingIPResult, 0, min(len(records), floatingIPPageSize))}
	for _, record := range records {
		if record.Retired || record.ID <= afterID {
			continue
		}
		if len(page.Items) == floatingIPPageSize {
			page.NextID = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, record.result())
	}
	return page
}

// GetFloatingIP observes root authority without relying on a caller-controlled cache record.
func (server *FloatingIPServer) GetFloatingIP(
	ctx context.Context, ownerUID uint32, request FloatingIPGetRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID))
	if err != nil {
		return FloatingIPResult{}, err
	}
	var result FloatingIPResult
	result, err = getObservedFloatingIP(ctx, tx, request)
	return result, tx.release(ctx, err)
}

func getObservedFloatingIP(
	ctx context.Context,
	tx *managedNetworkTransaction,
	request FloatingIPGetRequest,
) (_ FloatingIPResult, returnErr error) {
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
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
	result := record.result()
	if record.Retired || tx.priorBoot != nil || record.Phase != floatingIPOwned {
		return result, nil
	}
	if err := admitFloatingIPNetwork(ctx, tx); err != nil {
		return FloatingIPResult{}, err
	}
	observed, err := tx.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
	if err != nil {
		return FloatingIPResult{}, err
	}
	return observedFloatingIPResult(record, observed)
}

func readFloatingIPNetwork(
	ctx context.Context, ownerUID uint32, networkID string,
) (_ []floatingIPRecord, returnErr error) {
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(networkID))
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if !tx.found || tx.cancelled {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, networkID)
	}
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close floating-IP inventory",
			store.release(context.WithoutCancel(ctx)),
		)
	}()
	records, err := store.list(ctx)
	if err != nil {
		return nil, err
	}
	var selected []floatingIPRecord
	for _, record := range records {
		if record.NetworkID == networkID {
			selected = append(selected, record)
		}
	}
	return selected, nil
}
