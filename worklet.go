package barekit

import (
	"io"
	"math"
	"sync"
	"time"
)

type options struct {
	version     int32
	memoryLimit uintptr
	platform    [8]uintptr
}

// Options are the options of Library.Start.
type Options struct {
	// Args are passed to the worklet as `Bare.argv`.
	Args []string

	// Source is the source of the module, which is read from the filename if
	// nil.
	Source []byte

	// MemoryLimit is the memory limit of each JavaScript heap in bytes, which
	// is inferred from the amount of physical memory of the device if 0.
	MemoryLimit int

	// Android holds the options that apply on Android.
	Android AndroidOptions
}

// AndroidOptions are the options of a worklet that apply on Android.
type AndroidOptions struct {
	// JavaVM is the Java virtual machine, as a `JavaVM *`, which defaults to
	// the one that loaded Bare Kit through `JNI_OnLoad()`.
	JavaVM uintptr

	// Context is the application context, as a JNI reference to an
	// `android.content.Context`, which need only be valid for the call.
	Context uintptr
}

// A Worklet is a Bare runtime running on a thread of its own, connected to
// its `Bare.IPC`.
//
// Reading yields the data that the worklet writes to `Bare.IPC` until it ends
// its side of the connection, and writing sends data to it. One goroutine may
// read while another writes. CloseWrite ends the data sent to the worklet,
// while Close closes the connection and terminates the worklet.
type Worklet struct {
	lib *Library

	handle uintptr
	ipc    uintptr
	poll   uintptr

	readMu  sync.Mutex
	writeMu sync.Mutex

	mu     sync.Mutex
	events int32
	closed bool

	readable chan struct{}
	writable chan struct{}
	done     chan struct{}

	exitID   uintptr
	exited   chan struct{}
	exitCode int
}

// Start starts filename in a worklet with an IPC connection to it.
func (l *Library) Start(filename string, opts *Options) (*Worklet, error) {
	if opts == nil {
		opts = &Options{}
	}

	o := options{memoryLimit: uintptr(opts.MemoryLimit)}

	o.platform[0] = opts.Android.JavaVM
	o.platform[1] = opts.Android.Context

	w := &Worklet{
		lib:      l,
		readable: make(chan struct{}, 1),
		writable: make(chan struct{}, 1),
		done:     make(chan struct{}),
		exitID:   uintptr(exitID.Add(1)),
		exited:   make(chan struct{}),
	}

	if err := check(l.workletCreate(&o, &w.handle)); err != nil {
		return nil, err
	}

	if err := check(l.ipcCreate(w.handle, &w.ipc)); err != nil {
		l.workletDestroy(w.handle)

		return nil, err
	}

	if err := check(l.pollCreate(w.ipc, &w.poll)); err != nil {
		l.ipcDestroy(w.ipc)
		l.workletDestroy(w.handle)

		return nil, err
	}

	onExit, _ := callbacks()

	polls.Store(w.poll, w)
	exits.Store(w.exitID, w)

	l.workletOnExit(w.handle, onExit, w.exitID)

	argv := make([]*byte, len(opts.Args)+1)

	for i, arg := range opts.Args {
		argv[i] = &append([]byte(arg), 0)[0]
	}

	status := l.workletStart(w.handle, filename, opts.Source, uintptr(len(opts.Source)), int32(len(opts.Args)), &argv[0])

	if err := check(status); err != nil {
		exits.Delete(w.exitID)

		w.Close()

		return nil, err
	}

	return w, nil
}

// Read reads data that the worklet has written to `Bare.IPC`, returning
// io.EOF once it has ended its side of the connection.
func (w *Worklet) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	w.readMu.Lock()
	defer w.readMu.Unlock()

	n, err := w.wait(readable, func() int32 {
		return w.lib.ipcRead(w.ipc, b, uintptr(len(b)))
	})

	if err == nil && n == 0 {
		err = io.EOF
	}

	return int(n), err
}

