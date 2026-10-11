package mtconnect

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	modelTimeout = 90 * time.Second // сколько ждать модель устройства от драйвера
	readyTimeout = 60 * time.Second // сколько ждать AVAILABLE от агента
)

// Config — параметры запуска агента. Нулевые значения заменяются значениями по умолчанию.
type Config struct {
	Root      string    // корень проекта: здесь лежат fanuc-driver/ и agent-*/ (по умолчанию ".")
	DriverDir string    // папка драйвера с fanuc.exe и user/ (по умолчанию Root/fanuc-driver)
	AgentDir  string    // папка агента с bin/agent.exe (по умолчанию первая Root/agent-*)
	CNC       string    // FOCAS-адрес станка или эмулятора, host:port (по умолчанию 127.0.0.1:8193)
	Device    string    // имя устройства в MTConnect (по умолчанию f_sim)
	Machine   string    // id машины в config.machines.yml (по умолчанию f_sim_mtc_long)
	HTTPPort  int       // порт REST агента (по умолчанию 5000)
	SHDRPort  int       // порт, по которому драйвер отдаёт данные агенту (по умолчанию 7878)
	Log       io.Writer // если задан — сюда построчно дублируется вывод драйвера и агента
}

func (c Config) withDefaults() (Config, error) {
	var err error
	if c.Root == "" {
		c.Root = "."
	}
	if c.Root, err = filepath.Abs(c.Root); err != nil {
		return c, err
	}
	if c.DriverDir == "" {
		c.DriverDir = filepath.Join(c.Root, "fanuc-driver")
	}
	if c.AgentDir == "" {
		c.AgentDir = findAgentDir(c.Root)
	}
	for _, dir := range []*string{&c.DriverDir, &c.AgentDir} {
		if *dir, err = filepath.Abs(*dir); err != nil {
			return c, err
		}
	}
	if c.CNC == "" {
		c.CNC = "127.0.0.1:8193"
	}
	if c.Device == "" {
		c.Device = "f_sim"
	}
	if c.Machine == "" {
		c.Machine = "f_sim_mtc_long"
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = 5000
	}
	if c.SHDRPort == 0 {
		c.SHDRPort = 7878
	}
	return c, nil
}

func findAgentDir(root string) string {
	dirs, _ := filepath.Glob(filepath.Join(root, "agent-*"))
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, "bin", "agent.exe")); err == nil {
			return d
		}
	}
	return filepath.Join(root, "agent")
}

// Agent — запущенная цепочка fanuc-driver → MTConnect agent. Клиент *Client
// уже настроен на агент и устройство, поэтому Probe, Current, Sample и Watch
// вызываются прямо у Agent.
type Agent struct {
	*Client
	cfg    Config
	driver *proc
	agent  *proc

	once    sync.Once
	stopped chan struct{}
	done    chan error
}

// Start запускает драйвер и агент и возвращается, когда агент отдаёт данные
// (AVAILABLE). Порядок важен: devices.xml для агента не существует, пока
// драйвер не подключится к ЧПУ и сам не сгенерирует модель устройства.
// ctx используется только на время запуска.
func Start(ctx context.Context, cfg Config) (_ *Agent, err error) {
	if cfg, err = cfg.withDefaults(); err != nil {
		return nil, err
	}
	a := &Agent{cfg: cfg, stopped: make(chan struct{}), done: make(chan error, 1)}
	defer func() {
		if err != nil {
			a.Stop()
		}
	}()

	if err = a.preflight(); err != nil {
		return nil, err
	}
	if err = a.prepareDriver(); err != nil {
		return nil, err
	}

	// --- драйвер: подключается к ЧПУ и пишет модель устройства ---
	a.driver, err = a.run("driver", filepath.Join(cfg.DriverDir, "fanuc.exe"), []string{
		"--nlog", a.user("nlog.config"),
		"--config", strings.Join([]string{a.user("config.system.yml"), a.user("config.user.yml"), a.user("config.machines.runtime.yml")}, ","),
	}, cfg.DriverDir)
	if err != nil {
		return nil, err
	}
	var model []byte
	if model, err = a.waitModel(ctx); err != nil {
		return nil, err
	}

	// --- агент ---
	if err = a.prepareAgent(model); err != nil {
		return nil, err
	}
	a.agent, err = a.run("agent", a.bin("agent.exe"), []string{"run", "agent.cfg"}, filepath.Dir(a.bin("agent.exe")))
	if err != nil {
		return nil, err
	}
	a.Client = New("http://127.0.0.1:"+strconv.Itoa(cfg.HTTPPort), cfg.Device)
	if err = a.waitReady(ctx); err != nil {
		return nil, err
	}

	go a.watch()
	return a, nil
}

