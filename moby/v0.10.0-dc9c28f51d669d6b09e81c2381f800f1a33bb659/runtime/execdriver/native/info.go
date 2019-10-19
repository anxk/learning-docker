package native

import (
	"os"
	"path/filepath"
)

type info struct {
	ID     string
	driver *driver
}

// @anxk: 检查容器是否活着，通过其pid文件是否存在来判断。
// IsRunning is determined by looking for the
// pid file for a container.  If the file exists then the
// container is currently running
func (i *info) IsRunning() bool {
	if _, err := os.Stat(filepath.Join(i.driver.root, i.ID, "pid")); err == nil {
		return true
	}
	return false
}
