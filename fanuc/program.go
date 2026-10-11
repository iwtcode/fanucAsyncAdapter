package fanuc

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"
)

const (
	// maxProgramSize — защита от бесконечного цикла выгрузки.
	maxProgramSize = 64 << 20
	// probeIdle — сколько ждать данные от cnc_upload4 при первой попытке,
	// прежде чем считать, что ЧПУ его не поддерживает, и вернуться к cnc_upload.
	probeIdle = 3 * time.Second
)

// currentMainProgram определяет номер главной (а если его нет — выполняемой) программы.
func (s *session) currentMainProgram() (int, error) {
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
	} else if IsConnectionError(err) {
		return 0, err
	}

	b.exe = odbExePrg{}
	if err := s.call("cnc_exeprgname", ptr(&b.exe)); err != nil {
		return 0, err
	}
	if n := int(b.exe.ONum); n > 0 {
		return n, nil
	}
	return 0, errors.New("fanuc: не удалось определить номер выполняемой программы")
}

// program выгружает полный текст программы. number <= 0 — главная выполняемая программа.
func (s *session) program(number int) (*Program, error) {
	return s.programLimit(number, 0)
}

// programLimit — как program, но если limit > 0, выгрузка прекращается, как только
// получено не меньше limit байт (текст обрезается по границе порции).
func (s *session) programLimit(number, limit int) (*Program, error) {
	if number <= 0 {
		n, err := s.currentMainProgram()
		if err != nil {
			return nil, err
		}
		number = n
	}
	if number > math.MaxInt16 {
		return nil, fmt.Errorf("fanuc: номер программы %d вне диапазона", number)
	}
	text, err := s.upload(number, limit)
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
// Каждая порция — отдельный обмен с ЧПУ (≈30 мс), поэтому чем крупнее порция,
// тем быстрее: cnc_upload4 отдаёт до 1280 байт, cnc_upload — только 256.
// Сначала пробуется cnc_upload4; если ЧПУ его не поддерживает, это
// запоминается и дальше используется cnc_upload.
func (s *session) upload(number, limit int) (string, error) {
	switch s.cfg.UploadMode {
	case Upload256:
		return s.upload256(number, limit)
	case Upload4:
		return s.upload4(number, limit)
	}
	if !s.shared.noUpload4.Load() {
		text, err := s.upload4(number, limit)
		if err == nil {
			return text, nil
		}
		if IsConnectionError(err) {
			return "", err
		}
		s.shared.noUpload4.Store(true)
	}
	return s.upload256(number, limit)
}

// upload256 — cnc_upstart / cnc_upload / cnc_upend (порции по 256 байт).
func (s *session) upload256(number, limit int) (string, error) {
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
	return s.collect("cnc_upload", s.readChunk, s.cfg.UploadTimeout, limit)
}

// upload4 — cnc_upstart4 / cnc_upload4 / cnc_upend4 (порции до 1280 байт).
// Путь вида //CNC_MEM/USER/PATH1/O0001 — формат из документации FOCAS.
func (s *session) upload4(number, limit int) (string, error) {
	s.pathBuf = append([]byte(fmt.Sprintf("//CNC_MEM/USER/PATH1/O%04d", number)), 0)
	if err := s.call("cnc_upstart4", short(0), ptr(&s.pathBuf[0])); err != nil {
		return "", err
	}
	defer func() { _, _ = s.rc("cnc_upend4") }()

	// Первая попытка — «разведка»: короткий таймаут, чтобы не ждать
	// полминуты, если ЧПУ молча не отдаёт данные.
	idle := s.cfg.UploadTimeout
	if idle > probeIdle && s.cfg.UploadMode != Upload4 { // при явном Upload4 отката нет — ждём полный таймаут
		idle = probeIdle
	}
	return s.collect("cnc_upload4", s.readChunk4, idle, limit)
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
	n := int(b.upLen)
	if n > upChunk {
		n = upChunk
	}
	return rc, b.up.Data[:n], nil
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
	n := int(b.up4Len)
	if n < 0 {
		n = 0
	}
	if n > up4Chunk {
		n = up4Chunk
	}
	return rc, b.up4Buf[:n], nil
}

// collect склеивает порции, пока не встретится завершающий '%'.
// EW_BUFFER означает «данные ещё не готовы» — вызов надо повторить.
// idle — сколько можно ждать очередную порцию; limit > 0 — остановиться после limit байт.
func (s *session) collect(fn string, read func() (Code, []byte, error), idle time.Duration, limit int) (string, error) {
	var sb strings.Builder
	deadline := time.Now().Add(idle)
	retries := 0

	for {
		rc, chunk, err := read()
		if err != nil {
			return "", err
		}
		switch rc {
		case CodeOK:
			retries = 0
			deadline = time.Now().Add(s.cfg.UploadTimeout)
			if len(chunk) == 0 {
				return normalizeText(sb.String()), nil
			}
			sb.Write(chunk)
			if sb.Len() > maxProgramSize {
				return "", fmt.Errorf("%s: программа больше %d байт — выгрузка прервана", fn, maxProgramSize)
			}
			if limit > 0 && sb.Len() >= limit {
				return normalizeText(sb.String()), nil // нужно только начало программы
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
				runtime.Gosched()
				time.Sleep(time.Millisecond)
			}
		default:
			return "", &Error{Func: fn, Code: rc}
		}
	}
}
