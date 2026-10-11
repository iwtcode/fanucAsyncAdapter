package mtconnect

import "encoding/xml"

// Header — заголовок ответа агента.
type Header struct {
	Version       string `xml:"version,attr"`
	CreationTime  string `xml:"creationTime,attr"`
	FirstSequence uint64 `xml:"firstSequence,attr"`
	LastSequence  uint64 `xml:"lastSequence,attr"`
	NextSequence  uint64 `xml:"nextSequence,attr"`
}

// Probe — ответ /probe: устройства, их компоненты и описание DataItem.
type Probe struct {
	Header  Header      `xml:"Header"`
	Devices []Component `xml:"Devices>Device"`
}

// Component — устройство или его компонент. Тип компонента — имя XML-элемента
// (Device, Axes, Linear, Controller, Path, ...).
type Component struct {
	XMLName    xml.Name
	ID         string        `xml:"id,attr"`
	Name       string        `xml:"name,attr"`
	DataItems  []DataItem    `xml:"DataItems>DataItem"`
	Components ComponentList `xml:"Components"`
}

// ComponentList — вложенные компоненты (элемент Components): любые дочерние элементы.
type ComponentList struct {
	Items []Component `xml:",any"`
}

// DataItem — описание одной величины.
type DataItem struct {
	ID       string `xml:"id,attr"`
	Name     string `xml:"name,attr"`
	Category string `xml:"category,attr"` // SAMPLE, EVENT, CONDITION
	Type     string `xml:"type,attr"`
	SubType  string `xml:"subType,attr"`
	Units    string `xml:"units,attr"`
}

// Walk обходит все компоненты всех устройств (в глубину, включая сами устройства).
// path — путь от устройства, например "Axes/Linear(X)"; у самого устройства он пуст.
func (p *Probe) Walk(fn func(path string, c *Component)) {
	var rec func(path string, c *Component)
	rec = func(path string, c *Component) {
		fn(path, c)
		for i := range c.Components.Items {
			ch := &c.Components.Items[i]
			l := label(ch.XMLName.Local, ch.Name)
			if path != "" {
				l = path + "/" + l
			}
			rec(l, ch)
		}
	}
	for i := range p.Devices {
		rec("", &p.Devices[i])
	}
}

// meta — DataItem вместе с путём компонента, в котором он описан.
type meta struct {
	path string
	DataItem
}

func (p *Probe) index() map[string]meta {
	idx := make(map[string]meta)
	p.Walk(func(path string, c *Component) {
		for _, di := range c.DataItems {
			idx[di.ID] = meta{path, di}
		}
	})
	return idx
}

func label(typ, name string) string {
	if name == "" {
		return typ
	}
	return typ + "(" + name + ")"
}
