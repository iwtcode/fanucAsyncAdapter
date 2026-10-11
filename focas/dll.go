package focas

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const dllName = "Fwlib64.dll"

// dll — загруженная FOCAS-библиотека. Используется только из рабочего потока Client.
type dll struct {
	d     *syscall.DLL
	procs map[string]*syscall.Proc
}

// loadDLL загружает библиотеку. Пустой path — автопоиск (см. findDLL).
func loadDLL(path string) (*dll, error) {
	if path == "" {
		path = findDLL()
	}
	if filepath.Base(path) != path { // задан путь, а не просто имя файла
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		// Fwlib64.dll подгружает Fwlibe64.dll сама, поэтому каталог с DLL
		// должен входить в путь поиска, даже если exe лежит в другом месте.
		setDLLDirectory(filepath.Dir(path))
	}
	d, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("не удалось загрузить %s: %w (рядом должна лежать Fwlibe64.dll; программа должна быть 64-битной)", path, err)
	}
	return &dll{d: d, procs: make(map[string]*syscall.Proc)}, nil
}

// findDLL ищет Fwlib64.dll рядом с exe, в текущем каталоге и в его родительских
// каталогах (до 6 уровней — удобно для `go test` из подпапки пакета).
// Если файл не найден, возвращается просто имя — Windows поищет сам.
func findDLL() string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if dir, err := os.Getwd(); err == nil {
		for range 6 {
			dirs = append(dirs, dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, dllName)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return dllName
}

func setDLLDirectory(dir string) {
	u, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetDllDirectoryW")
	_, _, _ = proc.Call(uintptr(unsafe.Pointer(u)))
}

// call вызывает функцию FOCAS и возвращает её код возврата (short).
// Ошибка возвращается только если функции нет в DLL.
func (l *dll) call(name string, args ...uintptr) (Code, error) {
	p, ok := l.procs[name]
	if !ok {
		var err error
		if p, err = l.d.FindProc(name); err != nil {
			return 0, err
		}
		l.procs[name] = p
	}
	r, _, _ := p.Call(args...)
	return Code(int16(r)), nil
}
