package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// Handlers returns every GitHub resource handler.
func Handlers() []providers.Handler {
	return []providers.Handler{repoH{}, filesH{}, secretH{}, variableH{}, envH{}}
}

func gconn(s *providers.Session) (*providers.Conn, error) { return s.Conn("github") }

// ================= Repository =================

type repoH struct{}

func (repoH) Kind() string { return KindRepo }

type repo struct {
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
}

func (repoH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	owner := providers.Str(spec.Props, "owner")
	if owner == "" {
		owner = c.Connection.Setting("owner")
	}
	login := c.Connection.Setting("login")
	if owner == "" {
		owner = login
	}
	name := providers.Str(spec.Props, "name")
	var r repo
	if _, err := Call(ctx, c, "GET", "/repos/"+owner+"/"+name, nil, &r); err == nil {
		if st == nil && !spec.Adopt {
			return nil, &core.Problem{Title: "Repository already exists", Provider: "github", Code: httpx.KindConflict,
				Summary: "The repository " + owner + "/" + name + " already exists. Choose ‘Use existing’ to commit Backplane's code into it (only new or unedited files are written), or rename it."}
		}
		next := providers.Touch(spec, st, r.FullName, r.FullName)
		if st == nil {
			next.CreatedBy = "adopted"
		}
		setRepoOutputs(next, &r)
		return &providers.ApplyResult{State: next}, nil
	} else if !httpx.IsNotFound(err) {
		return nil, err
	}
	body := map[string]any{"name": name, "private": !providers.Bool(spec.Props, "public"), "description": providers.Str(spec.Props, "description"), "auto_init": true}
	path := "/user/repos"
	if owner != "" && !strings.EqualFold(owner, login) {
		path = "/orgs/" + owner + "/repos"
	}
	s.Say("Creating GitHub repository %s/%s", owner, name)
	if _, err := Call(ctx, c, "POST", path, body, &r); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, r.FullName, r.FullName)
	setRepoOutputs(next, &r)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func setRepoOutputs(st *core.ResourceState, r *repo) {
	st.SetOutput("full_name", r.FullName)
	st.SetOutput("url", r.HTMLURL)
	branch := r.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	st.SetOutput("default_branch", branch)
}

func (repoH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var r repo
	if _, err := Call(ctx, c, "GET", "/repos/"+st.ID, nil, &r); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: r.FullName, Name: r.FullName, Props: map[string]any{"archived": r.Archived, "default_branch": r.DefaultBranch}}, nil
}

func (repoH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs != nil && obs.Exists && providers.Bool(obs.Props, "archived") {
		return []core.DriftItem{{Resource: spec.Key, Kind: spec.Kind, Provider: "github", Field: "repository", Expected: "active", Actual: "archived (read-only)",
			Severity: core.HealthFail, Breaks: []string{"Code export and automatic deploys"}, Recommended: "Unarchive the repository on GitHub"}}
	}
	return nil
}

func (repoH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil // repositories hold people's work; Backplane never deletes them
}

// ================= Generated files =================

type filesH struct{}

func (filesH) Kind() string { return KindFiles }

type gitRef struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// FileConflict is a generated file a person changed on GitHub.
type FileConflict struct {
	Path string `json:"path"`
}

