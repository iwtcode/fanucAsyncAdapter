package focas

import (
	"errors"
	"fmt"
)

// Code — код возврата функции FOCAS (EW_*).
type Code int16

// Коды возврата, которые обрабатывает сам пакет.
const (
	CodeOK       Code = 0
	CodeFunc     Code = 1   // EW_FUNC: функция не поддерживается этим ЧПУ
	CodeBuffer   Code = 10  // EW_BUFFER: данных пока нет, надо повторить
	CodeHandle   Code = -8  // EW_HANDLE
	CodeSocket   Code = -16 // EW_SOCKET
	CodeProtocol Code = -17 // EW_PROTOCOL
)

var codeText = map[Code]string{
	-17: "EW_PROTOCOL: ошибка протокола",
	-16: "EW_SOCKET: нет соединения (проверьте IP/порт и включён ли FOCAS Ethernet в эмуляторе)",
	-15: "EW_NODLL: не найдена DLL (рядом с Fwlib64.dll нужна Fwlibe64.dll)",
	-8:  "EW_HANDLE: ошибка получения handle",
	-7:  "EW_VERSION: несовместимая версия CNC/DLL",
	-2:  "EW_RESET: сброс ЧПУ / аварийное завершение обмена",
	-1:  "EW_BUSY: ЧПУ занято",
	1:   "EW_FUNC: функция не поддерживается этим ЧПУ",
	2:   "EW_LENGTH: неверная длина данных",
	3:   "EW_NUMBER: неверный номер",
	4:   "EW_ATTRIB: неверный атрибут данных",
	5:   "EW_DATA: неверные данные",
	6:   "EW_NOOPT: нет нужной опции ЧПУ",
	7:   "EW_PROT: запись защищена",
	8:   "EW_OVRFLOW: переполнение памяти ЧПУ",
	9:   "EW_PARAM: неверный параметр",
	10:  "EW_BUFFER: буфер пуст или переполнен",
	11:  "EW_PATH: неверный номер канала (path)",
	12:  "EW_MODE: неверный режим ЧПУ",
	13:  "EW_REJECT: запрос отклонён",
	15:  "EW_ALARM: на ЧПУ активна авария",
	16:  "EW_STOP: ЧПУ остановлено",
	17:  "EW_PASSWD: требуется пароль",
}

// String возвращает расшифровку кода.
func (c Code) String() string {
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
	return fmt.Sprintf("%s: %s (код %d)", e.Func, e.Code, int16(e.Code))
}

// ErrClosed возвращается при вызове методов закрытого Client.
var ErrClosed = errors.New("focas: клиент закрыт")

// connLost сообщает, что соединение потеряно или испорчено (EW_HANDLE,
// EW_SOCKET, EW_PROTOCOL) и имеет смысл переподключиться.
func connLost(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case CodeHandle, CodeSocket, CodeProtocol:
		return true
	}
	return false
}
