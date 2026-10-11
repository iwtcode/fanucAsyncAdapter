//go:build windows

package fanuc

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// dll — загруженная FOCAS-библиотека (Fwlib32.dll / Fwlib64.dll).
type dll struct {
	d     *syscall.DLL
	mu    sync.Mutex
	procs map[string]*syscall.Proc
}

func loadDLL(path string) (*dll, error) {
	if strings.ContainsAny(path, `\/`) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		// Fwlib64.dll подгружает fwlibe64.dll сама, поэтому каталог с DLL
		// должен входить в путь поиска, даже если exe лежит в другом месте.
		setDLLDirectory(filepath.Dir(path))
	}
	d, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось загрузить %s: %w\n"+
			"Подсказка: разрядность DLL должна совпадать с разрядностью программы "+
			"(GOARCH=386 -> Fwlib32.dll, GOARCH=amd64 -> Fwlib64.dll); "+
			"рядом должна лежать fwlibe1.dll (32-бит) или Fwlibe64.dll (64-бит)", path, err)
	}
	return &dll{d: d, procs: make(map[string]*syscall.Proc)}, nil
}

func setDLLDirectory(dir string) {
	u, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetDllDirectoryW")
	_, _, _ = proc.Call(uintptr(unsafe.Pointer(u)))
}

func (l *dll) proc(name string) (*syscall.Proc, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.procs[name]; ok {
		return p, nil
	}
	p, err := l.d.FindProc(name)
	if err != nil {
		return nil, err
	}
	l.procs[name] = p
	return p, nil
}

// call вызывает функцию FOCAS и возвращает её код возврата (short).
// Ошибка возвращается только если функция не найдена в DLL.
func (l *dll) call(name string, args ...uintptr) (Code, error) {
	p, err := l.proc(name)
	if err != nil {
		return 0, err
	}
	r, _, _ := p.Call(args...)
	return Code(int16(r)), nil
}
