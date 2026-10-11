package focas

import (
	"fmt"
	"io"
	"time"
)

// State — снимок всего состояния станка, кроме текста управляющей программы:
// статус, выполняемая программа, оси и шпиндели. Снимается за одну операцию
// на соединении (Client.State).
type State struct {
	Time     time.Time
	Status   Status
	Info     ProgramInfo
	Axes     []Axis
	Spindles []Spindle

	// Warnings — части, которые прочитать не удалось (например, функция не
	// поддерживается этим ЧПУ). Соответствующие поля остаются пустыми.
	Warnings []error
}

// Write печатает состояние в виде таблиц.
func (s *State) Write(w io.Writer) {
	row := func(name string, v any) { fmt.Fprintf(w, "%-20s %v\n", name+":", v) }

	fmt.Fprintf(w, "--- %s ---\n", s.Time.Format("15:04:05"))
	st, p := s.Status, s.Info
	row("Режим", st.Mode)
	row("Состояние", st.Run)
	row("Движение", st.Motion)
	row("Аварии", st.Alarm)
	if st.Emergency {
		row("Аварийный стоп", "АКТИВЕН")
	}
	row("Программа", fmt.Sprintf("%s (O%04d)", orDash(p.Name), p.Number))
	row("Выполняемая", fmt.Sprintf("O%04d", p.RunningNumber))
	row("Главная", fmt.Sprintf("O%04d", p.MainNumber))
	row("Номер кадра", fmt.Sprintf("N%d", p.Sequence))
	row("Текущий кадр", orDash(p.CurrentBlock))

	if len(s.Axes) > 0 {
		fmt.Fprintf(w, "\n%-6s %-5s %14s %14s %14s %14s\n", "Ось", "Ед.", "Абсолютная", "Машинная", "Относит.", "Ост. пути")
		for _, a := range s.Axes {
			fmt.Fprintf(w, "%-6s %-5s %14.4f %14.4f %14.4f %14.4f\n",
				a.Name, a.Unit, a.Absolute, a.Machine, a.Relative, a.DistanceToGo)
		}
	}
	if len(s.Spindles) > 0 {
		fmt.Fprintf(w, "\n%-8s %12s %12s\n", "Шпиндель", "Об/мин", "Нагрузка, %")
		for _, sp := range s.Spindles {
			fmt.Fprintf(w, "%-8s %12.1f %12.1f\n", sp.Name, sp.Speed, sp.Load)
		}
	}
	for _, e := range s.Warnings {
		fmt.Fprintf(w, "[!] %v\n", e)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
