package fanuc_test

// Интеграционные тесты: нужен запущенный FANUC CNC GUIDE (или реальное ЧПУ)
// с включённым FOCAS2 Ethernet. Если подключиться не удалось — тесты пропускаются.
//
// Параметры (переменные окружения):
//
//	FOCAS_IP    — адрес ЧПУ      (по умолчанию 127.0.0.1)
//	FOCAS_PORT  — порт           (по умолчанию 8193)
//	FOCAS_DLL   — путь к Fwlib64.dll (по умолчанию автопоиск вверх от каталога пакета)
//
// Запуск:  go test -count=1 -v ./fanuc/
//
// -count=1 отключает кэш результатов go test: интеграционный тест зависит от
// состояния ЧПУ, которое go не отслеживает, поэтому без этого флага повторный
// запуск мог бы показать старый результат «(cached)».
// В VS Code флаг уже включён в .vscode/settings.json.

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fanucAsyncAdapter/fanuc"
)

// maxLatency — допустимая задержка одного запроса состояния или осей.
const maxLatency = time.Second

// programLogFile — файл, в который TestProgram записывает текст программы
// (создаётся в каталоге пакета, т.е. fanuc/УП.log; перезаписывается при каждом запуске).
const programLogFile = "УП.log"

// logf печатает строку в консоль без префикса «путь/к/fanuc_test.go:NN:»,
// который t.Logf добавляет всегда. Отступ сохранён, чтобы вывод оставался
// сгруппированным под строкой «=== RUN». Сообщения об ошибках (t.Errorf,
// t.Fatalf) остаются с номером строки — он нужен при разборе падения.
func logf(format string, args ...any) {
	fmt.Printf("    "+format+"\n", args...)
}

func newClient(t *testing.T) *fanuc.Client {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("FOCAS DLL работает только в Windows")
	}

	cfg := fanuc.Config{
		IP:      os.Getenv("FOCAS_IP"),
		Timeout: 5 * time.Second,
		DLLPath: os.Getenv("FOCAS_DLL"),
		// В тестах автопереподключение выключено: иначе EW_HANDLE (-8)
		// незаметно «лечился» бы и тест не ловил бы регрессию.
		DisableReconnect: true,
		// Кэш программы выключен: каждый тест честно читает данные из ЧПУ.
		ProgramCacheTTL: -1,
	}
	if v := os.Getenv("FOCAS_PORT"); v != "" {
		p, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			t.Fatalf("неверный FOCAS_PORT %q: %v", v, err)
		}
		cfg.Port = uint16(p)
	}

	c, err := fanuc.Connect(cfg)
	if err != nil {
		t.Skipf("нет подключения к ЧПУ/эмулятору: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// 1. Информация о выполняемой программе.
func TestProgramInfo(t *testing.T) {
	c := newClient(t)

	start := time.Now()
	info, err := c.ProgramInfo()
	if err != nil {
		t.Fatalf("ProgramInfo: %v", err)
	}
	took := time.Since(start)
	logf("ProgramInfo выполнен за %v", took.Round(time.Millisecond))
	if took > maxLatency {
		t.Errorf("ProgramInfo слишком медленный: %v (нужно < %v)", took, maxLatency)
	}
	logf("режим=%s состояние=%s движение=%s аварии=%s",
		info.Status.Mode, info.Status.Run, info.Status.Motion, info.Status.Alarm)
	logf("программа: имя=%q O%04d, выполняемая O%04d, главная O%04d, кадр N%d",
		info.Name, info.Number, info.RunningNumber, info.MainNumber, info.Sequence)
	logf("текущий кадр: %q (+%d кадров упреждения)", info.CurrentBlock, len(info.NextBlocks))
	for _, w := range info.Warnings {
		logf("предупреждение: %v", w)
	}

	if info.Name == "" && info.Number == 0 && info.MainNumber == 0 && info.RunningNumber == 0 {
		t.Error("ЧПУ не вернуло ни имени, ни номера выполняемой программы")
	}

	// Регрессия: раньше при повторных вызовах случайно возникал EW_HANDLE (-8)
	// из-за переезда горутины на другой поток ОС. Вызовы из многих горутин
	// обязаны проходить без ошибок. Все вызовы идут по очереди через один поток,
	// поэтому берём лёгкий Status (1 вызов FOCAS ≈ 30 мс), а не ProgramInfo
	// (5 вызовов ≈ 150 мс). Пропускается при `go test -short`.
	if testing.Short() {
		return
	}
	const goroutines, iterations = 4, 5
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*iterations)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if _, err := c.Status(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("повторный Status: %v", err)
	}
}

// 2. Информация об осях.
func TestAxes(t *testing.T) {
	c := newClient(t)

	start := time.Now()
	axes, err := c.Axes()
	if err != nil {
		t.Fatalf("Axes: %v", err)
	}
	took := time.Since(start)
	logf("Axes выполнен за %v", took.Round(time.Millisecond))
	if took > maxLatency {
		t.Errorf("Axes слишком медленный: %v (нужно < %v)", took, maxLatency)
	}
	if len(axes) == 0 {
		t.Fatal("ЧПУ не вернуло ни одной оси")
	}

	seen := map[string]bool{}
	for _, a := range axes {
		logf("%-3s [%s] abs=%.4f mach=%.4f rel=%.4f dist=%.4f",
			a.Name, a.Unit, a.Absolute, a.Machine, a.Relative, a.DistanceToGo)

		if a.Name == "" {
			t.Error("у оси пустое имя")
		}
		if seen[a.Name] {
			t.Errorf("ось %q встретилась дважды", a.Name)
		}
		seen[a.Name] = true

		for _, v := range []float64{a.Absolute, a.Machine, a.Relative, a.DistanceToGo} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("ось %s: некорректное значение %v", a.Name, v)
			}
		}
	}
}

