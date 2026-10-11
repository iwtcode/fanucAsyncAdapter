package focas

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// status читает общее состояние станка (cnc_statinfo).
func (s *session) status() (Status, error) {
	b := s.buf
	b.st = odbSt{}
	if err := s.call("cnc_statinfo", ptr(&b.st)); err != nil {
		return Status{}, err
	}
	return Status{
		Mode:      Mode(b.st.Aut),
		Run:       RunState(b.st.Run),
		Motion:    MotionState(b.st.Motion),
		Alarm:     AlarmState(b.st.Alarm),
		Emergency: b.st.Emergency == 1,
	}, nil
}

// programInfo собирает сведения о выполняемой программе.
func (s *session) programInfo() (*ProgramInfo, error) {
	info := &ProgramInfo{}
	b := s.buf

	// step вызывает необязательную функцию FOCAS и при успехе разбирает ответ.
	// Её ошибка (кроме потери соединения) не прерывает чтение, а попадает в Warnings.
	var err error
	step := func(name string, parse func(), args ...uintptr) {
		if err != nil {
			return
		}
		switch callErr := s.call(name, args...); {
		case callErr == nil:
			parse()
		case connLost(callErr):
			err = callErr
		default:
			info.Warnings = append(info.Warnings, callErr)
		}
	}

	b.exe = odbExePrg{}
	step("cnc_exeprgname", func() {
		info.Name, info.Number = cString(b.exe.Name[:]), int(b.exe.ONum)
	}, ptr(&b.exe))

	b.pro = odbPro{}
	step("cnc_rdprgnum", func() {
		info.RunningNumber, info.MainNumber = int(b.pro.Data), int(b.pro.MData)
	}, ptr(&b.pro))

	b.seq = odbSeq{}
	step("cnc_rdseqnum", func() {
		info.Sequence = int(b.seq.Data)
	}, ptr(&b.seq))

	// cnc_rdexecprog отдаёт выполняемый кадр вместе с кадрами упреждения — берём первый.
	clear(b.execBuf[:])
	b.execLen, b.execBlk = execBufSize-1, 0
	step("cnc_rdexecprog", func() {
		n := min(int(b.execLen), execBufSize)
		info.CurrentBlock = firstLine(normalizeText(cString(b.execBuf[:n])))
	}, ptr(&b.execLen), ptr(&b.execBlk), ptr(&b.execBuf[0]))

	if err != nil {
		return nil, err
	}
	return info, nil
}

