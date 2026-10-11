package focas

import (
	"fmt"
	"strings"
)

func lookup[K ~int16](m map[K]string, k K) string {
	if s, ok := m[k]; ok {
		return s
	}
	return fmt.Sprintf("? (%d)", int16(k))
}

// Mode — режим работы ЧПУ (поле aut из cnc_statinfo).
type Mode int16

var modeNames = map[Mode]string{
	0: "MDI", 1: "MEM (авто)", 2: "****", 3: "EDIT", 4: "HANDLE/INC", 5: "JOG",
	6: "TEACH IN JOG", 7: "TEACH IN HANDLE", 8: "INC FEED", 9: "REFERENCE", 10: "REMOTE",
}

func (m Mode) String() string { return lookup(modeNames, m) }

// RunState — состояние выполнения программы (поле run из cnc_statinfo).
type RunState int16

// Состояния выполнения программы.
const (
	RunReset RunState = 0
	RunStop  RunState = 1
	RunHold  RunState = 2
	RunStart RunState = 3
	RunMSTR  RunState = 4
)

var runNames = map[RunState]string{0: "RESET", 1: "STOP", 2: "HOLD", 3: "START", 4: "MSTR"}

func (r RunState) String() string { return lookup(runNames, r) }

// MotionState — состояние движения осей (поле motion из cnc_statinfo).
type MotionState int16

var motionNames = map[MotionState]string{0: "нет движения", 1: "движение", 2: "выдержка (dwell)"}

func (m MotionState) String() string { return lookup(motionNames, m) }

// AlarmState — состояние аварий (поле alarm из cnc_statinfo).
type AlarmState int16

var alarmNames = map[AlarmState]string{0: "нет", 1: "ALARM", 2: "BATTERY", 3: "FAN"}

func (a AlarmState) String() string { return lookup(alarmNames, a) }

// Unit — единица измерения позиции оси.
type Unit int16

var unitNames = map[Unit]string{0: "mm", 1: "inch", 2: "deg"}

func (u Unit) String() string { return lookup(unitNames, u) }

// Status — общее состояние станка (cnc_statinfo).
type Status struct {
	Mode      Mode
	Run       RunState
	Motion    MotionState
	Alarm     AlarmState
	Emergency bool // активен аварийный стоп
}

// ProgramInfo — сведения о выполняемой программе.
//
// Ошибки отдельных функций FOCAS (например, «не поддерживается этим ЧПУ») не
// прерывают чтение: они попадают в Warnings, а соответствующие поля остаются
// нулевыми. Прерывает чтение только потеря соединения.
type ProgramInfo struct {
	Name          string // имя выполняемой программы (cnc_exeprgname), например "O1"
	Number        int    // номер выполняемой программы (cnc_exeprgname)
	RunningNumber int    // номер выполняемой программы (cnc_rdprgnum): может быть подпрограммой
	MainNumber    int    // номер главной программы (cnc_rdprgnum)
	Sequence      int    // номер кадра N (cnc_rdseqnum)
	CurrentBlock  string // выполняемый кадр (cnc_rdexecprog)
	Warnings      []error
}

// Axis — позиции одной оси.
type Axis struct {
	Name         string // имя оси с индексом: "X", "Y", "Z", "A", "X1"…
	Unit         Unit
	Absolute     float64 // абсолютные координаты
	Machine      float64 // машинные координаты
	Relative     float64 // относительные координаты
	DistanceToGo float64 // остаток пути
}

// Spindle — состояние шпинделя.
type Spindle struct {
	Name  string  // "S", "S1"…
	Speed float64 // фактическая скорость, об/мин
	Load  float64 // нагрузка, %
}

// Program — текст управляющей программы, выгруженный из ЧПУ целиком.
type Program struct {
	Number int    // O-номер
	Text   string // полный текст, переводы строк — "\n"; заканчивается на "%"
}

// Lines возвращает текст программы по строкам (без завершающей пустой).
func (p *Program) Lines() []string {
	if p.Text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(p.Text, "\n"), "\n")
}
