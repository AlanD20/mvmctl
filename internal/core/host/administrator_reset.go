package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/hostadmission"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// AdministratorReset restores recorded host changes only after every owner has
// cleaned its root-authoritative VM/network state. It never kills or removes VMs.
// CRITICAL: exclusive admission survives all guards, restoration, and receipts.
func AdministratorReset(ctx context.Context, operator AdministratorOperator) (returnErr error) {
	if os.Geteuid() != 0 || operator.uid == "" {
		return errs.New(errs.CodePrivilegeRequired, "administrator operator admission is required")
	}
	if err := verifyCanonicalSystemBinaryForSudoers(); err != nil {
		return err
	}
	admission, err := hostadmission.AcquireAdministrator(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = resetFailure(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	dir, err := openAdministratorDirectory(ctx, []string{"var", "lib", "mvmctl", "host"}, -1)
	if err != nil {
		return resetFailure(err, nil)
	}
	defer func() { returnErr = resetFailure(returnErr, dir.Close()) }()
	journal, err := loadAdministratorJournal(dir)
	if err != nil {
		return resetFailure(err, nil)
	}
	if journal == nil {
		return errs.New(errs.CodeHostResetFailed, "no root-owned administrator journal exists")
	}
	if journal.ResetState == "complete" {
		return admission.Disable(ctx)
	}
	if (!journal.Initialized && journal.ResetState != "restoring") || journal.Pending || journal.GroupGID == nil {
		return errs.New(errs.CodeHostResetFailed, "complete interrupted initialization before host reset")
	}
	files, err := openResetFiles(ctx, journal)
	if err != nil {
		return resetFailure(err, nil)
	}
	defer func() { returnErr = resetFailure(returnErr, files.close()) }()
	vmGuard, err := jailer.AcquireHostResetGuard(ctx)
	if err != nil {
		return resetFailure(err, nil)
	}
	defer func() { returnErr = resetFailure(returnErr, vmGuard.Release(context.WithoutCancel(ctx))) }()
	networkGuard, err := network.AcquireHostResetGuard(ctx)
	if err != nil {
		return resetFailure(err, nil)
	}
	defer func() { returnErr = resetFailure(returnErr, networkGuard.Release(context.WithoutCancel(ctx))) }()
	records, err := loadResetEnrollments(ctx, dir, journal)
	if err != nil {
		return resetFailure(err, nil)
	}
	if err := preflightResetGroup(ctx, journal, records); err != nil {
		return resetFailure(err, nil)
	}
	if err := admission.Disable(ctx); err != nil {
		return resetFailure(err, nil)
	}
	journal.ResetState = "restoring"
	if err := saveAdministratorJournal(context.WithoutCancel(ctx), dir, journal); err != nil {
		return resetFailure(err, nil)
	}
	for journal.ResetStep < 4 {
		switch journal.ResetStep {
		case 0:
			err = files.restoreSysctl(ctx, journal)
		case 1:
			err = restoreResetMemberships(ctx, dir, journal, records)
		case 2:
			err = restoreResetGroup(ctx, journal)
		case 3:
			err = files.restoreSudoers(ctx, journal)
		}
		if err != nil {
			return resetFailure(err, nil)
		}
		journal.ResetStep++
		if journal.ResetStep == 4 {
			journal.ResetState = "complete"
			journal.Initialized = false
		}
		if err := saveAdministratorJournal(context.WithoutCancel(ctx), dir, journal); err != nil {
			return resetFailure(err, nil)
		}
	}
	return nil
}

func resetFailure(primary, cleanup error) error {
	if primary == nil && cleanup == nil {
		return nil
	}
	details := map[string]any{"journal_retained": true}
	for _, failure := range []error{primary, cleanup} {
		if domain := errs.AsDomainError(failure); domain != nil {
			for key, value := range domain.Details {
				details[key] = value
			}
		}
	}
	return errs.WrapMsg(errs.CodeHostResetFailed, "restore recorded administrator host state",
		errors.Join(primary, cleanup), errs.WithDetails(details))
}

func requireMarkerOnlyResetPolicy(content string) error {
	want := fmt.Sprintf("%%%s ALL=(root) NOPASSWD: %s %s *",
		infra.MVMUnixGroup, infra.SystemBinaryPath, infra.PrivilegedProtocolMarker)
	found := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#include")) {
			continue
		}
		if line != want {
			return errors.New(
				"host reset requires the marker-only sudoers policy; transitional or unknown grants bypass admission",
			)
		}
		if found {
			return errors.New("duplicate managed sudoers grant")
		}
		found = true
	}
	if !found {
		return errors.New("managed marker-only sudoers grant is missing")
	}
	return nil
}

