package agent

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

// Successful announcements are local reachability evidence only. They never grant or extend host ownership.
type guestControlAliasAnnouncement struct {
	Generation uint64
	Session    string
	Link       guestControlAliasLink
	Address    guestControlAliasAddress
}

// CALLER MUST HOLD: aliases.mu, after publishing the installed receipt from fresh authenticated ownership.
func (aliases *guestControlAliases) announceOwnedAlias(ctx context.Context, record guestControlAliasReceipt) error {
	current, found := aliases.records[record.Resource]
	if !found || current != record || !record.Installed || record.BootID != aliases.bootID ||
		record.Session != aliases.active {
		return errors.New("alias announcement requires current installed ownership")
	}
	if err := aliases.admitAnnouncementLifetime(ctx, record); err != nil {
		return err
	}
	snapshot, err := aliases.kernel.snapshot(ctx)
	if err != nil {
		return err
	}
	actual, err := ownedGuestControlAlias(snapshot, record)
	if err != nil {
		return err
	}
	if actual == nil {
		return errors.New("owned alias is absent before announcement")
	}
	var link guestControlAliasLink
	for _, candidate := range snapshot.Links {
		if candidate.Index == record.Link.Index && candidate.Name == record.Link.Name &&
			candidate.MAC == record.Link.MAC {
			link = candidate
		}
	}
	identity := guestControlAliasAnnouncement{
		Generation: record.Generation,
		Session:    record.Session,
		Link:       link,
		Address:    *actual,
	}
	identity.Address.Flags = stableGuestControlAliasFlags(actual.Flags)
	if previous, announced := aliases.announced[record.Resource]; announced && previous == identity {
		return nil
	}
	if err := aliases.kernel.announceIPv4(ctx, link, *actual); err != nil {
		return err
	}
	if err := aliases.admitAnnouncementLifetime(ctx, record); err != nil {
		return err
	}
	aliases.announced[record.Resource] = identity
	return nil
}

func (aliases *guestControlAliases) admitAnnouncementLifetime(
	ctx context.Context,
	record guestControlAliasReceipt,
) error {
	if ctx == nil {
		return errors.New("alias announcement context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Lifetime == guestcontrolwire.Finite {
		now, err := guestControlAliasNow()
		if err != nil {
			return err
		}
		if now >= record.Deadline {
			return aliases.expiredResponse(ctx, record, true)
		}
	}
	return nil
}

// marshalGuestControlAliasAnnouncement emits an Ethernet/IPv4 ARP announcement request, never an arbitrary frame.
func marshalGuestControlAliasAnnouncement(
	link guestControlAliasLink,
	address guestControlAliasAddress,
) ([]byte, error) {
	if link.Index <= 0 || link.Name == "" || len(link.Name) >= unix.IFNAMSIZ || link.Flags&unix.IFF_UP == 0 || link.Flags&unix.IFF_LOOPBACK != 0 ||
		link.MAC == ([6]byte{}) || link.MAC[0]&1 != 0 || address.Index != link.Index || !address.IP.Is4() || !address.IP.IsPrivate() || address.Prefix != 32 ||
		address.Scope != unix.RT_SCOPE_UNIVERSE ||
		!strings.HasPrefix(address.Label, link.Name+":") ||
		len(address.Label) > 15 ||
		address.Flags&unix.IFA_F_PERMANENT == 0 ||
		address.Flags&unix.IFA_F_NOPREFIXROUTE == 0 {
		return nil, errors.New("ARP announcement requires an exact active Ethernet NIC and owned private IPv4 alias")
	}
	frame := make([]byte, 42)
	for index := 0; index < 6; index++ {
		frame[index] = 0xff
	}
	copy(frame[6:12], link.MAC[:])
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_ARP)
	binary.BigEndian.PutUint16(frame[14:16], unix.ARPHRD_ETHER)
	binary.BigEndian.PutUint16(frame[16:18], unix.ETH_P_IP)
	frame[18], frame[19] = 6, 4
	binary.BigEndian.PutUint16(frame[20:22], 1)
	copy(frame[22:28], link.MAC[:])
	ipv4 := address.IP.As4()
	copy(frame[28:32], ipv4[:])
	copy(frame[38:42], ipv4[:])
	return frame, nil
}

