// mtconnect — запускает fanuc-driver и MTConnect agent поверх эмулятора
// (или реального ЧПУ) и печатает ВСЁ состояние станка из /current.
//
//	go build -o mtconnect.exe ./cmd/mtconnect
//
//	mtconnect.exe                         # запуск + опрос раз в секунду, Ctrl+C — выход
//	mtconnect.exe -interval 0             # прочитать один раз
//	mtconnect.exe -changes                # после первого снимка — только изменения
//	mtconnect.exe -cnc 192.168.56.1:8193  # ЧПУ на другом адресе
//	mtconnect.exe -url http://host:5000   # агент уже запущен: только читать
//
// Вывод драйвера и агента пишется в logs/driver.log и logs/agent.log (-v — ещё и в консоль).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"fanucAsyncAdapter/mtconnect"
)

func main() { os.Exit(run()) }

func run() int {
	var cfg mtconnect.Config
	addr := flag.String("url", "", "адрес уже запущенного агента, например http://127.0.0.1:5000 (драйвер и агент не запускаются)")
	flag.StringVar(&cfg.Root, "root", ".", "корень проекта (там лежат fanuc-driver и agent-*)")
	flag.StringVar(&cfg.CNC, "cnc", "", "адрес ЧПУ / эмулятора host:port (по умолчанию 127.0.0.1:8193)")
	flag.StringVar(&cfg.Device, "device", "", "имя устройства в MTConnect (по умолчанию f_sim; с -url пусто — все устройства)")
	interval := flag.Duration("interval", time.Second, "период опроса; 0 — прочитать один раз")
	changes := flag.Bool("changes", false, "после первого снимка печатать только изменения")
	verbose := flag.Bool("v", false, "показывать вывод fanuc.exe и agent.exe в консоли")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	var c *mtconnect.Client
	var done <-chan error // сообщает о падении драйвера или агента
	if *addr != "" {
		c = mtconnect.New(*addr, cfg.Device)
	} else {
		if *verbose {
			cfg.Log = os.Stderr
		}
		fmt.Fprintln(os.Stderr, "запускаю fanuc-driver и MTConnect agent...")
		a, err := mtconnect.Start(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			fmt.Fprintln(os.Stderr, "Ошибка:", err)
			return 1
		}
		defer a.Stop()
		c, done = a.Client, a.Done()
		fmt.Fprintln(os.Stderr, "готово:", a.URL())
	}

	var prev *mtconnect.State
	err := c.Watch(ctx, *interval, func(s *mtconnect.State) error {
		select {
		case err := <-done:
			return err
		default:
		}
		out := s
		if *changes && prev != nil {
			out = s.Changed(prev)
		}
		prev = s
		if len(out.Items) > 0 {
			out.Write(os.Stdout)
			fmt.Println()
		}
		return nil
	})
	if err != nil && ctx.Err() == nil { // Ctrl+C дошёл и до дочерних процессов — это не сбой
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		return 1
	}
	return 0
}
