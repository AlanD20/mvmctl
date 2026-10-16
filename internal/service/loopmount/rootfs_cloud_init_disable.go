package loopmount

import (
	"context"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra/provcontent"
)

// CRITICAL: OFF suppresses only fixed cloud-init configuration and activation leaves beneath
// the admitted guest root. It neither executes guest code nor performs unrelated deblob effects.
func applyRootfsCloudInitDisable(ctx context.Context, tree rootfsGuestTree) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, rootfsAtomicError("disable guest cloud-init", err)
	}
	d := rootfsDeblob{tree: tree}
	for _, directory := range []string{"/etc/cloud", "/etc/cloud/cloud.cfg.d"} {
		if _, err := tree.ensureDirectory(ctx, directory, 0755, 0, 0); err != nil {
			return d.filesWritten, err
		}
	}
	for _, file := range []rootfsProvisionFile{
		{path: "/etc/cloud/cloud.cfg.d/99-disable-datasources.cfg",
			content: provcontent.CloudInitDisableDatasource, mode: 0644},
		{path: "/etc/cloud/cloud-init.disabled", content: provcontent.CloudInitDisabledMarker, mode: 0644},
	} {
		if err := d.write(ctx, file); err != nil {
			return d.filesWritten, err
		}
	}
	systemd, err := tree.fixedPathHasType(ctx, "/etc/systemd/system", unix.S_IFDIR)
	if err != nil {
		return d.filesWritten, err
	}
	if systemd {
		for _, unit := range []string{
			"cloud-init.service", "cloud-init-local.service", "cloud-config.service", "cloud-final.service",
		} {
			if err := d.mask(ctx, unit); err != nil {
				return d.filesWritten, err
			}
		}
	}
	// Alpine enables local initialization in boot; hotplugd does not honor the disable marker.
	// The optional ssh link retains the earlier supported cloud-init placement.
	for _, path := range []string{
		"/etc/runlevels/boot/cloud-init-local",
		"/etc/runlevels/default/cloud-init", "/etc/runlevels/default/cloud-config",
		"/etc/runlevels/default/cloud-final", "/etc/runlevels/default/cloud-init-hotplugd",
		"/etc/runlevels/ssh/cloud-init",
	} {
		if err := ctx.Err(); err != nil {
			return d.filesWritten, rootfsAtomicError("disable guest cloud-init activation", err)
		}
		if err := d.removeRunlevelLink(ctx, path); err != nil {
			return d.filesWritten, err
		}
	}
	return d.filesWritten, nil
}
