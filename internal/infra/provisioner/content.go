// Package provisioner — re-exports from provisionercontent for backward compat.
//
// IMPORTANT: New code should import "mvmctl/internal/infra/provisionercontent"
// directly to avoid circular import issues between guestfs/provisioner.go
// and provisioner/backend.go.
package provisioner

import (
	"mvmctl/internal/infra/provisionercontent"
)

// Re-exported types from provisionercontent.
type (
	FileOp       = provisionercontent.FileOp
	ChrootOp     = provisionercontent.ChrootOp
	CopyDirOp    = provisionercontent.CopyDirOp
	ResizeOp     = provisionercontent.ResizeOp
	ResizeAction = provisionercontent.ResizeAction
	Operation    = provisionercontent.Operation
)

// Re-export constants.
const (
	ResizeActionGrow   = provisionercontent.ResizeActionGrow
	ResizeActionShrink = provisionercontent.ResizeActionShrink
	FileOpDefaultMode  = provisionercontent.FileOpDefaultMode
)

// Re-export variables.
var (
	CloudInitDisableDatasource = provisionercontent.CloudInitDisableDatasource
	CloudInitDisabledMarker    = provisionercontent.CloudInitDisabledMarker
	SnapdOverride              = provisionercontent.SnapdOverride
	NetworkdWaitOverride       = provisionercontent.NetworkdWaitOverride
)

// ProvisionerContent re-export.
type ProvisionerContent struct {
	provisionercontent.ProvisionerContent
}

func (pc ProvisionerContent) SSHDConfig(user string) string {
	return pc.ProvisionerContent.SSHDConfig(user)
}

func (ProvisionerContent) FirstBootInstaller() string {
	return provisionercontent.ProvisionerContent{}.FirstBootInstaller()
}

func (ProvisionerContent) FirstBootService() string {
	return provisionercontent.ProvisionerContent{}.FirstBootService()
}

func (ProvisionerContent) Hosts(hostname string) string {
	return provisionercontent.ProvisionerContent{}.Hosts(hostname)
}

func (pc ProvisionerContent) BuildHostnameOps(hostname string) []Operation {
	return pc.ProvisionerContent.BuildHostnameOps(hostname)
}

func (ProvisionerContent) BuildDNSOps(dnsServer string) []Operation {
	return provisionercontent.ProvisionerContent{}.BuildDNSOps(dnsServer)
}

func (pc ProvisionerContent) BuildSSHOps(user string, sshPubkeys []string) []Operation {
	return pc.ProvisionerContent.BuildSSHOps(user, sshPubkeys)
}

func (ProvisionerContent) BuildCloudInitDisableOps() []Operation {
	return provisionercontent.ProvisionerContent{}.BuildCloudInitDisableOps()
}

func (ProvisionerContent) BuildCloudInitInjectOps(cloudInitDir string) []Operation {
	return provisionercontent.ProvisionerContent{}.BuildCloudInitInjectOps(cloudInitDir)
}

func (ProvisionerContent) BuildResizeOps(targetSizeBytes int64) []Operation {
	return provisionercontent.ProvisionerContent{}.BuildResizeOps(targetSizeBytes)
}

func (ProvisionerContent) BuildShrinkOps(limitBytes int64) []Operation {
	return provisionercontent.ProvisionerContent{}.BuildShrinkOps(limitBytes)
}

func (pc ProvisionerContent) BuildDeblobOps(osType string) []Operation {
	return pc.ProvisionerContent.BuildDeblobOps(osType)
}

func (ProvisionerContent) BuildFixFstabOps() []Operation {
	return provisionercontent.ProvisionerContent{}.BuildFixFstabOps()
}

// Re-exported helpers.
var (
	JoinLines       = provisionercontent.JoinLines
	MaskServicePath = provisionercontent.MaskServicePath
)
