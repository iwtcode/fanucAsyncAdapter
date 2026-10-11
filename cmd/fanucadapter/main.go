// fanucadapter — консольная утилита поверх пакета fanuc: подключается к
// FANUC CNC GUIDE (или реальному ЧПУ) по FOCAS2 Ethernet и выводит
// информацию о выполняемой программе, осях и полный текст управляющей программы.
//
// Сборка:
//
//	64-бит (Fwlib64.dll):  set GOARCH=amd64 && go build -o fanucadapter.exe ./cmd/fanucadapter
//	32-бит (Fwlib32.dll):  set GOARCH=386   && go build -o fanucadapter.exe ./cmd/fanucadapter
//
// Рядом с exe (или в текущем каталоге) должны лежать Fwlib64.dll + Fwlibe64.dll
// (для 32-бит: Fwlib32.dll + fwlibe1.dll).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"sync"
	"time"

	"fanucAsyncAdapter/fanuc"
)

type options struct {
	axes      bool
	program   bool
	progNum   int
	lookahead bool
	refresh   bool
	trace     bool
}

// callStats — сводка по вызовам FOCAS (флаг -trace): где уходит время.
type callStats struct {
	mu sync.Mutex
	m  map[string]*callStat
}

type callStat struct {
	n          int
	total, max time.Duration
}

func (s *callStats) add(fn string, _ fanuc.Code, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.m[fn]
	if st == nil {
		st = &callStat{}
		s.m[fn] = st
	}
	st.n++
	st.total += d
	if d > st.max {
		st.max = d
	}
}

func (s *callStats) printAndReset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.m))
	for n := range s.m {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return s.m[names[i]].total > s.m[names[j]].total })
	fmt.Println("\n--- вызовы FOCAS ---")
	fmt.Printf("%-18s %6s %12s %12s %12s\n", "функция", "раз", "всего", "среднее", "макс")
	for _, n := range names {
		st := s.m[n]
		fmt.Printf("%-18s %6d %12v %12v %12v\n", n, st.n,
			st.total.Round(time.Millisecond), (st.total / time.Duration(st.n)).Round(time.Microsecond), st.max.Round(time.Millisecond))
	}
	s.m = make(map[string]*callStat)
}

func main() { os.Exit(run()) }

func run() int {
	ip := flag.String("ip", "127.0.0.1", "IP-адрес ЧПУ / эмулятора")
	port := flag.Uint("port", 8193, "порт FOCAS2 Ethernet")
	timeout := flag.Int("timeout", 10, "таймаут соединения, сек")
	dllPath := flag.String("dll", "", "путь к Fwlib32.dll / Fwlib64.dll (по умолчанию — автопоиск)")
	interval := flag.Int("interval", 0, "период опроса, мс (0 — один раз)")

	var opt options
	flag.BoolVar(&opt.axes, "axes", true, "показывать позиции осей (-axes=false — отключить)")
	flag.BoolVar(&opt.program, "program", true, "выгружать и показывать полный текст программы (-program=false — отключить)")
	flag.IntVar(&opt.progNum, "prognum", 0, "номер программы для -program (0 — главная выполняемая)")
	flag.BoolVar(&opt.lookahead, "lookahead", false, "показывать кадры упреждающего чтения")
	flag.BoolVar(&opt.refresh, "refresh", false, "каждый раз перечитывать программу из ЧПУ, а не из кэша")
	flag.BoolVar(&opt.trace, "trace", false, "показывать время каждого этапа и вызовов FOCAS")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	stats := &callStats{m: make(map[string]*callStat)}
	cfg := fanuc.Config{
		IP:      *ip,
		Port:    uint16(*port),
		Timeout: time.Duration(*timeout) * time.Second,
		DLLPath: *dllPath,
	}
	if opt.trace {
		cfg.Trace = stats.add
	}

	t0 := time.Now()
	client, err := fanuc.Connect(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка подключения:", err)
		return 1
	}
	defer client.Close()
	fmt.Printf("Подключено к %s:%d", *ip, *port)
	if opt.trace {
		fmt.Printf(" (%v)", time.Since(t0).Round(time.Millisecond))
	}
	fmt.Print("\n\n")

	printedProg := -1 // номер программы, текст которой уже выведен
	for {
		if *interval > 0 {
			fmt.Printf("--- %s ---\n", time.Now().Format("15:04:05"))
		}
		if err := report(client, opt, &printedProg); err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка чтения:", err)
			if *interval <= 0 {
				return 1
			}
		}
		if opt.trace {
			stats.printAndReset()
		}
		if *interval <= 0 {
			return 0
		}
		fmt.Println()

		select {
		case <-ctx.Done():
			return 0
		case <-time.After(time.Duration(*interval) * time.Millisecond):
		}
	}
}

