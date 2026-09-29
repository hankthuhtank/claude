package sim

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"
)

type ghRepo struct {
	FullName, Branch string
	Private          bool
	Archived         bool
	Head             string
	Commits          map[string]ghCommit // sha -> commit
	Trees            map[string]map[string]string
	Secrets          map[string]time.Time
	Variables        map[string]string
	Envs             map[string]bool
	PubKey           [32]byte
	KeyID            string
	Runs             []map[string]any
}

type ghCommit struct {
	SHA, Tree, Message string
	Parents            []string
}

type ghState struct {
	login string
	repos map[string]*ghRepo
}

func newGH() *ghState { return &ghState{login: "practice-user", repos: map[string]*ghRepo{}} }

func blobSHA(content string) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

func ghErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "documentation_url": "https://docs.github.com/rest"})
}

func (s *Server) newRepoLocked(full string, private bool) *ghRepo {
	pub, _, _ := box.GenerateKey(rand.Reader)
	r := &ghRepo{FullName: full, Branch: "main", Private: private, Commits: map[string]ghCommit{}, Trees: map[string]map[string]string{},
		Secrets: map[string]time.Time{}, Variables: map[string]string{}, Envs: map[string]bool{}, PubKey: *pub, KeyID: newID("", 10)}
	files := map[string]string{"README.md": "# " + full + "\n"}
	tree := newID("", 20)
	r.Trees[tree] = files
	c := ghCommit{SHA: newID("", 20), Tree: tree, Message: "Initial commit"}
	r.Commits[c.SHA] = c
	r.Head = c.SHA
	s.gh.repos[full] = r
	return r
}

func (s *Server) repoJSON(r *ghRepo) map[string]any {
	return map[string]any{"full_name": r.FullName, "name": strings.SplitN(r.FullName, "/", 2)[1], "html_url": "https://github.com/" + r.FullName,
		"default_branch": r.Branch, "private": r.Private, "archived": r.Archived, "pushed_at": s.now().UTC().Format(time.RFC3339)}
}

