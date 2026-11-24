package barekit

import "syscall"

func open(path string) (uintptr, error) {
	handle, err := syscall.LoadLibrary(path)

	return uintptr(handle), err
}

func symbol(handle uintptr, name string) (uintptr, error) {
	return syscall.GetProcAddress(syscall.Handle(handle), name)
}
