package network

import "context"

// AssociateFloatingIP requests persistent operator ownership of an unowned resource.
func (client *FloatingIPClient) AssociateFloatingIP(
	ctx context.Context,
	request FloatingIPAssociateRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	result, err := exchangeFloatingIP[FloatingIPAssociateRequest, FloatingIPResult](
		ctx,
		client,
		FloatingIPAssociateAction,
		request,
	)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := validateFloatingIPOperatorResult(result, request.ID, request.NetworkID, request.VMID, request.ExpectedGeneration); err != nil {
		return FloatingIPResult{}, floatingIPResponseError(true, true, err)
	}
	return result, nil
}

// TransferFloatingIP explicitly replaces an existing owner.
func (client *FloatingIPClient) TransferFloatingIP(
	ctx context.Context,
	request FloatingIPTransferRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	result, err := exchangeFloatingIP[FloatingIPTransferRequest, FloatingIPResult](
		ctx,
		client,
		FloatingIPTransferAction,
		request,
	)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := validateFloatingIPOperatorResult(result, request.ID, request.NetworkID, request.VMID, request.ExpectedGeneration); err != nil {
		return FloatingIPResult{}, floatingIPResponseError(true, true, err)
	}
	return result, nil
}

// ReleaseFloatingIP revokes exact current root ownership.
func (client *FloatingIPClient) ReleaseFloatingIP(
	ctx context.Context,
	request FloatingIPReleaseRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	result, err := exchangeFloatingIP[FloatingIPReleaseRequest, FloatingIPResult](
		ctx,
		client,
		FloatingIPReleaseAction,
		request,
	)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := validateFloatingIPOperatorResult(result, request.ID, request.NetworkID, "", request.ExpectedGeneration); err != nil {
		return FloatingIPResult{}, floatingIPResponseError(true, true, err)
	}
	return result, nil
}

func validateFloatingIPOperatorResult(result FloatingIPResult, id, networkID, vmID string, generation uint64) error {
	if err := result.validate(networkID); err != nil {
		return err
	}
	if result.ID != id || result.Retired || generation == ^uint64(0) || result.Generation != generation+1 ||
		result.OwnerVMID != vmID {
		return networkAuthorityValidationError("floating-IP ownership acknowledgement differs from request")
	}
	if vmID != "" &&
		(result.OwnerOrigin != "operator" || result.Lifetime != "persistent" || result.AuthorityState != "active" || result.AliasSynchronization != "synchronized") {
		return networkAuthorityValidationError("floating-IP ownership acknowledgement is incomplete")
	}
	if vmID == "" && (result.Phase != "unassociated" || result.AuthorityState != "absent") {
		return networkAuthorityValidationError("floating-IP release acknowledgement retains authority")
	}
	return nil
}