// Write sends data to `Bare.IPC` of the worklet, returning io.ErrClosedPipe
// once the connection has ended or closed.
func (w *Worklet) Write(b []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	written := 0

	for written < len(b) {
		rest := b[written:]

		n, err := w.wait(writable, func() int32 {
			return w.lib.ipcWrite(w.ipc, rest, uintptr(len(rest)))
		})

		if err == nil && n == failed {
			err = io.ErrClosedPipe
		}

		if err != nil {
			return written, err
		}

		written += int(n)
	}

	return written, nil
}

// CloseWrite ends the data sent to the worklet, after which `Bare.IPC` reads
// end-of-stream while reading continues.
func (w *Worklet) CloseWrite() error {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	return w.do(func() int32 {
		w.lib.ipcEnd(w.ipc)

		return 0
	})
}

// Close closes the connection, terminating the worklet if it is still
// running, and releases its resources. Afterwards, Terminate does nothing and
// Suspend, Resume and Wakeup return io.ErrClosedPipe, while Wait still returns
// once the worklet has exited.
func (w *Worklet) Close() error {
	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()

		return nil
	}

	w.closed = true

	close(w.done)

	w.lib.ipcClose(w.ipc)

	w.mu.Unlock()

	// Destroyed without holding the lock, as destroying the poll waits for a
	// callback that may be running.
	w.lib.pollDestroy(w.poll)

	polls.CompareAndDelete(w.poll, w)

	w.lib.ipcDestroy(w.ipc)
	w.lib.workletDestroy(w.handle)

	return nil
}

// Wait waits for the worklet to exit and returns its exit code.
func (w *Worklet) Wait() int {
	<-w.exited

	return w.exitCode
}

// Suspend suspends the worklet, emitting `suspend` on `Bare`. It keeps running
// for linger before going idle, or for as long as the platform allows if
// linger is negative.
func (w *Worklet) Suspend(linger time.Duration) error {
	return w.do(func() int32 {
		return w.lib.workletSuspend(w.handle, milliseconds(linger))
	})
}

// Resume resumes a suspended worklet, emitting `resume` on `Bare`.
func (w *Worklet) Resume() error {
	return w.do(func() int32 {
		return w.lib.workletResume(w.handle)
	})
}

// Wakeup wakes up a suspended worklet, emitting `wakeup` on `Bare`. It runs
// for deadline before going idle again, or for as long as the platform allows
// if deadline is negative.
func (w *Worklet) Wakeup(deadline time.Duration) error {
	return w.do(func() int32 {
		return w.lib.workletWakeup(w.handle, milliseconds(deadline))
	})
}

// Terminate asks the worklet to exit unless it has already exited or been
// closed.
func (w *Worklet) Terminate() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return nil
	}

	return check(w.lib.workletTerminate(w.handle))
}

func (w *Worklet) do(fn func() int32) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return io.ErrClosedPipe
	}

	return check(fn())
}

// Calls op until it no longer would block, polling for event in between.
func (w *Worklet) wait(event int32, op func() int32) (int32, error) {
	ready := w.readable

	if event == writable {
		ready = w.writable
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	defer w.update(event, false)

	for {
		if w.closed {
			return 0, io.ErrClosedPipe
		}

		if n := op(); n != wouldBlock {
			return n, nil
		}

		w.update(event, true)

		w.mu.Unlock()

		select {
		case <-ready:
		case <-w.done:
		}

		w.mu.Lock()
	}
}

func (w *Worklet) update(event int32, enabled bool) {
	events := w.events &^ event

	if enabled {
		events |= event
	}

	if w.closed || events == w.events {
		return
	}

	w.events = events

	if events == 0 {
		w.lib.pollStop(w.poll)
	} else {
		_, onPoll := callbacks()

		w.lib.pollStart(w.poll, events, onPoll)
	}
}

func (w *Worklet) ready(events int32) {
	if events&readable != 0 {
		notify(w.readable)
	}

	if events&writable != 0 {
		notify(w.writable)
	}
}

func (w *Worklet) exit(code int) {
	w.exitCode = code

	close(w.exited)
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func milliseconds(d time.Duration) int32 {
	if d < 0 {
		return -1
	}

	return int32(min(d.Milliseconds(), math.MaxInt32))
}
