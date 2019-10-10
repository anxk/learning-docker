package docker

import "syscall"

// @anxk: linux系统mount系统调用的封装。
func mount(source string, target string, fstype string, flags uintptr, data string) (err error) {
	return syscall.Mount(source, target, fstype, flags, data)
}
