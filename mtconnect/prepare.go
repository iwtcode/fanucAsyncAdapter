package mtconnect

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	bom       = []byte{0xEF, 0xBB, 0xBF}
	reXMLDecl = regexp.MustCompile(`^\s*<\?xml[^>]*\?>`)
)

// patchMachines включает в config.machines.yml машину machine и подставляет
// адрес и порт станка (FOCAS), порт SHDR и имя устройства. Остальной файл
// (комментарии, CRLF) не меняется.
//
// В блоке машины ожидается порядок, как в исходном файле: сначала net.port
// станка, затем net.port SHDR-транспорта.
func patchMachines(src []byte, machine, host, cncPort, shdrPort, device string) ([]byte, error) {
	// блок машины: от «- id: <machine>» до следующего «- id:» с тем же отступом
	// (вложенные списки с «- id:» глубже границей не считаются)
	start := regexp.MustCompile(`(?m)^([ \t]*)- id:[ \t]*` + regexp.QuoteMeta(machine) + `[ \t]*\r?$`).FindSubmatchIndex(src)
	if start == nil {
		return nil, fmt.Errorf("в config.machines.yml нет машины с id: %s", machine)
	}
	end := len(src)
	next := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(string(src[start[2]:start[3]])) + `- id:`)
	if loc := next.FindIndex(src[start[1]:]); loc != nil {
		end = start[1] + loc[0]
	}
	body := string(src[start[0]:end])

	for _, e := range []struct {
		key  string
		vals []string // значения для 1-го, 2-го... вхождения ключа
	}{
		{"enabled", []string{"!!bool true"}},
		{"ip", []string{host}},
		{"port", []string{"!!int " + cncPort, "!!int " + shdrPort}},
		{"device_name", []string{device}},
	} {
		re := regexp.MustCompile(`(?m)^([ \t]+` + e.key + `:[ \t]*)([^\r\n]*)`)
		ms := re.FindAllStringSubmatchIndex(body, len(e.vals))
		if len(ms) < len(e.vals) {
			return nil, fmt.Errorf("в блоке %s не найдено: %s (нужно %d)", machine, e.key, len(e.vals))
		}
		for i := len(ms) - 1; i >= 0; i-- { // с конца, чтобы не сбить смещения
			body = body[:ms[i][4]] + e.vals[i] + body[ms[i][5]:]
		}
	}
	return []byte(string(src[:start[0]]) + body + string(src[end:])), nil
}

// renderAgentCfg превращает user/agent.cfg (рассчитанный на docker) в рабочий
// конфиг для локального запуска: адрес и порт адаптера, порт REST, имя устройства.
func renderAgentCfg(tpl []byte, shdrPort, httpPort int, device string) ([]byte, error) {
	out := tpl
	for _, s := range []struct{ re, repl string }{
		{`(?m)^([ \t]+)Host[ \t]*=[^\r\n]*`, "${1}Host = 127.0.0.1"},
		{`(?m)^([ \t]+)Port[ \t]*=[^\r\n]*`, fmt.Sprintf("${1}Port = %d", shdrPort)}, // адаптер — с отступом
		{`(?m)^Port[ \t]*=[^\r\n]*`, fmt.Sprintf("Port = %d", httpPort)},             // REST — без отступа
		{`(?m)^([ \t]+)Device[ \t]*=[^\r\n]*`, "${1}Device = " + device},
		{`(?m)^([ \t]*)logging_level[ \t]*=[^\r\n]*`, "${1}logging_level = info"},
	} {
		re := regexp.MustCompile(s.re)
		if !re.Match(out) {
			return nil, fmt.Errorf("в agent.cfg не найдена строка по шаблону %s", s.re)
		}
		out = re.ReplaceAll(out, []byte(s.repl))
	}
	return out, nil
}

// buildDevices вставляет <Device>, сгенерированный драйвером, в devices_template.xml.
func buildDevices(tpl, model []byte) ([]byte, error) {
	dev := reXMLDecl.ReplaceAllString(string(bytes.TrimPrefix(model, bom)), "")
	t := strings.ReplaceAll(string(bytes.TrimPrefix(tpl, bom)), "SENDER_ID", "fanuc-driver")
	if !strings.Contains(t, "</Devices>") {
		return nil, errors.New("в devices_template.xml нет </Devices>")
	}
	out := []byte(strings.Replace(t, "</Devices>", strings.TrimSpace(dev)+"\n</Devices>", 1))
	if _, err := deviceName(out); err != nil {
		return nil, fmt.Errorf("получился некорректный devices.xml: %w", err)
	}
	return out, nil
}

// deviceName проверяет, что data — целый корректный XML, и возвращает name
// первого элемента Device (пусто, если такого нет).
func deviceName(data []byte) (string, error) {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, bom)))
	name := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return name, nil
		}
		if err != nil {
			return "", err
		}
		if se, ok := tok.(xml.StartElement); ok && name == "" && se.Name.Local == "Device" {
			for _, a := range se.Attr {
				if a.Name.Local == "name" {
					name = a.Value
				}
			}
		}
	}
}
