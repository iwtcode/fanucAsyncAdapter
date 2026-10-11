package fanuc

import (
	"fmt"
	"math"
)

// axes читает позиции всех осей (cnc_rdposition, type = -1: abs/mach/rel/dist).
func (s *session) axes() ([]Axis, error) {
	b := s.buf

	// Сначала просим буфер на maxAxes осей; если ЧПУ не принимает такой размер —
	// пробуем 8 (старые версии Fwlib).
	var lastErr error
	for _, size := range []int16{maxAxes, 8} {
		b.pos = [maxAxes]odbPos{}
		b.posNum = size
		err := s.call("cnc_rdposition", short(-1), ptr(&b.posNum), ptr(&b.pos[0]))
		if err == nil {
			lastErr = nil
			break
		}
		if IsConnectionError(err) {
			return nil, err
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}

	n := int(b.posNum)
	if n < 0 {
		n = 0
	}
	if n > maxAxes {
		n = maxAxes
	}
	axes := make([]Axis, 0, n)
	for i := 0; i < n; i++ {
		p := &b.pos[i]
		axes = append(axes, Axis{
			Name:         axisName(p.Abs.Name, p.Abs.Suff, i),
			Unit:         Unit(p.Abs.Unit),
			Absolute:     scale(p.Abs),
			Machine:      scale(p.Mach),
			Relative:     scale(p.Rel),
			DistanceToGo: scale(p.Dist),
		})
	}
	return axes, nil
}

func scale(e posElm) float64 {
	if e.Dec <= 0 {
		return float64(e.Data)
	}
	return float64(e.Data) / math.Pow10(int(e.Dec))
}

func axisName(name, suff byte, idx int) string {
	if name < 0x21 || name > 0x7e {
		return fmt.Sprintf("#%d", idx+1)
	}
	n := string(rune(name))
	if suff >= 0x21 && suff <= 0x7e {
		n += string(rune(suff))
	}
	return n
}
