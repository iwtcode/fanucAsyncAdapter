package focas

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	maxProgramSize = 64 << 20          // защита от бесконечного цикла выгрузки
	uploadTimeout  = 30 * time.Second  // сколько ждать очередную порцию текста
	probeTimeout   = 3 * time.Second   // сколько ждать первую порцию cnc_upload4, прежде чем считать, что ЧПУ его не поддерживает
	upChunk        = 256               // размер порции cnc_upload
	up4Chunk       = 1280              // максимум cnc_upload4
)

// mainProgram определяет номер главной (а если его нет — выполняемой) программы.
func (s *session) mainProgram() (int, error) {
	b := s.buf

	b.pro = odbPro{}
	err := s.call("cnc_rdprgnum", ptr(&b.pro))
	if err == nil {
		if n := int(b.pro.MData); n > 0 {
			return n, nil
		}
		if n := int(b.pro.Data); n > 0 {
			return n, nil
		}
	} else if connLost(err) {
		return 0, err
	}

	b.exe = odbExePrg{}
	if err := s.call("cnc_exeprgname", ptr(&b.exe)); err != nil {
		return 0, err
	}
	if n := int(b.exe.ONum); n > 0 {
		return n, nil
	}
	return 0, errors.New("focas: не удалось определить номер выполняемой программы")
}

// program выгружает полный текст программы. number <= 0 — главная выполняемая программа.
func (s *session) program(number int) (*Program, error) {
	if number <= 0 {
		n, err := s.mainProgram()
		if err != nil {
			return nil, err
		}
		number = n
	}
	if number > math.MaxInt16 {
		return nil, fmt.Errorf("focas: номер программы %d вне диапазона", number)
	}
	text, err := s.upload(number)
	if err != nil {
		return nil, err
	}
	return &Program{Number: number, Text: text}, nil
}

// upload выгружает программу целиком.
//
// cnc_rdexecprog для этого не годится: он отдаёт только выполняемый кадр и
// несколько следующих (не больше 256 байт). Поэтому используется выгрузка
// программы из памяти ЧПУ порциями; признак конца — символ '%'.
//
// Сначала пробуется cnc_upload4 (порции до 1280 байт); если ЧПУ его не
// поддерживает, это запоминается и дальше используется cnc_upload (256 байт).
func (s *session) upload(number int) (string, error) {
	if !s.noUpload4 {
		text, err := s.upload4(number)
		if err == nil || connLost(err) {
			return text, err
		}
		s.noUpload4 = true
	}
	return s.upload256(number)
}

// upload256 — cnc_upstart / cnc_upload / cnc_upend.
func (s *session) upload256(number int) (string, error) {
	rc, err := s.rc("cnc_upstart", short(number))
	if err != nil {
		return "", err
	}
	if rc != CodeOK && rc != CodeFunc {
		// Возможно, осталась «зависшая» предыдущая выгрузка — закрыть и повторить.
		_, _ = s.rc("cnc_upend")
		if rc, err = s.rc("cnc_upstart", short(number)); err != nil {
			return "", err
		}
	}
	if rc != CodeOK {
		return "", &Error{Func: "cnc_upstart", Code: rc}
	}
	defer func() { _, _ = s.rc("cnc_upend") }()
	return s.collect("cnc_upload", s.readChunk, uploadTimeout)
}

// upload4 — cnc_upstart4 / cnc_upload4 / cnc_upend4.
// Путь вида //CNC_MEM/USER/PATH1/O0001 — формат из документации FOCAS.
func (s *session) upload4(number int) (string, error) {
	s.path = append([]byte(fmt.Sprintf("//CNC_MEM/USER/PATH1/O%04d", number)), 0)
	if err := s.call("cnc_upstart4", short(0), ptr(&s.path[0])); err != nil {
		return "", err
	}
	defer func() { _, _ = s.rc("cnc_upend4") }()
	return s.collect("cnc_upload4", s.readChunk4, probeTimeout)
}

// readChunk — одна порция cnc_upload.
func (s *session) readChunk() (Code, []byte, error) {
	b := s.buf
	b.up = odbUp{}
	b.upLen = upChunk
	rc, err := s.rc("cnc_upload", ptr(&b.up), ptr(&b.upLen))
	if err != nil || rc != CodeOK {
		return rc, nil, err
	}
	return rc, b.up.Data[:min(int(b.upLen), upChunk)], nil
}

// readChunk4 — одна порция cnc_upload4.
func (s *session) readChunk4() (Code, []byte, error) {
	b := s.buf
	clear(b.up4Buf[:])
	b.up4Len = up4Chunk
	rc, err := s.rc("cnc_upload4", ptr(&b.up4Len), ptr(&b.up4Buf[0]))
	if err != nil || rc != CodeOK {
		return rc, nil, err
	}
	return rc, b.up4Buf[:min(max(int(b.up4Len), 0), up4Chunk)], nil
}

// collect склеивает порции, пока не встретится завершающий '%'.
// EW_BUFFER означает «данные ещё не готовы» — вызов надо повторить.
// first — сколько можно ждать первую порцию, дальше — uploadTimeout на каждую.
func (s *session) collect(fn string, read func() (Code, []byte, error), first time.Duration) (string, error) {
	var sb strings.Builder
	deadline := time.Now().Add(first)
	for retries := 0; ; {
		rc, chunk, err := read()
		if err != nil {
			return "", err
		}
		switch rc {
		case CodeOK:
			retries = 0
			deadline = time.Now().Add(uploadTimeout)
			if len(chunk) == 0 {
				return normalizeText(sb.String()), nil
			}
			sb.Write(chunk)
			if sb.Len() > maxProgramSize {
				return "", fmt.Errorf("%s: программа больше %d байт — выгрузка прервана", fn, maxProgramSize)
			}
			// '%' в конце порции — конец программы (но не ведущий '%' в её начале).
			if endsWithPercent(chunk) && len(strings.TrimSpace(sb.String())) > 1 {
				return normalizeText(sb.String()), nil
			}
		case CodeBuffer:
			if time.Now().After(deadline) {
				return "", fmt.Errorf("%s: таймаут ожидания данных программы (получено %d байт)", fn, sb.Len())
			}
			// Каждый повтор и так стоит обмена с ЧПУ, поэтому без пауз;
			// спим лишь если ответ приходит мгновенно и многократно подряд.
			if retries++; retries > 50 {
				time.Sleep(time.Millisecond)
			}
		default:
			return "", &Error{Func: fn, Code: rc}
		}
	}
}

// normalizeText приводит текст ЧПУ к виду: UTF-8, переводы строк "\n", без NUL.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ToValidUTF8(s, "?")
}

// endsWithPercent — последний значащий символ порции равен '%' (конец программы).
func endsWithPercent(chunk []byte) bool {
	t := bytes.TrimRight(chunk, " \t\r\n\x00")
	return len(t) > 0 && t[len(t)-1] == '%'
}
