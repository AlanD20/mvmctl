package network

import (
	"math"
	"time"

	"mvmctl/internal/service/firewall"
)

type FloatingIPGuestOwnership string

const (
	FloatingIPGuestUnassociated FloatingIPGuestOwnership = "unassociated"
	FloatingIPGuestSelf         FloatingIPGuestOwnership = "self"
	FloatingIPGuestOther        FloatingIPGuestOwnership = "other"
)

// FloatingIPGuestResult contains only the public resource projection relative to the admitted guest.
// A remaining duration is measured at observation and never restarts on response receipt.
type FloatingIPGuestResult struct {
	ResourceID           string
	IPv4                 string
	Generation           uint64
	Ownership            FloatingIPGuestOwnership
	LeaseRemainingMillis uint64
}

func floatingIPGuestResult(
	record floatingIPRecord, principal floatingIPGuestPrincipal, observed firewall.FloatingIPLeaseObservation,
) (FloatingIPGuestResult, error) {
	expected := floatingIPFirewallBinding(record, record.Owner)
	if observed.Binding != expected {
		return FloatingIPGuestResult{}, networkAuthorityCorruptError(
			"floating-IP root and firewall ownership disagree",
			nil,
		)
	}
	result := FloatingIPGuestResult{
		ResourceID: record.ID, IPv4: record.IPv4, Generation: record.Generation, Ownership: FloatingIPGuestUnassociated,
	}
	if !observed.Present || observed.Remaining <= 0 {
		return result, nil
	}
	millis := observed.Remaining / time.Millisecond
	if millis <= 0 || uint64(millis) > uint64(math.MaxInt64/int64(time.Millisecond)) {
		return FloatingIPGuestResult{}, networkAuthorityCorruptError(
			"floating-IP remaining lease cannot be represented",
			nil,
		)
	}
	result.Ownership = FloatingIPGuestOther
	if principal.owns(record.Owner) {
		result.Ownership = FloatingIPGuestSelf
	}
	result.LeaseRemainingMillis = uint64(observed.Remaining / time.Millisecond)
	return result, nil
}
