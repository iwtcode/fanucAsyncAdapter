// focas — подключается к FANUC CNC GUIDE (или реальному ЧПУ) по FOCAS2 Ethernet
// и печатает состояние станка: статус, выполняемую программу, оси и шпиндели.
// Текст управляющей программы выгружается отдельным запросом (-program).
//
//	go build -o focas.exe ./cmd/focas
//
//	focas.exe                          # опрос раз в секунду, Ctrl+C — выход
//	focas.exe -interval 0              # прочитать один раз
//	focas.exe -interval 200ms          # чаще
//	focas.exe -ip 192.168.56.1         # ЧПУ на другом адресе
//	focas.exe -program 0               # выгрузить текст выполняемой программы в консоль
//	focas.exe -program 1234 -out 1234.txt  # программу O1234 — в файл
//
// Рядом с exe (или в текущем каталоге) должны лежать Fwlib64.dll и Fwlibe64.dll.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"fanucAsyncAdapter/focas"
)

func main() { os.Exit(run()) }

func run() int {
	var cfg focas.Config
	flag.StringVar(&cfg.IP, "ip", "", "IP-адрес ЧПУ / эмулятора (по умолчанию 127.0.0.1)")
	port := flag.Uint("port", 8193, "порт FOCAS2 Ethernet")
	flag.DurationVar(&cfg.Timeout, "timeout", 10*time.Second, "таймаут соединения")
	flag.StringVar(&cfg.DLLPath, "dll", "", "путь к Fwlib64.dll (по умолчанию — автопоиск)")
	interval := flag.Duration("interval", time.Second, "период опроса; 0 — прочитать один раз")
	program := flag.Int("program", -1, "вместо опроса выгрузить текст программы с этим номером (0 — выполняемая)")
	out := flag.String("out", "", "с -program: записать текст в файл (по умолчанию — в консоль)")
	flag.Parse()
	cfg.Port = uint16(*port)

	c, err := focas.Connect(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка подключения:", err)
		return 1
	}
	defer c.Close()

	if *program >= 0 {
		return dumpProgram(c, *program, *out)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err = c.Watch(ctx, *interval, func(s *focas.State) error {
		s.Write(os.Stdout)
		fmt.Println()
		return nil
	})
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		return 1
	}
	return 0
}

// dumpProgram выгружает текст программы в файл out или (если он не задан) в консоль.
func dumpProgram(c *focas.Client, number int, out string) int {
	p, err := c.Program(number)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		return 1
	}
	summary := fmt.Sprintf("Программа O%04d: %d строк, %d байт", p.Number, len(p.Lines()), len(p.Text))

	if out == "" {
		fmt.Println(p.Text)
		fmt.Fprintln(os.Stderr, summary)
		return 0
	}
	if err := os.WriteFile(out, []byte(p.Text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		return 1
	}
	path, _ := filepath.Abs(out)
	fmt.Printf("%s → %s\n", summary, path)
	return 0
}
