// Package focas читает состояние станка FANUC (или эмулятора FANUC CNC GUIDE)
// по FOCAS2 Ethernet.
//
// Cgo не нужен: Fwlib64.dll загружается напрямую через syscall, поэтому пакет
// работает только в Windows (64-бит). Рядом с Fwlib64.dll должна лежать Fwlibe64.dll.
//
//	c, err := focas.Connect(focas.Config{IP: "127.0.0.1", Port: 8193})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer c.Close()
//
//	st, _ := c.State() // всё состояние: статус, программа, оси, шпиндели
//	st.Write(os.Stdout)
//
//	c.Watch(ctx, time.Second, func(st *focas.State) error { // или опрос
//		st.Write(os.Stdout)
//		return nil
//	})
//
//	prog, _ := c.Program(0) // текст управляющей программы — отдельный запрос
//
// # Потоки
//
// Библиотека FOCAS привязывает handle соединения к потоку ОС, в котором он
// был получен. Рантайм Go может перенести горутину на другой поток, и тогда
// любая функция FOCAS отвечает EW_HANDLE (-8). Поэтому Client держит одну
// выделенную горутину, закреплённую за потоком ОС (runtime.LockOSThread), и
// выполняет в ней все вызовы. Методы Client безопасны для вызова из любого
// числа горутин — вызовы выстраиваются в очередь.
//
// Один Client — одно соединение с ЧПУ. Выгрузка программы занимает секунды, и
// всё это время остальные запросы этого Client ждут; если опрос не должен
// простаивать, откройте для программы второй Client.
package focas

import (
	"context"
	"math"
	"runtime"
	"sync"
	"time"
)

// Config — параметры подключения. Нулевые значения заменяются значениями по умолчанию.
type Config struct {
	IP      string        // адрес ЧПУ или эмулятора (по умолчанию 127.0.0.1)
	Port    uint16        // порт FOCAS2 Ethernet (по умолчанию 8193)
	Timeout time.Duration // таймаут соединения (по умолчанию 10 с, округляется вверх до секунд)
	DLLPath string        // путь к Fwlib64.dll; пусто — автопоиск рядом с exe и вверх от текущего каталога
}

func (c Config) withDefaults() Config {
	if c.IP == "" {
		c.IP = "127.0.0.1"
	}
	if c.Port == 0 {
		c.Port = 8193
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	return c
}

func (c Config) timeoutSec() int32 {
	return max(int32(math.Ceil(c.Timeout.Seconds())), 1)
}

// Client — подключение к ЧПУ. Безопасен для использования из нескольких горутин.
type Client struct {
	s      *session
	reqs   chan func()
	stop   chan struct{}
	exited chan struct{}
	once   sync.Once
}

// Connect загружает DLL и подключается к ЧПУ.
func Connect(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()
	lib, err := loadDLL(cfg.DLLPath)
	if err != nil {
		return nil, err
	}
	c := &Client{
		s:      newSession(lib, cfg),
		reqs:   make(chan func()),
		stop:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go c.loop(ready)
	if err := <-ready; err != nil {
		<-c.exited
		return nil, err
	}
	return c, nil
}

// loop — рабочая горутина, закреплённая за потоком ОС: подключается к ЧПУ и
// выполняет в этом потоке все вызовы FOCAS.
func (c *Client) loop(ready chan<- error) {
	defer close(c.exited)

	// Намеренно без UnlockOSThread: когда горутина завершится, закреплённый
	// за ней поток будет уничтожен вместе со всем, что с ним связано.
	runtime.LockOSThread()

	if err := c.s.connect(); err != nil {
		ready <- err
		return
	}
	ready <- nil

	for {
		select {
		case fn := <-c.reqs:
			fn()
		case <-c.stop:
			c.s.disconnect()
			return
		}
	}
}

// Close закрывает соединение. Повторный вызов безопасен.
func (c *Client) Close() error {
	c.once.Do(func() { close(c.stop) })
	<-c.exited
	return nil
}

// exec ставит операцию в очередь рабочего потока и ждёт её завершения.
func (c *Client) exec(op func(*session) error) error {
	done := make(chan error, 1)
	select {
	case c.reqs <- func() { done <- c.s.run(op) }:
	case <-c.stop:
		return ErrClosed
	}
	return <-done
}

func query[T any](c *Client, op func(*session) (T, error)) (T, error) {
	var res T
	err := c.exec(func(s *session) (err error) {
		res, err = op(s)
		return
	})
	return res, err
}

// Status возвращает общее состояние станка: режим, состояние программы, движение, аварии.
func (c *Client) Status() (Status, error) { return query(c, (*session).status) }

// ProgramInfo возвращает сведения о выполняемой программе: имя, номера,
// номер кадра N и выполняемый кадр. Текст программы целиком — Program.
func (c *Client) ProgramInfo() (*ProgramInfo, error) { return query(c, (*session).programInfo) }

// Axes возвращает позиции осей: абсолютные, машинные, относительные и остаток пути.
func (c *Client) Axes() ([]Axis, error) { return query(c, (*session).axes) }

// Spindles возвращает состояние шпинделей: скорость и нагрузку.
func (c *Client) Spindles() ([]Spindle, error) { return query(c, (*session).spindles) }

// State возвращает всё состояние станка, кроме текста программы, одним снимком.
func (c *Client) State() (*State, error) { return query(c, (*session).state) }

// Program выгружает из ЧПУ полный текст программы с номером number
// (number <= 0 — главная выполняемая программа). Это отдельный запрос, не
// входящий в State: выгрузка идёт секунды, и все остальные запросы этого
// Client ждут её окончания.
func (c *Client) Program(number int) (*Program, error) {
	return query(c, func(s *session) (*Program, error) { return s.program(number) })
}

// Watch опрашивает State каждые every и передаёт его в fn (первый раз — сразу).
// every <= 0 — один опрос. Возвращается при отмене ctx (ctx.Err()), при ошибке
// опроса или если fn вернула ошибку.
func (c *Client) Watch(ctx context.Context, every time.Duration, fn func(*State) error) error {
	var tick <-chan time.Time
	if every > 0 {
		t := time.NewTicker(every)
		defer t.Stop()
		tick = t.C
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		st, err := c.State()
		if err != nil {
			return err
		}
		if err := fn(st); err != nil {
			return err
		}
		if tick == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
		}
	}
}