func (filesH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	repoName := providers.Str(spec.Props, "repo")
	branch := providers.Str(spec.Props, "branch")
	if branch == "" {
		branch = "main"
	}
	files := providers.Map(spec.Props, "files")
	if repoName == "" || len(files) == 0 {
		return nil, fmt.Errorf("files spec needs a repo and files")
	}
	var ref gitRef
	if _, err := Call(ctx, c, "GET", "/repos/"+repoName+"/git/ref/heads/"+url.PathEscape(branch), nil, &ref); err != nil {
		return nil, err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+repoName+"/git/commits/"+ref.Object.SHA, nil, &commit); err != nil {
		return nil, err
	}
	var tree struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+repoName+"/git/trees/"+commit.Tree.SHA+"?recursive=1", nil, &tree); err != nil {
		return nil, err
	}
	current := map[string]string{}
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			current[e.Path] = e.SHA
		}
	}
	lastWritten := map[string]string{}
	if st != nil && st.Applied != nil {
		lastWritten = providers.Map(st.Applied, "blobs")
	}
	written := map[string]string{}
	for k, v := range lastWritten {
		written[k] = v
	}
	var entries []map[string]any
	var conflicts []string
	for _, path := range providers.SortedKeys(files) {
		content := files[path]
		ours := BlobSHA([]byte(content))
		have, exists := current[path]
		switch {
		case exists && have == ours:
			written[path] = ours
		case exists && have != lastWritten[path] && !(lastWritten[path] == "" && initReadme(ctx, c, s, repoName, path, have)):
			// Someone edited (or created) this file; never overwrite silently.
			conflicts = append(conflicts, path)
		default:
			entries = append(entries, map[string]any{"path": path, "mode": "100644", "type": "blob", "content": content})
			written[path] = ours
		}
	}
	next := providers.Touch(spec, st, repoName+"@"+branch, repoName)
	if next.Applied == nil {
		next.Applied = map[string]any{}
	}
	next.Applied["blobs"] = written
	next.Applied["conflicts"] = conflicts
	if len(conflicts) > 0 {
		next.Note = fmt.Sprintf("%d file(s) were changed on GitHub by someone else and were NOT overwritten: %s. Review them on the Code screen.", len(conflicts), strings.Join(conflicts, ", "))
	} else {
		next.Note = ""
	}
	if len(entries) == 0 {
		next.SetOutput("commit", ref.Object.SHA)
		return &providers.ApplyResult{State: next}, nil
	}
	s.Say("Committing %d file(s) to %s on %s", len(entries), branch, repoName)
	var newTree struct {
		SHA string `json:"sha"`
	}
	if _, err := Call(ctx, c, "POST", "/repos/"+repoName+"/git/trees", map[string]any{"base_tree": commit.Tree.SHA, "tree": entries}, &newTree); err != nil {
		return nil, err
	}
	msg := providers.Str(spec.Props, "message")
	if msg == "" {
		msg = "Backplane: update generated backend code"
	}
	var newCommit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	}
	if _, err := Call(ctx, c, "POST", "/repos/"+repoName+"/git/commits", map[string]any{"message": msg, "tree": newTree.SHA, "parents": []string{ref.Object.SHA}}, &newCommit); err != nil {
		return nil, err
	}
	if _, err := Call(ctx, c, "PATCH", "/repos/"+repoName+"/git/refs/heads/"+url.PathEscape(branch), map[string]any{"sha": newCommit.SHA, "force": false}, nil); err != nil {
		return nil, err
	}
	next.SetOutput("commit", newCommit.SHA)
	next.SetOutput("url", newCommit.HTMLURL)
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (filesH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	repoName := providers.Str(spec.Props, "repo")
	branch := providers.Str(spec.Props, "branch")
	if branch == "" {
		branch = "main"
	}
	if repoName == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var ref gitRef
	if _, err := Call(ctx, c, "GET", "/repos/"+repoName+"/git/ref/heads/"+url.PathEscape(branch), nil, &ref); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	var tree struct {
		Tree []treeEntry `json:"tree"`
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+repoName+"/git/trees/"+ref.Object.SHA+"?recursive=1", nil, &tree); err != nil {
		return nil, err
	}
	blobs := map[string]string{}
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			blobs[e.Path] = e.SHA
		}
	}
	return &providers.Observation{Exists: true, ID: repoName + "@" + branch, Props: map[string]any{"blobs": blobs, "head": ref.Object.SHA}}, nil
}

func (filesH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists || st == nil {
		return nil
	}
	live, _ := obs.Props["blobs"].(map[string]string)
	var items []core.DriftItem
	for path, ours := range providers.Map(st.Applied, "blobs") {
		have, ok := live[path]
		switch {
		case !ok:
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "github", Field: path, Expected: "present", Actual: "Deleted on GitHub",
				Severity: core.HealthWarn, Breaks: []string{"Automatic deploys may fail"}, Recommended: "Restore the generated file", FixID: "reapply:" + spec.Key})
		case have != ours:
			items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "github", Field: path, Expected: "Backplane version", Actual: "USER MODIFIED",
				Severity: core.HealthWarn, Breaks: []string{"Nothing is broken; Backplane will not overwrite this file without asking"}, Recommended: "Review on the Code screen"})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Field < items[j].Field })
	return items
}

func (filesH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil
}

// ================= Actions secret =================

type secretH struct{}

func (secretH) Kind() string { return KindSecret }

func secretPaths(repoName, env, name string) (keyPath, secretPath string) {
	if env != "" {
		return "/repos/" + repoName + "/environments/" + url.PathEscape(env) + "/secrets/public-key",
			"/repos/" + repoName + "/environments/" + url.PathEscape(env) + "/secrets/" + name
	}
	return "/repos/" + repoName + "/actions/secrets/public-key", "/repos/" + repoName + "/actions/secrets/" + name
}

func (secretH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	repoName, name, env := providers.Str(spec.Props, "repo"), providers.Str(spec.Props, "name"), providers.Str(spec.Props, "environment")
	value := providers.Str(spec.Props, "value")
	sum := sha256.Sum256([]byte(value))
	dg := hex.EncodeToString(sum[:])[:12]
	keyPath, secretPath := secretPaths(repoName, env, name)
	if st != nil && providers.Str(st.Applied, "digest") == dg {
		if _, err := Call(ctx, c, "GET", secretPath, nil, nil); err == nil {
			return &providers.ApplyResult{State: providers.Touch(spec, st, repoName+"#"+name, name)}, nil
		}
	}
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if _, err := Call(ctx, c, "GET", keyPath, nil, &pk); err != nil {
		return nil, err
	}
	sealed, err := SealSecret(pk.Key, value)
	if err != nil {
		return nil, err
	}
	s.Say("Setting GitHub Actions secret %s", name)
	if _, err := Call(ctx, c, "PUT", secretPath, map[string]any{"encrypted_value": sealed, "key_id": pk.KeyID}, nil); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, repoName+"#"+name, name)
	next.Applied = map[string]any{"digest": dg, "repo": repoName, "environment": env}
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (secretH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	_, secretPath := secretPaths(providers.Str(spec.Props, "repo"), providers.Str(spec.Props, "environment"), providers.Str(spec.Props, "name"))
	if _, err := Call(ctx, c, "GET", secretPath, nil, nil); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: providers.Str(spec.Props, "name")}, nil
}

