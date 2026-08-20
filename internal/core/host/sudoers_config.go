package host

import (
	"context"
	"errors"
	"fmt"
	"os"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

type sudoersConfigDeps struct {
	verifySystemBinary func() error
	readFile           func(string) ([]byte, error)
	writeSudoers       func(context.Context, string, string) error
}

// ConfigureSudoers reconciles the single project-owned sudoers drop-in.
// Its fixed group, path, and content are not caller-selectable.
func (s *Service) ConfigureSudoers(ctx context.Context) (bool, error) {
	return configureSudoers(ctx, sudoersConfigDeps{
		verifySystemBinary: verifyCanonicalSystemBinaryForSudoers,
		readFile:           os.ReadFile,
		writeSudoers:       WriteSudoers,
	})
}

func configureSudoers(ctx context.Context, deps sudoersConfigDeps) (bool, error) {
	if err := deps.verifySystemBinary(); err != nil {
		return false, err
	}
	path := infra.SudoersDropInPath()
	want := GenerateSudoersContent(infra.MVMUnixGroup)
	current, err := deps.readFile(path)
	if err == nil && string(current) == want {
		return false, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, errs.WrapMsg(
			errs.CodePrivilegeSudoers,
			fmt.Sprintf("read managed sudoers policy %s", path),
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	if err := deps.writeSudoers(ctx, path, want); err != nil {
		if errs.AsDomainError(err) != nil {
			return false, err
		}
		return false, errs.WrapMsg(
			errs.CodePrivilegeSudoers,
			fmt.Sprintf("configure managed sudoers policy %s", path),
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return true, nil
}
