package firewall

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"syscall"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	kernelObservationDatagramBytes = 64 << 10
	kernelObservationMaxErrno      = 4095

	kernelObservationOutcomeUnknownDetailKey = "kernel_observation_outcome_unknown"
	kernelObservationRejectedRequestMessage  = "kernel rejected observation request"
)

type kernelObservationNetlinkConnection interface {
	Send(netlink.Message) (netlink.Message, error)
	SyscallConn() (syscall.RawConn, error)
	Close() error
}

type kernelObservationRecvmsg func(
	fd int,
	payload []byte,
	outOfBand []byte,
	flags int,
) (payloadBytes int, outOfBandBytes int, responseFlags int, source unix.Sockaddr, err error)

type kernelObservationReplyMode uint8

const (
	kernelObservationReplySingular kernelObservationReplyMode = iota + 1
	kernelObservationReplyMultipart
)

type kernelObservationReplySpec struct {
	flags netlink.HeaderFlags
	mode  kernelObservationReplyMode
}

type kernelObservationTransportLimits struct {
	messageBytes int
	messages     int
	totalBytes   int
}

type kernelObservationTransport struct {
	// IMPORTANT: exchange is deliberately not concurrency-safe. One request owns the connection until its exact
	// terminal shape; concurrent calls could consume each other's datagrams. closeConnection may run concurrently to
	// cancel that sole exchange.
	connection kernelObservationNetlinkConnection
	recvmsg    kernelObservationRecvmsg
	limits     kernelObservationTransportLimits
	// budget is shared by every fresh transport opened for one logical snapshot.
	budget *kernelObservationReceiveBudget

	// THREAD-SAFE: closeOnce publishes closeErr after exactly one connection close to all concurrent callers.
	closeOnce sync.Once
	closeErr  error
}

type kernelObservationReceiveBudget struct {
	messages int
	bytes    int
}

// newKernelObservationTransport retains only typed connection authority. It never accepts a path or descriptor and
// therefore cannot escape the namespace in which its caller acquired the netlink connection.
func newKernelObservationTransport(
	connection kernelObservationNetlinkConnection,
	recvmsg kernelObservationRecvmsg,
) *kernelObservationTransport {
	if recvmsg == nil {
		recvmsg = unix.Recvmsg
	}
	return &kernelObservationTransport{
		connection: connection,
		recvmsg:    recvmsg,
		budget:     &kernelObservationReceiveBudget{},
		limits: kernelObservationTransportLimits{
			messageBytes: maxKernelObservationMessageBytes,
			messages:     maxKernelObservationMessages,
			totalBytes:   maxKernelObservationBytes,
		},
	}
}

