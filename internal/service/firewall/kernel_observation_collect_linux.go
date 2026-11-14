package firewall

import (
	"context"
	"errors"

	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	// x/sys/unix does not expose enum nft_flowtable_attributes.
	kernelNFTAFlowtableTable = uint16(1)
	kernelTaskCommBytes      = 16
)

type kernelObservationSnapshotTransport interface {
	exchange(context.Context, netlink.Message, kernelObservationReplySpec) ([]netlink.Message, error)
	closeConnection(context.Context) error
}

type kernelObservationSnapshotTransportFactory interface {
	newTransport(context.Context, *kernelObservationReceiveBudget) (kernelObservationSnapshotTransport, error)
}

type kernelRulesetGeneration struct {
	id uint32
}

type kernelSnapshotDumpKind struct {
	requestType    uint16
	tableAttribute uint16
	replyFlags     netlink.HeaderFlags
}

type kernelSnapshotChainIdentity struct {
	table tableIdentity
	name  string
}

// collectKernelObservationSnapshot is deliberately inert: its factory supplies connection authority, while this
// closed transaction chooses every read-only request, family, table, chain, and object class itself.
//
// CONCURRENCY: Calls may run concurrently only with independent factories. Each exchange exclusively owns one fresh
// transport until checked close; no transport is reused after either a successful reply or poisoning.
func collectKernelObservationSnapshot(
	ctx context.Context,
	factory kernelObservationSnapshotTransportFactory,
) (kernelObservation, error) {
	if factory == nil {
		return kernelObservation{}, generationAuthorityError(
			"kernel observation transport factory is required",
			nil,
			nil,
		)
	}
	if ctx == nil {
		return kernelObservation{}, generationAuthorityError("kernel observation context is required", nil, nil)
	}
	if err := ctx.Err(); err != nil {
		return kernelObservation{}, generationAuthorityError("collect kernel observation snapshot", err, nil)
	}

	budget := &kernelObservationReceiveBudget{}
	before, err := collectKernelRulesetGeneration(ctx, factory, budget)
	if err != nil {
		return kernelObservation{}, err
	}

	raw := kernelObservationMessages{}
	present := [len(managedTableIdentities)]bool{}
	for index, identity := range managedTableIdentities {
		request, err := kernelSnapshotTableRequest(identity)
		if err != nil {
			return kernelObservation{}, err
		}
		replies, err := exchangeKernelSnapshot(ctx, factory, budget, request, kernelObservationReplySpec{
			mode: kernelObservationReplySingular,
		})
		if exactKernelSnapshotTableAbsent(err) {
			continue
		}
		if err != nil {
			return kernelObservation{}, err
		}
		if len(replies) != 1 {
			return kernelObservation{}, invalidKernelObservation("exact managed table lookup is not singular")
		}
		present[index] = true
		raw.tableReplies = append(raw.tableReplies, replies[0])
	}
	if present[0] != present[1] {
		after, generationErr := collectKernelRulesetGeneration(ctx, factory, budget)
		if generationErr != nil {
			return kernelObservation{}, generationErr
		}
		if before.id != after.id {
			return kernelObservation{}, invalidKernelObservation("ruleset generation changed during collection")
		}
		return kernelObservation{}, invalidKernelObservation("managed table observation is partial")
	}

	if present[0] {
		// Linux ignores table attributes on GETCHAIN dumps. Exact named lookups plus NFTA_TABLE_USE therefore prove
		// that the five known chains are the complete chain set. Keep this aligned with the pinned nf_tables_api.c.
		for _, identity := range kernelSnapshotManagedChainIdentities() {
			request, err := kernelSnapshotChainRequest(identity)
			if err != nil {
				return kernelObservation{}, err
			}
			replies, err := exchangeKernelSnapshot(ctx, factory, budget, request, kernelObservationReplySpec{
				mode: kernelObservationReplySingular,
			})
			if err != nil {
				return kernelObservation{}, err
			}
			if len(replies) != 1 {
				return kernelObservation{}, invalidKernelObservation("exact managed chain lookup is not singular")
			}
			raw.chainReplies = append(raw.chainReplies, replies[0])
		}

		// These four Linux dump handlers honor their table attribute. The strict decoder still verifies every returned
		// rule identity and fails closed on any set, object, or flowtable reply.
		dumpKinds := [...]kernelSnapshotDumpKind{
			{
				requestType: unix.NFT_MSG_GETRULE, tableAttribute: unix.NFTA_RULE_TABLE,
				replyFlags: netlink.Multi | netlink.Append,
			},
			{
				requestType: unix.NFT_MSG_GETSET, tableAttribute: unix.NFTA_SET_TABLE,
				replyFlags: netlink.Multi,
			},
			{
				requestType: unix.NFT_MSG_GETOBJ, tableAttribute: unix.NFTA_OBJ_TABLE,
				replyFlags: netlink.Multi | netlink.Append,
			},
			{
				requestType: unix.NFT_MSG_GETFLOWTABLE, tableAttribute: kernelNFTAFlowtableTable,
				replyFlags: netlink.Multi | netlink.Append,
			},
		}
		for kindIndex, kind := range dumpKinds {
			for _, identity := range managedTableIdentities {
				request, err := kernelSnapshotTableDumpRequest(kind, identity)
				if err != nil {
					return kernelObservation{}, err
				}
				replies, err := exchangeKernelSnapshot(ctx, factory, budget, request, kernelObservationReplySpec{
					flags: kind.replyFlags,
					mode:  kernelObservationReplyMultipart,
				})
				if err != nil {
					return kernelObservation{}, err
				}
				switch kindIndex {
				case 0:
					raw.ruleReplies = append(raw.ruleReplies, replies...)
				default:
					if len(replies) != 0 {
						return kernelObservation{}, invalidKernelObservation(
							"managed tables contain a set, object, or flowtable",
						)
					}
				}
			}
		}
	}

	after, err := collectKernelRulesetGeneration(ctx, factory, budget)
	if err != nil {
		return kernelObservation{}, err
	}
	if before.id != after.id {
		return kernelObservation{}, invalidKernelObservation("ruleset generation changed during collection")
	}

	observed, err := decodeKernelObservation(raw)
	if err != nil {
		return kernelObservation{}, err
	}
	if !observed.empty && observed.nftBaseSequence != uint16(before.id) {
		return kernelObservation{}, invalidKernelObservation(
			"object base sequence does not match ruleset generation",
		)
	}
	return observed, nil
}

