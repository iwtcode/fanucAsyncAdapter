package fanuc

import (
	"fmt"
	"time"
	"unsafe"
)

const (
	maxAxes     = 32  // размер буфера cnc_rdposition (MAX_AXIS в новых Fwlib)
	execBufSize = 256 // максимум данных cnc_rdexecprog
	upChunk     = 256 // размер порции cnc_upload
	up4Chunk    = 1280 // максимум cnc_upload4
)

// ---------- Структуры FOCAS (на Windows long = 4 байта) ----------
//
// В конец структур, размер которых различается между версиями ЧПУ/Fwlib,
// добавлен запас (_ [..]), чтобы DLL не могла записать за границу буфера.

// ODBST — cnc_statinfo
type odbSt struct {
	Dummy     [2]int16
	Aut       int16
	Run       int16
	Motion    int16
	Mstb      int16
	Emergency int16
	Alarm     int16
	Edit      int16
	_         [4]int16
}

// ODBEXEPRG — cnc_exeprgname
type odbExePrg struct {
	Name [36]byte
	ONum int32
	_    [8]byte
}

// ODBPRO — cnc_rdprgnum
type odbPro struct {
	Dummy [2]int16
	Data  int16 // номер выполняемой программы
	MData int16 // номер главной программы
	_     [8]byte
}

// ODBSEQ — cnc_rdseqnum
type odbSeq struct {
	Dummy [2]int16
	Data  int32 // номер кадра (N)
	_     [8]byte
}

// POSELM — одна координата из cnc_rdposition
type posElm struct {
	Data int32 // значение в единицах 10^-Dec
	Dec  int16 // число знаков после запятой
	Unit int16 // 0 — мм, 1 — дюймы, 2 — градусы
	Disp int16
	Name byte // имя оси
	Suff byte // индекс оси
}

// ODBPOS — элемент массива cnc_rdposition (4 вида координат одной оси)
type odbPos struct {
	Abs  posElm
	Mach posElm
	Rel  posElm
	Dist posElm
}

// ODBUP — cnc_upload
type odbUp struct {
	Dummy [2]int16
	Data  [1024]byte // по документации 256, остальное — запас
}

// scratch — буферы, в которые пишет DLL. Они лежат в куче (внутри session),
// поэтому их адрес стабилен на время вызова: стек горутины Go может быть
// перемещён, а переданный как uintptr адрес локальной переменной — нет.
// Использует их только рабочий поток Client, так что гонок нет.
type scratch struct {
	st  odbSt
	exe odbExePrg
	pro odbPro
	seq odbSeq

	execBuf [512]byte
	execLen uint16
	execBlk int16

	posNum int16
	pos    [maxAxes]odbPos

	up    odbUp
	upLen uint16

	up4Buf [1536]byte
	up4Len int32
}

func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

// short преобразует число в аргумент типа short (старшие биты не используются).
func short(v int) uintptr { return uintptr(uint16(int16(v))) }

// ---------- Сессия ----------

// session — соединение с ЧПУ. Все методы вызываются только из рабочего потока Client.
type session struct {
	lib     *dll
	cfg     Config
	handle  uint16
	ipBuf   []byte
	pathBuf []byte
	buf     *scratch

	noUpload4 bool // cnc_upstart4/cnc_upload4 не работают на этом ЧПУ — не пробовать снова
}

func newSession(lib *dll, cfg Config) *session {
	return &session{
		lib:   lib,
		cfg:   cfg,
		ipBuf: append([]byte(cfg.IP), 0),
		buf:   new(scratch),
	}
}

func (s *session) connect() error {
	s.handle = 0
	rc, err := s.raw("cnc_allclibhndl3",
		ptr(&s.ipBuf[0]),
		uintptr(s.cfg.Port),
		uintptr(s.cfg.timeoutSec()),
		ptr(&s.handle),
	)
	if err != nil {
		return err
	}
	if rc != CodeOK {
		s.handle = 0
		return &Error{Func: "cnc_allclibhndl3", Code: rc}
	}
	return nil
}

func (s *session) disconnect() {
	if s.handle != 0 {
		_, _ = s.raw("cnc_freelibhndl", uintptr(s.handle))
		s.handle = 0
	}
}

func (s *session) reconnect() error {
	s.disconnect()
	time.Sleep(100 * time.Millisecond)
	return s.connect()
}

// raw вызывает функцию FOCAS как есть и, если включено, сообщает время вызова.
func (s *session) raw(name string, args ...uintptr) (Code, error) {
	if s.cfg.Trace == nil {
		return s.lib.call(name, args...)
	}
	t0 := time.Now()
	rc, err := s.lib.call(name, args...)
	s.cfg.Trace(name, rc, time.Since(t0))
	return rc, err
}

// rc вызывает функцию FOCAS, подставляя handle первым аргументом, и возвращает
// сырой код возврата. Ошибка — только если функции нет в DLL.
func (s *session) rc(name string, args ...uintptr) (Code, error) {
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, uintptr(s.handle))
	all = append(all, args...)
	return s.raw(name, all...)
}

// call — как rc, но ненулевой код превращается в *Error.
func (s *session) call(name string, args ...uintptr) error {
	rc, err := s.rc(name, args...)
	if err != nil {
		return err
	}
	if rc != CodeOK {
		return &Error{Func: name, Code: rc}
	}
	return nil
}

// run выполняет операцию; при потере соединения (EW_HANDLE и т.п.)
// переподключается и повторяет её один раз.
func (s *session) run(op func(*session) error) error {
	err := op(s)
	if err == nil || s.cfg.DisableReconnect || !IsConnectionError(err) {
		return err
	}
	if rerr := s.reconnect(); rerr != nil {
		return fmt.Errorf("%w (переподключение не удалось: %v)", err, rerr)
	}
	return op(s)
}
