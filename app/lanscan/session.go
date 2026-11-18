package lanscan

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Session states.
const (
	StateRunning  = "running"
	StateDone     = "done"
	StateCanceled = "canceled"
	StateFailed   = "failed"
)

var (
	// ErrBusy is returned by Start while another scan runs.
	ErrBusy = errors.New("已有扫描在进行")
	// ErrUnknown is returned for a session that does not exist (any more).
	ErrUnknown = errors.New("扫描不存在")
)

// Snapshot is what Poll returns: progress, plus the devices that are new or
// changed since the caller's cursor.
type Snapshot struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Phase   string   `json:"phase"`
	Step    int      `json:"step"`
	Steps   int      `json:"steps"`
	Cursor  int      `json:"cursor"`
	Devices []Device `json:"devices"`
	Error   string   `json:"error,omitempty"`
}

type session struct {
	id      string
	cancel  context.CancelFunc
	state   string
	prog    Progress
	devices map[string]Device
	revs    map[string]int
	rev     int
	err     string
}

// Manager runs at most one scan at a time and keeps the latest session for
// polling. Results stay in memory only.
type Manager struct {
	mu  sync.Mutex
	env func() Env
	cur *session
	seq int
	// pacer is shared by every scan, so a canceled scan still winding down
	// and its successor together stay within one send rate.
	pacer *Pacer
}

// NewManager returns a manager that builds each scan's environment with env.
func NewManager(env func() Env) *Manager { return &Manager{env: env} }

// Start begins a scan and returns its id.
func (m *Manager) Start() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.state == StateRunning {
		return "", ErrBusy
	}
	m.seq++
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{
		id: strconv.Itoa(m.seq) + "-" + strconv.FormatInt(time.Now().UnixNano()%1e6, 36), cancel: cancel, state: StateRunning,
		devices: map[string]Device{}, revs: map[string]int{},
	}
	m.cur = s
	env := m.env()
	if m.pacer == nil {
		pps := env.Limits.PacketsPerSecond
		if pps <= 0 {
			pps = 200
		}
		m.pacer = NewPacer(pps)
	}
	env.Pacer = m.pacer
	go func() {
		err := Scan(ctx, env,
			func(p Progress) { m.update(s, func() { s.prog = p }) },
			func(list []Device) { m.update(s, func() { s.merge(list) }) })
		m.update(s, func() {
			switch {
			case s.state != StateRunning: // canceled while running
			case ctx.Err() == context.Canceled:
				s.state = StateCanceled
			case err != nil && !errors.Is(err, context.DeadlineExceeded):
				s.state, s.err = StateFailed, err.Error()
			default: // finished, or ran out of time with partial results
				s.state = StateDone
			}
		})
		cancel()
	}()
	return s.id, nil
}

// RunningID is the id of the scan in progress, empty when none runs.
func (m *Manager) RunningID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.state == StateRunning {
		return m.cur.id
	}
	return ""
}

// update applies f while the session still runs; late results of a canceled
// scan are dropped.
func (m *Manager) update(s *session, f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.state == StateRunning {
		f()
	}
}

func (s *session) merge(list []Device) {
	for _, d := range list {
		old, ok := s.devices[d.IP]
		if ok && sameDevice(old, d) {
			continue
		}
		s.rev++
		s.devices[d.IP], s.revs[d.IP] = d, s.rev
	}
}

func sameDevice(a, b Device) bool {
	if len(a.Sources) != len(b.Sources) {
		return false
	}
	for i := range a.Sources {
		if a.Sources[i] != b.Sources[i] {
			return false
		}
	}
	a.Sources, b.Sources = nil, nil
	return a.IP == b.IP && a.Name == b.Name && a.Type == b.Type && a.Model == b.Model && a.ModelID == b.ModelID && a.OS == b.OS && a.MAC == b.MAC &&
		a.MACRandom == b.MACRandom && a.Vendor == b.Vendor && a.Image == b.Image && a.Gateway == b.Gateway && a.Self == b.Self
}

// Poll returns the state of scan id and the devices changed after since.
func (m *Manager) Poll(id string, since int) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.cur
	if s == nil || s.id != id {
		return Snapshot{}, ErrUnknown
	}
	snap := Snapshot{ID: s.id, State: s.state, Phase: s.prog.Phase, Step: s.prog.Step, Steps: s.prog.Steps,
		Cursor: s.rev, Devices: []Device{}, Error: s.err}
	for ip, d := range s.devices {
		if s.revs[ip] > since {
			snap.Devices = append(snap.Devices, d)
		}
	}
	sortDevices(snap.Devices)
	return snap, nil
}

// Cancel stops scan id immediately; devices found so far stay readable.
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.cur
	if s == nil || s.id != id {
		return ErrUnknown
	}
	if s.state == StateRunning {
		s.state = StateCanceled
		s.cancel()
	}
	return nil
}

func sortDevices(list []Device) {
	sort.Slice(list, func(i, j int) bool { return ipLess(list[i].IP, list[j].IP) })
}
