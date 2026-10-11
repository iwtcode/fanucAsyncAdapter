package focas_test

// Интеграционные тесты на настоящем FANUC CNC GUIDE (или реальном ЧПУ) с
// включённым FOCAS2 Ethernet. Если подключиться не удалось — тесты пропускаются.
//
// Параметры (переменные окружения):
//
//	FOCAS_IP    — адрес ЧПУ          (по умолчанию 127.0.0.1)
//	FOCAS_PORT  — порт               (по умолчанию 8193)
//	FOCAS_DLL   — путь к Fwlib64.dll (по умолчанию автопоиск вверх от каталога пакета)
//
// Запуск:  go test -count=1 -v ./focas/
//          go test -count=1 -v -short ./focas/   (без выгрузки программы)
//
// Тесты идут строго по очереди через одно соединение, времени не проверяют
// (оно только выводится в лог): скорость ответов эмулятора от нас не зависит.
// -count=1 отключает кэш результатов go test: результат зависит от состояния
// ЧПУ, которое go не отслеживает. В VS Code флаг уже включён в .vscode/settings.json.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fanucAsyncAdapter/focas"
)

// programLogFile — файл, в который TestProgram записывает текст программы
// (создаётся в каталоге пакета, т.е. focas/УП.log; перезаписывается при каждом запуске).
const programLogFile = "УП.log"

// Одно общее подключение на все тесты.
var (
	client *focas.Client
	skip   string // причина пропуска, если подключиться не удалось
)

func TestMain(m *testing.M) { os.Exit(run(m)) }

func run(m *testing.M) int {
	cfg := focas.Config{
		IP:      os.Getenv("FOCAS_IP"),
		DLLPath: os.Getenv("FOCAS_DLL"),
		Timeout: 5 * time.Second,
	}
	if v := os.Getenv("FOCAS_PORT"); v != "" {
		p, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			fmt.Fprintf(os.Stderr, "неверный FOCAS_PORT %q: %v\n", v, err)
			return 2
		}
		cfg.Port = uint16(p)
	}

	c, err := focas.Connect(cfg)
	if err != nil {
		skip = fmt.Sprintf("нет подключения к ЧПУ/эмулятору: %v", err)
		return m.Run()
	}
	defer c.Close()
	client = c
	return m.Run()
}

func conn(t *testing.T) *focas.Client {
	t.Helper()
	if client == nil {
		t.Skip(skip)
	}
	return client
}

// logf печатает строку в консоль без префикса «путь/к/файлу.go:NN:», который
// t.Logf добавляет всегда. Отступ сохранён, чтобы вывод оставался сгруппированным
// под строкой «=== RUN». Сообщения об ошибках (t.Errorf, t.Fatalf) остаются с
// номером строки — он нужен при разборе падения.
func logf(format string, args ...any) {
	fmt.Printf("    "+format+"\n", args...)
}

// timed выполняет запрос, падает при ошибке и выводит время выполнения.
func timed[T any](t *testing.T, name string, f func() (T, error)) T {
	t.Helper()
	t0 := time.Now()
	v, err := f()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	logf("%s выполнен за %v", name, time.Since(t0).Round(time.Millisecond))
	return v
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// 1. Общее состояние станка.
func TestStatus(t *testing.T) {
	st := timed(t, "Status", conn(t).Status)
	logf("режим=%s состояние=%s движение=%s аварии=%s аварийный стоп=%t",
		st.Mode, st.Run, st.Motion, st.Alarm, st.Emergency)
}

// 2. Сведения о выполняемой программе.
func TestProgramInfo(t *testing.T) {
	info := timed(t, "ProgramInfo", conn(t).ProgramInfo)
	logf("программа: имя=%q O%04d, выполняемая O%04d, главная O%04d, кадр N%d",
		info.Name, info.Number, info.RunningNumber, info.MainNumber, info.Sequence)
	logf("текущий кадр: %q", info.CurrentBlock)
	for _, w := range info.Warnings {
		logf("предупреждение: %v", w)
	}
	if info.Name == "" && info.Number == 0 && info.MainNumber == 0 && info.RunningNumber == 0 {
		t.Error("ЧПУ не вернуло ни имени, ни номера выполняемой программы")
	}
}

// 3. Оси.
func TestAxes(t *testing.T) {
	axes := timed(t, "Axes", conn(t).Axes)
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
			if !finite(v) {
				t.Errorf("ось %s: некорректное значение %v", a.Name, v)
			}
		}
	}
}

