// Package privilegedwire encodes and decodes the bounded control frames shared by
// typed privileged clients and receivers. It owns no actions, dispatch, subprocesses,
// or privileged effects.
//
// DomainError details use map[string]any because the project error type defines that
// intentional sum type. This package closes the wire form to bool, string, int64, and
// bounded string slices through an explicit type switch.
package privilegedwire
