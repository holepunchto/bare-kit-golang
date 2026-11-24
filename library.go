// Package barekit runs Bare worklets in process through a Bare Kit library
// loaded at runtime.
package barekit

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

const (
	readable = 0x1
	writable = 0x2
)

const (
	wouldBlock = -1
	failed     = -2
)

// An Error is a call into Bare Kit that failed with Status.
type Error struct {
	Status int
}

func (e *Error) Error() string {
	return fmt.Sprintf("barekit: failed with status %d", e.Status)
}

func check(status int32) error {
	if status != 0 {
		return &Error{int(status)}
	}

	return nil
}

// A Library is a loaded Bare Kit library, which stays loaded for as long as
// the process lives.
type Library struct {
	optimizeForMemory func(enabled bool) int32
	teardown          func() int32

	workletCreate    func(options *options, result *uintptr) int32
	workletDestroy   func(worklet uintptr)
	workletOnExit    func(worklet, cb, data uintptr) int32
	workletStart     func(worklet uintptr, filename string, source []byte, len uintptr, argc int32, argv **byte) int32
	workletSuspend   func(worklet uintptr, linger int32) int32
	workletResume    func(worklet uintptr) int32
	workletWakeup    func(worklet uintptr, deadline int32) int32
	workletTerminate func(worklet uintptr) int32

	ipcCreate  func(worklet uintptr, result *uintptr) int32
	ipcClose   func(ipc uintptr)
	ipcEnd     func(ipc uintptr)
	ipcDestroy func(ipc uintptr)
	ipcRead    func(ipc uintptr, data []byte, len uintptr) int32
	ipcWrite   func(ipc uintptr, data []byte, len uintptr) int32

	pollCreate  func(ipc uintptr, result *uintptr) int32
	pollDestroy func(poll uintptr)
	pollStart   func(poll uintptr, events int32, cb uintptr) int32
	pollStop    func(poll uintptr) int32
}

// Open loads the Bare Kit library at path, such as one downloaded with the
// prebuild package. On Android, path may name a library that the application
// bundles.
func Open(path string) (*Library, error) {
	handle, err := open(path)
	if err != nil {
		return nil, err
	}

	lib := &Library{}

	for _, f := range []struct {
		fptr any
		name string
	}{
		{&lib.optimizeForMemory, "bare_worklet_optimize_for_memory"},
		{&lib.teardown, "bare_worklet_teardown"},
		{&lib.workletCreate, "bare_worklet_create"},
		{&lib.workletDestroy, "bare_worklet_destroy"},
		{&lib.workletOnExit, "bare_worklet_on_exit"},
		{&lib.workletStart, "bare_worklet_start"},
		{&lib.workletSuspend, "bare_worklet_suspend"},
		{&lib.workletResume, "bare_worklet_resume"},
		{&lib.workletWakeup, "bare_worklet_wakeup"},
		{&lib.workletTerminate, "bare_worklet_terminate"},
		{&lib.ipcCreate, "bare_ipc_create"},
		{&lib.ipcClose, "bare_ipc_close"},
		{&lib.ipcEnd, "bare_ipc_end"},
		{&lib.ipcDestroy, "bare_ipc_destroy"},
		{&lib.ipcRead, "bare_ipc_read"},
		{&lib.ipcWrite, "bare_ipc_write"},
		{&lib.pollCreate, "bare_ipc_poll_create"},
		{&lib.pollDestroy, "bare_ipc_poll_destroy"},
		{&lib.pollStart, "bare_ipc_poll_start"},
		{&lib.pollStop, "bare_ipc_poll_stop"},
	} {
		sym, err := symbol(handle, f.name)
		if err != nil {
			return nil, err
		}

		purego.RegisterFunc(f.fptr, sym)
	}

	return lib, nil
}

// OptimizeForMemory trades performance for memory. Must be configured before
// starting the first worklet.
func (l *Library) OptimizeForMemory(enabled bool) error {
	return check(l.optimizeForMemory(enabled))
}

// Teardown tears down the state shared by the worklets of the process.
//
// Every worklet must have been closed first. Must be called at most once,
// after which starting a worklet fails. A process that runs worklets for as
// long as it lives need not call it.
func (l *Library) Teardown() error {
	return check(l.teardown())
}

// The callbacks are created once per process, as they are never released.
var callbacks = sync.OnceValues(func() (onExit, onPoll uintptr) {
	return purego.NewCallback(handleExit), purego.NewCallback(handlePoll)
})

// Exits are found by an ID of their own, as a destroyed worklet may exit after
// a new worklet has taken its handle, while polls are found by their handle,
// as destroying a poll waits for its callback.
var (
	exits  sync.Map
	exitID atomic.Uint64
	polls  sync.Map
)

func handleExit(code, data uintptr) uintptr {
	if w, ok := exits.LoadAndDelete(data); ok {
		w.(*Worklet).exit(int(int32(code)))
	}

	return 0
}

func handlePoll(poll, events uintptr) uintptr {
	if w, ok := polls.Load(poll); ok {
		w.(*Worklet).ready(int32(events))
	}

	return 0
}