func loadResetEnrollments(
	ctx context.Context, dir *os.File, journal *administratorJournal,
) ([]*administratorEnrollment, error) {
	var records []*administratorEnrollment
	seenInitial := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if name == "lock" || name == "journal.json" || name == "admission.json" {
				continue
			}
			uid, found := strings.CutPrefix(name, "operator-")
			if !found || !strings.HasSuffix(uid, ".json") {
				return nil, errors.New("unexpected administrator state entry")
			}
			uid = strings.TrimSuffix(uid, ".json")
			account, err := user.LookupId(uid)
			if err != nil {
				return nil, err
			}
			operator := AdministratorOperator{name: account.Username, uid: account.Uid, gid: account.Gid}
			exactName, err := administratorEnrollmentName(operator)
			if err != nil || exactName != name {
				return nil, errors.New("invalid operator enrollment filename")
			}
			record, err := loadAdministratorEnrollment(dir, operator)
			if err != nil || record == nil {
				return nil, errors.Join(err, errors.New("operator enrollment disappeared"))
			}
			if record.UID == journal.UID {
				seenInitial = true
			}
			records = append(records, record)
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if !seenInitial {
		return nil, errors.New("initial operator enrollment receipt is missing")
	}
	slices.SortFunc(records, func(a, b *administratorEnrollment) int { return strings.Compare(a.UID, b.UID) })
	return records, nil
}

func resetGroupSnapshot(ctx context.Context) (gid string, members []string, primaries []string, returnErr error) {
	dir, err := openAdministratorDirectory(ctx, []string{"etc"}, -1)
	if err != nil {
		return "", nil, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, dir.Close()) }()
	groups, err := readAdministratorFile(dir, "group", 0644)
	if err != nil {
		return "", nil, nil, err
	}
	for _, line := range strings.Split(string(groups), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			return "", nil, nil, errors.New("invalid local group database")
		}
		if fields[0] != infra.MVMUnixGroup {
			continue
		}
		if gid != "" {
			return "", nil, nil, errors.New("duplicate local operator group")
		}
		value, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != fields[2] {
			return "", nil, nil, errors.New("invalid operator group identity")
		}
		gid = fields[2]
		if fields[3] != "" {
			members = strings.Split(fields[3], ",")
		}
	}
	if gid == "" {
		return "", nil, nil, nil
	}
	passwd, err := readAdministratorFile(dir, "passwd", 0644)
	if err != nil {
		return "", nil, nil, err
	}
	for _, line := range strings.Split(string(passwd), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			return "", nil, nil, errors.New("invalid local account database")
		}
		if fields[3] == gid {
			primaries = append(primaries, fields[0])
		}
	}
	return gid, members, primaries, nil
}

func preflightResetGroup(ctx context.Context, journal *administratorJournal, records []*administratorEnrollment) error {
	gid, members, primaries, err := resetGroupSnapshot(ctx)
	if err != nil {
		return err
	}
	if gid == "" {
		if journal.ResetState == "restoring" && journal.ResetStep >= 2 && !journal.GroupExisted {
			return nil
		}
		return errors.New("recorded operator group is missing")
	}
	if gid != *journal.GroupGID {
		return errors.New("operator group identity changed after initialization")
	}
	if journal.GroupExisted {
		return nil
	}
	if len(primaries) != 0 {
		return errors.New("created operator group now has primary users; refusing removal")
	}
	for _, member := range members {
		found := false
		for _, record := range records {
			if record.Operator == member && !record.MemberExisted {
				found = true
				break
			}
		}
		if !found {
			return errors.New("created operator group contains unrecorded membership")
		}
	}
	return nil
}

func restoreResetMemberships(
	ctx context.Context,
	dir *os.File,
	journal *administratorJournal,
	records []*administratorEnrollment,
) error {
	for _, record := range records {
		if record.Reverted {
			continue
		}
		operator := AdministratorOperator{name: record.Operator, uid: record.UID, gid: record.GID}
		exists, member, err := administratorMembership(operator)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("operator group disappeared before membership restoration")
		}
		group, err := user.LookupGroup(infra.MVMUnixGroup)
		if err != nil || group.Gid != *journal.GroupGID {
			return errors.Join(err, errors.New("operator group identity changed"))
		}
		if member && !record.MemberExisted {
			if err := runAdministratorCommand(
				ctx,
				[]string{"/usr/bin/gpasswd", "-d", record.Operator, infra.MVMUnixGroup},
			); err != nil {
				return err
			}
		}
		record.Reverted = true
		if err := saveAdministratorEnrollment(context.WithoutCancel(ctx), dir, operator, record); err != nil {
			return err
		}
	}
	return nil
}

func restoreResetGroup(ctx context.Context, journal *administratorJournal) error {
	if journal.GroupExisted {
		return nil
	}
	gid, members, primaries, err := resetGroupSnapshot(ctx)
	if err != nil {
		return err
	}
	if gid == "" {
		return nil
	}
	if gid != *journal.GroupGID || len(members) != 0 || len(primaries) != 0 {
		return errors.New("created operator group identity or membership changed before removal")
	}
	return runAdministratorCommand(ctx, []string{"/usr/sbin/groupdel", infra.MVMUnixGroup})
}
