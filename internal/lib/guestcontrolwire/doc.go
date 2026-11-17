// Package guestcontrolwire defines the closed, versioned Guest Control transport.
// Decoding admits request structure only; it never authenticates a VM or grants an operation.
// Session owners must enforce transport deadlines and close blocked connections on cancellation.
package guestcontrolwire
