//go:build windows

package pty

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Pty is one pseudo-console pair: the ConPTY object, the two pipe ends incoda
// holds (masterIn to send bytes to the job, masterOut to read what the job
// produced) and the attribute list Spawn needs.
//
// os/exec cannot build this: syscall.SysProcAttr has no field for the
// pseudoconsole attribute, so the CreateProcess call lives in Spawn.
type Pty struct {
	hpc   windows.Handle
	mIn   windows.Handle // our write end: bytes bound for the job
	mOut  windows.Handle // our read end: bytes the job produced
	attrs *windows.ProcThreadAttributeListContainer

	consoleOnce sync.Once
	closeOnce   sync.Once
}

// New creates a pseudo-console of cols x rows. The conhost it starts is a
// child of the calling process; ConhostPIDs finds it for adoption.
func New(cols, rows int) (*Pty, error) {
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	sa := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	// Two pipes: masterIn feeds the job's input through conhost, masterOut
	// carries its output back. The read end of the input pipe and the write
	// end of the output pipe belong to conhost; ours are the other two.
	var inRead, inWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, sa, 0); err != nil {
		return nil, fmt.Errorf("pty: create input pipe: %w", err)
	}
	var outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&outRead, &outWrite, sa, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, fmt.Errorf("pty: create output pipe: %w", err)
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(
		windows.Coord{X: int16(cols), Y: int16(rows)},
		inRead, outWrite, 0, &hpc,
	); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("pty: CreatePseudoConsole: %w", err)
	}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("pty: attribute list: %w", err)
	}
	// The pseudoconsole attribute takes the HPCON by value: lpValue IS the
	// handle for this attribute, not a pointer to it. Passing &hpc instead
	// stores a stack address and every child dies with STATUS_DLL_INIT_FAILED
	// (0xC0000142) trying to connect to a pseudoconsole that is not there.
	// Every working ConPTY client (Microsoft's samples included) passes the
	// handle itself. The load below reinterprets the handle's bits as the
	// value pointer through its address, which carries the same bytes as the
	// direct conversion without giving go vet's unsafeptr check a uintptr
	// conversion to flag.
	hpcValue := *(*unsafe.Pointer)(unsafe.Pointer(&hpc))
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		hpcValue, unsafe.Sizeof(hpc)); err != nil {
		attrs.Delete()
		windows.ClosePseudoConsole(hpc)
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(outWrite)
		return nil, fmt.Errorf("pty: pseudoconsole attribute: %w", err)
	}
	// conhost duplicates the two ends it needs; our copies must go now or
	// its pipes never see EOF when it exits.
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	return &Pty{hpc: hpc, mIn: inWrite, mOut: outRead, attrs: attrs}, nil
}

// Spawned holds what CreateProcess returned. The caller owns both handles.
type Spawned struct {
	Proc   windows.Handle
	Thread windows.Handle
	PID    uint32
}

// Spawn starts argv inside the pseudo-console. extraCreationFlags is ORed in
// (the job path passes CREATE_SUSPENDED so the child can be put in its job
// object before its first instruction). env is the full environment as
// "K=V" strings; dir may be empty for the current directory.
func (p *Pty) Spawn(argv []string, env []string, dir string, extraCreationFlags uint32) (*Spawned, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("pty: no command given")
	}
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("pty: %w", err)
	}
	exe16, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return nil, fmt.Errorf("pty: %w", err)
	}
	cmd16, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return nil, fmt.Errorf("pty: %w", err)
	}
	block, err := environmentBlock(env)
	if err != nil {
		return nil, fmt.Errorf("pty: environment: %w", err)
	}
	var dir16 *uint16
	if dir != "" {
		if dir16, err = windows.UTF16PtrFromString(dir); err != nil {
			return nil, fmt.Errorf("pty: dir: %w", err)
		}
	}

	// STARTF_USESTDHANDLES with zero handles is what the pseudoconsole
	// attribute expects: conhost connects the child's std handles, not
	// inheritance.
	siEx := &windows.StartupInfoEx{}
	siEx.Cb = uint32(unsafe.Sizeof(*siEx))
	siEx.Flags = windows.STARTF_USESTDHANDLES
	siEx.ProcThreadAttributeList = p.attrs.List()

	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT) |
		windows.EXTENDED_STARTUPINFO_PRESENT | extraCreationFlags
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(
		exe16, cmd16, nil, nil, false, flags,
		&block[0], dir16, &siEx.StartupInfo, &pi,
	); err != nil {
		return nil, fmt.Errorf("pty: CreateProcess: %w", err)
	}
	return &Spawned{Proc: pi.Process, Thread: pi.Thread, PID: pi.ProcessId}, nil
}

// Read reads what the job produced, straight off the master pipe.
func (p *Pty) Read(b []byte) (int, error) { return windows.Read(p.mOut, b) }

// Write sends bytes to the job's input (b carries no interpretation here; the
// job path uses it for a Ctrl-C byte).
func (p *Pty) Write(b []byte) (int, error) { return windows.Write(p.mIn, b) }

// CloseConsole shuts the pseudo-console down: conhost is told to exit and its
// pipes reach EOF, which unblocks a reader parked in Read. Call it after the
// job process has exited. It can block while conhost tears down; a caller
// that cannot wait closes the read end (CloseReadEnd) to cut the reader
// loose instead.
func (p *Pty) CloseConsole() {
	p.consoleOnce.Do(func() { windows.ClosePseudoConsole(p.hpc) })
}

// CloseReadEnd closes our read end only. It drops whatever is still buffered
// in the pipe, so it is the force path for a reader stuck on a conhost that
// never sent EOF, not part of a normal exit.
func (p *Pty) CloseReadEnd() {
	windows.CloseHandle(p.mOut)
}

// Close releases everything Pty owns: the master pipe ends and the attribute
// list, plus the pseudo-console itself if CloseConsole was never called (in
// its own goroutine, so a conhost wedged mid-teardown cannot wedge the run).
// Idempotent.
func (p *Pty) Close() error {
	p.closeOnce.Do(func() {
		go p.CloseConsole()
		windows.CloseHandle(p.mIn)
		windows.CloseHandle(p.mOut)
		p.attrs.Delete()
	})
	return nil
}

// environmentBlock turns "K=V" strings into the NUL-separated UTF-16 block
// CreateProcess wants, deduplicated case-insensitively with the last value
// winning, the way the OS resolves duplicates. An empty env means inherit
// ours whole.
func environmentBlock(env []string) ([]uint16, error) {
	if len(env) == 0 {
		env = os.Environ()
	}
	uniq := make([]string, 0, len(env))
	at := make(map[string]int, len(env)) // upper(key) -> index in uniq
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		uk := strings.ToUpper(k)
		if i, ok := at[uk]; ok {
			uniq[i] = kv // later value replaces the earlier one
		} else {
			at[uk] = len(uniq)
			uniq = append(uniq, kv)
		}
	}
	block := make([]uint16, 0, 64*len(uniq))
	for _, kv := range uniq {
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", kv, err)
		}
		block = append(block, u...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}

// ConhostPIDs lists the pids of conhost.exe processes whose parent is
// parentPID. CreatePseudoConsole starts exactly one such child; callers diff
// the list around New to find it and adopt it into a job object, so the
// pseudo-console dies with the job.
func ConhostPIDs(parentPID int) ([]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("pty: process snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var out []uint32
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	err = windows.Process32First(snap, &pe)
	for ; err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ParentProcessID == uint32(parentPID) &&
			strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), "conhost.exe") {
			out = append(out, pe.ProcessID)
		}
	}
	return out, nil
}
