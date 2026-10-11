//go:build windows

// fanuc_prog — подключается к FANUC CNC GUIDE (или реальному ЧПУ) по FOCAS2 Ethernet
// и выводит информацию о выполняемой программе.
//
// Cgo не нужен: Fwlib32.dll / Fwlib64.dll загружаются напрямую через syscall.
//
// Сборка:
//
//	32-бит (Fwlib32.dll):  set GOARCH=386   && go build -o fanuc_prog.exe .
//	64-бит (Fwlib64.dll):  set GOARCH=amd64 && go build -o fanuc_prog.exe .
//
// Рядом с exe должны лежать:
//
//	32-бит: Fwlib32.dll + fwlibe1.dll
//	64-бит: Fwlib64.dll + Fwlibe64.dll
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ---------- Структуры FOCAS (на Windows long = 4 байта) ----------

// ODBEXEPRG — cnc_exeprgname
type odbExePrg struct {
	Name [36]byte
	ONum int32
}

// ODBPRO — cnc_rdprgnum
type odbPro struct {
	Dummy [2]int16
	Data  int16 // номер выполняемой программы
	MData int16 // номер главной программы
}

// ODBSEQ — cnc_rdseqnum
type odbSeq struct {
	Dummy [2]int16
	Data  int32 // номер кадра (N)
}

// ODBST — cnc_statinfo
type odbSt struct {
	Dummy     [2]int16
	Aut       int16
	Run       int16
	Motion    int16
	Mstb      int16
	Emergency int16
	Alarm     int16
	Edit      int16
}

// ---------- Описание кодов ----------

var focasErrors = map[int16]string{
	-17: "EW_PROTOCOL: ошибка протокола",
	-16: "EW_SOCKET: нет соединения (проверьте IP/порт и включён ли FOCAS Ethernet в эмуляторе)",
	-15: "EW_NODLL: не найдена DLL (нужны fwlibe1.dll для 32-бит или Fwlibe64.dll для 64-бит)",
	-8:  "EW_HANDLE: ошибка получения handle",
	-7:  "EW_VERSION: несовместимая версия CNC/DLL",
	-1:  "EW_BUSY: ЧПУ занято",
	1:   "EW_FUNC: функция не поддерживается этим ЧПУ",
	2:   "EW_LENGTH: неверная длина данных",
	3:   "EW_NUMBER: неверный номер",
	5:   "EW_DATA: неверные данные",
	6:   "EW_NOOPT: нет нужной опции ЧПУ",
	12:  "EW_MODE: неверный режим ЧПУ",
	13:  "EW_REJECT: запрос отклонён",
}

func errText(code int16) string {
	if s, ok := focasErrors[code]; ok {
		return fmt.Sprintf("%s (код %d)", s, code)
	}
	return fmt.Sprintf("ошибка FOCAS, код %d", code)
}

func lookup(m map[int16]string, v int16) string {
	if s, ok := m[v]; ok {
		return s
	}
	return fmt.Sprintf("? (%d)", v)
}

var modeNames = map[int16]string{
	0: "MDI", 1: "MEM (авто)", 2: "****", 3: "EDIT", 4: "HANDLE",
	5: "JOG", 6: "TEACH IN HANDLE", 7: "INC FEED", 8: "REFERENCE", 9: "REMOTE",
}
var runNames = map[int16]string{
	0: "RESET", 1: "STOP", 2: "HOLD", 3: "START", 4: "MSTR",
}
var motionNames = map[int16]string{
	0: "нет движения", 1: "движение", 2: "выдержка (dwell)",
}
var alarmNames = map[int16]string{
	0: "нет", 1: "ALARM", 2: "BATTERY", 3: "FAN",
}

// ---------- Обёртка над FOCAS ----------

type focas struct {
	dll    *syscall.DLL
	handle uint16
}

func loadFocas(path string) (*focas, error) {
	dll, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось загрузить %s: %w\n"+
			"Подсказка: разрядность DLL должна совпадать с разрядностью программы "+
			"(GOARCH=386 -> Fwlib32.dll, GOARCH=amd64 -> Fwlib64.dll)", path, err)
	}
	return &focas{dll: dll}, nil
}

// call вызывает функцию FOCAS и возвращает её код возврата (short).
func (f *focas) call(name string, args ...uintptr) (int16, error) {
	proc, err := f.dll.FindProc(name)
	if err != nil {
		return 0, err
	}
	r, _, _ := proc.Call(args...)
	return int16(r), nil
}

func (f *focas) connect(ip string, port uint16, timeoutSec int32) error {
	ipPtr, err := syscall.BytePtrFromString(ip)
	if err != nil {
		return err
	}
	var h uint16
	rc, err := f.call("cnc_allclibhndl3",
		uintptr(unsafe.Pointer(ipPtr)),
		uintptr(port),
		uintptr(timeoutSec),
		uintptr(unsafe.Pointer(&h)),
	)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("cnc_allclibhndl3: %s", errText(rc))
	}
	f.handle = h
	return nil
}

func (f *focas) disconnect() {
	if f.handle != 0 {
		f.call("cnc_freelibhndl", uintptr(f.handle))
		f.handle = 0
	}
}

func cstr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// ---------- Чтение информации о программе ----------

type progInfo struct {
	Status    *odbSt
	ExeName   string
	ExeONum   int32
	HasName   bool
	RunProg   int16
	MainProg  int16
	HasProgNo bool
	SeqNum    int32
	HasSeq    bool
	Block     string
	HasBlock  bool
	Warnings  []string
}