func exchangeKernelSnapshot(
	ctx context.Context,
	factory kernelObservationSnapshotTransportFactory,
	budget *kernelObservationReceiveBudget,
	request netlink.Message,
	spec kernelObservationReplySpec,
) (replies []netlink.Message, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("open kernel observation transport", err, nil)
	}
	transport, err := factory.newTransport(ctx, budget)
	if err != nil {
		primary := preserveGenerationAuthorityError("open kernel observation transport", err)
		if transport == nil {
			return nil, primary
		}
		return nil, appendGenerationCleanupError(
			primary,
			"close kernel observation connection",
			transport.closeConnection(context.WithoutCancel(ctx)),
		)
	}
	if transport == nil {
		return nil, generationAuthorityError("kernel observation transport is unavailable", nil, nil)
	}
	defer func() {
		returnErr = appendGenerationCleanupError(
			returnErr,
			"close kernel observation connection",
			transport.closeConnection(context.WithoutCancel(ctx)),
		)
	}()
	return transport.exchange(ctx, request, spec)
}

func exactKernelSnapshotTableAbsent(err error) bool {
	if err == nil {
		return false
	}
	domainErr := errs.AsDomainError(err)
	if domainErr == nil || domainErr.Message != kernelObservationRejectedRequestMessage ||
		domainErr.Details[kernelObservationOutcomeUnknownDetailKey] != true ||
		domainErr.Details[generationCleanupFailedDetailKey] == true {
		return false
	}
	return errors.Is(domainErr.Err, unix.ENOENT)
}

func collectKernelRulesetGeneration(
	ctx context.Context,
	factory kernelObservationSnapshotTransportFactory,
	budget *kernelObservationReceiveBudget,
) (kernelRulesetGeneration, error) {
	replies, err := exchangeKernelSnapshot(
		ctx,
		factory,
		budget,
		kernelSnapshotGenerationRequest(),
		kernelObservationReplySpec{
			mode: kernelObservationReplySingular,
		},
	)
	if err != nil {
		return kernelRulesetGeneration{}, err
	}
	if len(replies) != 1 {
		return kernelRulesetGeneration{}, invalidKernelObservation("ruleset generation lookup is not singular")
	}
	return decodeKernelRulesetGeneration(replies[0])
}

