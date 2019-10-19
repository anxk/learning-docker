package runtime

import "sort"

// @anxk: 提供一个可以保存容器指针slice的类型，可由用户自定义sort.Interface的接口方法Less，
// 用来排序其中的容器指针slice。相比History类型，除了不可导出外，更灵活一些。
type containerSorter struct {
	containers []*Container
	by         func(i, j *Container) bool
}

func (s *containerSorter) Len() int {
	return len(s.containers)
}

func (s *containerSorter) Swap(i, j int) {
	s.containers[i], s.containers[j] = s.containers[j], s.containers[i]
}

func (s *containerSorter) Less(i, j int) bool {
	return s.by(s.containers[i], s.containers[j])
}

// @anxk: 根据predicate函数对容器指针slice进行排序。
func sortContainers(containers []*Container, predicate func(i, j *Container) bool) {
	s := &containerSorter{containers, predicate}
	sort.Sort(s)
}
