package mtconnect_test

// Интеграционный тест на настоящих fanuc-driver и MTConnect agent поверх
// запущенного эмулятора ЧПУ (FANUC CNC GUIDE, FOCAS Ethernet на 127.0.0.1:8193).
// Без эмулятора тест пропускается.
//
// Корень проекта (fanuc-driver/ и agent-*/) ищется вверх от папки пакета;
// можно задать явно: MTCONNECT_ROOT=C:\path\to\project
//
// Запуск:  go test -count=1 -v ./mtconnect/

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fanucAsyncAdapter/mtconnect"
)

func TestAgent(t *testing.T) {
	root := projectRoot(t)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8193", 2*time.Second)
	if err != nil {
		t.Skipf("эмулятор ЧПУ не запущен: %v", err)
	}
	conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	a, err := mtconnect.Start(ctx, mtconnect.Config{Root: root})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Stop()

	// /probe
	probe, err := a.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	dataItems := 0
	probe.Walk(func(_ string, c *mtconnect.Component) { dataItems += len(c.DataItems) })
	if dataItems == 0 {
		t.Fatal("в /probe нет ни одного DataItem")
	}
	t.Logf("/probe: агент %s, DataItem: %d", probe.Header.Version, dataItems)

	// /current
	cur, err := a.Current(ctx)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(cur.Items) == 0 {
		t.Fatal("в /current нет ни одного наблюдения")
	}
	units := false
	for _, it := range cur.Items {
		if it.ID == "" || it.Category == "" || it.Type == "" || it.Time.IsZero() {
			t.Errorf("неполное наблюдение: %+v", it)
		}
		units = units || it.Units != ""
	}
	if !units {
		t.Error("ни у одного значения нет единиц измерения: /probe не сопоставлен с /current")
	}
	var sb strings.Builder
	cur.Write(&sb)
	t.Logf("/current: наблюдений %d\n%s", len(cur.Items), sb.String())

	// /sample
	smp, err := a.Sample(ctx, cur.Header.FirstSequence, 100)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if len(smp.Items) == 0 {
		t.Fatal("в /sample нет ни одного наблюдения")
	}
	for i, it := range smp.Items {
		if it.Sequence < cur.Header.FirstSequence || (i > 0 && it.Sequence < smp.Items[i-1].Sequence) {
			t.Errorf("/sample: нарушен порядок sequence: %d после %d", it.Sequence, smp.Items[max(i-1, 0)].Sequence)
		}
	}
	t.Logf("/sample: наблюдений %d (seq %d..%d)", len(smp.Items), smp.Items[0].Sequence, smp.Items[len(smp.Items)-1].Sequence)
}

// projectRoot ищет вверх от текущей папки каталог с fanuc-driver/fanuc.exe.
func projectRoot(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("MTCONNECT_ROOT"); r != "" {
		return r
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "fanuc-driver", "fanuc.exe")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("fanuc-driver/fanuc.exe не найден выше папки пакета (задайте MTCONNECT_ROOT)")
		}
		dir = parent
	}
}
