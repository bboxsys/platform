package supervisor

import (
	"sync"
	"sync/atomic"
)

type RoutineSupervisor struct {
	Name         string
	ready        int32
	running      int32
	nomoreclose  int32
	closed       chan struct{}
	waitingClose sync.WaitGroup
	waitingReady sync.WaitGroup
}

func (rs *RoutineSupervisor) SignalClose() {
	if !atomic.CompareAndSwapInt32(&rs.nomoreclose, 0, 1) {
		l.InfoE().Str("routine", rs.Name).Msg("routine already got close signal")
		return
	}
	running := int(atomic.LoadInt32(&rs.running))
	l.InfoE().Int("running", running).Str("routine", rs.Name).Msg("sending close signal")
	for i := 0; i < running; i++ {
		rs.closed <- struct{}{}
	}
}

func (rs *RoutineSupervisor) SignalCloseAndWait() {
	rs.SignalClose()
	rs.WaitClose()
}

func (rs *RoutineSupervisor) HasBeenClosed() <-chan struct{} {
	return rs.closed
}

func (rs *RoutineSupervisor) SetReady() {
	ready := atomic.AddInt32(&rs.ready, 1)
	if ready == 1 {
		rs.waitingReady.Done()
	}
}

func (rs *RoutineSupervisor) Done() {
	if atomic.LoadInt32(&rs.running) <= 0 {
		return
	}

	running := atomic.AddInt32(&rs.running, -1)
	if running == 0 {
		rs.waitingClose.Done()
	}
}

func (rs *RoutineSupervisor) WaitReady() {
	rs.waitingReady.Wait()
}

func (rs *RoutineSupervisor) WaitClose() {
	rs.waitingClose.Wait()
}
