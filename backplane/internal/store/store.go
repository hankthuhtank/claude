// Package store persists Backplane's state as plain JSON files so everything
// is inspectable, exportable and easy to back up. Writes are atomic (write to
// a temp file, then rename) so a crash can never leave half a manifest.
//
// Layout under the data directory:
//
//	settings.json
//	connections.json
//	projects/<id>/project.json
//	projects/<id>/env/<env>/manifest.json
//	projects/<id>/env/<env>/runs/<run>.json
//	projects/<id>/env/<env>/reports/<report>.json
//	projects/<id>/env/<env>/history.jsonl
//	projects/<id>/env/<env>/snapshots/<snapshot>.json
//	projects/<id>/code/...                 generated code the user can inspect
//	logs/<yyyy-mm-dd>.jsonl                unified, redacted log
//	vault.dat                              encrypted secrets (see package vault)
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
)

// Store is the file-backed persistence layer.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// Open prepares the data directory.
func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "projects"), filepath.Join(dir, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &Store{Dir: dir}, nil
}

// DefaultDir returns the per-user data directory.
func DefaultDir() string {
	if d := os.Getenv("BACKPLANE_DATA_DIR"); d != "" {
		return d
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "Backplane")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".backplane")
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-.]{0,80}$`)

func clean(part string) (string, error) {
	if !safeName.MatchString(part) || strings.Contains(part, "..") {
		return "", fmt.Errorf("invalid name %q", part)
	}
	return part, nil
}

// WriteJSON writes v atomically.
func WriteJSON(path string, v any) error {
	bs, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(bs); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ReadJSON reads a JSON file into v.
func ReadJSON(path string, v any) error {
	bs, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(bs, v)
}

// ---- settings ----

// Settings are user preferences.
type Settings struct {
	Theme          string            `json:"theme"`   // rack, aluminum, bench, copper, fiber, contrast, system
	Density        string            `json:"density"` // comfortable | compact
	ShowAdvanced   bool              `json:"showAdvanced"`
	Onboarded      bool              `json:"onboarded"`
	ReduceMotion   bool              `json:"reduceMotion"`
	DefaultEnv     string            `json:"defaultEnv"`
	TextScale      float64           `json:"textScale"`
	AIProvider     string            `json:"aiProvider"` // "" (offline only) | anthropic
	AIModel        string            `json:"aiModel"`
	BackgroundTask bool              `json:"backgroundTask"` // Windows scheduled background checks
	Extra          map[string]string `json:"extra,omitempty"`
}

// DefaultSettings for a fresh install.
func DefaultSettings() Settings {
	return Settings{Theme: "rack", Density: "comfortable", DefaultEnv: core.EnvProduction, TextScale: 1, AIModel: "claude-opus-5-5"}
}

// LoadSettings reads settings, falling back to defaults.
func (s *Store) LoadSettings() Settings {
	st := DefaultSettings()
	_ = ReadJSON(filepath.Join(s.Dir, "settings.json"), &st)
	if st.TextScale == 0 {
		st.TextScale = 1
	}
	return st
}

// SaveSettings writes settings.
func (s *Store) SaveSettings(st Settings) error {
	return WriteJSON(filepath.Join(s.Dir, "settings.json"), st)
}

// ---- connections ----

// LoadConnections returns every saved provider connection.
func (s *Store) LoadConnections() ([]core.Connection, error) {
	var out []core.Connection
	err := ReadJSON(filepath.Join(s.Dir, "connections.json"), &out)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

// SaveConnections writes all connections.
func (s *Store) SaveConnections(cs []core.Connection) error {
	sort.Slice(cs, func(i, j int) bool { return cs[i].CreatedAt.Before(cs[j].CreatedAt) })
	return WriteJSON(filepath.Join(s.Dir, "connections.json"), cs)
}

// ---- projects ----

func (s *Store) projectDir(id string) (string, error) {
	id, err := clean(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, "projects", id), nil
}

func (s *Store) envDir(id, env string) (string, error) {
	pd, err := s.projectDir(id)
	if err != nil {
		return "", err
	}
	env, err = clean(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(pd, "env", env), nil
}

// SaveProject writes a project.
func (s *Store) SaveProject(p *core.Project) error {
	d, err := s.projectDir(p.ID)
	if err != nil {
		return err
	}
	p.UpdatedAt = time.Now().UTC()
	return WriteJSON(filepath.Join(d, "project.json"), p)
}

// LoadProject reads one project.
func (s *Store) LoadProject(id string) (*core.Project, error) {
	d, err := s.projectDir(id)
	if err != nil {
		return nil, err
	}
	var p core.Project
	if err := ReadJSON(filepath.Join(d, "project.json"), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProjects returns all projects, newest first.
func (s *Store) ListProjects() ([]*core.Project, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "projects"))
	if err != nil {
		return nil, err
	}
	var out []*core.Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := s.LoadProject(e.Name())
		if err == nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// DeleteProject removes a project's local records (never remote resources).
func (s *Store) DeleteProject(id string) error {
	d, err := s.projectDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(d)
}

// CodeDir is where generated code for a project lives.
func (s *Store) CodeDir(id string) (string, error) {
	d, err := s.projectDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "code"), nil
}

// LoadGenerated reads generated-file tracking for a project.
func (s *Store) LoadGenerated(id string) (map[string]core.GeneratedFile, error) {
	d, err := s.projectDir(id)
	if err != nil {
		return nil, err
	}
	out := map[string]core.GeneratedFile{}
	err = ReadJSON(filepath.Join(d, "generated.json"), &out)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]core.GeneratedFile{}, nil
	}
	return out, err
}

// SaveGenerated writes generated-file tracking.
func (s *Store) SaveGenerated(id string, m map[string]core.GeneratedFile) error {
	d, err := s.projectDir(id)
	if err != nil {
		return err
	}
	return WriteJSON(filepath.Join(d, "generated.json"), m)
}

// FilesDir holds files the user uploads for a project (product downloads).
// They are never committed to a repository.
func (s *Store) FilesDir(id string) (string, error) {
	d, err := s.projectDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "files"), nil
}

// ---- manifests ----

// LoadManifest reads the manifest for an environment (empty if none yet).
func (s *Store) LoadManifest(id, env string) (*core.Manifest, error) {
	d, err := s.envDir(id, env)
	if err != nil {
		return nil, err
	}
	m := core.NewManifest(id, env)
	err = ReadJSON(filepath.Join(d, "manifest.json"), m)
	if errors.Is(err, os.ErrNotExist) {
		return core.NewManifest(id, env), nil
	}
	if m.Resources == nil {
		m.Resources = map[string]*core.ResourceState{}
	}
	if m.Providers == nil {
		m.Providers = map[string]core.ProviderLink{}
	}
	if m.Generated == nil {
		m.Generated = map[string]core.GeneratedFile{}
	}
	return m, err
}

// SaveManifest writes the manifest atomically.
func (s *Store) SaveManifest(m *core.Manifest) error {
	d, err := s.envDir(m.Project, m.Environment)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m.UpdatedAt = time.Now().UTC()
	return WriteJSON(filepath.Join(d, "manifest.json"), m)
}

// UpdateManifest applies fn to the latest saved manifest and writes it back
// under the store lock, so a long-running reader (a health check) can record
// a field without overwriting what a build saved in the meantime.
func (s *Store) UpdateManifest(id, env string, fn func(*core.Manifest)) (*core.Manifest, error) {
	d, err := s.envDir(id, env)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := core.NewManifest(id, env)
	if err := ReadJSON(filepath.Join(d, "manifest.json"), m); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if m.Resources == nil {
		m.Resources = map[string]*core.ResourceState{}
	}
	if m.Providers == nil {
		m.Providers = map[string]core.ProviderLink{}
	}
	if m.Generated == nil {
		m.Generated = map[string]core.GeneratedFile{}
	}
	fn(m)
	m.UpdatedAt = time.Now().UTC()
	return m, WriteJSON(filepath.Join(d, "manifest.json"), m)
}

// ResetEnv forgets everything recorded for one environment (manifest, runs,
// reports, history, snapshots). Used for practice projects, whose simulated
// resources disappear when the simulator stops.
func (s *Store) ResetEnv(id, env string) error {
	d, err := s.envDir(id, env)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(d)
}

// ---- runs ----

// SaveRun checkpoints a run.
func (s *Store) SaveRun(r *core.Run) error {
	d, err := s.envDir(r.Project, r.Environment)
	if err != nil {
		return err
	}
	id, err := clean(r.ID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.UpdatedAt = time.Now().UTC()
	return WriteJSON(filepath.Join(d, "runs", id+".json"), r)
}

// LoadRun reads a run.
func (s *Store) LoadRun(project, env, id string) (*core.Run, error) {
	d, err := s.envDir(project, env)
	if err != nil {
		return nil, err
	}
	id, err = clean(id)
	if err != nil {
		return nil, err
	}
	var r core.Run
	if err := ReadJSON(filepath.Join(d, "runs", id+".json"), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRuns returns runs newest first.
func (s *Store) ListRuns(project, env string) ([]*core.Run, error) {
	d, err := s.envDir(project, env)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(d, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*core.Run
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r core.Run
		if ReadJSON(filepath.Join(d, "runs", e.Name()), &r) == nil {
			out = append(out, &r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// ---- snapshots ----

// SaveSnapshot stores a restorable checkpoint.
func (s *Store) SaveSnapshot(sn *core.Snapshot) error {
	d, err := s.envDir(sn.Project, sn.Environment)
	if err != nil {
		return err
	}
	id, err := clean(sn.ID)
	if err != nil {
		return err
	}
	return WriteJSON(filepath.Join(d, "snapshots", id+".json"), sn)
}

// LoadSnapshot reads one snapshot.
func (s *Store) LoadSnapshot(project, env, id string) (*core.Snapshot, error) {
	d, err := s.envDir(project, env)
	if err != nil {
		return nil, err
	}
	id, err = clean(id)
	if err != nil {
		return nil, err
	}
	var sn core.Snapshot
	if err := ReadJSON(filepath.Join(d, "snapshots", id+".json"), &sn); err != nil {
		return nil, err
	}
	return &sn, nil
}

// ListSnapshots returns snapshots newest first (without heavy bodies trimmed).
func (s *Store) ListSnapshots(project, env string) ([]*core.Snapshot, error) {
	d, err := s.envDir(project, env)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(d, "snapshots"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*core.Snapshot
	for _, e := range entries {
		var sn core.Snapshot
		if ReadJSON(filepath.Join(d, "snapshots", e.Name()), &sn) == nil {
			out = append(out, &sn)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ---- health ----

// SaveReport stores a health report and appends to the history.
func (s *Store) SaveReport(r *core.HealthReport, note string) error {
	d, err := s.envDir(r.Project, r.Environment)
	if err != nil {
		return err
	}
	id, err := clean(r.ID)
	if err != nil {
		return err
	}
	if err := WriteJSON(filepath.Join(d, "reports", id+".json"), r); err != nil {
		return err
	}
	h := core.HistoryEntry{At: r.FinishedAt, Kind: r.Kind, Trigger: r.Trigger, Overall: r.Overall, Headline: r.Headline, Note: note, ReportID: r.ID}
	return appendJSONL(filepath.Join(d, "history.jsonl"), h)
}

// LoadReport reads one health report.
func (s *Store) LoadReport(project, env, id string) (*core.HealthReport, error) {
	d, err := s.envDir(project, env)
	if err != nil {
		return nil, err
	}
	id, err = clean(id)
	if err != nil {
		return nil, err
	}
	var r core.HealthReport
	if err := ReadJSON(filepath.Join(d, "reports", id+".json"), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// History returns the most recent history entries, newest first.
func (s *Store) History(project, env string, limit int) ([]core.HistoryEntry, error) {
	d, err := s.envDir(project, env)
	if err != nil {
		return nil, err
	}
	var out []core.HistoryEntry
	err = readJSONL(filepath.Join(d, "history.jsonl"), func(line []byte) {
		var h core.HistoryEntry
		if json.Unmarshal(line, &h) == nil {
			out = append(out, h)
		}
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, err
}

// PruneReports keeps the newest n report files per environment.
func (s *Store) PruneReports(project, env string, keep int) {
	d, err := s.envDir(project, env)
	if err != nil {
		return
	}
	dir := filepath.Join(d, "reports")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type fi struct {
		name string
		t    time.Time
	}
	var files []fi
	for _, e := range entries {
		info, err := e.Info()
		if err == nil {
			files = append(files, fi{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].t.After(files[j].t) })
	for _, f := range files[keep:] {
		os.Remove(filepath.Join(dir, f.name))
	}
}

// ---- logs ----

// AppendLog writes an (already redacted) log entry.
func (s *Store) AppendLog(e core.LogEntry) error {
	name := e.At.UTC().Format("2006-01-02") + ".jsonl"
	return appendJSONL(filepath.Join(s.Dir, "logs", name), e)
}

// LogQuery filters the unified log.
type LogQuery struct {
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Provider    string    `json:"provider"`
	Resource    string    `json:"resource"`
	Level       string    `json:"level"` // minimum level
	RequestID   string    `json:"requestId"`
	RunID       string    `json:"runId"`
	Text        string    `json:"text"`
	Since       time.Time `json:"since"`
	Until       time.Time `json:"until"`
	Limit       int       `json:"limit"`
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// QueryLogs scans recent log files newest first.
func (s *Store) QueryLogs(q LogQuery) ([]core.LogEntry, error) {
	dir := filepath.Join(s.Dir, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if q.Limit <= 0 {
		q.Limit = 500
	}
	text := strings.ToLower(q.Text)
	var out []core.LogEntry
	for _, n := range names {
		if !q.Since.IsZero() && n < q.Since.UTC().Format("2006-01-02") {
			break
		}
		var day []core.LogEntry
		_ = readJSONL(filepath.Join(dir, n), func(line []byte) {
			var e core.LogEntry
			if json.Unmarshal(line, &e) != nil {
				return
			}
			if q.Project != "" && e.Project != q.Project {
				return
			}
			if q.Environment != "" && e.Environment != "" && e.Environment != q.Environment {
				return
			}
			if q.Provider != "" && e.Provider != q.Provider {
				return
			}
			if q.Resource != "" && e.Resource != q.Resource {
				return
			}
			if q.RequestID != "" && e.RequestID != q.RequestID {
				return
			}
			if q.RunID != "" && e.RunID != q.RunID {
				return
			}
			if q.Level != "" && levelRank[e.Level] < levelRank[q.Level] {
				return
			}
			if !q.Since.IsZero() && e.At.Before(q.Since) {
				return
			}
			if !q.Until.IsZero() && e.At.After(q.Until) {
				return
			}
			if text != "" && !strings.Contains(strings.ToLower(e.Message+" "+e.Detail+" "+e.Resource), text) {
				return
			}
			day = append(day, e)
		})
		for i := len(day) - 1; i >= 0; i-- {
			out = append(out, day[i])
			if len(out) >= q.Limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// PruneLogs deletes log files older than the given number of days.
func (s *Store) PruneLogs(days int) {
	dir := filepath.Join(s.Dir, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format("2006-01-02")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") && e.Name() < cutoff {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func appendJSONL(path string, v any) error {
	bs, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(bs, '\n'))
	return err
}

func readJSONL(path string, fn func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}
