package fanuc

import (
	"runtime"
	"sync"
)

// conn — одно соединение с ЧПУ и единственная горутина, закреплённая за
// потоком ОС, в которой выполняются все его вызовы FOCAS.
type conn struct {
	reqs   chan func(*session)
	stop   chan struct{}
	exited chan struct{}
	once   sync.Once
}

func startConn(lib *dll, cfg Config, shared *sharedState) (*conn, error) {
	w := &conn{
		reqs:   make(chan func(*session)),
		stop:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go w.loop(lib, cfg, shared, ready)

	if err := <-ready; err != nil {
		<-w.exited
		return nil, err
	}
	return w, nil
}

func (w *conn) loop(lib *dll, cfg Config, shared *sharedState, ready chan<- error) {
	defer close(w.exited)

	// Намеренно без UnlockOSThread: когда горутина завершится, закреплённый
	// за ней поток будет уничтожен вместе со всем, что с ним связано.
	runtime.LockOSThread()

	s := newSession(lib, cfg, shared)
	if err := s.connect(); err != nil {
		ready <- err
		return
	}
	ready <- nil

	for {
		select {
		case fn := <-w.reqs:
			fn(s)
		case <-w.stop:
			s.disconnect()
			return
		}
	}
}

func (w *conn) close() {
	w.once.Do(func() { close(w.stop) })
	<-w.exited
}

// exec ставит операцию в очередь рабочего потока и ждёт её завершения.
func (w *conn) exec(op func(*session) error) error {
	done := make(chan error, 1)
	fn := func(s *session) { done <- s.run(op) }

	select {
	case w.reqs <- fn:
	case <-w.stop:
		return ErrClosed
	}
	return <-done
}

func queryOn[T any](w *conn, op func(*session) (T, error)) (T, error) {
	var res T
	err := w.exec(func(s *session) error {
		var e error
		res, e = op(s)
		return e
	})
	return res, err
}