func (secretH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil // a missing secret is reported by the existence level
}

func (secretH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := gconn(s)
	if err != nil {
		return err
	}
	_, secretPath := secretPaths(providers.Str(st.Applied, "repo"), providers.Str(st.Applied, "environment"), st.Name)
	_, err = Call(ctx, c, "DELETE", secretPath, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Actions variable =================

type variableH struct{}

func (variableH) Kind() string { return KindVariable }

func (variableH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	repoName, name, value := providers.Str(spec.Props, "repo"), providers.Str(spec.Props, "name"), providers.Str(spec.Props, "value")
	var cur struct {
		Value string `json:"value"`
	}
	_, err = Call(ctx, c, "GET", "/repos/"+repoName+"/actions/variables/"+name, nil, &cur)
	switch {
	case err == nil && cur.Value == value:
	case err == nil:
		s.Say("Updating GitHub Actions variable %s", name)
		if _, err := Call(ctx, c, "PATCH", "/repos/"+repoName+"/actions/variables/"+name, map[string]any{"name": name, "value": value}, nil); err != nil {
			return nil, err
		}
	case httpx.IsNotFound(err):
		s.Say("Adding GitHub Actions variable %s", name)
		if _, err := Call(ctx, c, "POST", "/repos/"+repoName+"/actions/variables", map[string]any{"name": name, "value": value}, nil); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	next := providers.Touch(spec, st, repoName+"#"+name, name)
	next.Applied = map[string]any{"value": value, "repo": repoName}
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (variableH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	var cur struct {
		Value string `json:"value"`
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+providers.Str(spec.Props, "repo")+"/actions/variables/"+providers.Str(spec.Props, "name"), nil, &cur); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, Props: map[string]any{"value": cur.Value}}, nil
}

func (variableH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	return providers.DriftIf(nil, spec, "variable "+providers.Str(spec.Props, "name"), providers.Str(spec.Props, "value"), providers.Str(obs.Props, "value"),
		core.HealthWarn, []string{"Automatic deploys use a different value"}, "Restore the variable")
}

func (variableH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := gconn(s)
	if err != nil {
		return err
	}
	_, err = Call(ctx, c, "DELETE", "/repos/"+providers.Str(st.Applied, "repo")+"/actions/variables/"+st.Name, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Environment =================

type envH struct{}

func (envH) Kind() string { return KindEnvironment }

func (envH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	repoName, name := providers.Str(spec.Props, "repo"), providers.Str(spec.Props, "name")
	s.Say("Ensuring GitHub environment %s", name)
	if _, err := Call(ctx, c, "PUT", "/repos/"+repoName+"/environments/"+url.PathEscape(name), map[string]any{}, nil); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, repoName+"/"+name, name)
	next.Applied = map[string]any{"repo": repoName}
	return &providers.ApplyResult{State: next, Created: st == nil}, nil
}

func (envH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := gconn(s)
	if err != nil {
		return nil, err
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+providers.Str(spec.Props, "repo")+"/environments/"+url.PathEscape(providers.Str(spec.Props, "name")), nil, nil); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true}, nil
}

func (envH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (envH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	return nil
}

// initReadme reports whether a file is the README GitHub generated when
// Backplane created the repository ("initialize with a README"). That file
// is Backplane's to replace; anything else a person wrote is never touched.
func initReadme(ctx context.Context, c *providers.Conn, s *providers.Session, repo, path, sha string) bool {
	if path != "README.md" || s.Manifest == nil {
		return false
	}
	ours := false
	for _, st := range s.Manifest.Resources {
		if st.Kind == KindRepo && (st.ID == repo || st.Output("full_name") == repo) && st.CreatedBy == "backplane" {
			ours = true
		}
	}
	if !ours {
		return false
	}
	var blob struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		Size     int    `json:"size"`
	}
	if _, err := Call(ctx, c, "GET", "/repos/"+repo+"/git/blobs/"+sha, nil, &blob); err != nil || blob.Size > 600 {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
	if err != nil {
		raw = []byte(blob.Content)
	}
	text := strings.TrimSpace(string(raw))
	return strings.HasPrefix(text, "# ") && strings.Count(text, "\n") <= 3
}
