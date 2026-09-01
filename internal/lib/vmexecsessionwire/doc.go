// Package vmexecsessionwire provides the closed retained-authority VM-exec sidecar protocol.
// Layer: Lib — owns bounded framing and validation but no process, policy, relay, or authority effects.
// DomainError details use map[string]any because that project error type is an intentional sum type; this package
// projects it to a smaller bool-and-int64-only wire form.
package vmexecsessionwire
