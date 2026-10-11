package fanuc

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Config — параметры подключения. Нулевые значения заменяются значениями по умолчанию.
type Config struct {
	// IP — адрес ЧПУ или эмулятора (по умолчанию 127.0.0.1).
	IP string
	// Port — порт FOCAS2 Ethernet (по умолчанию 8193).
	Port uint16
	// Timeout — таймаут соединения (по умолчанию 10 с, округляется вверх до секунд).
	Timeout time.Duration
	// DLLPath — путь к Fwlib32.dll / Fwlib64.dll. Пусто — автопоиск (см. FindDLL).
	DLLPath string
	// UploadTimeout — сколько ждать очередную порцию текста программы
	// при выгрузке (по умолчанию 30 с).
	UploadTimeout time.Duration
	// ProgramCacheTTL — сколько хранить выгруженный текст программы в памяти
	// (0 — 30 с, отрицательное значение — не кэшировать). Выгрузка 27 КБ
	// идёт по сети порциями и занимает секунды, а повторный запрос из кэша — мгновенно.
	// Принудительно перечитать программу: Client.ReloadProgram.
	ProgramCacheTTL time.Duration
	// UploadMode — способ выгрузки программы (по умолчанию UploadAuto).
	// Скорость зависит от ЧПУ/эмулятора: на разных стендах быстрее оказывается
	// то cnc_upload4, то cnc_upload — сравните с помощью -trace.
	UploadMode UploadMode
	// MainNumberTTL — сколько номер главной программы, полученный из
	// ProgramInfo или предыдущего чтения, считать актуальным: пока он свеж,
	// Program(0) / RunningProgram не делают лишний вызов ЧПУ (0 — 2 с,
	// отрицательное значение — каждый раз спрашивать ЧПУ).
	MainNumberTTL time.Duration
	// Trace, если задан, вызывается после каждого вызова функции FOCAS
	// (имя, код возврата, длительность). Вызывается из рабочего потока —
	// не блокируйте его.
	Trace TraceFunc
	// DisableReconnect отключает автоматическое переподключение и повтор
	// операции при EW_HANDLE / EW_SOCKET / EW_PROTOCOL. Нужно в тестах,
	// чтобы такие ошибки не маскировались.
	DisableReconnect bool
}

// UploadMode — способ выгрузки текста программы.
type UploadMode int

const (
	// UploadAuto — cnc_upload4 (до 1280 байт), при неудаче — cnc_upload.
	UploadAuto UploadMode = iota
	// Upload256 — только cnc_upload (порции по 256 байт).
	Upload256
	// Upload4 — только cnc_upload4, без отката на cnc_upload.
	Upload4
)

// TraceFunc — обработчик трассировки вызовов FOCAS.
type TraceFunc func(fn string, rc Code, d time.Duration)

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
	if c.UploadTimeout <= 0 {
		c.UploadTimeout = 30 * time.Second
	}
	if c.ProgramCacheTTL == 0 {
		c.ProgramCacheTTL = 30 * time.Second
	}
	if c.MainNumberTTL == 0 {
		c.MainNumberTTL = 2 * time.Second
	}
	if c.DLLPath == "" {
		c.DLLPath = FindDLL()
	}
	return c
}

func (c Config) timeoutSec() int32 {
	s := int32(math.Ceil(c.Timeout.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

// DLLName возвращает имя FOCAS-библиотеки, соответствующее разрядности программы.
func DLLName() string {
	if runtime.GOARCH == "amd64" {
		return "Fwlib64.dll"
	}
	return "Fwlib32.dll"
}

// FindDLL ищет DLLName() рядом с exe, в текущем каталоге и в его родительских
// каталогах (до 6 уровней — удобно для `go test` из подпапки пакета).
// Если файл не найден, возвращается просто имя — Windows поищет сам.
func FindDLL() string {
	name := DLLName()
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		d := wd
		for range 6 {
			dirs = append(dirs, d)
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}
