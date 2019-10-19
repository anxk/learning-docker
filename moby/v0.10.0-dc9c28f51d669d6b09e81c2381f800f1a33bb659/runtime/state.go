package runtime

import (
	"fmt"
	"sync"
	"time"

	"github.com/dotcloud/docker/utils"
)

// @anxk: State用来表示容器生命过程中的状态信息，是容器中进程状态在容器层面的体现。作为容器
// 运行时的docker，它的操作单元是容器，所以State是必需的。
type State struct {
	sync.RWMutex // @anxk: 嵌入sync.RWMutex来避免出现竞争条件。
	Running      bool
	Pid          int
	ExitCode     int
	StartedAt    time.Time
	FinishedAt   time.Time
	Ghost        bool // @ 在Running=true时Ghost可为真或假。
}

// @anxk: 提供State的字符串表示。
// String returns a human-readable description of the state
func (s *State) String() string {
	s.RLock()
	defer s.RUnlock()

	if s.Running {
		if s.Ghost {
			return fmt.Sprintf("Ghost")
		}
		return fmt.Sprintf("Up %s", utils.HumanDuration(time.Now().UTC().Sub(s.StartedAt)))
	}
	if s.FinishedAt.IsZero() { // @anxk: 这个状态表示容器还没有被设置为运行状态。
		return ""
	}
	return fmt.Sprintf("Exited (%d) %s ago", s.ExitCode, utils.HumanDuration(time.Now().UTC().Sub(s.FinishedAt)))
}

func (s *State) IsRunning() bool {
	s.RLock()
	defer s.RUnlock()

	return s.Running
}

func (s *State) IsGhost() bool {
	s.RLock()
	defer s.RUnlock()

	return s.Ghost
}

func (s *State) GetExitCode() int {
	s.RLock()
	defer s.RUnlock()

	return s.ExitCode
}

func (s *State) SetGhost(val bool) {
	s.Lock()
	defer s.Unlock()

	s.Ghost = val
}

func (s *State) SetRunning(pid int) {
	s.Lock()
	defer s.Unlock()

	s.Running = true
	s.Ghost = false
	s.ExitCode = 0
	s.Pid = pid
	s.StartedAt = time.Now().UTC()
}

func (s *State) SetStopped(exitCode int) {
	s.Lock()
	defer s.Unlock()

	s.Running = false
	s.Pid = 0
	s.FinishedAt = time.Now().UTC()
	s.ExitCode = exitCode
}
