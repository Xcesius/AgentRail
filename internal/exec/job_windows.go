//go:build windows

package execmod

import (
	"fmt"
	osexec "os/exec"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000
	processSetQuota                        = 0x0100
	processTerminate                       = 0x0001
	processSuspendResume                   = 0x0800
	processQueryLimitedInformation         = 0x1000
	createSuspended                        = 0x00000004
)

var (
	kernel32Proc                 = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32Proc.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32Proc.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32Proc.NewProc("AssignProcessToJobObject")
	procOpenProcess              = kernel32Proc.NewProc("OpenProcess")
	procCloseHandle              = kernel32Proc.NewProc("CloseHandle")

	ntdllProc           = syscall.NewLazyDLL("ntdll.dll")
	procNtResumeProcess = ntdllProc.NewProc("NtResumeProcess")
)

type jobObject struct {
	handle syscall.Handle
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func prepareCommandForJob(cmd *osexec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended
}

func newJobObject() (*jobObject, error) {
	r1, _, callErr := procCreateJobObjectW.Call(0, 0)
	if r1 == 0 {
		return nil, fmt.Errorf("CreateJobObjectW: %v", callErr)
	}
	job := &jobObject{handle: syscall.Handle(r1)}
	info := jobObjectExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	r1, _, callErr = procSetInformationJobObject.Call(
		uintptr(job.handle),
		uintptr(jobObjectExtendedLimitInformationClass),
		uintptr(unsafe.Pointer(&info)),
		uintptr(uint32(unsafe.Sizeof(info))),
	)
	if r1 == 0 {
		_ = job.Close()
		return nil, fmt.Errorf("SetInformationJobObject: %v", callErr)
	}
	return job, nil
}

func (j *jobObject) AssignAndResume(pid int) error {
	if j == nil || j.handle == 0 {
		return nil
	}
	processHandle, err := openProcessForJob(pid)
	if err != nil {
		return err
	}
	defer closeHandle(processHandle)

	r1, _, callErr := procAssignProcessToJobObject.Call(uintptr(j.handle), uintptr(processHandle))
	if r1 == 0 {
		return fmt.Errorf("AssignProcessToJobObject: %v", callErr)
	}
	status, _, _ := procNtResumeProcess.Call(uintptr(processHandle))
	if status != 0 {
		return fmt.Errorf("NtResumeProcess: status=0x%x", status)
	}
	return nil
}

func (j *jobObject) Close() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	handle := j.handle
	j.handle = 0
	return closeHandle(handle)
}

func (j *jobObject) CloseAndKill() error {
	return j.Close()
}

func openProcessForJob(pid int) (syscall.Handle, error) {
	r1, _, callErr := procOpenProcess.Call(
		uintptr(processSetQuota|processTerminate|processSuspendResume|processQueryLimitedInformation),
		uintptr(0),
		uintptr(uint32(pid)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("OpenProcess: %v", callErr)
	}
	return syscall.Handle(r1), nil
}

func closeHandle(handle syscall.Handle) error {
	r1, _, callErr := procCloseHandle.Call(uintptr(handle))
	if r1 == 0 {
		return fmt.Errorf("CloseHandle: %v", callErr)
	}
	return nil
}
