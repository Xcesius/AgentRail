//go:build !windows

package execmod

import osexec "os/exec"

type jobObject struct{}

func prepareCommandForJob(cmd *osexec.Cmd) {}

func newJobObject() (*jobObject, error) {
	return nil, nil
}

func (j *jobObject) AssignAndResume(pid int) error {
	return nil
}

func (j *jobObject) Close() error {
	return nil
}

func (j *jobObject) CloseAndKill() error {
	return nil
}
