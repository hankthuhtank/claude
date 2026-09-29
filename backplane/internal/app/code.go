package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/blueprints"
	"safisolutions.org/backplane/internal/core"
)

// CodeFile is one generated file and whether the user changed it.
type CodeFile struct {
	Path            string    `json:"path"`
	Role            string    `json:"role"`   // generated | system-config
	Status          string    `json:"status"` // generated | user-modified | missing
	Language        string    `json:"language"`
	Size            int       `json:"size"`
	Version         int       `json:"version"`
	UpdatedAt       time.Time `json:"updatedAt"`
	UpdateAvailable bool      `json:"updateAvailable"` // Backplane would now generate something different
}

// writeGenerated writes the project's generated files. Files the user edited
// are kept (and reported) unless listed in overwrite.
func (a *App) writeGenerated(p *core.Project, overwrite map[string]bool) (written, kept []string, err error) {
	files, err := blueprints.GenerateFiles(p)
	if err != nil {
		return nil, nil, err
	}
	track, err := a.Store.LoadGenerated(p.ID)
	if err != nil {
		return nil, nil, err
	}
	roles := map[string]string{}
	for _, g := range p.Blueprint.Generated {
		roles[g.Path] = g.Role
	}
	now := time.Now().UTC()
	for _, rel := range sortedKeys(files) {
		content := files[rel]
		full, err := a.Engine.CodePath(p.ID, rel)
		if err != nil {
			return written, kept, err
		}
		want := core.HashBytes([]byte(content))
		t := track[rel]
		t.Path = rel
		t.Role = orStr(roles[rel], "generated")
		if cur, rerr := os.ReadFile(full); rerr == nil {
			have := core.HashBytes(cur)
			if have == want {
				t.GeneratedHash, t.CurrentHash = want, have
				track[rel] = t
				continue
			}
			edited := t.GeneratedHash == "" || have != t.GeneratedHash
			if edited && !overwrite[rel] {
				t.CurrentHash = have
				track[rel] = t
				kept = append(kept, rel)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return written, kept, err
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			return written, kept, err
		}
		t.Version++
		t.GeneratedHash, t.CurrentHash, t.UpdatedAt = want, want, now
		track[rel] = t
		written = append(written, rel)
	}
	return written, kept, a.Store.SaveGenerated(p.ID, track)
}

// codeFiles lists generated files with their live status.
func (a *App) codeFiles(p *core.Project) ([]CodeFile, error) {
	track, err := a.Store.LoadGenerated(p.ID)
	if err != nil {
		return nil, err
	}
	fresh, _ := blueprints.GenerateFiles(p)
	var out []CodeFile
	for _, rel := range sortedKeys(track) {
		t := track[rel]
		f := CodeFile{Path: rel, Role: t.Role, Language: languageOf(rel), Version: t.Version, UpdatedAt: t.UpdatedAt}
		full, err := a.Engine.CodePath(p.ID, rel)
		if err != nil {
			continue
		}
		cur, rerr := os.ReadFile(full)
		switch {
		case rerr != nil:
			f.Status = "missing"
		case core.HashBytes(cur) == t.GeneratedHash:
			f.Status = "generated"
			f.Size = len(cur)
		default:
			f.Status = "user-modified"
			f.Size = len(cur)
		}
		if c, ok := fresh[rel]; ok && core.HashBytes([]byte(c)) != t.GeneratedHash {
			f.UpdateAvailable = true
		}
		out = append(out, f)
	}
	return out, nil
}

func languageOf(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".mjs":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".sql":
		return "sql"
	case ".json", ".jsonc":
		return "json"
	case ".yml", ".yaml":
		return "yaml"
	case ".html":
		return "html"
	case ".md":
		return "markdown"
	}
	return "text"
}

// CodeParams selects a project (and optionally one file).
type CodeParams struct {
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
}

// CodeResult lists files and, when a path is given, its content.
type CodeResult struct {
	Dir       string     `json:"dir"`
	Files     []CodeFile `json:"files"`
	Path      string     `json:"path,omitempty"`
	Content   string     `json:"content,omitempty"`
	Generated string     `json:"generated,omitempty"` // what Backplane would write now (for comparing edits)
}

// Code returns the generated code for the Code screen.
func (a *App) Code(ctx context.Context, p CodeParams) (*CodeResult, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	dir, _ := a.Store.CodeDir(pr.ID)
	files, err := a.codeFiles(pr)
	if err != nil {
		return nil, err
	}
	res := &CodeResult{Dir: dir, Files: files}
	if p.Path != "" {
		if !tracked(files, p.Path) {
			return nil, fmt.Errorf("%s is not a generated file", p.Path)
		}
		res.Path = p.Path
		res.Content, _ = a.Engine.ReadCode(pr.ID, p.Path)
		if fresh, err := blueprints.GenerateFiles(pr); err == nil {
			res.Generated = fresh[p.Path]
		}
	}
	return res, nil
}

func tracked(files []CodeFile, rel string) bool {
	for _, f := range files {
		if f.Path == rel {
			return true
		}
	}
	return false
}