// 4. Шпиндели.
func TestSpindles(t *testing.T) {
	spindles := timed(t, "Spindles", conn(t).Spindles)
	if len(spindles) == 0 {
		t.Fatal("ЧПУ не вернуло ни одного шпинделя")
	}
	seen := map[string]bool{}
	for _, sp := range spindles {
		logf("%-3s скорость=%.1f об/мин нагрузка=%.1f%%", sp.Name, sp.Speed, sp.Load)
		if sp.Name == "" {
			t.Error("у шпинделя пустое имя")
		}
		if seen[sp.Name] {
			t.Errorf("шпиндель %q встретился дважды", sp.Name)
		}
		seen[sp.Name] = true
		for _, v := range []float64{sp.Speed, sp.Load} {
			if !finite(v) {
				t.Errorf("шпиндель %s: некорректное значение %v", sp.Name, v)
			}
		}
	}
}

// 5. Агрегат всех состояний (без текста программы): всё прочитано одним снимком
// и согласуется с отдельными запросами.
func TestState(t *testing.T) {
	c := conn(t)
	st := timed(t, "State", c.State)

	var sb strings.Builder
	st.Write(&sb)
	logf("\n%s", strings.TrimRight(sb.String(), "\n"))

	if st.Time.IsZero() {
		t.Error("не задано время снимка")
	}
	if st.Info.Name == "" && st.Info.Number == 0 && st.Info.MainNumber == 0 && st.Info.RunningNumber == 0 {
		t.Error("в состоянии нет ни имени, ни номера выполняемой программы")
	}
	if len(st.Axes) == 0 {
		t.Error("в состоянии нет осей")
	}
	if len(st.Spindles) == 0 {
		t.Error("в состоянии нет шпинделей")
	}
	for _, w := range st.Warnings {
		logf("предупреждение: %v", w)
	}

	// Состав осей и шпинделей должен совпадать с отдельными запросами
	// (значения не сравниваем: станок может двигаться).
	axes, err := c.Axes()
	if err != nil {
		t.Fatalf("Axes: %v", err)
	}
	if len(axes) != len(st.Axes) {
		t.Fatalf("осей в State: %d, в Axes: %d", len(st.Axes), len(axes))
	}
	for i := range axes {
		if axes[i].Name != st.Axes[i].Name {
			t.Errorf("ось %d: в State %q, в Axes %q", i, st.Axes[i].Name, axes[i].Name)
		}
	}
	spindles, err := c.Spindles()
	if err != nil {
		t.Fatalf("Spindles: %v", err)
	}
	if len(spindles) != len(st.Spindles) {
		t.Fatalf("шпинделей в State: %d, в Spindles: %d", len(st.Spindles), len(spindles))
	}
	for i := range spindles {
		if spindles[i].Name != st.Spindles[i].Name {
			t.Errorf("шпиндель %d: в State %q, в Spindles %q", i, st.Spindles[i].Name, spindles[i].Name)
		}
	}
}

// 6. Опрос: Watch вызывает fn с новым состоянием каждый период и останавливается по ctx.
func TestWatch(t *testing.T) {
	c := conn(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	n := 0
	err := c.Watch(ctx, 100*time.Millisecond, func(st *focas.State) error {
		if n++; n == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch вернул %v, ожидалась context.Canceled", err)
	}
	if n < 3 {
		t.Fatalf("fn вызвана %d раз, ожидалось не меньше 3", n)
	}
	logf("Watch: %d опросов", n)
}

// 7. Текст управляющей программы целиком — отдельный запрос, в State не входит.
func TestProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("выгрузка программы идёт секунды (отключено флагом -short)")
	}
	c := conn(t)
	prog := timed(t, "Program", func() (*focas.Program, error) { return c.Program(0) })

	text := strings.TrimSpace(prog.Text)
	if text == "" {
		t.Fatal("получен пустой текст программы")
	}
	logf("программа O%04d: %d байт, %d строк", prog.Number, len(prog.Text), len(prog.Lines()))

	// Программа выгружена целиком, если дошли до завершающего '%'.
	if !strings.HasSuffix(text, "%") {
		t.Errorf("программа выгружена не полностью: нет завершающего '%%' (получено %d байт)", len(prog.Text))
	}
	if strings.Contains(prog.Text, "\r") || strings.Contains(prog.Text, "\x00") {
		t.Error("в тексте остались \\r или NUL")
	}

	// Вся программа целиком — в файл, в консоль не выводится.
	if err := os.WriteFile(programLogFile, []byte(prog.Text), 0o644); err != nil {
		t.Errorf("запись %s: %v", programLogFile, err)
		return
	}
	path, _ := filepath.Abs(programLogFile)
	logf("текст программы записан в %s", path)
}
