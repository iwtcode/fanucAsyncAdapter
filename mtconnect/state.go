package mtconnect

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Item — одно наблюдение: значение DataItem вместе с его описанием из /probe.
type Item struct {
	Device   string    // имя устройства
	Path     string    // компонент внутри устройства, например "Axes/Linear(X)"; пусто — само устройство
	ID       string    // id DataItem, например "f_sim_p1_X_pos_absolute"
	Category string    // SAMPLE, EVENT или CONDITION
	Type     string    // POSITION, EXECUTION, ...
	SubType  string    // ACTUAL, MAIN, ...
	Units    string    // MILLIMETER, ...
	Value    string    // значение; у CONDITION — NORMAL, WARNING, FAULT или UNAVAILABLE
	Text     string    // у CONDITION — код и сообщение
	Time     time.Time // время наблюдения
	Sequence uint64    // номер в буфере агента
}

// State — наблюдения из ответа /current (по одному на DataItem) или /sample
// (серия наблюдений по возрастанию Sequence).
type State struct {
	Header Header
	Items  []Item
}

// Get возвращает последнее наблюдение DataItem с данным id.
func (s *State) Get(id string) (Item, bool) {
	for i := len(s.Items) - 1; i >= 0; i-- {
		if s.Items[i].ID == id {
			return s.Items[i], true
		}
	}
	return Item{}, false
}

// Changed возвращает только те наблюдения, значение которых отличается от prev.
func (s *State) Changed(prev *State) *State {
	old := make(map[string]Item, len(prev.Items))
	for _, it := range prev.Items {
		old[it.ID] = it
	}
	out := &State{Header: s.Header}
	for _, it := range s.Items {
		if o, ok := old[it.ID]; !ok || o.Value != it.Value || o.Text != it.Text {
			out.Items = append(out.Items, it)
		}
	}
	return out
}

// maxValueWidth — значения шире не растягивают столбец при печати.
const maxValueWidth = 32

// Write печатает состояние в виде таблицы, сгруппированной по компонентам.
func (s *State) Write(w io.Writer) {
	h := s.Header
	fmt.Fprintf(w, "MTConnect %s, %s, seq %d..%d (next %d)\n",
		h.Version, h.CreationTime, h.FirstSequence, h.LastSequence, h.NextSequence)

	idW, valW := 0, 0
	for _, it := range s.Items {
		idW = max(idW, utf8.RuneCountInString(it.short()))
		if n := utf8.RuneCountInString(it.display()); n <= maxValueWidth {
			valW = max(valW, n)
		}
	}

	var dev, path string
	for i, it := range s.Items {
		if i == 0 || it.Device != dev || it.Path != path {
			dev, path = it.Device, it.Path
			title := dev
			if path != "" {
				title += "/" + path
			}
			fmt.Fprintf(w, "\n%s\n", title)
		}
		fmt.Fprintf(w, "  %-*s  %-*s  %s\n", idW, it.short(), valW, it.display(), it.kind())
	}
}

// short — id без префикса устройства.
func (it Item) short() string { return strings.TrimPrefix(it.ID, it.Device+"_") }

// display — значение с единицами измерения (для CONDITION — с сообщением).
func (it Item) display() string {
	v := it.Value
	if it.Text != "" {
		v += ": " + it.Text
	}
	if it.Units != "" && it.Value != "UNAVAILABLE" {
		v += " " + it.Units
	}
	return v
}

// kind — «Тип/ПодТип».
func (it Item) kind() string {
	if it.SubType != "" {
		return it.Type + "/" + it.SubType
	}
	return it.Type
}

// ---------------------------------------------------------------- разбор XML

// streams — ответ /current и /sample (MTConnectStreams).
type streams struct {
	Header  Header         `xml:"Header"`
	Devices []deviceStream `xml:"Streams>DeviceStream"`
}

type deviceStream struct {
	Name       string            `xml:"name,attr"`
	Components []componentStream `xml:"ComponentStream"`
}

type componentStream struct {
	Component  string  `xml:"component,attr"`
	Name       string  `xml:"name,attr"`
	Samples    obsList `xml:"Samples"`
	Events     obsList `xml:"Events"`
	Conditions obsList `xml:"Condition"`
}

// obsList — наблюдения: любые дочерние элементы (имя элемента — тип наблюдения).
type obsList struct {
	Items []observation `xml:",any"`
}

type observation struct {
	XMLName xml.Name
	ID      string `xml:"dataItemId,attr"`
	Time    string `xml:"timestamp,attr"`
	Seq     uint64 `xml:"sequence,attr"`
	SubType string `xml:"subType,attr"`
	Type    string `xml:"type,attr"` // у CONDITION
	Code    string `xml:"nativeCode,attr"`
	Text    string `xml:",chardata"`
}

// state собирает State, подставляя описание DataItem из idx. Второй результат
// false, если в idx нет хотя бы одного из id (нужно перечитать /probe).
func (s *streams) state(idx map[string]meta) (*State, bool) {
	st := &State{Header: s.Header}
	known := true
	for _, d := range s.Devices {
		for _, cs := range d.Components {
			for _, g := range []struct {
				category string
				list     obsList
			}{{"SAMPLE", cs.Samples}, {"EVENT", cs.Events}, {"CONDITION", cs.Conditions}} {
				for _, o := range g.list.Items {
					text := strings.TrimSpace(o.Text)
					it := Item{
						Device:   d.Name,
						Path:     label(cs.Component, cs.Name),
						ID:       o.ID,
						Category: g.category,
						Type:     o.Type,
						SubType:  o.SubType,
						Value:    text,
						Sequence: o.Seq,
					}
					if it.Type == "" {
						it.Type = o.XMLName.Local
					}
					it.Time, _ = time.Parse(time.RFC3339Nano, o.Time)
					if g.category == "CONDITION" {
						it.Value = strings.ToUpper(o.XMLName.Local)
						it.Text = strings.TrimSpace(o.Code + " " + text)
					}
					if m, ok := idx[o.ID]; ok {
						it.Path, it.Type, it.SubType, it.Units = m.path, m.Type, m.SubType, m.Units
					} else {
						known = false
					}
					st.Items = append(st.Items, it)
				}
			}
		}
	}
	return st, known
}
