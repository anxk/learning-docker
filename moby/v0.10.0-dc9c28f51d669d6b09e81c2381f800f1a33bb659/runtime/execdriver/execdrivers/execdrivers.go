package execdrivers

import (
	"fmt"
	"path"

	"github.com/dotcloud/docker/pkg/sysinfo"
	"github.com/dotcloud/docker/runtime/execdriver"
	"github.com/dotcloud/docker/runtime/execdriver/lxc"
	"github.com/dotcloud/docker/runtime/execdriver/native"
)

// @anxk: 当前0.10.0版本支持lxc和native两个exec驱动。
func NewDriver(name, root, initPath string, sysInfo *sysinfo.SysInfo) (execdriver.Driver, error) {
	switch name {
	case "lxc":
		// we want to five the lxc driver the full docker root because it needs
		// to access and write config and template files in /var/lib/docker/containers/*
		// to be backwards compatible
		return lxc.NewDriver(root, sysInfo.AppArmor)
	case "native":
		return native.NewDriver(path.Join(root, "execdriver", "native"), initPath)
	}
	return nil, fmt.Errorf("unknown exec driver %s", name)
}
