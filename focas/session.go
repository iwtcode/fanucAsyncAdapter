package focas

import (
	"fmt"
	"time"
	"unsafe"
)

const (
	maxAxes     = 32  // размер буфера cnc_rdposition
	maxSpindles = 24  // размер буферов шпинделей (MAX_SPINDLE в Fwlib64.h)
	execBufSize = 256 // максимум данных cnc_rdexecprog
)

// ---------- Структуры FOCAS (Fwlib64.h, pack 4; на Windows long = 4 байта) ----------
//
// В конец структур, размер которых различается между версиями ЧПУ/Fwlib,
// добавлен запас (_ [..]), чтобы DLL не могла записать за границу буфера.

// ODBST — cnc_statinfo
type odbSt struct {
	_         [2]int16 // hdck, tmmode
	Aut       int16
	Run       int16
	Motion    int16
	_         int16 // mstb
	Emergency int16
	Alarm     int16
	_         [5]int16 // edit и запас
}

// ODBEXEPRG — cnc_exeprgname
type odbExePrg struct {
	Name [36]byte
	ONum int32
	_    [8]byte
}

// ODBPRO — cnc_rdprgnum
type odbPro struct {
	_     [2]int16
	Data  int16 // номер выполняемой программы
	MData int16 // номер главной программы
	_     [8]byte
}

// ODBSEQ — cnc_rdseqnum
type odbSeq struct {
	_    [2]int16
	Data int32 // номер кадра (N)
	_    [8]byte
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

// LOADELM — значение из cnc_rdspmeter
type loadElm struct {
	Data  int32 // значение в единицах 10^-Dec
	Dec   int16
	Unit  int16
	Name  byte
	Suff1 byte
	Suff2 byte
	_     byte
}

// ODBSPLOAD — элемент массива cnc_rdspmeter
type odbSpLoad struct {
	Load  loadElm // нагрузка шпинделя
	Speed loadElm // скорость шпинделя
}

// ODBSPDLNAME — элемент массива cnc_rdspdlname
type odbSpName struct {
	Name  byte
	Suff1 byte
	Suff2 byte
	Suff3 byte
}

// ODBACT2 — cnc_acts2
type odbAct2 struct {
	_    [2]int16
	Data [maxSpindles]int32 // скорость каждого шпинделя
}

// ODBSPN — cnc_rdspload
type odbSpn struct {
	_    [2]int16
	Data [maxSpindles]int16 // нагрузка каждого шпинделя
}

// ODBUP — cnc_upload
type odbUp struct {
	_    [2]int16
	Data [1024]byte // по документации 256, остальное — запас
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

	n      int16 // число осей или шпинделей: на входе размер буфера, на выходе — прочитано
	pos    [maxAxes]odbPos
	spm    [maxSpindles]odbSpLoad
	spName [maxSpindles]odbSpName
	act    odbAct2
	spn    odbSpn

	up     odbUp
	upLen  uint16
	up4Buf [1536]byte
	up4Len int32
}

func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

// short преобразует число в аргумент типа short (старшие биты не используются).
func short(v int) uintptr { return uintptr(uint16(int16(v))) }

// ---------- Сессия ----------

// session — соединение с ЧПУ. Все методы вызываются только из рабочего потока Client.
type session struct {
	lib    *dll
	cfg    Config
	handle uint16
	ip     []byte // адрес с завершающим нулём
	path   []byte // путь программы для cnc_upstart4
	buf    *scratch

	noUpload4 bool // ЧПУ не поддерживает cnc_upload4 — выгружаем через cnc_upload
}

func newSession(lib *dll, cfg Config) *session {
	return &session{lib: lib, cfg: cfg, ip: append([]byte(cfg.IP), 0), buf: new(scratch)}
}

func (s *session) connect() error {
	s.handle = 0
	rc, err := s.lib.call("cnc_allclibhndl3",
		ptr(&s.ip[0]), uintptr(s.cfg.Port), uintptr(s.cfg.timeoutSec()), ptr(&s.handle))
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
		_, _ = s.lib.call("cnc_freelibhndl", uintptr(s.handle))
		s.handle = 0
	}
}

func (s *session) reconnect() error {
	s.disconnect()
	time.Sleep(100 * time.Millisecond)
	return s.connect()
}

// rc вызывает функцию FOCAS, подставляя handle первым аргументом, и возвращает
// сырой код возврата. Ошибка — только если функции нет в DLL.
func (s *session) rc(name string, args ...uintptr) (Code, error) {
	return s.lib.call(name, append([]uintptr{uintptr(s.handle)}, args...)...)
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
	if err == nil || !connLost(err) {
		return err
	}
	if rerr := s.reconnect(); rerr != nil {
		return fmt.Errorf("%w (переподключение не удалось: %v)", err, rerr)
	}
	return op(s)
}