// RegenerateParams regenerates code; Overwrite lists user-edited files the
// user agreed to replace.
type RegenerateParams struct {
	ProjectID string   `json:"projectId"`
	Overwrite []string `json:"overwrite"`
}

// RegenerateResult reports what was written and what was kept.
type RegenerateResult struct {
	Written []string   `json:"written"`
	Kept    []string   `json:"kept"`
	Files   []CodeFile `json:"files"`
}

// RegenerateCode rewrites generated files, never silently replacing edits.
func (a *App) RegenerateCode(ctx context.Context, p RegenerateParams) (*RegenerateResult, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	ow := map[string]bool{}
	for _, f := range p.Overwrite {
		ow[f] = true
	}
	written, kept, err := a.writeGenerated(pr, ow)
	if err != nil {
		return nil, err
	}
	if len(written) > 0 {
		a.log("info", pr.ID, "", fmt.Sprintf("Regenerated %d file(s): %s", len(written), strings.Join(written, ", ")))
	}
	files, _ := a.codeFiles(pr)
	return &RegenerateResult{Written: written, Kept: kept, Files: files}, nil
}

// SaveCodeParams saves an edit made in the Code screen.
type SaveCodeParams struct {
	ProjectID string `json:"projectId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// SaveCode writes a user edit to a generated file. The file is then marked
// USER MODIFIED and is deployed as edited on the next build.
func (a *App) SaveCode(ctx context.Context, p SaveCodeParams) (*CodeFile, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	files, err := a.codeFiles(pr)
	if err != nil {
		return nil, err
	}
	if !tracked(files, p.Path) {
		return nil, fmt.Errorf("%s is not a generated file", p.Path)
	}
	if len(p.Content) > 4<<20 {
		return nil, fmt.Errorf("the file is too large to edit here — edit it in the code folder instead")
	}
	full, err := a.Engine.CodePath(pr.ID, p.Path)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, []byte(p.Content), 0o600); err != nil {
		return nil, err
	}
	a.dropPlans(pr.ID)
	a.log("info", pr.ID, "", "Edited "+p.Path+" (marked USER MODIFIED; Backplane will not overwrite it)")
	files, _ = a.codeFiles(pr)
	for _, f := range files {
		if f.Path == p.Path {
			return &f, nil
		}
	}
	return nil, fmt.Errorf("file not found after saving")
}

// OpenCodeFolder opens the project's code folder in the file manager.
func (a *App) OpenCodeFolder(ctx context.Context, p IDParams) (bool, error) {
	pr, err := a.project(p.ID)
	if err != nil {
		return false, err
	}
	dir, err := a.Store.CodeDir(pr.ID)
	if err != nil {
		return false, err
	}
	if !dirExists(dir) {
		return false, fmt.Errorf("the code folder does not exist yet")
	}
	return true, openPath(dir)
}

// ---- product files ----

// UploadParams carries a small file inline (large files stream through the
// local /upload endpoint instead).
type UploadParams struct {
	ProjectID  string `json:"projectId"`
	Name       string `json:"name"`
	DataBase64 string `json:"dataBase64"`
}

// UploadResult describes a stored product file.
type UploadResult struct {
	Name    string         `json:"name"`
	Size    int64          `json:"size"`
	SHA256  string         `json:"sha256"`
	Project ProjectSummary `json:"project"`
}

// MaxUpload is the largest product file Backplane accepts (R2's single-part
// upload through the API is limited; bigger files need multipart uploads).
const MaxUpload = 300 << 20

// UploadProductFile stores the file customers buy (inline variant).
func (a *App) UploadProductFile(ctx context.Context, p UploadParams) (*UploadResult, error) {
	data, err := base64.StdEncoding.DecodeString(p.DataBase64)
	if err != nil {
		return nil, fmt.Errorf("the file data is not valid base64")
	}
	return a.SaveProductFile(p.ProjectID, p.Name, strings.NewReader(string(data)))
}

// SaveProductFile streams a product file into the project's private files
// folder and points the blueprint at it. The file is uploaded to the private
// R2 bucket on the next build and is never committed to a repository.
func (a *App) SaveProductFile(projectID, name string, r io.Reader) (*UploadResult, error) {
	pr, err := a.project(projectID)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "." || base == "/" || strings.TrimSpace(base) == "" {
		return nil, fmt.Errorf("the file needs a name")
	}
	dir, err := a.Store.FilesDir(pr.ID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, base)
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, MaxUpload+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if n > MaxUpload {
		os.Remove(tmp)
		return nil, fmt.Errorf("the file is larger than %d MB", MaxUpload>>20)
	}
	if n == 0 {
		os.Remove(tmp)
		return nil, fmt.Errorf("the file is empty")
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	res, err := a.UpdateProject(context.Background(), UpdateProjectParams{ID: pr.ID, Answers: blueprints.Answers{"product_file": dst, "product_file_sha": sum}})
	if err != nil {
		return nil, err
	}
	a.log("info", pr.ID, "", fmt.Sprintf("Stored product file %s (%s). It is uploaded to private storage on the next build.", base, humanBytes(n)))
	return &UploadResult{Name: base, Size: n, SHA256: sum, Project: res.Project}, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