// exchange deliberately uses Conn.Send followed by SyscallConn and Recvmsg. Pinned netlink Conn.Receive aggregates
// multipart datagrams and removes NLMSG_DONE, erasing evidence needed by this strict boundary:
// https://github.com/mdlayher/netlink/blob/fbb4dce95f42/conn.go#L221-L269
func (transport *kernelObservationTransport) exchange(
	ctx context.Context,
	request netlink.Message,
	spec kernelObservationReplySpec,
) (replies []netlink.Message, returnErr error) {
	if transport == nil || transport.connection == nil || transport.recvmsg == nil || transport.budget == nil {
		return nil, kernelObservationTransportError("kernel observation transport is unavailable", nil, false)
	}
	if ctx == nil {
		return nil, kernelObservationTransportError("kernel observation context is unavailable", nil, false)
	}
	expectedResponseType, err := validateKernelObservationRequest(request, spec)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, transport.close(context.WithoutCancel(ctx), kernelObservationTransportError(
			"kernel observation request context ended before send",
			err,
			false,
		))
	}

	sent := false
	stopCancellation := make(chan struct{})
	cancellationCloseResult := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			cancellationCloseResult <- transport.closeConnection(context.WithoutCancel(ctx))
		case <-stopCancellation:
			cancellationCloseResult <- nil
		}
	}()
	defer func() {
		close(stopCancellation)
		cancellationCloseErr := <-cancellationCloseResult
		if contextErr := ctx.Err(); contextErr != nil {
			replies = nil
			returnErr = kernelObservationTransportError(
				"kernel observation request context ended",
				contextErr,
				sent,
			)
			if cancellationCloseErr != nil {
				returnErr = appendGenerationCleanupError(
					returnErr,
					"close kernel observation connection",
					cancellationCloseErr,
				)
				return
			}
			returnErr = transport.close(context.WithoutCancel(ctx), returnErr)
			return
		}
		if sent && returnErr != nil {
			replies = nil
			returnErr = transport.close(context.WithoutCancel(ctx), returnErr)
		}
	}()

	sentRequest, err := transport.connection.Send(request)
	if err != nil {
		return nil, kernelObservationTransportError("send kernel observation request", err, false)
	}
	sent = true
	if sentRequest.Header.Sequence == 0 {
		return nil, kernelObservationTransportError("sent kernel observation sequence is zero", nil, true)
	}
	if err := validateSentKernelObservationRequest(request, sentRequest, expectedResponseType); err != nil {
		return nil, err
	}

	rawConnection, err := transport.connection.SyscallConn()
	if err != nil {
		return nil, kernelObservationTransportError("acquire raw kernel observation connection", err, true)
	}
	if rawConnection == nil {
		return nil, kernelObservationTransportError("raw kernel observation connection is unavailable", nil, true)
	}

	storage := make([]byte, kernelObservationDatagramBytes)
	for {
		datagram, err := transport.receiveDatagram(ctx, rawConnection, storage)
		if err != nil {
			if errs.AsDomainError(err) != nil {
				return nil, err
			}
			return nil, kernelObservationTransportError("receive kernel observation reply", err, true)
		}
		messages, err := transport.parseDatagram(
			datagram,
			sentRequest.Header.Sequence,
			sentRequest.Header.PID,
			transport.budget,
		)
		if err != nil {
			return nil, err
		}
		if len(messages) != 1 {
			for _, message := range messages {
				if message.Header.Type == netlink.Error {
					return nil, kernelObservationTransportError(
						"kernel observation error is not the sole datagram message",
						nil,
						true,
					)
				}
			}
		}
		terminal := false
		for _, message := range messages {
			if terminal {
				return nil, kernelObservationTransportError(
					"kernel observation reply contains data after its terminal message",
					nil,
					true,
				)
			}
			switch message.Header.Type {
			case netlink.Overrun:
				return nil, kernelObservationTransportError("kernel observation reply reports overrun", nil, true)
			case netlink.Error:
				return nil, decodeKernelObservationError(message, sentRequest)
			case netlink.Done:
				if err := validateKernelObservationDone(message, spec.mode); err != nil {
					return nil, err
				}
				terminal = true
			default:
				if message.Header.Type != expectedResponseType || message.Header.Flags != spec.flags {
					return nil, kernelObservationTransportError(
						"kernel observation response type or flags are invalid",
						nil,
						true,
					)
				}
				if spec.mode == kernelObservationReplySingular && len(replies) != 0 {
					return nil, kernelObservationTransportError(
						"kernel observation reply contains extra data",
						nil,
						true,
					)
				}
				replies = append(replies, message)
				terminal = spec.mode == kernelObservationReplySingular
			}
		}
		if terminal {
			return replies, nil
		}
	}
}

func validateKernelObservationRequest(
	request netlink.Message,
	spec kernelObservationReplySpec,
) (netlink.HeaderType, error) {
	expectedRequestFlags := netlink.Request
	switch spec.mode {
	case kernelObservationReplySingular:
		if spec.flags != 0 {
			return 0, kernelObservationTransportError(
				"singular kernel observation response flags are invalid",
				nil,
				false,
			)
		}
	case kernelObservationReplyMultipart:
		expectedRequestFlags |= netlink.Dump
		expectedFlags, ok := kernelObservationMultipartResponseFlags(request.Header.Type)
		if !ok || spec.flags != expectedFlags {
			return 0, kernelObservationTransportError(
				"multipart kernel observation response contract is invalid",
				nil,
				false,
			)
		}
	default:
		return 0, kernelObservationTransportError("kernel observation response mode is invalid", nil, false)
	}
	if request.Header.Length != 0 || request.Header.Sequence != 0 || request.Header.PID != 0 {
		return 0, kernelObservationTransportError("kernel observation request identity is already assigned", nil, false)
	}
	if request.Header.Flags != expectedRequestFlags {
		return 0, kernelObservationTransportError("kernel observation request flags are invalid", nil, false)
	}
	if netlinkHeaderBytes+len(request.Data) > maxKernelObservationMessageBytes {
		return 0, kernelObservationTransportError("kernel observation request exceeds size limit", nil, false)
	}
	responseType, ok := kernelObservationResponseType(request.Header.Type)
	if !ok {
		return 0, kernelObservationTransportError("kernel observation request type is not read-only", nil, false)
	}
	return responseType, nil
}