func decodeKernelRulesetGeneration(message netlink.Message) (kernelRulesetGeneration, error) {
	family, baseSequence, attrs, err := decodeKernelMessage(
		message,
		unix.NFT_MSG_NEWGEN,
		0,
		"ruleset generation",
		map[uint16]kernelAttributeRule{
			unix.NFTA_GEN_ID:        {required: true},
			unix.NFTA_GEN_PROC_PID:  {required: true},
			unix.NFTA_GEN_PROC_NAME: {required: true},
		},
	)
	if err != nil {
		return kernelRulesetGeneration{}, err
	}
	if family != nftables.TableFamilyUnspecified {
		return kernelRulesetGeneration{}, invalidKernelObservation("ruleset generation family is invalid")
	}
	generationID, err := kernelAttributeUint32(attrs[unix.NFTA_GEN_ID], "ruleset generation ID")
	if err != nil {
		return kernelRulesetGeneration{}, err
	}
	processID, err := kernelAttributeUint32(attrs[unix.NFTA_GEN_PROC_PID], "ruleset generation process ID")
	if err != nil {
		return kernelRulesetGeneration{}, err
	}
	processName, err := kernelAttributeString(attrs[unix.NFTA_GEN_PROC_NAME], "ruleset generation process name")
	if err != nil {
		return kernelRulesetGeneration{}, err
	}
	if processID == 0 || processName == "" || len(attrs[unix.NFTA_GEN_PROC_NAME].Data) > kernelTaskCommBytes {
		return kernelRulesetGeneration{}, invalidKernelObservation("ruleset generation process identity is invalid")
	}
	if uint16(generationID) != baseSequence {
		return kernelRulesetGeneration{}, invalidKernelObservation(
			"ruleset generation base sequence is inconsistent",
		)
	}
	return kernelRulesetGeneration{id: generationID}, nil
}

func kernelSnapshotGenerationRequest() netlink.Message {
	return netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETGEN),
			Flags: netlink.Request,
		},
		Data: kernelSnapshotNetfilterHeader(nftables.TableFamilyUnspecified),
	}
}

func kernelSnapshotTableRequest(identity tableIdentity) (netlink.Message, error) {
	data, err := kernelSnapshotTableData(identity, unix.NFTA_TABLE_NAME)
	if err != nil {
		return netlink.Message{}, err
	}
	return netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETTABLE),
			Flags: netlink.Request,
		},
		Data: data,
	}, nil
}

func kernelSnapshotChainRequest(identity kernelSnapshotChainIdentity) (netlink.Message, error) {
	attributes, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: unix.NFTA_CHAIN_TABLE, Data: []byte(identity.table.name + "\x00")},
		{Type: unix.NFTA_CHAIN_NAME, Data: []byte(identity.name + "\x00")},
	})
	if err != nil {
		return netlink.Message{}, generationAuthorityError("marshal kernel observation chain request", err, nil)
	}
	return netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETCHAIN),
			Flags: netlink.Request,
		},
		Data: append(kernelSnapshotNetfilterHeader(identity.table.family), attributes...),
	}, nil
}

func kernelSnapshotTableDumpRequest(
	kind kernelSnapshotDumpKind,
	identity tableIdentity,
) (netlink.Message, error) {
	data, err := kernelSnapshotTableData(identity, kind.tableAttribute)
	if err != nil {
		return netlink.Message{}, err
	}
	return netlink.Message{
		Header: netlink.Header{
			Type:  netlink.HeaderType(kernelNetfilterSubsystemType | kind.requestType),
			Flags: netlink.Request | netlink.Dump,
		},
		Data: data,
	}, nil
}

func kernelSnapshotTableData(identity tableIdentity, attributeType uint16) ([]byte, error) {
	attributes, err := netlink.MarshalAttributes([]netlink.Attribute{{
		Type: attributeType,
		Data: []byte(identity.name + "\x00"),
	}})
	if err != nil {
		return nil, generationAuthorityError("marshal kernel observation table request", err, nil)
	}
	return append(kernelSnapshotNetfilterHeader(identity.family), attributes...), nil
}

func kernelSnapshotNetfilterHeader(family nftables.TableFamily) []byte {
	return []byte{byte(family), unix.NFNETLINK_V0, 0, 0}
}

func kernelSnapshotManagedChainIdentities() [5]kernelSnapshotChainIdentity {
	return [5]kernelSnapshotChainIdentity{
		{table: managedTableIdentities[0], name: managedBridgePreroutingChainName},
		{table: managedTableIdentities[0], name: managedBridgeForwardChainName},
		{table: managedTableIdentities[1], name: managedINetForwardChainName},
		{table: managedTableIdentities[1], name: managedINetInputChainName},
		{table: managedTableIdentities[1], name: managedINetNATChainName},
	}
}
