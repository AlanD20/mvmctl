package guestfs

// GuestfsNotAvailableError indicates libguestfs/guestfish is not installed.
type GuestfsNotAvailableError struct {
	msg string
}

func (e *GuestfsNotAvailableError) Error() string { return e.msg }

// GuestfsError indicates a guestfs operation failure.
type GuestfsError struct {
	msg string
}

func (e *GuestfsError) Error() string { return e.msg }
