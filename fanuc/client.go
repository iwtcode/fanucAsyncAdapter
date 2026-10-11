package fanuc

import (
	"sync"
	"sync/atomic"
	"time"
)

// Client — подключение к ЧПУ. Безопасен для использования из нескольких горутин.
//
// Внутри два соединения, у каждого свой поток ОС (см. описание пакета):
//   - быстрое — статус, информация о программе, оси;
//   - «тяжёлое» — выгрузка текста программы. Она занимает секунды, и без
//     отдельного соединения все остальные запросы стояли бы за ней в очереди.
//
// «Тяжёлое» соединение открывается при первом запросе программы; если ЧПУ
// второе соединение не принимает, выгрузка идёт по основному.
type Client struct {
	cfg  Config
	lib  *dll
	fast *conn

	bulkMu    sync.Mutex
	bulk      *conn
	bulkTried bool
	closed    bool

	cacheMu sync.Mutex
	cache   map[int]cachedProgram

	// Номер главной программы, уже полученный быстрым соединением
	// (ProgramInfo / предыдущий RunningProgram): повторно cnc_rdprgnum не зовём.
	numMu sync.Mutex
	num   mainNumber

	// Выгрузки, идущие прямо сейчас: параллельные запросы одной программы
	// ждут одну выгрузку, даже если кэш выключен (ProgramCacheTTL < 0).
	flightMu sync.Mutex
	flight   map[int]*flightCall

	shared *sharedState
}

// sharedState — то, что общее для всех соединений одного Client.
type sharedState struct {
	// cnc_upload4 не работает на этом ЧПУ. Флаг общий: иначе каждое новое
	// соединение заново тратило бы до probeIdle (3 с) на «разведку».
	noUpload4 atomic.Bool
}

type mainNumber struct {
	n  int
	at time.Time
}

type flightCall struct {
	done chan struct{}
	prog *Program
	err  error
}

type cachedProgram struct {
	prog *Program
	at   time.Time
}

// Connect загружает DLL и подключается к ЧПУ.
func Connect(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()

	lib, err := loadDLL(cfg.DLLPath)
	if err != nil {
		return nil, err
	}
	shared := &sharedState{}

	// «Тяжёлое» соединение открываем параллельно с основным: его установка
	// не попадает на критический путь первой выгрузки программы.
	type bulkRes struct {
		c   *conn
		err error
	}
	bulkCh := make(chan bulkRes, 1)
	go func() {
		c, err := startConn(lib, cfg, shared)
		bulkCh <- bulkRes{c, err}
	}()

	fast, err := startConn(lib, cfg, shared)
	br := <-bulkCh
	if err != nil {
		if br.c != nil {
			br.c.close()
		}
		return nil, err
	}
	c := &Client{
		cfg: cfg, lib: lib, fast: fast, shared: shared,
		cache:     make(map[int]cachedProgram),
		flight:    make(map[int]*flightCall),
		bulkTried: true,
	}
	if br.err == nil {
		c.bulk = br.c // иначе выгрузка пойдёт по основному соединению
	}
	return c, nil
}

// Config возвращает действующие параметры (с подставленными значениями по умолчанию).
func (c *Client) Config() Config { return c.cfg }

// Close закрывает соединения. Повторный вызов безопасен.
func (c *Client) Close() error {
	c.bulkMu.Lock()
	c.closed = true
	bulk := c.bulk
	c.bulkMu.Unlock()

	if bulk != nil {
		bulk.close()
	}
	c.fast.close()
	return nil
}

// bulkConn возвращает соединение для выгрузки программ (открывает при первом вызове).
func (c *Client) bulkConn() *conn {
	c.bulkMu.Lock()
	defer c.bulkMu.Unlock()

	if c.closed {
		return c.fast // вернёт ErrClosed
	}
	if c.bulk == nil && !c.bulkTried {
		c.bulkTried = true
		if w, err := startConn(c.lib, c.cfg, c.shared); err == nil {
			c.bulk = w
		}
	}
	if c.bulk != nil {
		return c.bulk
	}
	return c.fast
}

// Status возвращает общее состояние станка: режим, состояние программы, движение, аварии.
func (c *Client) Status() (Status, error) {
	return queryOn(c.fast, (*session).status)
}

// ProgramInfo возвращает информацию о выполняемой программе: имя, номера,
// номер кадра N, выполняемый кадр и состояние станка.
func (c *Client) ProgramInfo() (*ProgramInfo, error) {
	info, err := queryOn(c.fast, (*session).programInfo)
	if err == nil {
		if n := mainNumberOf(info); n > 0 {
			c.setMainNumber(n)
		}
	}
	return info, err
}

// mainNumberOf — тот же приоритет, что в currentMainProgram: главная, затем выполняемая.
func mainNumberOf(info *ProgramInfo) int {
	switch {
	case info.MainNumber > 0:
		return info.MainNumber
	case info.RunningNumber > 0:
		return info.RunningNumber
	default:
		return info.Number
	}
}