// Stop останавливает агент, затем драйвер. Можно вызывать повторно.
func (a *Agent) Stop() {
	a.once.Do(func() {
		close(a.stopped)
		a.agent.stop()
		a.driver.stop()
	})
}

// Done получает ошибку, если драйвер или агент неожиданно завершились.
// После Stop ничего не приходит.
func (a *Agent) Done() <-chan error { return a.done }

// --- пути ---

func (a *Agent) user(name string) string { return filepath.Join(a.cfg.DriverDir, "user", name) }
func (a *Agent) bin(name string) string  { return filepath.Join(a.cfg.AgentDir, "bin", name) }
func (a *Agent) logs(name string) string { return filepath.Join(a.cfg.Root, "logs", name) }

// --- шаги запуска ---

func (a *Agent) preflight() error {
	for _, f := range []string{
		filepath.Join(a.cfg.DriverDir, "fanuc.exe"), a.user("nlog.config"), a.user("config.system.yml"),
		a.user("config.user.yml"), a.user("config.machines.yml"), a.user("agent.cfg"),
		a.user("devices_template.xml"), a.bin("agent.exe"),
	} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("не найден файл %s (проверьте Root, fanuc-driver/user и agent-*/bin)", f)
		}
	}

	conn, err := net.DialTimeout("tcp", a.cfg.CNC, 3*time.Second)
	if err != nil {
		return fmt.Errorf("ЧПУ %s недоступно: %w (эмулятор запущен? включён FOCAS Ethernet?)", a.cfg.CNC, err)
	}
	conn.Close()

	for _, port := range []int{a.cfg.SHDRPort, a.cfg.HTTPPort} {
		l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return fmt.Errorf("порт %d занят: %w (остались fanuc.exe/agent.exe от прошлого запуска? "+
				"taskkill /F /IM fanuc.exe /IM agent.exe)", port, err)
		}
		l.Close()
	}

	if err := os.MkdirAll(a.logs(""), 0o755); err != nil {
		return err
	}
	// Старые артефакты убираем, чтобы по их появлению судить о свежем запуске.
	for _, f := range []string{a.user("mtc_device_" + a.cfg.Device + ".xml"), a.bin("devices.xml")} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// prepareDriver: config.machines.yml → config.machines.runtime.yml (адрес ЧПУ, порты, устройство).
func (a *Agent) prepareDriver() error {
	host, port, err := net.SplitHostPort(a.cfg.CNC)
	if err != nil {
		return fmt.Errorf("CNC %q: нужен адрес host:port: %w", a.cfg.CNC, err)
	}
	src, err := os.ReadFile(a.user("config.machines.yml"))
	if err != nil {
		return err
	}
	out, err := patchMachines(src, a.cfg.Machine, host, port, strconv.Itoa(a.cfg.SHDRPort), a.cfg.Device)
	if err != nil {
		return err
	}
	return os.WriteFile(a.user("config.machines.runtime.yml"), out, 0o644)
}

// prepareAgent пишет devices.xml (шаблон + модель драйвера) и agent.cfg рядом с agent.exe.
func (a *Agent) prepareAgent(model []byte) error {
	tpl, err := os.ReadFile(a.user("devices_template.xml"))
	if err != nil {
		return err
	}
	devices, err := buildDevices(tpl, model)
	if err != nil {
		return err
	}
	if err := os.WriteFile(a.bin("devices.xml"), devices, 0o644); err != nil {
		return err
	}

	cfgTpl, err := os.ReadFile(a.user("agent.cfg"))
	if err != nil {
		return err
	}
	cfg, err := renderAgentCfg(cfgTpl, a.cfg.SHDRPort, a.cfg.HTTPPort, a.cfg.Device)
	if err != nil {
		return err
	}
	return os.WriteFile(a.bin("agent.cfg"), cfg, 0o644)
}

