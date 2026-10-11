// Package mtconnect читает состояние станка через MTConnect.
//
// Start сам поднимает цепочку
//
//	ЧПУ / эмулятор (FOCAS :8193) → fanuc-driver (SHDR :7878) → MTConnect agent (HTTP :5000)
//
// и возвращает *Agent, из которого можно получить всё: Probe, Current, Sample.
// Если агент уже запущен, достаточно New — клиента без запуска процессов.
//
//	a, err := mtconnect.Start(ctx, mtconnect.Config{Root: "."})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer a.Stop()
//
//	st, _ := a.Current(ctx) // всё состояние станка, по одному Item на каждый DataItem
//	mode, _ := st.Get("f_sim_p1_ctl_mode")
//
//	a.Watch(ctx, time.Second, func(st *mtconnect.State) error { // или опрос
//		st.Write(os.Stdout)
//		return nil
//	})
package mtconnect

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client — клиент MTConnect-агента. Безопасен для использования из нескольких горутин.
type Client struct {
	base   string
	device string
	http   *http.Client

	mu  sync.Mutex
	idx map[string]meta // описание DataItem из /probe, подтягивается при первой необходимости
}

// New создаёт клиент уже запущенного агента, например New("http://127.0.0.1:5000", "f_sim").
// Пустой addr — 127.0.0.1:5000, пустой device — все устройства агента.
// Сетевых запросов не делает.
func New(addr, device string) *Client {
	if addr == "" {
		addr = "127.0.0.1:5000"
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return &Client{
		base:   strings.TrimRight(addr, "/"),
		device: device,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

// URL — адрес агента с учётом устройства, например "http://127.0.0.1:5000/f_sim".
func (c *Client) URL() string {
	if c.device == "" {
		return c.base
	}
	return c.base + "/" + url.PathEscape(c.device)
}

// Probe запрашивает /probe: устройства, компоненты и описание DataItem.
func (c *Client) Probe(ctx context.Context) (*Probe, error) {
	var p Probe
	if err := c.get(ctx, "probe", nil, &p); err != nil {
		return nil, err
	}
	idx := p.index()
	c.mu.Lock()
	c.idx = idx
	c.mu.Unlock()
	return &p, nil
}

// Current запрашивает /current: последнее значение каждого DataItem.
func (c *Client) Current(ctx context.Context) (*State, error) {
	return c.state(ctx, "current", nil)
}

// Sample запрашивает /sample: до count наблюдений, начиная с номера from, по
// возрастанию Sequence. from == 0 — с начала буфера агента, count <= 0 — как решит агент.
func (c *Client) Sample(ctx context.Context, from uint64, count int) (*State, error) {
	q := url.Values{}
	if from > 0 {
		q.Set("from", strconv.FormatUint(from, 10))
	}
	if count > 0 {
		q.Set("count", strconv.Itoa(count))
	}
	st, err := c.state(ctx, "sample", q)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(st.Items, func(i, j int) bool { return st.Items[i].Sequence < st.Items[j].Sequence })
	return st, nil
}

// Watch опрашивает /current каждые every и передаёт состояние в fn (первый раз —
// сразу). every <= 0 — один опрос. Возвращается при отмене ctx (ctx.Err()), при
// ошибке запроса или если fn вернула ошибку.
func (c *Client) Watch(ctx context.Context, every time.Duration, fn func(*State) error) error {
	var tick <-chan time.Time
	if every > 0 {
		t := time.NewTicker(every)
		defer t.Stop()
		tick = t.C
	}
	for {
		st, err := c.Current(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := fn(st); err != nil {
			return err
		}
		if tick == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
		}
	}
}

// state запрашивает endpoint и сопоставляет наблюдения с описанием из /probe.
func (c *Client) state(ctx context.Context, endpoint string, q url.Values) (*State, error) {
	var s streams
	if err := c.get(ctx, endpoint, q, &s); err != nil {
		return nil, err
	}
	if st, ok := s.state(c.index()); ok {
		return st, nil
	}
	if _, err := c.Probe(ctx); err != nil { // первый запрос или модель устройства изменилась
		return nil, err
	}
	st, _ := s.state(c.index())
	return st, nil
}

func (c *Client) index() map[string]meta {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.idx
}

// get выполняет GET <url>/<endpoint> и разбирает XML-ответ в v.
func (c *Client) get(ctx context.Context, endpoint string, q url.Values, v any) error {
	u := c.URL() + "/" + endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("GET %s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", u, resp.Status, snippet(body))
	}
	if err := xml.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: разбор XML: %w", u, err)
	}
	return nil
}

// snippet — начало тела ответа (например, документа MTConnectError) в одну строку.
func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "..."
	}
	return s
}
