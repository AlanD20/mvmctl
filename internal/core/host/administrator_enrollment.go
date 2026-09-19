package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type administratorEnrollment struct {
	Version       int    `json:"version"`
	Operator      string `json:"operator"`
	UID           string `json:"uid"`
	GID           string `json:"gid"`
	MemberExisted bool   `json:"member_existed"`
	Completed     bool   `json:"completed"`
}

// CALLER MUST HOLD: the global administrator journal lock, including during the
// membership mutation. Each account keeps its own durable pre-enrollment state.
func enrollAdministratorOperator(
	ctx context.Context,
	dir *os.File,
	operator AdministratorOperator,
	journal *administratorJournal,
	result *AdministratorInitResult,
) (*AdministratorInitResult, error) {
	record, err := loadAdministratorEnrollment(dir, operator)
	if err != nil {
		return result, administratorFailure("read operator enrollment", err)
	}
	groupExists, memberExists, err := administratorMembership(operator)
	if err != nil {
		return result, administratorFailure("read operator membership", err)
	}
	if !groupExists {
		return result, errs.New(errs.CodeHostInitFailed,
			"initialized operator group is missing; refusing to repeat global host mutations")
	}
	if record == nil {
		priorMembership := memberExists
		if journal.UID == operator.uid {
			if journal.Operator != operator.name || journal.GID != operator.gid {
				return result, errs.New(errs.CodeHostInitFailed, "initial operator identity changed")
			}
			// The first operator was enrolled by the global transaction. Preserve
			// its original membership even after a crash before this record existed.
			priorMembership = journal.MemberExisted
		}
		record = &administratorEnrollment{
			Version: 1, Operator: operator.name, UID: operator.uid, GID: operator.gid,
			MemberExisted: priorMembership,
		}
		if err := saveAdministratorEnrollment(ctx, dir, operator, record); err != nil {
			return result, administratorFailure("persist operator enrollment intent", err)
		}
	}
	if !memberExists {
		if record.Completed {
			record.Completed = false
			if err := saveAdministratorEnrollment(ctx, dir, operator, record); err != nil {
				return result, administratorFailure("persist renewed operator enrollment intent", err)
			}
		}
		changes, err := applyAdministratorStep(ctx, operator, 1)
		result.Changes = append(result.Changes, changes...)
		if err != nil {
			return result, administratorFailure("enroll operator", err)
		}
		result.UserAddedToGroup = true
	}
	if !record.Completed {
		record.Completed = true
		if err := saveAdministratorEnrollment(context.WithoutCancel(ctx), dir, operator, record); err != nil {
			return result, administratorFailure("persist operator enrollment outcome", err)
		}
	}
	return result, nil
}

func administratorEnrollmentName(operator AdministratorOperator) (string, error) {
	uid, err := strconv.ParseUint(operator.uid, 10, 32)
	if err != nil || uid == 0 || uid == 1<<32-1 || strconv.FormatUint(uid, 10) != operator.uid {
		return "", errors.New("operator enrollment requires a canonical non-root UID")
	}
	return "operator-" + operator.uid + ".json", nil
}

func loadAdministratorEnrollment(dir *os.File, operator AdministratorOperator) (*administratorEnrollment, error) {
	name, err := administratorEnrollmentName(operator)
	if err != nil {
		return nil, err
	}
	data, err := readAdministratorFile(dir, name, 0600)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wire struct {
		Version       int    `json:"version"`
		Operator      string `json:"operator"`
		UID           string `json:"uid"`
		GID           string `json:"gid"`
		MemberExisted *bool  `json:"member_existed"`
		Completed     *bool  `json:"completed"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing operator enrollment data")
	}
	if wire.Version != 1 || wire.UID != operator.uid || wire.GID != operator.gid ||
		wire.Operator != operator.name || wire.MemberExisted == nil || wire.Completed == nil {
		return nil, errors.New("operator enrollment identity mismatch")
	}
	return &administratorEnrollment{
		Version: wire.Version, Operator: wire.Operator, UID: wire.UID, GID: wire.GID,
		MemberExisted: *wire.MemberExisted, Completed: *wire.Completed,
	}, nil
}

func saveAdministratorEnrollment(
	ctx context.Context,
	dir *os.File,
	operator AdministratorOperator,
	record *administratorEnrollment,
) error {
	name, err := administratorEnrollmentName(operator)
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeAdministratorFile(ctx, dir, name, data, 0600)
}
