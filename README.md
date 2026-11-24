# bare-kit-golang

Go bindings for [Bare Kit](https://github.com/holepunchto/bare-kit), which runs Bare in a thread of its own within the application, known as a worklet, with an IPC to it exposed as `Bare.IPC` inside the worklet.

```
go get github.com/holepunchto/bare-kit-golang
```

## Usage

```go
package main

import (
	_ "embed"
	"fmt"
	"io"

	barekit "github.com/holepunchto/bare-kit-golang"
)

//go:embed app.bundle
var bundle []byte

func main() {
	lib, err := barekit.Open("path/to/libbare-kit.dylib")
	if err != nil {
		panic(err)
	}

	w, err := lib.Start("/app.bundle", &barekit.Options{Source: bundle})
	if err != nil {
		panic(err)
	}

	defer w.Close()

	if _, err := w.Write([]byte("Hello world")); err != nil {
		panic(err)
	}

	data, err := io.ReadAll(w)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))
}
```

Where `app.bundle` is a Bare bundle using `Bare.IPC`:

```js
Bare.IPC.on('data', (data) => Bare.IPC.end(data))
```

Reading a worklet yields the data that it writes to `Bare.IPC` until it ends its side, after which `Read()` returns `io.EOF`. As `Bare.IPC` keeps the worklet alive, `CloseWrite()` ends the data sent to it so that it may exit on its own, and `Wait()` returns its exit code. Writes return `io.ErrClosedPipe` once the data sent has ended or the worklet has closed its side. Closing a worklet terminates it, so keep it open for as long as it should run. Once closed, `Terminate()` does nothing and `Suspend()`, `Resume()` and `Wakeup()` return `io.ErrClosedPipe`.

These bindings are a thin layer over [Bare Kit](https://github.com/holepunchto/bare-kit), whose README covers the [lifecycle](https://github.com/holepunchto/bare-kit#lifecycle), [IPC](https://github.com/holepunchto/bare-kit#ipc), [suspension](https://github.com/holepunchto/bare-kit#suspension), [addons](https://github.com/holepunchto/bare-kit#addons), and [error handling](https://github.com/holepunchto/bare-kit#error-handling) of a worklet. In Go terms:

- If `Options.Source` is `nil`, `Start()` reads `filename` at runtime, so the worklet may be a module next to its `node_modules` rather than a bundle.
- `Open()` loads Bare Kit as a shared library, so native addons resolve the symbols of Bare from it as is.
- `Read()` and `Write()` block the calling goroutine until the IPC is ready, and one goroutine may read while another writes.
- An uncaught error in the worklet aborts the process, the Go program included, so report errors over `Bare.IPC` as described in [error handling](https://github.com/holepunchto/bare-kit#error-handling).

## Platforms

The module supports Android, iOS, Linux, macOS, and Windows, and loads Bare Kit at runtime. The releases of the module provide prebuilt Bare Kit libraries, which the `prebuild` package downloads, verifies, and caches:

```go
path, err := prebuild.Download(ctx, nil)
```

To download them as a build step instead, such as to bundle them with an application, add the `bare-kit-prebuild` tool to the module, which then always downloads the libraries of the version that the module depends on:

```
go get -tool github.com/holepunchto/bare-kit-golang/cmd/bare-kit-prebuild
go tool bare-kit-prebuild -dir prebuilds -target android-arm64
```

On Android and iOS, the application bundles the library itself. Loading it requires cgo on those platforms, and on Android the Java virtual machine and application context are passed with `Options.Android`.

## License

Apache-2.0
