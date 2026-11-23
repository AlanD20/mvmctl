package host

import (
	"fmt"

	"mvmctl/internal/infra"
)

func verifyCanonicalSystemBinaryForSudoers() error {
	return verifySystemBinaryForSudoers(
		realSystemBinaryInstallDeps(),
		productionSystemBinaryInstallPolicy(),
	)
}

func verifySystemBinaryForSudoers(
	deps systemBinaryInstallDeps,
	policy systemBinaryInstallPolicy,
) (returnErr error) {
	retainedFDs := make([]int, 0, 5)
	defer func() {
		if closeErr := closeSystemBinaryDescriptors(deps, retainedFDs); closeErr != nil {
			returnErr = joinSystemBinaryInstallError(
				returnErr,
				"close canonical system binary verifier descriptor",
				closeErr,
			)
		}
	}()

	rootFD, err := deps.open(policy.rootPath, directoryInstallFlags, 0)
	if err != nil {
		return systemBinaryInstallError("open filesystem root for sudoers verification", err)
	}
	retainedFDs = append(retainedFDs, rootFD)
	if err := verifySystemBinaryInstallDirectory(deps, rootFD, "/", policy); err != nil {
		return err
	}

	usrFD, err := openSystemBinaryInstallDirectory(deps, rootFD, "usr", false, policy)
	if err != nil {
		return err
	}
	retainedFDs = append(retainedFDs, usrFD)
	localFD, err := openSystemBinaryInstallDirectory(deps, usrFD, "local", false, policy)
	if err != nil {
		return err
	}
	retainedFDs = append(retainedFDs, localFD)
	binFD, err := openSystemBinaryInstallDirectory(deps, localFD, "bin", false, policy)
	if err != nil {
		return err
	}
	retainedFDs = append(retainedFDs, binFD)

	targetFD, targetStat, exists, err := inspectExistingSystemBinary(deps, binFD)
	if err != nil {
		return err
	}
	if !exists {
		return systemBinaryInstallError(
			"verify canonical system binary for sudoers",
			fmt.Errorf("%s does not exist", infra.SystemBinaryPath),
		)
	}
	retainedFDs = append(retainedFDs, targetFD)
	if targetStat.Uid != policy.expectedUID || targetStat.Gid != policy.expectedGID ||
		targetStat.Mode&07777 != infra.ExecutablePerm {
		return systemBinaryInstallError(
			"verify canonical system binary for sudoers",
			fmt.Errorf("%s must be root:root with mode 0755", infra.SystemBinaryPath),
		)
	}
	return nil
}