func kernelObservationMultipartResponseFlags(requestType netlink.HeaderType) (netlink.HeaderFlags, bool) {
	switch requestType {
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETRULE),
		netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETOBJ),
		netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETFLOWTABLE):
		return netlink.Multi | netlink.Append, true
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETGEN),
		netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETTABLE),
		netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETCHAIN),
		netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETSET):
		return netlink.Multi, true
	default:
		return 0, false
	}
}

func kernelObservationResponseType(requestType netlink.HeaderType) (netlink.HeaderType, bool) {
	var responseType uint16
	switch requestType {
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETGEN):
		responseType = unix.NFT_MSG_NEWGEN
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETTABLE):
		responseType = unix.NFT_MSG_NEWTABLE
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETCHAIN):
		responseType = unix.NFT_MSG_NEWCHAIN
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETRULE):
		responseType = unix.NFT_MSG_NEWRULE
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETSET):
		responseType = unix.NFT_MSG_NEWSET
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETOBJ):
		responseType = unix.NFT_MSG_NEWOBJ
	case netlink.HeaderType(kernelNetfilterSubsystemType | unix.NFT_MSG_GETFLOWTABLE):
		responseType = unix.NFT_MSG_NEWFLOWTABLE
	default:
		return 0, false
	}
	return netlink.HeaderType(kernelNetfilterSubsystemType | responseType), true
}

func validateSentKernelObservationRequest(
	request netlink.Message,
	sent netlink.Message,
	expectedResponseType netlink.HeaderType,
) error {
	responseType, ok := kernelObservationResponseType(sent.Header.Type)
	expectedLength := uint32((netlinkHeaderBytes + len(request.Data) + 3) &^ 3)
	if !ok || responseType != expectedResponseType || sent.Header.Type != request.Header.Type ||
		sent.Header.Flags != request.Header.Flags || sent.Header.Length != expectedLength || sent.Header.PID == 0 ||
		!bytes.Equal(sent.Data, request.Data) {
		return kernelObservationTransportError("sent kernel observation request identity is invalid", nil, true)
	}
	return nil
}

func (transport *kernelObservationTransport) receiveDatagram(
	ctx context.Context,
	rawConnection syscall.RawConn,
	storage []byte,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var (
		payloadBytes   int
		outOfBandBytes int
		responseFlags  int
		source         unix.Sockaddr
		receiveErr     error
	)
	readErr := rawConnection.Read(func(fd uintptr) bool {
		payloadBytes, outOfBandBytes, responseFlags, source, receiveErr = transport.recvmsg(
			int(fd),
			storage,
			nil,
			0,
		)
		return !errors.Is(receiveErr, unix.EAGAIN) && !errors.Is(receiveErr, unix.EWOULDBLOCK)
	})
	if readErr != nil {
		return nil, readErr
	}
	if receiveErr != nil {
		return nil, receiveErr
	}
	if payloadBytes <= 0 || payloadBytes > len(storage) {
		return nil, kernelObservationTransportError("netlink datagram length is invalid", nil, true)
	}
	if responseFlags != 0 || responseFlags&unix.MSG_TRUNC != 0 {
		return nil, kernelObservationTransportError("netlink datagram is truncated or flagged", nil, true)
	}
	if outOfBandBytes != 0 {
		return nil, kernelObservationTransportError("netlink datagram contains out-of-band data", nil, true)
	}
	kernelSource, ok := source.(*unix.SockaddrNetlink)
	if !ok || kernelSource.Family != unix.AF_NETLINK || kernelSource.Pid != 0 || kernelSource.Groups != 0 {
		return nil, kernelObservationTransportError("netlink datagram source is not the kernel", nil, true)
	}
	return storage[:payloadBytes], nil
}

