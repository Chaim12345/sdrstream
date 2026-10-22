package monitor

import "sync"

// liveControls holds the knobs the web UI can change while the monitor
// runs. Every value starts unset, which means "follow Options"; once the
// operator moves a control the stored value wins until the process exits.
type liveControls struct {
	mu          sync.RWMutex
	thresholdDB float64
	stepHz      float64
	mode        string
}

// Controls is one control update from the web UI. Zero and empty values
// keep the current setting, so a panel can change one knob at a time.
type Controls struct {
	ThresholdDB float64
	StepHz      float64
	Mode        string
}

// SetControls applies the live knobs. It is safe while Run is active: a
// sweep step or a detection pass picks the new value up on its next
// iteration without a restart.
func (m *Monitor) SetControls(c Controls) {
	m.ctrl.mu.Lock()
	defer m.ctrl.mu.Unlock()
	if c.ThresholdDB > 0 {
		m.ctrl.thresholdDB = c.ThresholdDB
	}
	if c.StepHz > 0 {
		m.ctrl.stepHz = c.StepHz
	}
	if c.Mode != "" {
		m.ctrl.mode = c.Mode
	}
}

// thresholdDB is the detection level above the noise floor, live value first.
func (m *Monitor) thresholdDB() float64 {
	m.ctrl.mu.RLock()
	defer m.ctrl.mu.RUnlock()
	if m.ctrl.thresholdDB > 0 {
		return m.ctrl.thresholdDB
	}
	return m.opts.ThresholdDB
}

// dialStepHz is the sweep dial step, live value first.
func (m *Monitor) dialStepHz() float64 {
	m.ctrl.mu.RLock()
	defer m.ctrl.mu.RUnlock()
	if m.ctrl.stepHz > 0 {
		return m.ctrl.stepHz
	}
	return m.opts.StepHz
}

// listenMode is the listening modulation, live value first.
func (m *Monitor) listenMode() string {
	m.ctrl.mu.RLock()
	defer m.ctrl.mu.RUnlock()
	if m.ctrl.mode != "" {
		return m.ctrl.mode
	}
	return m.opts.Mode
}
