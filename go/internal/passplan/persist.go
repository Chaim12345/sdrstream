package passplan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const stateVersion = 1

type stateFile struct {
	Version      int           `json:"version"`
	Strategy     string        `json:"strategy"`
	Observations []Observation `json:"observations"`
}

// Save writes the timeline atomically (temp file + rename) with mode 0600,
// creating the directory 0700. Callers pick the path, e.g.
// web.StateFile("passplan_state.json") so SDRSTREAM_STATE_DIR is honoured.
func (p *Planner) Save(path string) error {
	p.mu.Lock()
	st := stateFile{Version: stateVersion, Strategy: p.Strategy}
	for _, o := range p.obs {
		st.Observations = append(st.Observations, *o)
	}
	p.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Load restores a saved timeline. A missing file is not an error. An
// observation saved as running belongs to a process that no longer exists,
// so it is marked failed rather than resumed. An unknown version or corrupt
// file returns an error and leaves the planner unchanged.
func (p *Planner) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("passplan state %s: %w", path, err)
	}
	if st.Version != stateVersion {
		return fmt.Errorf("passplan state %s: unsupported version %d", path, st.Version)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.obs = p.obs[:0]
	for i := range st.Observations {
		o := st.Observations[i]
		if o.Status == StatusRunning {
			o.Status, o.Detail = StatusFailed, "interrupted by restart"
		}
		p.obs = append(p.obs, &o)
	}
	return nil
}
