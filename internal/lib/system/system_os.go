package system

import (
	"os"
	"os/exec"
	"os/user"
)

// OSProvider abstracts OS-level operations used by privilege/group/process checks.
// The real implementation delegates to stdlib os/user/exec.
// Tests can inject a fake to avoid depending on the actual OS environment.
type OSProvider interface {
	Geteuid() int
	Getegid() int
	Getgid() int
	Getgroups() ([]int, error)
	LookupGroup(name string) (*user.Group, error)
	Current() (*user.User, error)
	LookPath(file string) (string, error)
	Stat(name string) (os.FileInfo, error)
	FindProcess(pid int) (*os.Process, error)
	IsNotExist(err error) bool
}

// realOS delegates to stdlib functions.
type realOS struct{}

func (realOS) Geteuid() int                                   { return os.Geteuid() }
func (realOS) Getegid() int                                   { return os.Getegid() }
func (realOS) Getgid() int                                    { return os.Getgid() }
func (realOS) Getgroups() ([]int, error)                      { return os.Getgroups() }
func (realOS) LookupGroup(name string) (*user.Group, error)   { return user.LookupGroup(name) }
func (realOS) Current() (*user.User, error)                   { return user.Current() }
func (realOS) LookPath(file string) (string, error)           { return exec.LookPath(file) }
func (realOS) Stat(name string) (os.FileInfo, error)          { return os.Stat(name) }
func (realOS) FindProcess(pid int) (*os.Process, error)       { return os.FindProcess(pid) }
func (realOS) IsNotExist(err error) bool                      { return os.IsNotExist(err) }

// DefaultOS is the package-level OS provider.
// Swap this in tests to inject a fake OS environment.
var DefaultOS OSProvider = realOS{}