func (kernel *guestControlAliasNetlink) announceIPv4(
	ctx context.Context,
	link guestControlAliasLink,
	address guestControlAliasAddress,
) (returnErr error) {
	if ctx == nil {
		return errors.New("ARP announcement context is required")
	}
	ctx, cancel := context.WithTimeout(ctx, guestControlAliasIOTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	frame, err := marshalGuestControlAliasAnnouncement(link, address)
	if err != nil {
		return err
	}
	snapshot, err := kernel.snapshot(ctx)
	if err != nil {
		return err
	}
	linkFound, addressFound := false, false
	for _, candidate := range snapshot.Links {
		if candidate == link {
			linkFound = true
		}
	}
	for _, candidate := range snapshot.Addresses {
		candidate.Flags = stableGuestControlAliasFlags(candidate.Flags)
		expected := address
		expected.Flags = stableGuestControlAliasFlags(expected.Flags)
		if candidate == expected {
			addressFound = true
		}
	}
	if !linkFound || !addressFound {
		return errors.New("ARP announcement NIC or owned alias identity changed")
	}
	fd, err := openGuestControlAliasARPSocket(ctx, kernel.namespace, link)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	destination := &unix.SockaddrLinklayer{
		Protocol: guestControlAliasARPProtocol(),
		Ifindex:  link.Index,
		Halen:    6,
		Addr:     [8]uint8{255, 255, 255, 255, 255, 255},
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := unix.SendmsgN(fd, frame, nil, destination, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		if err == nil {
			if count != len(frame) {
				return io.ErrShortWrite
			}
			return ctx.Err()
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return err
		}
		if _, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 25); err != nil &&
			!errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func guestControlAliasARPProtocol() uint16 { return binary.NativeEndian.Uint16([]byte{0x08, 0x06}) }

// A retained socket is opened in the pinned guest namespace on one disposable locked thread.
// A failed restoration retires the thread instead of returning it to the scheduler.
func openGuestControlAliasARPSocket(ctx context.Context, namespace int, link guestControlAliasLink) (int, error) {
	if ctx == nil || namespace <= 0 {
		return -1, errors.New("ARP announcement requires a pinned namespace and context")
	}
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	pinned, err := unix.FcntlInt(uintptr(namespace), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, err
	}
	type result struct {
		fd  int
		err error
	}
	ready := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		fd, original := -1, -1
		restored := false
		var failure error
		defer func() {
			if original >= 0 {
				restoreErr := unix.Setns(original, unix.CLONE_NEWNET)
				if restoreErr == nil {
					restoreErr = verifyGuestControlAliasNamespace(original)
				}
				failure = errors.Join(failure, restoreErr, unix.Close(original))
				restored = restoreErr == nil
			} else {
				restored = true
			}
			failure = errors.Join(failure, unix.Close(pinned), ctx.Err())
			if failure != nil && fd >= 0 {
				failure = errors.Join(failure, unix.Close(fd))
				fd = -1
			}
			if restored {
				runtime.UnlockOSThread()
			}
			ready <- result{fd: fd, err: failure}
		}()
		original, failure = unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if failure != nil {
			return
		}
		if failure = unix.Setns(pinned, unix.CLONE_NEWNET); failure != nil {
			return
		}
		if failure = verifyGuestControlAliasNamespace(pinned); failure != nil {
			return
		}
		if failure = ctx.Err(); failure != nil {
			return
		}
		actual, err := net.InterfaceByIndex(link.Index)
		if err != nil {
			failure = err
			return
		}
		if actual.Name != link.Name || len(actual.HardwareAddr) != 6 ||
			string(actual.HardwareAddr) != string(link.MAC[:]) ||
			actual.Flags&net.FlagUp == 0 ||
			actual.Flags&net.FlagLoopback != 0 {
			failure = errors.New("ARP announcement socket NIC changed")
			return
		}
		fd, failure = unix.Socket(
			unix.AF_PACKET,
			unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK,
			int(guestControlAliasARPProtocol()),
		)
		if failure != nil {
			return
		}
		failure = unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: guestControlAliasARPProtocol(), Ifindex: link.Index})
	}()
	completed := <-ready
	return completed.fd, completed.err
}

func verifyGuestControlAliasNamespace(expected int) error {
	actual, err := unix.Open("/proc/thread-self/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var want, got unix.Stat_t
	inspectErr := errors.Join(unix.Fstat(expected, &want), unix.Fstat(actual, &got))
	closeErr := unix.Close(actual)
	if inspectErr != nil || closeErr != nil {
		return errors.Join(inspectErr, closeErr)
	}
	if want.Dev != got.Dev || want.Ino != got.Ino {
		return errors.New("ARP announcement network namespace restoration differs")
	}
	return nil
}
