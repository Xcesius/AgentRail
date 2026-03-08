//go:build !windows

package execmod

type jobObject struct{}

func newJobObject() (*jobObject, error) {
	return nil, nil
}

func (j *jobObject) Assign(pid int) error {
	return nil
}

func (j *jobObject) Close() error {
	return nil
}

func (j *jobObject) CloseAndKill() error {
	return nil
}
