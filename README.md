# fanucAsyncAdapter

Go-библиотека (и консольная утилита) для чтения данных с ЧПУ FANUC / эмулятора
**FANUC CNC GUIDE** по FOCAS2 Ethernet. Cgo не нужен — `Fwlib64.dll` загружается через `syscall`,
поэтому работает только в Windows.

## Использование как библиотеки

```go
import "fanucAsyncAdapter/fanuc"

c, err := fanuc.Connect(fanuc.Config{IP: "127.0.0.1", Port: 8193})
if err != nil { log.Fatal(err) }
defer c.Close()

info, _ := c.ProgramInfo()    // режим, состояние, имя/номер программы, N, текущий кадр
axes, _ := c.Axes()           // X, Y, Z…: abs / machine / relative / distance-to-go
prog, _ := c.RunningProgram() // ПОЛНЫЙ текст главной выполняемой программы
other, _ := c.Program(1234)   // или любой другой программы по номеру
```

`Client` потокобезопасен. Внутри два соединения, у каждого своя горутина, закреплённая за потоком ОС:
быстрое (статус, информация, оси) и «тяжёлое» (выгрузка текста программы). Поэтому опрос
статуса и осей не ждёт, пока докачивается большая программа.

Текст программы кэшируется (`Config.ProgramCacheTTL`, по умолчанию 30 с): первый запрос идёт в ЧПУ,
повторные — мгновенные. `ReloadProgram(n)` читает из ЧПУ принудительно.

## Утилита

```bash
go build -o fanucadapter.exe ./cmd/fanucadapter

fanucadapter.exe                                  # всё: информация + оси + программа целиком
fanucadapter.exe -program=false                   # без текста программы (самый быстрый вариант)
fanucadapter.exe -axes=false                      # без осей
fanucadapter.exe -prognum 1234                    # текст конкретной программы
fanucadapter.exe -interval 500                    # опрос раз в 0,5 с (текст программы — один раз), Ctrl+C — выход
fanucadapter.exe -interval 500 -refresh           # + перечитывать программу из ЧПУ каждый раз
fanucadapter.exe -trace                           # время этапов и сводка по вызовам FOCAS
fanucadapter.exe -ip 192.168.56.1 -port 8193 -dll ./Fwlib64.dll -lookahead
```

## Тесты

Нужен запущенный эмулятор с включённым FOCAS Ethernet; если подключиться нельзя — тесты пропускаются.

```bash
go test -count=1 -v ./fanuc/
```
