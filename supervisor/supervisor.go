package supervisor

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bboxsys/platform/logger"
)

var (
	l *logger.Logger
)

func init() {
	l = logger.NewLogger("blackbox", "supervisor")
}

// Logger returns the package logger so a process can attach a log publisher
// to it alongside its own loggers.
func Logger() *logger.Logger {
	return l
}

type Supervisor struct {
	sync.RWMutex
	Routines map[string]*RoutineSupervisor
	Ready    bool
}

func NewSupervisor() *Supervisor {
	return &Supervisor{
		Routines: make(map[string]*RoutineSupervisor),
	}
}

func (s *Supervisor) Register(name string) *RoutineSupervisor {
	s.Lock()
	defer s.Unlock()

	rs, has := s.Routines[name]
	if !has {
		rs = &RoutineSupervisor{
			Name:   name,
			closed: make(chan struct{}, 10),
		}
		rs.waitingReady.Add(1)
		rs.waitingClose.Add(1)
		s.Routines[name] = rs
	}

	AssertTruef(atomic.LoadInt32(&rs.nomoreclose) == 0, "Can't register with closer after close signal.")
	atomic.AddInt32(&rs.running, 1)
	atomic.AddInt32(&rs.ready, 0)
	return rs
}

func (s *Supervisor) Get(name string) *RoutineSupervisor {
	s.RLock()
	defer s.RUnlock()

	rs, has := s.Routines[name]
	if !has {
		l.Fatal(fmt.Sprintf("%q not present in Supervisor", name))
		return nil
	}
	return rs
}

func (s *Supervisor) SetAllReady() {
	s.RLock()
	defer s.RUnlock()

	for _, r := range s.Routines {
		r.SetReady()
	}
}

func (s *Supervisor) WaitAllReady() {
	s.RLock()
	defer s.RUnlock()

	for _, r := range s.Routines {
		r.WaitReady()
	}
}

func (s *Supervisor) SignalAllClose() {
	s.RLock()
	defer s.RUnlock()

	for _, r := range s.Routines {
		r.SignalClose()
	}
}

func (s *Supervisor) WaitAllClose() {
	s.RLock()
	defer s.RUnlock()

	for _, r := range s.Routines {
		r.WaitClose()
	}
}
