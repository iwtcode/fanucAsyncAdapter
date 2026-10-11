# fanucAsyncAdapter

Два Go-пакета и две утилиты для чтения данных с ЧПУ FANUC / эмулятора **FANUC CNC GUIDE**.
Только Windows (64-бит).

| Пакет       | Утилита        | Что делает                                                        |
|-------------|----------------|-------------------------------------------------------------------|
| `focas`     | `cmd/focas`     | Читает ЧПУ напрямую по FOCAS2 Ethernet через `Fwlib64.dll`        |
| `mtconnect` | `cmd/mtconnect` | Поднимает fanuc-driver + MTConnect agent и читает их состояние   |

## focas

```go
import "fanucAsyncAdapter/focas"

c, err := focas.Connect(focas.Config{IP: "127.0.0.1", Port: 8193})
if err != nil { log.Fatal(err) }
defer c.Close()

st, _ := c.State() // всё состояние одним снимком: статус, программа, оси, шпиндели
st.Write(os.Stdout)

c.Watch(ctx, time.Second, func(st *focas.State) error { // опрос
    st.Write(os.Stdout)
    return nil
})

prog, _ := c.Program(0) // текст управляющей программы — отдельный запрос (0 — выполняемая, или номер)
```

Отдельные части: `Status()`, `ProgramInfo()`, `Axes()`, `Spindles()`.

- **Опрос** (`State`, `Watch`) текст программы не читает: он быстрый и не зависит от размера программы.
- **Программа** (`Program`) выгружается секунды (на эмуляторе ≈4 КБ/с). Один `Client` — одно соединение,
  поэтому пока идёт выгрузка, его остальные запросы ждут. Если опрос не должен простаивать,
  откройте для программы второй `Client`.
- Части состояния, которые ЧПУ не поддерживает, не обрывают чтение: причина попадает в `State.Warnings`.
- При потере соединения `Client` один раз переподключается и повторяет запрос.
- Все вызовы FOCAS идут из одной горутины, закреплённой за потоком ОС (иначе DLL отвечает `EW_HANDLE`).

### Утилита

```bash
go build -o focas.exe ./cmd/focas

./focas.exe                              # опрос раз в секунду, Ctrl+C — выход
./focas.exe -interval 0                  # прочитать один раз
./focas.exe -interval 200ms              # чаще
./focas.exe -ip 192.168.56.1 -port 8193  # ЧПУ на другом адресе
./focas.exe -program 0                   # вместо опроса: текст выполняемой программы в консоль
./focas.exe -program 1234 -out 1234.txt  # программа O1234 — в файл
```

### Тесты

Нужен запущенный эмулятор с включённым FOCAS Ethernet; без него тесты пропускаются.
Тесты идут по очереди через одно соединение и ничего не имитируют: всё читается с настоящего ЧПУ.

```bash
go test -count=1 -v ./focas/          # всё, включая выгрузку программы (≈8 с, текст — в focas/УП.log)
go test -count=1 -v -short ./focas/   # без выгрузки программы
```

Проверяются: статус, сведения о программе, оси, шпиндели, агрегат `State` (согласован с отдельными запросами),
`Watch` и выгрузка программы. Адрес и DLL — `FOCAS_IP`, `FOCAS_PORT`, `FOCAS_DLL`.

## mtconnect

Сам поднимает цепочку `ЧПУ/эмулятор (FOCAS :8193) → fanuc-driver (SHDR :7878) → MTConnect agent (HTTP :5000)`
и отдаёт **всё состояние станка** — по одному значению на каждый DataItem из модели устройства. Чистый Go,
от пакета `focas` не зависит.

### Требования: MTConnect agent

Бинарники агента в репозитории не хранятся (файлы слишком большие для GitHub), их нужно скачать отдельно:

1. Откройте релиз [cppagent v2.8.0.3](https://github.com/mtconnect/cppagent/releases#release-v2.8.0.3).
2. Скачайте архив для Windows 64-бит (`win64`).
3. Распакуйте его в **корень проекта** так, чтобы получилась папка `agent-2.8.0.3-win64/`.

### Как библиотека

```go
import "fanucAsyncAdapter/mtconnect"

a, err := mtconnect.Start(ctx, mtconnect.Config{Root: ".", CNC: "127.0.0.1:8193"})
if err != nil { log.Fatal(err) }
defer a.Stop()

st, _ := a.Current(ctx)                  // всё состояние: []Item с путём компонента, типом, единицами, значением
mode, _ := st.Get("f_sim_p1_ctl_mode")   // или одно значение по id DataItem
probe, _ := a.Probe(ctx)                 // описание устройства: компоненты → DataItem
smp, _ := a.Sample(ctx, 0, 100)          // серия наблюдений из буфера агента

a.Watch(ctx, time.Second, func(s *mtconnect.State) error { // опрос
    s.Write(os.Stdout)
    return nil
})
```

Агент уже запущен — достаточно клиента без запуска процессов: `mtconnect.New("http://127.0.0.1:5000", "f_sim")`.
`a.Done()` сообщает, если драйвер или агент упали сами.

### Утилита

```bash
go build -o mtconnect.exe ./cmd/mtconnect

./mtconnect.exe                          # запуск + всё состояние раз в секунду, Ctrl+C — выход
./mtconnect.exe -interval 0              # прочитать один раз
./mtconnect.exe -changes                 # после первого снимка — только изменения
./mtconnect.exe -cnc 192.168.56.1:8193   # ЧПУ на другом адресе
./mtconnect.exe -url http://host:5000    # агент уже запущен: только читать
./mtconnect.exe -v                       # + вывод fanuc.exe/agent.exe в консоль (иначе — только в logs/*.log)
```

Что именно доступно, определяют коллекторы драйвера в `fanuc-driver/user/config.machines.yml`
(машина `f_sim_mtc_long`): клиент показывает всё, что агент отдаёт по `/current`.

### Тест

Один интеграционный тест на настоящих драйвере и агенте: запуск, `/probe`, `/current`, `/sample`.
Нужен запущенный эмулятор ЧПУ; без него тест пропускается.

```bash
go test -count=1 -v ./mtconnect/     # корень проекта ищется вверх от папки пакета или MTCONNECT_ROOT
```