// waitModel ждёт, пока драйвер запишет user/mtc_device_<device>.xml целиком.
func (a *Agent) waitModel(ctx context.Context) ([]byte, error) {
	file := a.user("mtc_device_" + a.cfg.Device + ".xml")
	timeout := time.After(modelTimeout)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()

	var prev []byte
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.driver.done:
			return nil, fmt.Errorf("fanuc-driver завершился до создания модели устройства: %v (см. %s)", a.driver.err, a.logs("driver.log"))
		case <-timeout:
			return nil, fmt.Errorf("за %s драйвер не создал %s: не подключился к ЧПУ? (см. %s)", modelTimeout, file, a.logs("driver.log"))
		case <-tick.C:
		}
		data, _ := os.ReadFile(file)
		if len(data) == 0 || !bytes.Equal(data, prev) { // файл ещё пишется: ждём два одинаковых чтения подряд
			prev = data
			continue
		}
		name, err := deviceName(data)
		if err != nil {
			continue
		}
		if name != a.cfg.Device {
			return nil, fmt.Errorf("в модели устройство %q, а ожидалось %q: device_name в config.machines.yml и Device в agent.cfg должны совпадать", name, a.cfg.Device)
		}
		return data, nil
	}
}

// waitReady ждёт ответа агента и AVAILABLE.
func (a *Agent) waitReady(ctx context.Context) error {
	timeout := time.After(readyTimeout)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	last := "нет ответа"
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.agent.done:
			return fmt.Errorf("agent.exe завершился при запуске: %v (см. %s)", a.agent.err, a.logs("agent.log"))
		case <-a.driver.done:
			return fmt.Errorf("fanuc-driver завершился: %v (см. %s)", a.driver.err, a.logs("driver.log"))
		case <-timeout:
			return fmt.Errorf("за %s агент не стал AVAILABLE (последнее состояние: %s)", readyTimeout, last)
		case <-tick.C:
		}
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		st, err := a.Current(cctx)
		cancel()
		if err != nil {
			last = err.Error()
			continue
		}
		for _, it := range st.Items {
			if strings.EqualFold(it.Type, "AVAILABILITY") {
				last = it.ID + "=" + it.Value
				if it.Value == "AVAILABLE" {
					return nil
				}
			}
		}
	}
}

// watch сообщает в Done, если один из процессов умер сам.
func (a *Agent) watch() {
	var err error
	select {
	case <-a.stopped:
		return
	case <-a.driver.done:
		err = fmt.Errorf("fanuc-driver завершился: %v (см. %s)", a.driver.err, a.logs("driver.log"))
	case <-a.agent.done:
		err = fmt.Errorf("agent.exe завершился: %v (см. %s)", a.agent.err, a.logs("agent.log"))
	}
	a.done <- err // буфер 1, отправка одна
}

// --- процессы ---

type proc struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // валидно после закрытия done
}

// run запускает процесс; весь его вывод пишется в logs/<name>.log (и в cfg.Log, если задан).
func (a *Agent) run(name, exe string, args []string, dir string) (*proc, error) {
	f, err := os.Create(a.logs(name + ".log"))
	if err != nil {
		return nil, err
	}
	var out io.Writer = f
	var lw *lineWriter
	if a.cfg.Log != nil {
		lw = &lineWriter{w: a.cfg.Log, prefix: "[" + name + "] "}
		out = io.MultiWriter(f, lw)
	}

	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out // один и тот же writer: os/exec использует один канал
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("запуск %s: %w", exe, err)
	}

	p := &proc{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		if lw != nil {
			lw.flush()
		}
		f.Close()
		close(p.done)
	}()
	return p, nil
}

// stop убивает процесс и ждёт его завершения (nil-безопасно).
func (p *proc) stop() {
	if p == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

// lineWriter режет поток на строки и пишет их в w с префиксом.
type lineWriter struct {
	w      io.Writer
	prefix string
	buf    []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
}

func (l *lineWriter) flush() {
	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

func (l *lineWriter) emit(line []byte) {
	fmt.Fprintf(l.w, "%s%s\n", l.prefix, bytes.TrimRight(line, "\r"))
}
