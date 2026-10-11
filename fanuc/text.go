package fanuc

import (
	"bytes"
	"strings"
)

// cString обрезает байтовый буфер по первому нулю и убирает пробелы по краям.
func cString(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// normalizeText приводит текст ЧПУ к виду: UTF-8, переводы строк "\n", без NUL.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ToValidUTF8(s, "?")
}

// splitBlocks делит ответ cnc_rdexecprog на выполняемый кадр и кадры упреждения.
func splitBlocks(text string) (current string, next []string) {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return lines[0], lines[1:]
}

// endsWithPercent — последний значащий символ порции равен '%' (конец программы).
func endsWithPercent(chunk []byte) bool {
	t := bytes.TrimRight(chunk, " \t\r\n\x00")
	return len(t) > 0 && t[len(t)-1] == '%'
}
