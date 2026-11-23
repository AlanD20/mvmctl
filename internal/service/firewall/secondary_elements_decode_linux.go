package firewall

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

// These are kernel observations, not durable ownership or permission to renew a lease.
type secondaryLeaseElement struct {
	lifetime  FloatingIPLifetime
	key       [20]byte
	timeout   time.Duration
	remaining time.Duration
}

func decodeSecondaryLeaseElements(
	messages []netlink.Message,
	sequence *kernelBaseSequence,
) ([]secondaryLeaseElement, error) {
	var elements []secondaryLeaseElement
	seen := make(map[[20]byte]struct{})
	for _, message := range messages {
		family, baseSequence, attrs, err := decodeKernelMessage(message, unix.NFT_MSG_NEWSETELEM,
			netlink.Multi, "secondary lease elements", map[uint16]kernelAttributeRule{
				unix.NFTA_SET_ELEM_LIST_TABLE:    {required: true},
				unix.NFTA_SET_ELEM_LIST_SET:      {required: true},
				unix.NFTA_SET_ELEM_LIST_ELEMENTS: {required: true},
			})
		if err != nil {
			return nil, err
		}
		table, err := kernelAttributeString(attrs[unix.NFTA_SET_ELEM_LIST_TABLE], "secondary lease elements table")
		if err != nil {
			return nil, err
		}
		name, err := kernelAttributeString(attrs[unix.NFTA_SET_ELEM_LIST_SET], "secondary lease elements set")
		if err != nil {
			return nil, err
		}
		if family != nftables.TableFamilyBridge || table != "mvmctl_bridge" || name != secondaryLeaseSetName {
			return nil, invalidKernelObservation("secondary lease elements identity is invalid")
		}
		if err := sequence.observe(baseSequence); err != nil {
			return nil, err
		}
		// Element lists legitimately exceed the generic 64-field metadata limit. Walk
		// each framed element separately, applying the aggregate object cap first.
		raw := attrs[unix.NFTA_SET_ELEM_LIST_ELEMENTS].Data
		for len(raw) != 0 {
			if len(elements) >= maxKernelObservationRules {
				return nil, invalidKernelObservation("secondary lease element count exceeds limit")
			}
			if len(raw) < 4 {
				return nil, invalidKernelObservation("secondary lease element header is truncated")
			}
			length := int(binary.NativeEndian.Uint16(raw[:2]))
			aligned := (length + 3) &^ 3
			if length < 4 || aligned > len(raw) {
				return nil, invalidKernelObservation("secondary lease element length is invalid")
			}
			wrappers, err := decodeKernelAttributeList(raw[:aligned], "secondary lease element wrapper")
			if err != nil {
				return nil, err
			}
			wrapper := wrappers[0]
			if kernelAttributeType(wrapper) != unix.NFTA_LIST_ELEM || kernelAttributeFlags(wrapper) != 0 {
				return nil, invalidKernelObservation("secondary lease element wrapper is invalid")
			}
			element, err := decodeSecondaryLeaseElement(wrapper.Data)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[element.key]; exists {
				return nil, invalidKernelObservation("secondary lease element key is duplicated")
			}
			seen[element.key] = struct{}{}
			elements = append(elements, element)
			raw = raw[aligned:]
		}
	}
	return elements, nil
}

func decodeSecondaryLeaseElement(raw []byte) (secondaryLeaseElement, error) {
	var element secondaryLeaseElement
	attrs, err := decodeUniqueKernelAttributes(raw, "secondary lease element", map[uint16]kernelAttributeRule{
		unix.NFTA_SET_ELEM_KEY:        {required: true},
		unix.NFTA_SET_ELEM_TIMEOUT:    {},
		unix.NFTA_SET_ELEM_EXPIRATION: {},
		unix.NFTA_SET_ELEM_PAD:        {zeroLength: true},
	})
	if err != nil {
		return element, err
	}
	key, err := decodeKernelDataValue(attrs[unix.NFTA_SET_ELEM_KEY].Data, "secondary lease element key")
	if err != nil {
		return element, err
	}
	if _, _, err := decodeSecondaryLeaseKey(key); err != nil {
		return element, err
	}
	copy(element.key[:], key)
	_, timed := attrs[unix.NFTA_SET_ELEM_TIMEOUT]
	_, expiring := attrs[unix.NFTA_SET_ELEM_EXPIRATION]
	if !timed && !expiring {
		element.lifetime = FloatingIPPersistent
		return element, nil
	}
	if !timed || !expiring {
		return element, invalidKernelObservation("secondary lease element has incomplete lifetime metadata")
	}
	element.lifetime = FloatingIPFinite
	timeout, err := kernelAttributeUint64(attrs[unix.NFTA_SET_ELEM_TIMEOUT], "secondary lease element timeout")
	if err != nil {
		return element, err
	}
	remaining, err := kernelAttributeUint64(attrs[unix.NFTA_SET_ELEM_EXPIRATION], "secondary lease element expiration")
	if err != nil {
		return element, err
	}
	if timeout == 0 || timeout > uint64(math.MaxInt64/int64(time.Millisecond)) || remaining > timeout {
		return element, invalidKernelObservation("secondary lease element timeout or expiration is invalid")
	}
	copy(element.key[:], key)
	element.timeout = time.Duration(timeout) * time.Millisecond
	element.remaining = time.Duration(remaining) * time.Millisecond
	return element, nil
}
