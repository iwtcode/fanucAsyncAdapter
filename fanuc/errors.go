package fanuc

import (
	"errors"
	"fmt"
)

// Code — код возврата функции FOCAS (EW_*).
type Code int16

// Коды возврата FOCAS.
const (
	CodeOK       Code = 0
	CodeProtocol Code = -17 // EW_PROTOCOL
	CodeSocket   Code = -16 // EW_SOCKET
	CodeNoDLL    Code = -15 // EW_NODLL
	CodeHandle   Code = -8  // EW_HANDLE
	CodeVersion  Code = -7  // EW_VERSION
	CodeReset    Code = -2  // EW_RESET
	CodeBusy     Code = -1  // EW_BUSY
	CodeFunc     Code = 1   // EW_FUNC
	CodeLength   Code = 2   // EW_LENGTH
	CodeNumber   Code = 3   // EW_NUMBER
	CodeAttrib   Code = 4   // EW_ATTRIB
	CodeData     Code = 5   // EW_DATA
	CodeNoOpt    Code = 6   // EW_NOOPT
	CodeProt     Code = 7   // EW_PROT
	CodeOverflow Code = 8   // EW_OVRFLOW
	CodeParam    Code = 9   // EW_PARAM
	CodeBuffer   Code = 10  // EW_BUFFER: данных пока нет, надо повторить
	CodePath     Code = 11  // EW_PATH
	CodeMode     Code = 12  // EW_MODE
	CodeReject   Code = 13  // EW_REJECT
	CodeAlarm    Code = 15  // EW_ALARM
	CodeStop     Code = 16  // EW_STOP
	CodePasswd   Code = 17  // EW_PASSWD
)

var codeText = map[Code]string{
	CodeProtocol: "EW_PROTOCOL: ошибка протокола",
	CodeSocket:   "EW_SOCKET: нет соединения (проверьте IP/порт и включён ли FOCAS Ethernet в эмуляторе)",
	CodeNoDLL:    "EW_NODLL: не найдена DLL (нужны fwlibe1.dll для 32-бит или Fwlibe64.dll для 64-бит)",
	CodeHandle:   "EW_HANDLE: ошибка получения handle",
	CodeVersion:  "EW_VERSION: несовместимая версия CNC/DLL",
	CodeReset:    "EW_RESET: сброс ЧПУ / аварийное завершение обмена",
	CodeBusy:     "EW_BUSY: ЧПУ занято",
	CodeFunc:     "EW_FUNC: функция не поддерживается этим ЧПУ",
	CodeLength:   "EW_LENGTH: неверная длина данных",
	CodeNumber:   "EW_NUMBER: неверный номер",
	CodeAttrib:   "EW_ATTRIB: неверный атрибут данных",
	CodeData:     "EW_DATA: неверные данные",
	CodeNoOpt:    "EW_NOOPT: нет нужной опции ЧПУ",
	CodeProt:     "EW_PROT: запись защищена",
	CodeOverflow: "EW_OVRFLOW: переполнение памяти ЧПУ",
	CodeParam:    "EW_PARAM: неверный параметр",
	CodeBuffer:   "EW_BUFFER: буфер пуст или переполнен",
	CodePath:     "EW_PATH: неверный номер канала (path)",
	CodeMode:     "EW_MODE: неверный режим ЧПУ",
	CodeReject:   "EW_REJECT: запрос отклонён",
	CodeAlarm:    "EW_ALARM: на ЧПУ активна авария",
	CodeStop:     "EW_STOP: ЧПУ остановлено",
	CodePasswd:   "EW_PASSWD: требуется пароль",
}

// Description возвращает расшифровку кода на русском языке.
func (c Code) Description() string {
	if s, ok := codeText[c]; ok {
		return s
	}
	return "ошибка FOCAS"
}

// Error — ненулевой код возврата функции FOCAS.
type Error struct {
	Func string // имя функции FOCAS, например "cnc_statinfo"
	Code Code
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s (код %d)", e.Func, e.Code.Description(), int16(e.Code))
}

var (
	// ErrClosed возвращается при вызове методов закрытого Client.
	ErrClosed = errors.New("fanuc: клиент закрыт")
	// ErrUnsupportedPlatform — FOCAS-библиотеки доступны только в Windows.
	ErrUnsupportedPlatform = errors.New("fanuc: FOCAS (Fwlib*.dll) поддерживается только в Windows")
)

// CodeOf извлекает код FOCAS из ошибки (в том числе обёрнутой).
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return 0, false
}

// IsCode сообщает, что err — ошибка FOCAS с указанным кодом.
func IsCode(err error, code Code) bool {
	c, ok := CodeOf(err)
	return ok && c == code
}

// IsConnectionError сообщает, что ошибка связана с потерей/порчей соединения
// (EW_HANDLE, EW_SOCKET, EW_PROTOCOL) и имеет смысл переподключиться.
func IsConnectionError(err error) bool {
	c, ok := CodeOf(err)
	if !ok {
		return false
	}
	switch c {
	case CodeHandle, CodeSocket, CodeProtocol:
		return true
	}
	return false
}
