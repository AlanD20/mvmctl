package inputs

import (
	"context"
	"strconv"
	"strings"
	"time"

	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

// NetworkToVMTrafficPolicyCreateInput is the public input for one managed-network-to-VM IP traffic grant.
type NetworkToVMTrafficPolicyCreateInput struct {
	SourceNetwork   string `json:"source_network"   yaml:"source_network"`
	DestinationVM   string `json:"destination_vm"   yaml:"destination_vm"`
	Protocol        string `json:"protocol"         yaml:"protocol"`
	DestinationPort string `json:"destination_port" yaml:"destination_port"`
}

// ResolvedNetworkToVMTrafficPolicyCreateInput contains exact resource identities and canonical traffic values.
type ResolvedNetworkToVMTrafficPolicyCreateInput struct {
	SourceNetwork        *model.NetworkItem
	DestinationVM        *model.VMItem
	DestinationNetwork   *model.NetworkItem
	Protocol             model.TrafficProtocol
	DestinationPortStart int
	DestinationPortEnd   int
	NetworkRelation      model.ManagedNetworkRelation
}

// Validate checks the closed network-to-VM traffic policy shape.
func (i *NetworkToVMTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceNetwork) == "" {
		return errs.New(errs.CodeValidationFailed, "source network is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}

// Resolve binds the public network-to-VM input to exact current resource identities.
func (i *NetworkToVMTrafficPolicyCreateInput) Resolve(
	ctx context.Context,
	networkRepo network.Repository,
	vmRepo vm.Repository,
) (*ResolvedNetworkToVMTrafficPolicyCreateInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	protocol, portStart, portEnd, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	if err != nil {
		return nil, err
	}
	source, err := resolveOnePolicyNetwork(ctx, networkRepo, i.SourceNetwork)
	if err != nil {
		return nil, err
	}
	destination, err := resolveOnePolicyVM(ctx, vmRepo, i.DestinationVM)
	if err != nil {
		return nil, err
	}
	if destination.NetworkID == "" {
		return nil, errs.New(errs.CodePolicyEndpointInvalid, "destination VM must have a managed network")
	}
	destinationNetwork, err := resolvePolicyNetworkByID(ctx, networkRepo, destination.NetworkID)
	if err != nil {
		return nil, err
	}
	relation := model.ManagedNetworkRelationCross
	if source.ID == destination.NetworkID {
		relation = model.ManagedNetworkRelationSame
	}
	return &ResolvedNetworkToVMTrafficPolicyCreateInput{
		SourceNetwork: source, DestinationVM: destination, DestinationNetwork: destinationNetwork, Protocol: protocol,
		DestinationPortStart: portStart, DestinationPortEnd: portEnd, NetworkRelation: relation,
	}, nil
}

// NetworkToVMIntent builds one immutable desired-policy row from the resolved identity.
func (i *ResolvedNetworkToVMTrafficPolicyCreateInput) NetworkToVMIntent(
	at time.Time,
) *model.NetworkToVMTrafficAllowIntent {
	timestamp := at.UTC().Format(time.RFC3339)
	intent := &model.NetworkToVMTrafficAllowIntent{
		SourceNetworkID: i.SourceNetwork.ID, DestinationVMID: i.DestinationVM.ID, Protocol: i.Protocol,
		DestinationPortStart: i.DestinationPortStart, DestinationPortEnd: i.DestinationPortEnd,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.NetworkToVMTrafficPolicyDigest(*intent)
	return intent
}

// VMToVMTrafficPolicyCreateInput is the public input for one exact-VM-to-VM IP traffic grant.
type VMToVMTrafficPolicyCreateInput struct {
	SourceVM        string `json:"source_vm"        yaml:"source_vm"`
	DestinationVM   string `json:"destination_vm"   yaml:"destination_vm"`
	Protocol        string `json:"protocol"         yaml:"protocol"`
	DestinationPort string `json:"destination_port" yaml:"destination_port"`
}

// ResolvedVMToVMTrafficPolicyCreateInput contains exact resource identities and canonical traffic values.
type ResolvedVMToVMTrafficPolicyCreateInput struct {
	SourceVM             *model.VMItem
	SourceNetwork        *model.NetworkItem
	DestinationVM        *model.VMItem
	DestinationNetwork   *model.NetworkItem
	Protocol             model.TrafficProtocol
	DestinationPortStart int
	DestinationPortEnd   int
	NetworkRelation      model.ManagedNetworkRelation
}

// Validate checks the closed VM-to-VM traffic policy shape.
func (i *VMToVMTrafficPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceVM) == "" {
		return errs.New(errs.CodeValidationFailed, "source VM is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	_, _, _, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	return err
}

// Resolve binds the public VM-to-VM input to exact current resource identities.
func (i *VMToVMTrafficPolicyCreateInput) Resolve(
	ctx context.Context,
	vmRepo vm.Repository,
	networkRepo network.Repository,
) (*ResolvedVMToVMTrafficPolicyCreateInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	protocol, portStart, portEnd, err := resolveCanonicalTrafficValues(i.Protocol, i.DestinationPort)
	if err != nil {
		return nil, err
	}
	source, err := resolveOnePolicyVM(ctx, vmRepo, i.SourceVM)
	if err != nil {
		return nil, err
	}
	destination, err := resolveOnePolicyVM(ctx, vmRepo, i.DestinationVM)
	if err != nil {
		return nil, err
	}
	if source.ID == destination.ID {
		return nil, errs.New(errs.CodePolicyEndpointInvalid, "source and destination VM must differ")
	}
	if source.NetworkID == "" || destination.NetworkID == "" {
		return nil, errs.New(errs.CodePolicyEndpointInvalid, "traffic policy VMs must have managed networks")
	}
	sourceNetwork, err := resolvePolicyNetworkByID(ctx, networkRepo, source.NetworkID)
	if err != nil {
		return nil, err
	}
	destinationNetwork, err := resolvePolicyNetworkByID(ctx, networkRepo, destination.NetworkID)
	if err != nil {
		return nil, err
	}
	relation := model.ManagedNetworkRelationCross
	if source.NetworkID == destination.NetworkID {
		relation = model.ManagedNetworkRelationSame
	}
	return &ResolvedVMToVMTrafficPolicyCreateInput{
		SourceVM: source, SourceNetwork: sourceNetwork, DestinationVM: destination,
		DestinationNetwork: destinationNetwork, Protocol: protocol,
		DestinationPortStart: portStart, DestinationPortEnd: portEnd, NetworkRelation: relation,
	}, nil
}

// Intent builds one immutable desired-policy row from the resolved VM-to-VM traffic identity.
func (i *ResolvedVMToVMTrafficPolicyCreateInput) Intent(at time.Time) *model.VMToVMTrafficAllowIntent {
	timestamp := at.UTC().Format(time.RFC3339)
	intent := &model.VMToVMTrafficAllowIntent{
		SourceVMID: i.SourceVM.ID, DestinationVMID: i.DestinationVM.ID, Protocol: i.Protocol,
		DestinationPortStart: i.DestinationPortStart, DestinationPortEnd: i.DestinationPortEnd,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.VMToVMTrafficPolicyDigest(*intent)
	return intent
}

// VMToVMExecPolicyCreateInput is the public input for one directional VM-to-VM vsock exec grant.
type VMToVMExecPolicyCreateInput struct {
	SourceVM      string `json:"source_vm"      yaml:"source_vm"`
	DestinationVM string `json:"destination_vm" yaml:"destination_vm"`
	TargetUser    string `json:"target_user"    yaml:"target_user"`
}

// ResolvedVMToVMExecPolicyCreateInput contains the exact identities authorized by a VM-to-VM exec policy.
type ResolvedVMToVMExecPolicyCreateInput struct {
	SourceVM      *model.VMItem
	DestinationVM *model.VMItem
	TargetUser    string
}

// Validate checks the closed VM-to-VM exec policy shape and rejects root execution.
func (i *VMToVMExecPolicyCreateInput) Validate() error {
	if strings.TrimSpace(i.SourceVM) == "" {
		return errs.New(errs.CodeValidationFailed, "source VM is required")
	}
	if strings.TrimSpace(i.DestinationVM) == "" {
		return errs.New(errs.CodeValidationFailed, "destination VM is required")
	}
	if i.TargetUser == "root" {
		return errs.New(errs.CodePolicyExecUserInvalid, "VM-to-VM exec target user must not be root")
	}
	if len(i.TargetUser) > 32 {
		return errs.New(errs.CodePolicyExecUserInvalid, "VM-to-VM exec target user must be at most 32 bytes")
	}
	if err := validators.SSHUsername(i.TargetUser); err != nil {
		return errs.WrapMsg(errs.CodePolicyExecUserInvalid, "invalid VM-to-VM exec target user", err)
	}
	return nil
}

// Resolve binds the public exec-policy input to exact VM identities without consulting managed-network placement.
func (i *VMToVMExecPolicyCreateInput) Resolve(
	ctx context.Context,
	vmRepo vm.Repository,
) (*ResolvedVMToVMExecPolicyCreateInput, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	source, err := resolveOnePolicyVM(ctx, vmRepo, i.SourceVM)
	if err != nil {
		return nil, err
	}
	destination, err := resolveOnePolicyVM(ctx, vmRepo, i.DestinationVM)
	if err != nil {
		return nil, err
	}
	if source.ID == destination.ID {
		return nil, errs.New(errs.CodePolicyEndpointInvalid, "source and destination VM must differ")
	}
	return &ResolvedVMToVMExecPolicyCreateInput{
		SourceVM: source, DestinationVM: destination, TargetUser: i.TargetUser,
	}, nil
}

// Intent builds one immutable desired-policy row from the resolved VM-to-VM exec identity.
func (i *ResolvedVMToVMExecPolicyCreateInput) Intent(at time.Time) *model.VMToVMExecAllowIntent {
	timestamp := at.UTC().Format(time.RFC3339)
	intent := &model.VMToVMExecAllowIntent{
		SourceVMID: i.SourceVM.ID, DestinationVMID: i.DestinationVM.ID, TargetUser: i.TargetUser,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
	intent.ID = crypto.VMToVMExecPolicyDigest(*intent)
	return intent
}

func resolveCanonicalTrafficValues(protocolValue, destinationPort string) (model.TrafficProtocol, int, int, error) {
	protocol := model.TrafficProtocol(protocolValue)
	if protocol != model.TrafficProtocolTCP && protocol != model.TrafficProtocolUDP {
		return "", 0, 0, errs.New(errs.CodePolicyProtocolInvalid, "protocol must be canonical tcp or udp")
	}
	if destinationPort == "" || destinationPort != strings.TrimSpace(destinationPort) {
		return "", 0, 0, errs.New(
			errs.CodePolicyPortInvalid,
			"destination port must be a canonical port or bounded range",
		)
	}
	parts := strings.Split(destinationPort, "-")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == "" {
		return "", 0, 0, errs.New(
			errs.CodePolicyPortInvalid,
			"destination port must be a canonical port or bounded range",
		)
	}
	start, err := canonicalPolicyPort(parts[0])
	if err != nil {
		return "", 0, 0, err
	}
	end := start
	if len(parts) == 2 {
		end, err = canonicalPolicyPort(parts[1])
		if err != nil {
			return "", 0, 0, err
		}
		if end < start {
			return "", 0, 0, errs.New(
				errs.CodePolicyPortInvalid,
				"destination port range end must be greater than or equal to its start",
			)
		}
	}
	return protocol, start, end, nil
}

func canonicalPolicyPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != value {
		return 0, errs.New(errs.CodePolicyPortInvalid, "destination port must be between 1 and 65535")
	}
	return port, nil
}

func resolveOnePolicyNetwork(
	ctx context.Context,
	repo network.Repository,
	identifier string,
) (*model.NetworkItem, error) {
	items, err := (&NetworkInput{Identifiers: []string{identifier}}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}

func resolveOnePolicyVM(ctx context.Context, repo vm.Repository, identifier string) (*model.VMItem, error) {
	items, err := (&VMInput{Identifiers: []string{identifier}}).Resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	return items[0], nil
}

func resolvePolicyNetworkByID(
	ctx context.Context,
	repo network.Repository,
	networkID string,
) (*model.NetworkItem, error) {
	item, err := repo.Get(ctx, networkID)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeNetworkNotFound, "failed to resolve policy network", err)
	}
	if item == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, "policy network not found", errs.WithEntity(networkID))
	}
	return item, nil
}
