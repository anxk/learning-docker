package docker

import "errors"

// @anxk: darwin系统mount系统调用的封装。
func mount(source string, target string, fstype string, flags uintptr, data string) (err error) {
	return errors.New("mount is not implemented on darwin")
}
