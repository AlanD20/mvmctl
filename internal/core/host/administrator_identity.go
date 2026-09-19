package host

import (
	"os"
	"os/user"
	"strconv"
	"strings"

	"mvmctl/pkg/errs"
)

// AdministratorOperator is the enrolled non-root account selected at the early entry.
// Its identity is immutable outside this package.
type AdministratorOperator struct {
	name string
	uid  string
	gid  string
	sudo bool
}

// ResolveAdministratorOperator admits sudo's invoking account or an explicitly
// selected account in a direct-root session, before the environment is cleared.
func ResolveAdministratorOperator(selected string) (AdministratorOperator, error) {
	if os.Geteuid() != 0 {
		return AdministratorOperator{}, errs.New(errs.CodePrivilegeRequired,
			"run sudo /usr/local/bin/mvm host init; direct root requires --operator <non-root-user>")
	}
	sudoUID, hasUID := os.LookupEnv("SUDO_UID")
	sudoGID, hasGID := os.LookupEnv("SUDO_GID")
	sudoName, hasName := os.LookupEnv("SUDO_USER")
	fromSudo := hasUID || hasGID || hasName
	if fromSudo {
		if !hasUID || !hasGID || !hasName || selected != "" {
			return AdministratorOperator{}, errs.New(errs.CodePrivilegeRequired,
				"sudo requires a complete invoking identity and does not accept --operator")
		}
		selected = sudoName
	} else if selected == "" || os.Getuid() != 0 {
		return AdministratorOperator{}, errs.New(errs.CodePrivilegeRequired,
			"direct root requires --operator <non-root-user>")
	}
	if strings.HasPrefix(selected, "-") || strings.ContainsAny(selected, ":/\x00\n\r\t ") {
		return AdministratorOperator{}, errs.New(errs.CodeValidationFailed, "invalid operator account")
	}
	account, err := user.Lookup(selected)
	if err != nil {
		return AdministratorOperator{}, errs.WrapMsg(errs.CodePrivilegeRequired, "resolve operator account", err)
	}
	uid, uidErr := strconv.ParseUint(account.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(account.Gid, 10, 32)
	if uidErr != nil || gidErr != nil || uid == 0 || gid == 0 || uid == 1<<32-1 || gid == 1<<32-1 ||
		account.Username != selected {
		return AdministratorOperator{}, errs.New(errs.CodePrivilegeRequired, "operator must be a non-root account")
	}
	if fromSudo && (sudoUID != account.Uid || sudoGID != account.Gid) {
		return AdministratorOperator{}, errs.New(
			errs.CodePrivilegeRequired,
			"sudo identity does not match operator account",
		)
	}
	return AdministratorOperator{name: account.Username, uid: account.Uid, gid: account.Gid, sudo: fromSudo}, nil
}
