// Package vmexecsessionwire provides the closed structural-control protocol for a retained VM-exec sidecar.
// Layer: Lib — owns bounded framing and validation but no process, policy, relay, or privileged effects.
// DomainError details use map[string]any because that project error type is an intentional sum type; this package
// projects it to a smaller bool-and-int64-only wire form.
package vmexecsessionwire
