package fanuc

import (
	"sync"
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
	fast, err := startConn(lib, cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, lib: lib, fast: fast, cache: make(map[int]cachedProgram)}, nil
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
		if w, err := startConn(c.lib, c.cfg); err == nil {
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
	return queryOn(c.fast, (*session).programInfo)
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

func (c *Client) loadProgram(number int, force bool) (*Program, error) {
	if number <= 0 {
		// Номер определяем по быстрому соединению — оно не занято выгрузкой.
		n, err := queryOn(c.fast, (*session).currentMainProgram)
		if err != nil {
			return nil, err
		}
		number = n
	}
	if !force {
		if p, ok := c.cached(number); ok {
			return p, nil
		}
	}
	return queryOn(c.bulkConn(), func(s *session) (*Program, error) {
		// Пока запрос стоял в очереди, другой мог уже загрузить эту программу.
		if !force {
			if p, ok := c.cached(number); ok {
				return p, nil
			}
		}
		p, err := s.program(number)
		if err != nil {
			return nil, err
		}
		c.store(p)
		cp := *p
		return &cp, nil
	})
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