func (s *Server) github(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.auth(r, "github") {
		ghErr(w, 401, "Bad credentials")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	gh := s.gh
	p := segs(rest)
	m := r.Method
	switch {
	case eq(p, "user") && m == "GET":
		w.Header().Set("GitHub-Authentication-Token-Expiration", s.now().Add(60*24*time.Hour).UTC().Format("2006-01-02 15:04:05 MST"))
		writeJSON(w, 200, map[string]any{"login": gh.login, "id": 4242, "name": "Practice User"})
		return
	case eq(p, "rate_limit"):
		writeJSON(w, 200, map[string]any{"resources": map[string]any{"core": map[string]any{"limit": 5000, "remaining": 4987, "reset": s.now().Add(time.Hour).Unix()}}})
		return
	case eq(p, "user", "repos") && m == "POST", len(p) == 3 && p[0] == "orgs" && p[2] == "repos" && m == "POST":
		var b struct {
			Name    string `json:"name"`
			Private bool   `json:"private"`
		}
		_ = readJSON(r, &b)
		owner := gh.login
		if p[0] == "orgs" {
			owner = p[1]
		}
		full := owner + "/" + b.Name
		if gh.repos[full] != nil {
			writeJSON(w, 422, map[string]any{"message": "Repository creation failed.", "errors": []map[string]any{{"resource": "Repository", "code": "custom", "field": "name", "message": "name already exists on this account"}}})
			return
		}
		writeJSON(w, 201, s.repoJSON(s.newRepoLocked(full, b.Private)))
		return
	case eq(p, "user", "repos") && m == "GET":
		var out []any
		for _, n := range sortedKeys(gh.repos) {
			out = append(out, s.repoJSON(gh.repos[n]))
		}
		writeJSON(w, 200, orAny(out))
		return
	}
	if len(p) < 3 || p[0] != "repos" {
		ghErr(w, 404, "Not Found")
		return
	}
	full := p[1] + "/" + p[2]
	repo := gh.repos[full]
	if repo == nil {
		ghErr(w, 404, "Not Found")
		return
	}
	q := p[3:]
	switch {
	case len(q) == 0:
		writeJSON(w, 200, s.repoJSON(repo))
	case len(q) >= 3 && q[0] == "git" && q[1] == "ref":
		if strings.Join(q[2:], "/") != "heads/"+repo.Branch {
			ghErr(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"ref": "refs/heads/" + repo.Branch, "object": map[string]any{"sha": repo.Head, "type": "commit"}})
	case len(q) == 3 && q[0] == "git" && q[1] == "commits" && m == "GET":
		c, ok := repo.Commits[q[2]]
		if !ok {
			ghErr(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"sha": c.SHA, "tree": map[string]any{"sha": c.Tree}, "message": c.Message})
	case len(q) == 3 && q[0] == "git" && q[1] == "trees" && m == "GET":
		files, ok := repo.Trees[q[2]]
		if !ok {
			if c, isCommit := repo.Commits[q[2]]; isCommit {
				files = repo.Trees[c.Tree]
				ok = true
			}
		}
		if !ok {
			ghErr(w, 404, "Not Found")
			return
		}
		var entries []any
		for _, path := range sortedKeys(files) {
			entries = append(entries, map[string]any{"path": path, "mode": "100644", "type": "blob", "sha": blobSHA(files[path])})
		}
		writeJSON(w, 200, map[string]any{"sha": q[2], "tree": orAny(entries), "truncated": false})
	case len(q) == 3 && q[0] == "git" && q[1] == "blobs" && m == "GET":
		for _, files := range repo.Trees {
			for _, content := range files {
				if blobSHA(content) == q[2] {
					writeJSON(w, 200, map[string]any{"sha": q[2], "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))})
					return
				}
			}
		}
		ghErr(w, 404, "Not Found")
	case eq(q, "git", "trees") && m == "POST":
		var b struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			} `json:"tree"`
		}
		_ = readJSON(r, &b)
		base := repo.Trees[b.BaseTree]
		files := map[string]string{}
		for k, v := range base {
			files[k] = v
		}
		for _, e := range b.Tree {
			files[e.Path] = e.Content
		}
		id := newID("", 20)
		repo.Trees[id] = files
		writeJSON(w, 201, map[string]any{"sha": id})
	case eq(q, "git", "commits") && m == "POST":
		var b struct {
			Message string   `json:"message"`
			Tree    string   `json:"tree"`
			Parents []string `json:"parents"`
		}
		_ = readJSON(r, &b)
		if repo.Trees[b.Tree] == nil {
			writeJSON(w, 422, map[string]any{"message": "Tree SHA does not exist"})
			return
		}
		c := ghCommit{SHA: newID("", 20), Tree: b.Tree, Message: b.Message, Parents: b.Parents}
		repo.Commits[c.SHA] = c
		writeJSON(w, 201, map[string]any{"sha": c.SHA, "html_url": "https://github.com/" + full + "/commit/" + c.SHA})
	case len(q) >= 3 && q[0] == "git" && q[1] == "refs" && m == "PATCH":
		var b struct {
			SHA string `json:"sha"`
		}
		_ = readJSON(r, &b)
		c, ok := repo.Commits[b.SHA]
		if !ok {
			writeJSON(w, 422, map[string]any{"message": "Object does not exist"})
			return
		}
		prev := repo.Trees[repo.Commits[repo.Head].Tree]
		repo.Head = b.SHA
		now := s.now().UTC()
		files := repo.Trees[c.Tree]
		if _, has := files[".github/workflows/deploy.yml"]; has && workerChanged(prev, files) {
			repo.Runs = append([]map[string]any{{"id": now.UnixNano(), "name": "Deploy Worker", "status": "completed", "conclusion": "success",
				"html_url": "https://github.com/" + full + "/actions/runs/" + fmt.Sprint(now.Unix()), "head_sha": b.SHA, "created_at": now.Format(time.RFC3339), "event": "push"}}, repo.Runs...)
		}
		writeJSON(w, 200, map[string]any{"ref": "refs/heads/" + repo.Branch, "object": map[string]any{"sha": b.SHA}})
	case eq(q, "actions", "secrets", "public-key"), len(q) == 5 && q[0] == "environments" && q[2] == "secrets" && q[3] == "public-key":
		writeJSON(w, 200, map[string]any{"key_id": repo.KeyID, "key": base64.StdEncoding.EncodeToString(repo.PubKey[:])})
	case eq(q, "actions", "secrets") && m == "GET":
		var out []any
		for _, n := range sortedKeys(repo.Secrets) {
			out = append(out, map[string]any{"name": n, "created_at": repo.Secrets[n].Format(time.RFC3339)})
		}
		writeJSON(w, 200, map[string]any{"total_count": len(out), "secrets": orAny(out)})
	case len(q) == 3 && q[0] == "actions" && q[1] == "secrets", len(q) == 4 && q[0] == "environments" && q[2] == "secrets":
		name := q[len(q)-1]
		switch m {
		case "PUT":
			var b struct {
				EncryptedValue string `json:"encrypted_value"`
				KeyID          string `json:"key_id"`
			}
			_ = readJSON(r, &b)
			raw, err := base64.StdEncoding.DecodeString(b.EncryptedValue)
			if err != nil || len(raw) < box.AnonymousOverhead || b.KeyID != repo.KeyID {
				writeJSON(w, 422, map[string]any{"message": "Bad request: encrypted_value must be sealed with the repository public key"})
				return
			}
			_, existed := repo.Secrets[name]
			repo.Secrets[name] = s.now().UTC()
			if existed {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(201)
			}
		case "GET":
			if t, ok := repo.Secrets[name]; ok {
				writeJSON(w, 200, map[string]any{"name": name, "created_at": t.Format(time.RFC3339)})
			} else {
				ghErr(w, 404, "Not Found")
			}
		case "DELETE":
			delete(repo.Secrets, name)
			w.WriteHeader(204)
		}
	case eq(q, "actions", "variables") && m == "POST":
		var b struct{ Name, Value string }
		_ = readJSON(r, &b)
		if _, ok := repo.Variables[b.Name]; ok {
			writeJSON(w, 409, map[string]any{"message": "Already exists"})
			return
		}
		repo.Variables[b.Name] = b.Value
		w.WriteHeader(201)
	case len(q) == 3 && q[0] == "actions" && q[1] == "variables":
		switch m {
		case "GET":
			if v, ok := repo.Variables[q[2]]; ok {
				writeJSON(w, 200, map[string]any{"name": q[2], "value": v})
			} else {
				ghErr(w, 404, "Not Found")
			}
		case "PATCH":
			var b struct{ Name, Value string }
			_ = readJSON(r, &b)
			repo.Variables[q[2]] = b.Value
			w.WriteHeader(204)
		case "DELETE":
			delete(repo.Variables, q[2])
			w.WriteHeader(204)
		}
	case len(q) == 2 && q[0] == "environments":
		if m == "PUT" {
			repo.Envs[q[1]] = true
		}
		if !repo.Envs[q[1]] {
			ghErr(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"name": q[1]})
	case eq(q, "actions", "runs"):
		writeJSON(w, 200, map[string]any{"total_count": len(repo.Runs), "workflow_runs": orAnyM(repo.Runs)})
	case len(q) == 4 && q[0] == "actions" && q[1] == "workflows" && q[3] == "dispatches":
		now := s.now().UTC()
		repo.Runs = append([]map[string]any{{"id": now.UnixNano(), "name": "Deploy Worker", "status": "completed", "conclusion": "success", "html_url": "https://github.com/" + full + "/actions",
			"head_sha": repo.Head, "created_at": now.Format(time.RFC3339), "event": "workflow_dispatch"}}, repo.Runs...)
		w.WriteHeader(204)
	default:
		ghErr(w, 404, "Not Found")
	}
}

func workerChanged(prev, next map[string]string) bool {
	for k, v := range next {
		if strings.HasPrefix(k, "worker/") && prev[k] != v {
			return true
		}
	}
	return false
}

func orAnyM(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

func (s *Server) ghFailDeploy(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range sortedKeys(s.gh.repos) {
		if target != "" && n != target {
			continue
		}
		repo := s.gh.repos[n]
		now := s.now().UTC()
		repo.Runs = append([]map[string]any{{"id": now.UnixNano(), "name": "Deploy Worker", "status": "completed", "conclusion": "failure",
			"html_url": "https://github.com/" + n + "/actions", "head_sha": repo.Head, "created_at": now.Format(time.RFC3339), "event": "push"}}, repo.Runs...)
		return "The latest deploy of " + n + " failed", nil
	}
	return "", fmt.Errorf("no repository")
}
