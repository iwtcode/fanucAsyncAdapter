//go:build !windows

package fanuc

// dll — заглушка для не-Windows платформ: пакет компилируется (go vet, go build),
// но подключиться нельзя.
type dll struct{}

func loadDLL(string) (*dll, error) { return nil, ErrUnsupportedPlatform }

func (l *dll) call(string, ...uintptr) (Code, error) { return 0, ErrUnsupportedPlatform }
