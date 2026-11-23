package firewall

import (
	"bytes"
	"context"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// conntrackExchange is deliberately separate from the read-only nftables whitelist. Its only callers
// construct an AF_INET dump or a fully identified deletion from a decoded kernel flow.
func conntrackExchange(
	ctx context.Context,
	transport *kernelObservationTransport,
	request netlink.Message,
) (replies []netlink.Message, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, kernelObservationTransportError("conntrack context ended before send", err, false)
	}
	closed := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { closed <- transport.closeConnection(context.WithoutCancel(ctx)) })
	defer func() {
		var closeErr error
		if !stop() {
			closeErr = <-closed
		}
		if err := ctx.Err(); err != nil {
			replies = nil
			returnErr = kernelObservationTransportError("conntrack request context ended", err, true)
		}
		if closeErr != nil {
			returnErr = appendGenerationCleanupError(returnErr, "close conntrack cancellation socket", closeErr)
		}
	}()
	sent, err := transport.connection.Send(request)
	if err != nil {
		return nil, kernelObservationTransportError("send conntrack request", err, true)
	}
	length := uint32((netlinkHeaderBytes + len(request.Data) + 3) &^ 3)
	if sent.Header.Type != request.Header.Type ||
		sent.Header.Flags != request.Header.Flags ||
		sent.Header.Length != length ||
		sent.Header.Sequence == 0 ||
		sent.Header.PID == 0 ||
		!bytes.Equal(sent.Data, request.Data) {
		return nil, kernelObservationTransportError("sent conntrack request identity differs", nil, true)
	}
	raw, err := transport.connection.SyscallConn()
	if err != nil {
		return nil, kernelObservationTransportError("acquire conntrack raw socket", err, true)
	}
	if raw == nil {
		return nil, kernelObservationTransportError("conntrack raw socket is unavailable", nil, true)
	}
	storage := make([]byte, kernelObservationDatagramBytes)
	for {
		data, err := transport.receiveDatagram(ctx, raw, storage)
		if err != nil {
			return nil, kernelObservationTransportError("receive conntrack response", err, true)
		}
		messages, err := transport.parseDatagram(data, sent.Header.Sequence, sent.Header.PID, transport.budget)
		if err != nil {
			return nil, err
		}
		for i, message := range messages {
			switch message.Header.Type {
			case netlink.Error:
				if len(messages) != 1 {
					return nil, kernelObservationTransportError("conntrack error shares a datagram", nil, true)
				}
				return nil, decodeKernelNetlinkAcknowledgement(message, sent, request.Header.Type == conntrackDelete)
			case netlink.Done:
				if request.Header.Type != conntrackGet || i != len(messages)-1 {
					return nil, kernelObservationTransportError("conntrack terminal response is invalid", nil, true)
				}
				if err := validateKernelObservationDone(message, kernelObservationReplyMultipart); err != nil {
					return nil, err
				}
				return replies, nil
			default:
				// An AF_INET conntrack dump marks flow records DUMP_FILTERED on Linux. It still
				// requires MULTI; interruption and every other unexpected flag remain refusals.
				if request.Header.Type != conntrackGet ||
					message.Header.Type != conntrackNew ||
					message.Header.Flags & ^netlink.DumpFiltered != netlink.Multi {
					return nil, kernelObservationTransportError("conntrack response type or flags differ", nil, true)
				}
				replies = append(replies, message)
			}
		}
	}
}

func collectConntrackFlows(ctx context.Context, transport *kernelObservationTransport) ([]conntrackFlow, error) {
	request := netlink.Message{
		Header: netlink.Header{Type: conntrackGet, Flags: netlink.Request | netlink.Dump},
		Data:   []byte{unix.AF_INET, 0, 0, 0},
	}
	replies, err := conntrackExchange(ctx, transport, request)
	if err != nil {
		return nil, err
	}
	flows := make([]conntrackFlow, 0, len(replies))
	for _, reply := range replies {
		flow, err := decodeConntrackFlow(reply.Data)
		if err != nil {
			return nil, err
		}
		flows = append(flows, flow)
	}
	return flows, nil
}
