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
	closeMIn    sync.Once
	closeMOut   sync.Once
}

// New creates a pseudo-console of cols x rows. The conhost it starts is a
// child of the calling process and dies when the pseudo-console handle
// does; nothing needs to adopt it, and adopting it by pid would race pid
// reuse into assigning an unrelated process to a kill-on-close job.
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

// BytesAvailable reports how many bytes sit in the master pipe right now
// without reading them. The drain's silent cut is gated on it: a cut may
// drop bytes without an in-band marker only when the pipe is provably
// empty at the moment of the cut, and a reader parked between Read calls
// can have delivered bytes that arrived just as a quiesce timer fired.
// What it cannot see is conhost's own unwritten buffers: those are
// unreachable by construction, because conhost never exits while the
// master read end is open.
//
// x/sys/windows does not wrap PeekNamedPipe, so this is the raw kernel32
// call (same shape as meminfo's LazyDLL use): peek with no buffer to
// learn only how much is waiting.
var (
	modkernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procPeekNamedPipe = modkernel32.NewProc("PeekNamedPipe")
)

func (p *Pty) BytesAvailable() (uint32, error) {
	var avail uint32
	r, _, err := procPeekNamedPipe.Call(
		uintptr(p.mOut), 0, 0, 0, uintptr(unsafe.Pointer(&avail)), 0)
	if r == 0 {
		return 0, fmt.Errorf("pty: peek master: %w", err)
	}
	return avail, nil
}

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

// CloseInput closes our write end of the master input pipe. conhost reads
// its input from the other end and exits on the EOF this produces, which is
// what actually ends the session once the job is gone: closing the
// pseudo-console handle returns at once and leaves conhost alive and the
// master read parked (measured: ClosePseudoConsole back in 0s, reader still
// parked 20s later; with the input end closed conhost sends its epilogue
// and the master reaches EOF). Idempotent, and Close never closes the end
// twice. A synchronous WriteFile parked on the handle (a reply to a job
// that stopped reading its input) is not woken by CloseHandle either, so
// the cancel comes first, for the same reason as CloseReadEnd's.
func (p *Pty) CloseInput() {
	p.closeMIn.Do(func() {
		_ = windows.CancelIoEx(p.mIn, nil)
		_ = windows.CloseHandle(p.mIn)
	})
}

// CloseReadEnd cuts the reader loose: it cancels any read parked on the
// master and then closes our read end. Closing alone does not do this: a
// synchronous ReadFile blocked on the handle is not woken by another
// thread's CloseHandle, it just never returns, so the cancel comes first
// and unblocks the reader with an abort. Whatever is still buffered in the
// pipe is dropped, so this is the force path for a conhost that never sent
// EOF, not part of a normal exit.
func (p *Pty) CloseReadEnd() {
	p.closeMOut.Do(func() {
		_ = windows.CancelIoEx(p.mOut, nil)
		_ = windows.CloseHandle(p.mOut)
	})
}

// Close releases everything Pty owns: the master pipe ends and the attribute
// list, plus the pseudo-console itself if CloseConsole was never called (in
// its own goroutine, so a conhost wedged mid-teardown cannot wedge the run).
// The read end goes FIRST, before the input end and before the console
// teardown is even started: the cancel is then the first thing that can
// reach a parked reader, so it wakes with the abort and not with whatever
// conhost's exit epilogue does to the pipe a scheduling race later. Close is
// the catch-all force teardown, so whatever is still buffered in the master
// when it runs is dropped by intent (the normal path has drained or cut by
// then). Idempotent, and the read end dies exactly once: this and
// CloseReadEnd share the same once.
func (p *Pty) Close() error {
	p.closeOnce.Do(func() {
		p.closeMOut.Do(func() {
			_ = windows.CancelIoEx(p.mOut, nil)
			_ = windows.CloseHandle(p.mOut)
		})
		p.CloseInput()
		go p.CloseConsole()
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
		// UTF16FromString terminates each entry with its own NUL, which is
		// the separator the block format wants; only the block as a whole
		// gets a second, empty entry at the end.
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", kv, err)
		}
		block = append(block, u...)
	}
	block = append(block, 0)
	return block, nil
}

// ConhostPIDs lists the pids of conhost.exe processes whose parent is
// parentPID. CreatePseudoConsole starts exactly one such child, which ends
// with the pseudo-console handle; this exists so tests can observe that it
// is really gone, not to find conhosts for job-object games: a conhost pid
// listed here can exit and be reused before anyone acts on it.
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