// 3. Управляющая программа целиком.
func TestProgram(t *testing.T) {
	c := newClient(t)

	// Параллельно с выгрузкой запрашиваем состояние: оно не должно ждать,
	// пока программа докачается (выгрузка идёт по отдельному соединению).
	var (
		wg       sync.WaitGroup
		infoTook time.Duration
		infoErr  error
	)
	start := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(200 * time.Millisecond) // дать выгрузке начаться
		t0 := time.Now()
		_, infoErr = c.ProgramInfo()
		infoTook = time.Since(t0)
	}()

	prog, err := c.RunningProgram()
	loadTook := time.Since(start)
	wg.Wait()
	if err != nil {
		t.Fatalf("загрузка программы: %v", err)
	}
	logf("выгрузка из ЧПУ: %v", loadTook.Round(time.Millisecond))

	if infoErr != nil {
		t.Errorf("ProgramInfo во время выгрузки: %v", infoErr)
	}
	logf("ProgramInfo во время выгрузки: %v", infoTook.Round(time.Millisecond))
	if infoTook > maxLatency {
		t.Errorf("ProgramInfo ждал выгрузку: %v (нужно < %v); возможно, ЧПУ не приняло второе соединение",
			infoTook, maxLatency)
	}

	text := strings.TrimSpace(prog.Text)
	if text == "" {
		t.Fatal("получен пустой текст программы")
	}
	lines := prog.Lines()
	logf("программа O%04d: %d байт, %d строк", prog.Number, len(prog.Text), len(lines))

	// Программа выгружена целиком, если дошли до завершающего '%'.
	if !strings.HasSuffix(text, "%") {
		t.Errorf("программа выгружена не полностью: нет завершающего '%%' (получено %d байт)", len(prog.Text))
	}
	if strings.Contains(prog.Text, "\r") || strings.Contains(prog.Text, "\x00") {
		t.Error("в тексте остались \\r или NUL")
	}

	// Выполняемый кадр должен находиться в тексте программы (сравнение без пробелов).
	// Формат кадра в буфере CNC может отличаться, поэтому только предупреждение.
	if info, err := c.ProgramInfo(); err == nil && info.CurrentBlock != "" {
		strip := func(s string) string { return strings.ReplaceAll(s, " ", "") }
		if !strings.Contains(strip(prog.Text), strip(info.CurrentBlock)) {
			logf("выполняемый кадр %q не найден в тексте программы дословно", info.CurrentBlock)
		}
	}

	// Вся программа целиком — в файл, в консоль не выводится.
	if err := os.WriteFile(programLogFile, []byte(prog.Text), 0o644); err != nil {
		t.Errorf("запись %s: %v", programLogFile, err)
	} else {
		path, _ := filepath.Abs(programLogFile)
		logf("текст программы записан в %s", path)
	}
}
