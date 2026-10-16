package privileged

import (
	"fmt"
	"os"

	"mvmctl/pkg/errs"
)

const (
	privilegedPATH   = "/usr/sbin:/usr/bin:/sbin:/bin"
	privilegedHOME   = "/root"
	privilegedLocale = "C"
)

type environmentDeps struct {
	clear func()
	set   func(string, string) error
}

func sanitizeEnvironment(deps environmentDeps) error {
	deps.clear()
	fixedValues := [...]struct {
		key   string
		value string
	}{
		{key: "PATH", value: privilegedPATH},
		{key: "HOME", value: privilegedHOME},
		{key: "LANG", value: privilegedLocale},
		{key: "LC_ALL", value: privilegedLocale},
	}
	for _, entry := range fixedValues {
		if err := deps.set(entry.key, entry.value); err != nil {
			return errs.New(
				errs.CodeInternal,
				fmt.Sprintf("set fixed privileged environment %s: %v", entry.key, err),
			)
		}
	}
	return nil
}

func realEnvironmentDeps() environmentDeps {
	return environmentDeps{
		clear: os.Clearenv,
		set:   os.Setenv,
	}
}