// parseDatagram implements the alignment and header grammar from Linux UAPI netlink.h and checks every hard limit
// before cloning payload bytes: https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/tree/include/uapi/linux/netlink.h?h=v6.19.9
func (transport *kernelObservationTransport) parseDatagram(
	datagram []byte,
	sequence uint32,
	requesterPID uint32,
	budget *kernelObservationReceiveBudget,
) ([]netlink.Message, error) {
	result := make([]netlink.Message, 0, 1)
	for len(datagram) != 0 {
		if len(datagram) < netlinkHeaderBytes {
			return nil, kernelObservationTransportError("kernel observation reply header is truncated", nil, true)
		}
		messageBytes := int(binary.NativeEndian.Uint32(datagram[0:4]))
		if messageBytes < netlinkHeaderBytes {
			return nil, kernelObservationTransportError("kernel observation reply length is invalid", nil, true)
		}
		if messageBytes > transport.limits.messageBytes {
			return nil, kernelObservationTransportError(
				"kernel observation reply message exceeds size limit",
				nil,
				true,
			)
		}
		alignedBytes := (messageBytes + 3) &^ 3
		if alignedBytes < messageBytes || alignedBytes > len(datagram) {
			return nil, kernelObservationTransportError("kernel observation reply alignment is truncated", nil, true)
		}
		if !bytes.Equal(datagram[messageBytes:alignedBytes], make([]byte, alignedBytes-messageBytes)) {
			return nil, kernelObservationTransportError("kernel observation reply alignment is nonzero", nil, true)
		}
		if budget.messages >= transport.limits.messages {
			return nil, kernelObservationTransportError("kernel observation reply count exceeds limit", nil, true)
		}
		if messageBytes > transport.limits.totalBytes-budget.bytes {
			return nil, kernelObservationTransportError("kernel observation reply bytes exceed limit", nil, true)
		}
		budget.messages++
		budget.bytes += messageBytes

		header := netlink.Header{
			Length:   uint32(messageBytes),
			Type:     netlink.HeaderType(binary.NativeEndian.Uint16(datagram[4:6])),
			Flags:    netlink.HeaderFlags(binary.NativeEndian.Uint16(datagram[6:8])),
			Sequence: binary.NativeEndian.Uint32(datagram[8:12]),
			PID:      binary.NativeEndian.Uint32(datagram[12:16]),
		}
		if header.Sequence != sequence || header.PID != requesterPID {
			return nil, kernelObservationTransportError(
				"kernel observation reply sequence or PID is invalid",
				nil,
				true,
			)
		}
		if header.Flags&netlink.DumpInterrupted != 0 {
			return nil, kernelObservationTransportError("kernel observation dump was interrupted", nil, true)
		}
		result = append(result, netlink.Message{
			Header: header,
			Data:   bytes.Clone(datagram[netlinkHeaderBytes:messageBytes]),
		})
		datagram = datagram[alignedBytes:]
	}
	return result, nil
}