// axes читает позиции всех осей (cnc_rdposition, type = -1: abs/mach/rel/dist).
func (s *session) axes() ([]Axis, error) {
	b := s.buf

	// Сначала просим буфер на maxAxes осей; если ЧПУ не принимает такой размер —
	// пробуем 8 (старые версии Fwlib).
	var err error
	for _, size := range []int16{maxAxes, 8} {
		b.pos = [maxAxes]odbPos{}
		b.n = size
		err = s.call("cnc_rdposition", short(-1), ptr(&b.n), ptr(&b.pos[0]))
		if err == nil || connLost(err) {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	axes := make([]Axis, min(max(int(b.n), 0), maxAxes))
	for i := range axes {
		p := &b.pos[i]
		axes[i] = Axis{
			Name:         chName(p.Abs.Name, p.Abs.Suff, fmt.Sprintf("#%d", i+1)),
			Unit:         Unit(p.Abs.Unit),
			Absolute:     scaled(p.Abs.Data, p.Abs.Dec),
			Machine:      scaled(p.Mach.Data, p.Mach.Dec),
			Relative:     scaled(p.Rel.Data, p.Rel.Dec),
			DistanceToGo: scaled(p.Dist.Data, p.Dist.Dec),
		}
	}
	return axes, nil
}

// spindles читает состояние всех шпинделей. Основной способ — один вызов
// cnc_rdspmeter (имя, скорость и нагрузка сразу); если ЧПУ его не поддерживает,
// данные собираются из cnc_rdspdlname + cnc_acts2 + cnc_rdspload.
func (s *session) spindles() ([]Spindle, error) {
	sp, err := s.spindleMeters()
	if err == nil || connLost(err) {
		return sp, err
	}
	sp, err2 := s.spindleParts()
	if err2 != nil {
		return nil, errors.Join(err, err2)
	}
	return sp, nil
}

// spindleMeters — cnc_rdspmeter (type = -1: все шпиндели).
func (s *session) spindleMeters() ([]Spindle, error) {
	b := s.buf
	b.spm = [maxSpindles]odbSpLoad{}
	b.n = maxSpindles
	if err := s.call("cnc_rdspmeter", short(-1), ptr(&b.n), ptr(&b.spm[0])); err != nil {
		return nil, err
	}
	out := make([]Spindle, min(max(int(b.n), 0), maxSpindles))
	for i := range out {
		m := &b.spm[i]
		out[i] = Spindle{
			Name:  chName(m.Load.Name, m.Load.Suff1, fmt.Sprintf("S%d", i+1)),
			Speed: scaled(m.Speed.Data, m.Speed.Dec),
			Load:  scaled(m.Load.Data, m.Load.Dec),
		}
	}
	return out, nil
}

// spindleParts — запасной способ: имена, скорость и нагрузка отдельными вызовами.
func (s *session) spindleParts() ([]Spindle, error) {
	b := s.buf
	b.spName = [maxSpindles]odbSpName{}
	b.n = maxSpindles
	if err := s.call("cnc_rdspdlname", ptr(&b.n), ptr(&b.spName[0])); err != nil {
		return nil, err
	}
	out := make([]Spindle, min(max(int(b.n), 0), maxSpindles))
	for i := range out {
		out[i].Name = chName(b.spName[i].Name, b.spName[i].Suff1, fmt.Sprintf("S%d", i+1))
	}

	b.act = odbAct2{}
	if err := s.call("cnc_acts2", short(-1), ptr(&b.act)); err != nil {
		return nil, err
	}
	b.spn = odbSpn{}
	if err := s.call("cnc_rdspload", short(-1), ptr(&b.spn)); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Speed = float64(b.act.Data[i])
		out[i].Load = float64(b.spn.Data[i])
	}
	return out, nil
}

// state читает всё, кроме текста программы, за одну операцию на соединении.
// Статус обязателен; остальные части при ошибке (кроме потери соединения)
// остаются пустыми, а причина попадает в State.Warnings.
func (s *session) state() (*State, error) {
	status, err := s.status()
	if err != nil {
		return nil, err
	}
	st := &State{Time: time.Now(), Status: status}

	info, err := s.programInfo()
	if err != nil {
		return nil, err
	}
	st.Warnings, info.Warnings = info.Warnings, nil
	st.Info = *info

	// soft: потеря соединения прерывает чтение, остальные ошибки — предупреждение.
	soft := func(what string, err error) error {
		if err == nil || connLost(err) {
			return err
		}
		st.Warnings = append(st.Warnings, fmt.Errorf("%s: %w", what, err))
		return nil
	}
	st.Axes, err = s.axes()
	if err = soft("оси", err); err != nil {
		return nil, err
	}
	st.Spindles, err = s.spindles()
	if err = soft("шпиндели", err); err != nil {
		return nil, err
	}
	return st, nil
}

// ---------- вспомогательное ----------

func scaled(data int32, dec int16) float64 {
	if dec <= 0 {
		return float64(data)
	}
	return float64(data) / math.Pow10(int(dec))
}

// chName собирает имя оси или шпинделя из символа и индекса; без имени — fallback.
func chName(name, suff byte, fallback string) string {
	if name < 0x21 || name > 0x7e {
		return fallback
	}
	n := string(rune(name))
	if suff >= 0x21 && suff <= 0x7e {
		n += string(rune(suff))
	}
	return n
}

// cString обрезает байтовый буфер по первому нулю и убирает пробелы по краям.
func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// firstLine — первая непустая строка текста.
func firstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
