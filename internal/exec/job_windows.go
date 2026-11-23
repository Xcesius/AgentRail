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
	// Only KILL_ON_JOB_CLOSE is enabled; BREAKAWAY_OK and SILENT_BREAKAWAY_OK
	// are intentionally omitted so child processes cannot escape the Job Object
	// via CREATE_BREAKAWAY_FROM_JOB.
	jobObjectLimitKillOnJobClose   = 0x00002000
	processSetQuota                = 0x0100
	processTerminate               = 0x0001
	processSuspendResume           = 0x0800
	processQueryLimitedInformation = 0x1000
	createSuspended                = 0x00000004

	th32csSnapThread    = 0x00000004
	threadSuspendResume = 0x0002
)

var (
	kernel32Proc                 = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32Proc.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32Proc.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32Proc.NewProc("AssignProcessToJobObject")
	procOpenProcess              = kernel32Proc.NewProc("OpenProcess")
	procCloseHandle              = kernel32Proc.NewProc("CloseHandle")
	procCreateToolhelp32Snapshot = kernel32Proc.NewProc("CreateToolhelp32Snapshot")
	procThread32First            = kernel32Proc.NewProc("Thread32First")
	procThread32Next             = kernel32Proc.NewProc("Thread32Next")
	procOpenThread               = kernel32Proc.NewProc("OpenThread")
	procResumeThread             = kernel32Proc.NewProc("ResumeThread")

	// NtResumeProcess is an undocumented ntdll.dll native API used as the fast
	// single-call path to resume a CREATE_SUSPENDED child after Job Object
	// assignment because Go's os/exec does not expose the primary thread handle
	// from CreateProcessW. If NtResumeProcess is unavailable or fails,
	// resumeProcess falls back to the documented Win32 Toolhelp32 + ResumeThread
	// APIs above.
	ntdllProc           = syscall.NewLazyDLL("ntdll.dll")
	procNtResumeProcess = ntdllProc.NewProc("NtResumeProcess")
)

type threadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePri        int32
	DeltaPri       int32
	Flags          uint32
}

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
	return resumeProcess(pid, processHandle)
}

func resumeProcess(pid int, processHandle syscall.Handle) error {
	if err := procNtResumeProcess.Find(); err == nil {
		status, _, _ := procNtResumeProcess.Call(uintptr(processHandle))
		if status == 0 {
			return nil
		}
	}
	return resumeProcessThreadsWin32(uint32(pid))
}

func resumeProcessThreadsWin32(pid uint32) error {
	snap, _, callErr := procCreateToolhelp32Snapshot.Call(uintptr(th32csSnapThread), 0)
	if snap == uintptr(syscall.InvalidHandle) || snap == 0 {
		return fmt.Errorf("CreateToolhelp32Snapshot: %v", callErr)
	}
	defer closeHandle(syscall.Handle(snap))

	var entry threadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r1, _, callErr := procThread32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if r1 == 0 {
		return fmt.Errorf("Thread32First: %v", callErr)
	}
	resumed := 0
	for {
		if entry.OwnerProcessID == pid {
			th, _, openErr := procOpenThread.Call(uintptr(threadSuspendResume), 0, uintptr(entry.ThreadID))
			if th == 0 {
				return fmt.Errorf("OpenThread: %v", openErr)
			}
			prevCount, _, resErr := procResumeThread.Call(th)
			_ = closeHandle(syscall.Handle(th))
			if prevCount == ^uintptr(0) {
				return fmt.Errorf("ResumeThread: %v", resErr)
			}
			resumed++
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		r1, _, _ = procThread32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if r1 == 0 {
			break
		}
	}
	if resumed == 0 {
		return fmt.Errorf("no threads found to resume for pid %d", pid)
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
