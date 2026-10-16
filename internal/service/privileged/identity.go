package privileged

import (
	"fmt"
	"math"
	"os"
	"strconv"

	"mvmctl/pkg/errs"
)

type callerIdentity struct {
	uid uint32
	gid uint32
}

type identityDeps struct {
	effectiveUID func() int
	lookupEnv    func(string) (string, bool)
}

func invokingIdentity(deps identityDeps) (callerIdentity, error) {
	if deps.effectiveUID() != 0 {
		return callerIdentity{}, errs.New(
			errs.CodePrivilegeRequired,
			"privileged protocol requires effective UID 0",
		)
	}

	uid, err := parseNonRootIdentity("SUDO_UID", deps.lookupEnv)
	if err != nil {
		return callerIdentity{}, err
	}
	gid, err := parseNonRootIdentity("SUDO_GID", deps.lookupEnv)
	if err != nil {
		return callerIdentity{}, err
	}
	return callerIdentity{uid: uid, gid: gid}, nil
}

func parseNonRootIdentity(name string, lookupEnv func(string) (string, bool)) (uint32, error) {
	value, ok := lookupEnv(name)
	if !ok || value == "" {
		return 0, errs.New(errs.CodePrivilegeRequired, fmt.Sprintf("missing %s in sudo execution context", name))
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == math.MaxUint32 {
		return 0, errs.New(errs.CodePrivilegeRequired, fmt.Sprintf("invalid %s in sudo execution context", name))
	}
	if parsed == 0 {
		kind := "user"
		if name == "SUDO_GID" {
			kind = "group"
		}
		return 0, errs.New(
			errs.CodePrivilegeRequired,
			fmt.Sprintf("%s must identify a non-root %s", name, kind),
		)
	}
	return uint32(parsed), nil
}

func realIdentityDeps() identityDeps {
	return identityDeps{
		effectiveUID: os.Geteuid,
		lookupEnv:    os.LookupEnv,
	}
}
