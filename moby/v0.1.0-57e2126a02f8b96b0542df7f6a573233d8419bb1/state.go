package docker

import (
	"fmt"
	"sync"
	"time"
)

// @anxk: State表示容器的状态。
type State struct {
	Running   bool
	Pid       int
	ExitCode  int
	StartedAt time.Time

	stateChangeLock *sync.Mutex
	stateChangeCond *sync.Cond
}

// @anxk: 实现Stringer接口。
// String returns a human-readable description of the state
func (s *State) String() string {
	if s.Running {
		return fmt.Sprintf("Up %s", HumanDuration(time.Now().Sub(s.StartedAt)))
	}
	return fmt.Sprintf("Exit %d", s.ExitCode)
}

// @anxk: 设置为运行状态。
func (s *State) setRunning(pid int) {
	s.Running = true
	s.ExitCode = 0
	s.Pid = pid
	s.StartedAt = time.Now()
	s.broadcast()
}

// @axnk: 设置为停止状态。
func (s *State) setStopped(exitCode int) {
	s.Running = false
	s.Pid = 0
	s.ExitCode = exitCode
	s.broadcast()
}

func (s *State) broadcast() {
	s.stateChangeLock.Lock()
	s.stateChangeCond.Broadcast()
	s.stateChangeLock.Unlock()
}

func (s *State) wait() {
	s.stateChangeLock.Lock()
	s.stateChangeCond.Wait()
	s.stateChangeLock.Unlock()
}
