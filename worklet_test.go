package barekit_test

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	barekit "github.com/holepunchto/bare-kit-golang"
	"github.com/holepunchto/bare-kit-golang/prebuild"
)

var library = sync.OnceValues(func() (*barekit.Library, error) {
	target, err := prebuild.Target(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return nil, err
	}

	path, err := filepath.Abs(filepath.Join("prebuilds", target, prebuild.Library(target)))
	if err != nil {
		return nil, err
	}

	return barekit.Open(path)
})

func fixture(t *testing.T, name string) string {
	t.Helper()

	filename, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return filename
}

func start(t *testing.T, filename string, opts *barekit.Options) *barekit.Worklet {
	t.Helper()

	lib, err := library()
	if err != nil {
		t.Fatal(err)
	}

	w, err := lib.Start(filename, opts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		w.Close()
	})

	return w
}

func readAll(t *testing.T, w *barekit.Worklet) string {
	t.Helper()

	data, err := io.ReadAll(w)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func write(t *testing.T, w *barekit.Worklet, data string) {
	t.Helper()

	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
}

func TestEchoesIPCData(t *testing.T) {
	w := start(t, fixture(t, "echo.js"), nil)

	write(t, w, "Hello world")

	data := make([]byte, 11)

	if _, err := io.ReadFull(w, data); err != nil {
		t.Fatal(err)
	}

	if string(data) != "Hello world" {
		t.Fatalf("got %q", data)
	}
}

func TestEndsIPCData(t *testing.T) {
	w := start(t, fixture(t, "collect.js"), nil)

	write(t, w, "Hello ")
	write(t, w, "world")

	if err := w.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	if _, err := w.Write([]byte("!")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("got %v", err)
	}

	if data := readAll(t, w); data != "Hello world" {
		t.Fatalf("got %q", data)
	}

	if code := w.Wait(); code != 0 {
		t.Fatalf("got exit code %d", code)
	}
}

func TestPassesArguments(t *testing.T) {
	w := start(t, fixture(t, "argv.js"), &barekit.Options{Args: []string{"foo", "bar"}})

	if data := readAll(t, w); data != `["foo","bar"]` {
		t.Fatalf("got %q", data)
	}
}

func TestRunsSource(t *testing.T) {
	w := start(t, "/app.js", &barekit.Options{Source: []byte("Bare.IPC.end('Hello world')")})

	if data := readAll(t, w); data != "Hello world" {
		t.Fatalf("got %q", data)
	}
}

func TestReportsTheExitCode(t *testing.T) {
	w := start(t, fixture(t, "exit.js"), nil)

	if code := w.Wait(); code != 3 {
		t.Fatalf("got exit code %d", code)
	}

	if data := readAll(t, w); data != "" {
		t.Fatalf("got %q", data)
	}
}

func TestTerminates(t *testing.T) {
	w := start(t, fixture(t, "sleep.js"), nil)

	if err := w.Terminate(); err != nil {
		t.Fatal(err)
	}

	w.Wait()

	if data := readAll(t, w); data != "" {
		t.Fatalf("got %q", data)
	}
}

func TestIgnoresTerminateOnceClosedAndRejectsOtherCalls(t *testing.T) {
	w := start(t, fixture(t, "sleep.js"), nil)

	w.Close()

	if err := w.Terminate(); err != nil {
		t.Fatal(err)
	}

	if err := w.Suspend(-1); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("got %v", err)
	}
}

func TestSuspendsAndResumes(t *testing.T) {
	w := start(t, fixture(t, "lifecycle.js"), nil)

	var events []string

	data := make([]byte, 64)

	for len(events) < 3 {
		n, err := w.Read(data)
		if err != nil {
			t.Fatal(err)
		}

		event := string(data[:n])

		events = append(events, event)

		switch event {
		case "ready":
			err = w.Suspend(0)
		case "suspend":
			err = w.Resume()
		}

		if err != nil {
			t.Fatal(err)
		}
	}

	if !slices.Equal(events, []string{"ready", "suspend", "resume"}) {
		t.Fatalf("got %q", events)
	}
}

func TestClosesWhileReading(t *testing.T) {
	w := start(t, fixture(t, "echo.js"), nil)

	read := make(chan error)

	go func() {
		_, err := w.Read(make([]byte, 1))
		read <- err
	}()

	w.Close()

	if err := <-read; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("got %v", err)
	}

	if _, err := w.Write([]byte("Hello world")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("got %v", err)
	}

	w.Wait()
}

func TestStreamsLargeData(t *testing.T) {
	data := make([]byte, 256*4096)

	for i := range data {
		data[i] = byte(i)
	}

	w := start(t, fixture(t, "collect.js"), nil)

	written := make(chan error)

	go func() {
		_, err := w.Write(data)

		if err == nil {
			err = w.CloseWrite()
		}

		written <- err
	}()

	collected, err := io.ReadAll(w)
	if err != nil {
		t.Fatal(err)
	}

	if err := <-written; err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(collected, data) {
		t.Fatal("collected data differs")
	}
}