// Linux NLMSG_ERROR carries struct nlmsgerr: errno, the original nlmsghdr, and the original payload unless CAPPED.
// Keep this decoder aligned with the pinned UAPI definition:
// https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/tree/include/uapi/linux/netlink.h?h=v6.19.9
func decodeKernelObservationError(message netlink.Message, request netlink.Message) error {
	if message.Header.Flags & ^(netlink.Capped|netlink.AcknowledgeTLVs) != 0 {
		return kernelObservationTransportError(fmt.Sprintf(
			"kernel observation error flags are invalid (flags=%#x request_type=%#x)",
			uint16(message.Header.Flags), uint16(request.Header.Type),
		), nil, true)
	}
	if len(message.Data) < 4+netlinkHeaderBytes {
		return kernelObservationTransportError("kernel observation nlmsgerr is truncated", nil, true)
	}
	errno := int32(binary.NativeEndian.Uint32(message.Data[0:4]))
	if errno == 0 {
		return kernelObservationTransportError("kernel observation acknowledgement is unexpected", nil, true)
	}
	if errno > 0 || -int64(errno) > kernelObservationMaxErrno {
		return kernelObservationTransportError("kernel observation errno is invalid", nil, true)
	}

	embedded := message.Data[4 : 4+netlinkHeaderBytes]
	embeddedHeader := netlink.Header{
		Length:   binary.NativeEndian.Uint32(embedded[0:4]),
		Type:     netlink.HeaderType(binary.NativeEndian.Uint16(embedded[4:6])),
		Flags:    netlink.HeaderFlags(binary.NativeEndian.Uint16(embedded[6:8])),
		Sequence: binary.NativeEndian.Uint32(embedded[8:12]),
		PID:      binary.NativeEndian.Uint32(embedded[12:16]),
	}
	if embeddedHeader != request.Header {
		return kernelObservationTransportError("kernel observation error request header is invalid", nil, true)
	}

	payloadEnd := 4 + netlinkHeaderBytes
	if message.Header.Flags&netlink.Capped == 0 {
		requestPayloadBytes := int(request.Header.Length) - netlinkHeaderBytes
		payloadEnd = 4 + int(request.Header.Length)
		if requestPayloadBytes < len(request.Data) || len(message.Data) < payloadEnd {
			return kernelObservationTransportError(
				"kernel observation error request payload length is invalid",
				nil,
				true,
			)
		}
		embeddedPayload := message.Data[4+netlinkHeaderBytes : payloadEnd]
		if !bytes.Equal(embeddedPayload[:len(request.Data)], request.Data) ||
			!bytes.Equal(embeddedPayload[len(request.Data):], make([]byte, requestPayloadBytes-len(request.Data))) {
			return kernelObservationTransportError("kernel observation error request payload is invalid", nil, true)
		}
	}
	if message.Header.Flags&netlink.AcknowledgeTLVs == 0 {
		if len(message.Data) != payloadEnd {
			if message.Header.Flags&netlink.Capped != 0 {
				return kernelObservationTransportError("capped kernel observation error retained payload", nil, true)
			}
			return kernelObservationTransportError("kernel observation error request payload length is invalid", nil, true)
		}
	} else if !validKernelObservationExtendedAck(message.Data[payloadEnd:]) {
		return kernelObservationTransportError("kernel observation extended acknowledgement is invalid", nil, true)
	}
	return kernelObservationTransportError(
		kernelObservationRejectedRequestMessage,
		syscall.Errno(-int64(errno)),
		true,
	)
}

// Extended acknowledgement attributes are diagnostics only. Their bytes cannot change the admitted errno or request.
func validKernelObservationExtendedAck(attributes []byte) bool {
	if len(attributes) == 0 {
		return false
	}
	for len(attributes) > 0 {
		if len(attributes) < 4 {
			return false
		}
		attributeBytes := int(binary.NativeEndian.Uint16(attributes[0:2]))
		attributeType := binary.NativeEndian.Uint16(attributes[2:4]) & 0x3fff
		alignedBytes := (attributeBytes + 3) &^ 3
		if attributeBytes < 4 || attributeType == 0 || alignedBytes > len(attributes) {
			return false
		}
		for _, padding := range attributes[attributeBytes:alignedBytes] {
			if padding != 0 {
				return false
			}
		}
		attributes = attributes[alignedBytes:]
	}
	return true
}

func validateKernelObservationDone(message netlink.Message, mode kernelObservationReplyMode) error {
	if mode != kernelObservationReplyMultipart || message.Header.Flags != netlink.Multi || len(message.Data) != 4 ||
		binary.NativeEndian.Uint32(message.Data) != 0 {
		return kernelObservationTransportError("kernel observation DONE message is invalid", nil, true)
	}
	return nil
}

func (transport *kernelObservationTransport) closeConnection(ctx context.Context) error {
	if transport == nil || transport.connection == nil {
		return nil
	}
	if ctx == nil {
		return kernelObservationTransportError("kernel observation close context is unavailable", nil, false)
	}
	transport.closeOnce.Do(func() {
		transport.closeErr = transport.connection.Close()
	})
	return transport.closeErr
}

func (transport *kernelObservationTransport) close(ctx context.Context, primary error) error {
	closeErr := transport.closeConnection(ctx)
	if closeErr == nil {
		return primary
	}
	return appendGenerationCleanupError(primary, "close kernel observation connection", closeErr)
}

func kernelObservationTransportError(message string, cause error, outcomeUnknown bool) error {
	var details map[string]any
	if outcomeUnknown {
		details = map[string]any{kernelObservationOutcomeUnknownDetailKey: true}
	}
	return generationAuthorityError(message, cause, details)
}
