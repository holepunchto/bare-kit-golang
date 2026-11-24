//go:build unix

package barekit

import "github.com/ebitengine/purego"

func open(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

func symbol(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}
