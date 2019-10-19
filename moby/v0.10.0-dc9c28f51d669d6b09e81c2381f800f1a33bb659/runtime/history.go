package runtime

import (
	"sort"
)

// @anxk: History中保存的容器类型的指针，实现了sort.Interface接口，最年轻的排在最前面。
// History is a convenience type for storing a list of containers,
// ordered by creation date.
type History []*Container

func (history *History) Len() int {
	return len(*history)
}

func (history *History) Less(i, j int) bool {
	containers := *history
	return containers[j].When().Before(containers[i].When())
}

/* @anxk: 注意在go语言中，取指针中保存值的操作符"*"的优先级是低于index操作符"[]"的，所以
源码中的Swap函数可以改写为下面的形式，其中(*history)[i]中的小括号一定要有。
func (history *History) Swap(i, j int) {
	tmp := (*history)[i]
	(*history)[i] = (*history)[j]
	(*history)[j] = tmp
}
更为简洁的实现如下：
func (history *History) Swap(i, j int) {
	(*history)[i], (*history)[j] = (*history)[j], (*history)[i]
}
*/
func (history *History) Swap(i, j int) {
	containers := *history
	tmp := containers[i]
	containers[i] = containers[j]
	containers[j] = tmp
}

func (history *History) Add(container *Container) {
	*history = append(*history, container)
	sort.Sort(history)
}