func (f *focas) readInfo() (*progInfo, error) {
	info := &progInfo{}
	h := uintptr(f.handle)

	// Состояние станка (обязательный вызов — по нему понимаем, что связь жива)
	var st odbSt
	rc, err := f.call("cnc_statinfo", h, uintptr(unsafe.Pointer(&st)))
	if err != nil {
		return nil, err
	}
	if rc != 0 {
		return nil, fmt.Errorf("cnc_statinfo: %s", errText(rc))
	}
	info.Status = &st

	// Имя выполняемой программы
	var exe odbExePrg
	if rc, err = f.call("cnc_exeprgname", h, uintptr(unsafe.Pointer(&exe))); err == nil && rc == 0 {
		info.ExeName = cstr(exe.Name[:])
		info.ExeONum = exe.ONum
		info.HasName = true
	} else {
		info.Warnings = append(info.Warnings, warn("cnc_exeprgname", rc, err))
	}

	// Номера выполняемой и главной программ
	var pro odbPro
	if rc, err = f.call("cnc_rdprgnum", h, uintptr(unsafe.Pointer(&pro))); err == nil && rc == 0 {
		info.RunProg = pro.Data
		info.MainProg = pro.MData
		info.HasProgNo = true
	} else {
		info.Warnings = append(info.Warnings, warn("cnc_rdprgnum", rc, err))
	}

	// Номер кадра (N)
	var seq odbSeq
	if rc, err = f.call("cnc_rdseqnum", h, uintptr(unsafe.Pointer(&seq))); err == nil && rc == 0 {
		info.SeqNum = seq.Data
		info.HasSeq = true
	} else {
		info.Warnings = append(info.Warnings, warn("cnc_rdseqnum", rc, err))
	}

	// Текст текущего выполняемого кадра
	var (
		buf    [256]byte
		length = uint16(len(buf) - 1)
		blkNum int16
	)
	if rc, err = f.call("cnc_rdexecprog", h,
		uintptr(unsafe.Pointer(&length)),
		uintptr(unsafe.Pointer(&blkNum)),
		uintptr(unsafe.Pointer(&buf[0])),
	); err == nil && rc == 0 {
		n := int(length)
		if n > len(buf) {
			n = len(buf)
		}
		info.Block = cstr(buf[:n])
		info.HasBlock = true
	} else {
		info.Warnings = append(info.Warnings, warn("cnc_rdexecprog", rc, err))
	}

	return info, nil
}

func warn(fn string, rc int16, err error) string {
	if err != nil {
		return fmt.Sprintf("%s: %v", fn, err)
	}
	return fmt.Sprintf("%s: %s", fn, errText(rc))
}

func printInfo(info *progInfo) {
	st := info.Status
	fmt.Printf("Режим:               %s\n", lookup(modeNames, st.Aut))
	fmt.Printf("Состояние:           %s\n", lookup(runNames, st.Run))
	fmt.Printf("Движение:            %s\n", lookup(motionNames, st.Motion))
	fmt.Printf("Аварии:              %s\n", lookup(alarmNames, st.Alarm))
	if st.Emergency == 1 {
		fmt.Println("Аварийный стоп:      АКТИВЕН")
	}

	if info.HasName {
		fmt.Printf("Выполняемая прогр.:  %s (O%04d)\n", orDash(info.ExeName), info.ExeONum)
	}
	if info.HasProgNo {
		fmt.Printf("Номер выполняемой:   O%04d\n", info.RunProg)
		fmt.Printf("Главная программа:   O%04d\n", info.MainProg)
	}
	if info.HasSeq {
		fmt.Printf("Номер кадра:         N%d\n", info.SeqNum)
	}
	if info.HasBlock {
		fmt.Printf("Текущий кадр:        %s\n", orDash(info.Block))
	}
	for _, w := range info.Warnings {
		fmt.Printf("  [!] %s\n", w)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---------- main ----------

func defaultDLL() string {
	name := "Fwlib32.dll"
	if runtime.GOARCH == "amd64" {
		name = "Fwlib64.dll"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if wd, err := os.Getwd(); err == nil {
		p := filepath.Join(wd, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name // пусть Windows ищет сам
}

func main() {
	ip := flag.String("ip", "127.0.0.1", "IP-адрес ЧПУ / эмулятора")
	port := flag.Uint("port", 8193, "порт FOCAS2 Ethernet")
	timeout := flag.Int("timeout", 10, "таймаут соединения, сек")
	dllPath := flag.String("dll", defaultDLL(), "путь к Fwlib32.dll / Fwlib64.dll")
	interval := flag.Int("interval", 0, "период опроса, мс (0 — один раз)")
	flag.Parse()

	f, err := loadFocas(*dllPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}

	if err := f.connect(*ip, uint16(*port), int32(*timeout)); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка подключения:", err)
		os.Exit(1)
	}
	defer f.disconnect()

	fmt.Printf("Подключено к %s:%d\n\n", *ip, *port)

	for {
		info, err := f.readInfo()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Ошибка чтения:", err)
			os.Exit(1)
		}
		if *interval > 0 {
			fmt.Printf("--- %s ---\n", time.Now().Format("15:04:05"))
		}
		printInfo(info)

		if *interval <= 0 {
			return
		}
		fmt.Println()
		time.Sleep(time.Duration(*interval) * time.Millisecond)
	}
}
