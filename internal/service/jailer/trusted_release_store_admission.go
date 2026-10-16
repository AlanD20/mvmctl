package jailer

import "context"

type trustedReleaseDirectoryAdmission struct {
	manifest    trustedReleaseManifest
	identity    releaseIdentity
	executables *trustedReleaseExecutables
}

// CRITICAL: Installed releases and unpublished candidates cross this same seam. The returned value binds the strict
// manifest, its derived release identity, and the exact read-only executable descriptors admitted from one pinned
// directory.
func (directory *trustedReleaseDirectory) admit(
	ctx context.Context,
) (_ *trustedReleaseDirectoryAdmission, returnErr error) {
	manifest, err := directory.readManifest(ctx)
	if err != nil {
		return nil, err
	}
	executables, err := directory.openExecutables(ctx, manifest)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release directory admission",
			executables.Release(context.WithoutCancel(ctx)),
		)
	}()
	identity, err := manifest.releaseIdentity()
	if err != nil {
		return nil, err
	}
	return &trustedReleaseDirectoryAdmission{
		manifest: manifest, identity: identity, executables: executables,
	}, nil
}

func (admission *trustedReleaseDirectoryAdmission) Release(ctx context.Context) error {
	if admission == nil {
		return nil
	}
	var err error
	if admission.executables != nil {
		err = admission.executables.Release(context.WithoutCancel(ctx))
	}
	admission.executables = nil
	admission.manifest = trustedReleaseManifest{}
	admission.identity = releaseIdentity{}
	return err
}