// stage замеряет длительность этапа (выводится только с -trace).
func stage(trace bool, name string, f func() error) error {
	t0 := time.Now()
	err := f()
	if trace {
		fmt.Printf("[время] %-12s %v\n", name, time.Since(t0).Round(time.Millisecond))
	}
	return err
}

func report(c *fanuc.Client, opt options, printedProg *int) error {
	var info *fanuc.ProgramInfo
	if err := stage(opt.trace, "ProgramInfo", func() (err error) { info, err = c.ProgramInfo(); return }); err != nil {
		return err
	}
	printInfo(info, opt.lookahead)

	if opt.axes {
		var axes []fanuc.Axis
		if err := stage(opt.trace, "Axes", func() (err error) { axes, err = c.Axes(); return }); err != nil {
			return err
		}
		printAxes(axes)
	}

	if opt.program {
		var prog *fanuc.Program
		err := stage(opt.trace, "Program", func() (err error) {
			if opt.refresh {
				prog, err = c.ReloadProgram(opt.progNum)
			} else {
				prog, err = c.Program(opt.progNum)
			}
			return
		})
		if err != nil {
			return err
		}
		// В режиме опроса текст не повторяется каждую секунду — только при смене программы.
		if *printedProg != prog.Number || opt.refresh {
			*printedProg = prog.Number
			fmt.Printf("\n--- Программа O%04d (%d строк, %d байт) ---\n", prog.Number, len(prog.Lines()), len(prog.Text))
			fmt.Println(prog.Text)
		}
	}
	return nil
}

func printInfo(info *fanuc.ProgramInfo, lookahead bool) {
	st := info.Status
	fmt.Printf("Режим:               %s\n", st.Mode)
	fmt.Printf("Состояние:           %s\n", st.Run)
	fmt.Printf("Движение:            %s\n", st.Motion)
	fmt.Printf("Аварии:              %s\n", st.Alarm)
	if st.Emergency {
		fmt.Println("Аварийный стоп:      АКТИВЕН")
	}

	fmt.Printf("Выполняемая прогр.:  %s (O%04d)\n", orDash(info.Name), info.Number)
	fmt.Printf("Номер выполняемой:   O%04d\n", info.RunningNumber)
	fmt.Printf("Главная программа:   O%04d\n", info.MainNumber)
	fmt.Printf("Номер кадра:         N%d\n", info.Sequence)
	fmt.Printf("Текущий кадр:        %s\n", orDash(info.CurrentBlock))
	if lookahead {
		for _, b := range info.NextBlocks {
			fmt.Printf("                     %s\n", b)
		}
	}
	for _, w := range info.Warnings {
		fmt.Printf("  [!] %v\n", w)
	}
}

func printAxes(axes []fanuc.Axis) {
	fmt.Println()
	fmt.Printf("%-6s %-5s %14s %14s %14s %14s\n", "Ось", "Ед.", "Абсолютная", "Машинная", "Относит.", "Ост. пути")
	for _, a := range axes {
		fmt.Printf("%-6s %-5s %14.4f %14.4f %14.4f %14.4f\n",
			a.Name, a.Unit, a.Absolute, a.Machine, a.Relative, a.DistanceToGo)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