func (c *Client) setMainNumber(n int) {
	c.numMu.Lock()
	c.num = mainNumber{n: n, at: time.Now()}
	c.numMu.Unlock()
}

func (c *Client) freshMainNumber() (int, bool) {
	ttl := c.cfg.MainNumberTTL
	if ttl < 0 {
		return 0, false
	}
	c.numMu.Lock()
	defer c.numMu.Unlock()
	if c.num.n > 0 && time.Since(c.num.at) <= ttl {
		return c.num.n, true
	}
	return 0, false
}

// Axes возвращает позиции осей: абсолютные, машинные, относительные и остаток пути.
func (c *Client) Axes() ([]Axis, error) {
	return queryOn(c.fast, (*session).axes)
}

// Program возвращает полный текст программы с номером number
// (number <= 0 — главная выполняемая программа).
//
// Текст кэшируется на Config.ProgramCacheTTL: первый запрос выгружает программу
// из ЧПУ (секунды для программ в десятки КБ), повторные — мгновенные.
func (c *Client) Program(number int) (*Program, error) {
	return c.loadProgram(number, false)
}

// RunningProgram — то же, что Program(0): главная выполняемая программа.
func (c *Client) RunningProgram() (*Program, error) {
	return c.loadProgram(0, false)
}

// ReloadProgram выгружает программу из ЧПУ, игнорируя кэш, и обновляет его.
func (c *Client) ReloadProgram(number int) (*Program, error) {
	return c.loadProgram(number, true)
}

// resolveNumber определяет номер программы: number > 0 возвращается как есть,
// иначе берётся свежий номер главной программы или спрашивается ЧПУ.
func (c *Client) resolveNumber(number int) (int, error) {
	if number > 0 {
		return number, nil
	}
	if n, ok := c.freshMainNumber(); ok {
		return n, nil // уже знаем номер — лишний вызов ЧПУ не нужен
	}
	// Номер определяем по быстрому соединению — оно не занято выгрузкой.
	n, err := queryOn(c.fast, (*session).currentMainProgram)
	if err != nil {
		return 0, err
	}
	c.setMainNumber(n)
	return n, nil
}

// ProgramHead выгружает только начало программы: не меньше maxBytes байт
// (или всю программу, если она короче). Текст обрезается по границе порции
// и не кэшируется. Нужен для быстрых проверок: выгрузка целиком идёт секунды,
// а ЧПУ/эмулятор обслуживает выгрузки строго по очереди.
// number <= 0 — главная выполняемая программа.
func (c *Client) ProgramHead(number, maxBytes int) (*Program, error) {
	number, err := c.resolveNumber(number)
	if err != nil {
		return nil, err
	}
	return queryOn(c.bulkConn(), func(s *session) (*Program, error) {
		return s.programLimit(number, maxBytes)
	})
}

func (c *Client) loadProgram(number int, force bool) (*Program, error) {
	number, err := c.resolveNumber(number)
	if err != nil {
		return nil, err
	}
	if !force {
		if p, ok := c.cached(number); ok {
			return p, nil
		}
	}

	// Одна выгрузка на программу: остальные желающие ждут её результат.
	c.flightMu.Lock()
	if f, ok := c.flight[number]; ok {
		c.flightMu.Unlock()
		<-f.done
		if f.err != nil {
			return nil, f.err
		}
		cp := *f.prog
		return &cp, nil
	}
	// Предыдущая выгрузка могла завершиться между проверкой кэша и этим местом.
	if !force {
		if p, ok := c.cached(number); ok {
			c.flightMu.Unlock()
			return p, nil
		}
	}
	f := &flightCall{done: make(chan struct{})}
	c.flight[number] = f
	c.flightMu.Unlock()

	f.prog, f.err = queryOn(c.bulkConn(), func(s *session) (*Program, error) {
		p, err := s.program(number)
		if err != nil {
			return nil, err
		}
		c.store(p)
		return p, nil
	})

	c.flightMu.Lock()
	delete(c.flight, number)
	c.flightMu.Unlock()
	close(f.done)

	if f.err != nil {
		return nil, f.err
	}
	cp := *f.prog
	return &cp, nil
}

func (c *Client) cached(number int) (*Program, bool) {
	if c.cfg.ProgramCacheTTL < 0 {
		return nil, false
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	e, ok := c.cache[number]
	if !ok || time.Since(e.at) > c.cfg.ProgramCacheTTL {
		return nil, false
	}
	cp := *e.prog
	return &cp, true
}

func (c *Client) store(p *Program) {
	if c.cfg.ProgramCacheTTL < 0 {
		return
	}
	cp := *p
	c.cacheMu.Lock()
	c.cache[p.Number] = cachedProgram{prog: &cp, at: time.Now()}
	c.cacheMu.Unlock()
}
