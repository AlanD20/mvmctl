// Package network owns typed privileged network-namespace topology effects.
//
// Layer: Service. Callers provide only immutable VM/network identities through a held VM launch lease. The package
// derives every host identifier and delegates fixed effects to a root-side adapter; it never accepts interface names,
// namespace paths, command arguments, or generic netlink operations.
package network
