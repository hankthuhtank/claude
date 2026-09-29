package sim

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type cfScript struct {
	Name       string
	Code       string
	Bindings   []map[string]any
	Secrets    map[string]string
	WorkersDev bool
	Crons      []string
	Compat     string
	Created    time.Time
	Modified   time.Time
}

type cfBucket struct {
	Name    string
	Created time.Time
	Objects map[string][]byte
}

type kvVal struct {
	V   string
	Exp time.Time
}

type cfKV struct {
	ID, Title string
	Values    map[string]kvVal
}

type cfD1 struct{ UUID, Name string }

type cfQueue struct{ ID, Name string }

type cfDNS struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
}

type cfZone struct {
	ID, Name string
	Records  map[string]*cfDNS
}

type cfDomain struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	ZoneID   string `json:"zone_id"`
	ZoneName string `json:"zone_name"`
}

type cfState struct {
	accountID, accountName, subdomain string
	scripts                           map[string]*cfScript
	buckets                           map[string]*cfBucket
	kv                                map[string]*cfKV
	d1                                map[string]*cfD1
	queues                            map[string]*cfQueue
	zones                             map[string]*cfZone
	domains                           map[string]*cfDomain
	// AutoZones creates a zone for any domain looked up (practice mode), so
	// people can see Backplane publish DNS records automatically.
	AutoZones bool
}

func newCF() *cfState {
	return &cfState{accountID: "0123456789abcdef0123456789abcdef", accountName: "Practice Account", subdomain: "practice",
		scripts: map[string]*cfScript{}, buckets: map[string]*cfBucket{}, kv: map[string]*cfKV{}, d1: map[string]*cfD1{},
		queues: map[string]*cfQueue{}, zones: map[string]*cfZone{}, domains: map[string]*cfDomain{}, AutoZones: true}
}

// CFAccount returns the simulated Cloudflare account id.
func (s *Server) CFAccount() string { return s.cf.accountID }

// SetWorkersSubdomain changes (or clears) the account subdomain.
func (s *Server) SetWorkersSubdomain(sub string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cf.subdomain = sub
}

// AddZone creates a DNS zone.
func (s *Server) AddZone(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cf.zoneLocked(name)
}

func (c *cfState) zoneLocked(name string) *cfZone {
	if z, ok := c.zones[name]; ok {
		return z
	}
	z := &cfZone{ID: newID("", 16), Name: name, Records: map[string]*cfDNS{}}
	c.zones[name] = z
	return z
}

func cfOK(w http.ResponseWriter, result any) {
	writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result})
}

func cfPaged(w http.ResponseWriter, result any, info map[string]any) {
	writeJSON(w, 200, map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result, "result_info": info})
}

func cfErr(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, map[string]any{"success": false, "errors": []map[string]any{{"code": code, "message": msg}}, "messages": []any{}, "result": nil})
}

func (s *Server) cloudflare(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.auth(r, "cloudflare") {
		cfErr(w, 401, 1000, "Invalid API Token")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cf
	p := segs(rest)
	m := r.Method
	if v, ok := match(p, "user", "tokens", "verify"); ok && m == "GET" {
		_ = v
		cfOK(w, map[string]any{"id": "tok_practice", "status": "active"})
		return
	}
	if v, ok := match(p, "accounts", ":a", "tokens", "verify"); ok && m == "GET" {
		if v["a"] != c.accountID {
			cfErr(w, 403, 10000, "Authentication error")
			return
		}
		cfOK(w, map[string]any{"id": "tok_practice", "status": "active"})
		return
	}
	if len(p) == 1 && p[0] == "accounts" && m == "GET" {
		cfPaged(w, []map[string]any{{"id": c.accountID, "name": c.accountName, "type": "standard"}}, map[string]any{"page": 1, "per_page": 50, "count": 1, "total_count": 1, "total_pages": 1})
		return
	}
	if len(p) >= 2 && p[0] == "accounts" && p[1] != c.accountID {
		cfErr(w, 403, 10000, "Authentication error")
		return
	}
	switch {
	case eq(p, "accounts", "*", "workers", "subdomain"):
		switch m {
		case "GET":
			if c.subdomain == "" {
				cfErr(w, 404, 10007, "This account does not have a workers.dev subdomain.")
				return
			}
			cfOK(w, map[string]any{"subdomain": c.subdomain})
		case "PUT":
			var b struct {
				Subdomain string `json:"subdomain"`
			}
			_ = readJSON(r, &b)
			c.subdomain = b.Subdomain
			cfOK(w, map[string]any{"subdomain": c.subdomain})
		}
		return
	case eq(p, "accounts", "*", "workers", "scripts") && m == "GET":
		var out []map[string]any
		for _, n := range sortedKeys(c.scripts) {
			sc := c.scripts[n]
			out = append(out, map[string]any{"id": n, "created_on": sc.Created.Format(time.RFC3339), "modified_on": sc.Modified.Format(time.RFC3339)})
		}
		cfOK(w, orEmpty(out))
		return
	case len(p) >= 5 && p[2] == "workers" && p[3] == "scripts":
		s.cfScript(w, r, p[4], p[5:])
		return
	case eq(p, "accounts", "*", "workers", "domains"):
		switch m {
		case "GET":
			host := r.URL.Query().Get("hostname")
			var out []*cfDomain
			for _, d := range c.domains {
				if host == "" || d.Hostname == host {
					out = append(out, d)
				}
			}
			cfOK(w, orEmptyD(out))
		case "PUT":
			var b cfDomain
			_ = readJSON(r, &b)
			for _, d := range c.domains {
				if d.Hostname == b.Hostname {
					d.Service = b.Service
					cfOK(w, d)
					return
				}
			}
			b.ID = newID("", 16)
			for _, z := range c.zones {
				if z.ID == b.ZoneID {
					b.ZoneName = z.Name
				}
			}
			c.domains[b.ID] = &b
			cfOK(w, &b)
		}
		return
	case len(p) == 5 && p[2] == "workers" && p[3] == "domains" && m == "DELETE":
		delete(c.domains, p[4])
		cfOK(w, nil)
		return
	case len(p) >= 4 && p[2] == "r2" && p[3] == "buckets":
		s.cfR2(w, r, p[4:])
		return
	case len(p) >= 5 && p[2] == "storage" && p[3] == "kv" && p[4] == "namespaces":
		s.cfKV(w, r, p[5:])
		return
	case len(p) >= 4 && p[2] == "d1" && p[3] == "database":
		s.cfD1(w, r, p[4:])
		return
	case len(p) >= 3 && p[2] == "queues":
		s.cfQueues(w, r, p[3:])
		return
	case len(p) == 1 && p[0] == "zones" && m == "GET":
		name := r.URL.Query().Get("name")
		var out []map[string]any
		if z, ok := c.zones[name]; ok {
			out = append(out, map[string]any{"id": z.ID, "name": z.Name, "status": "active"})
		} else if c.AutoZones && name != "" && strings.Count(name, ".") == 1 {
			z := c.zoneLocked(name)
			out = append(out, map[string]any{"id": z.ID, "name": z.Name, "status": "active"})
		}
		cfOK(w, orEmpty(out))
		return
	case len(p) >= 3 && p[0] == "zones" && p[2] == "dns_records":
		s.cfDNSRoute(w, r, p[1], p[3:])
		return
	}
	cfErr(w, 404, 7003, "Could not route to "+rest+", perhaps your object identifier is invalid?")
}

func eq(p []string, pattern ...string) bool {
	if len(p) != len(pattern) {
		return false
	}
	for i := range pattern {
		if pattern[i] != "*" && pattern[i] != p[i] {
			return false
		}
	}
	return true
}

func orEmpty(v []map[string]any) any {
	if v == nil {
		return []any{}
	}
	return v
}

func orEmptyD(v []*cfDomain) any {
	if v == nil {
		return []any{}
	}
	return v
}

func (s *Server) cfScript(w http.ResponseWriter, r *http.Request, name string, rest []string) {
	c := s.cf
	sc := c.scripts[name]
	m := r.Method
	if len(rest) == 0 {
		switch m {
		case "PUT":
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				cfErr(w, 400, 10021, "Uncaught multipart parse error: "+err.Error())
				return
			}
			var meta map[string]any
			if v := r.MultipartForm.Value["metadata"]; len(v) > 0 {
				_ = json.Unmarshal([]byte(v[0]), &meta)
			} else if fs := r.MultipartForm.File["metadata"]; len(fs) > 0 {
				f, _ := fs[0].Open()
				b, _ := io.ReadAll(f)
				f.Close()
				_ = json.Unmarshal(b, &meta)
			}
			main, _ := meta["main_module"].(string)
			if main == "" {
				cfErr(w, 400, 10021, "No main_module in metadata")
				return
			}
			fs := r.MultipartForm.File[main]
			if len(fs) == 0 {
				cfErr(w, 400, 10021, "main_module "+main+" is not a part of the upload")
				return
			}
			f, _ := fs[0].Open()
			code, _ := io.ReadAll(f)
			f.Close()
			now := s.now().UTC()
			if sc == nil {
				sc = &cfScript{Name: name, Created: now, Secrets: map[string]string{}}
				c.scripts[name] = sc
			}
			keep := map[string]bool{}
			if kb, ok := meta["keep_bindings"].([]any); ok {
				for _, k := range kb {
					keep[fmt.Sprint(k)] = true
				}
			}
			if !keep["secret_text"] {
				sc.Secrets = map[string]string{}
			}
			sc.Code, sc.Modified = string(code), now
			sc.Compat, _ = meta["compatibility_date"].(string)
			sc.Bindings = nil
			if bs, ok := meta["bindings"].([]any); ok {
				for _, b := range bs {
					bm, _ := b.(map[string]any)
					if bm["type"] == "secret_text" {
						sc.Secrets[fmt.Sprint(bm["name"])] = fmt.Sprint(bm["text"])
						continue
					}
					if bm["type"] == "d1" && bm["database_id"] == nil {
						cfErr(w, 400, 10021, "d1 binding requires database_id")
						return
					}
					sc.Bindings = append(sc.Bindings, bm)
				}
			}
			cfOK(w, map[string]any{"id": name, "etag": newID("", 8), "modified_on": now.Format(time.RFC3339), "handlers": []string{"fetch", "scheduled"}})
		case "DELETE":
			if sc == nil {
				cfErr(w, 404, 10007, "This Worker does not exist on your account.")
				return
			}
			delete(c.scripts, name)
			cfOK(w, nil)
		default:
			cfErr(w, 405, 10000, "method not allowed")
		}
		return
	}
	if sc == nil {
		cfErr(w, 404, 10007, "This Worker does not exist on your account.")
		return
	}
	switch rest[0] {
	case "settings":
		bindings := append([]map[string]any{}, sc.Bindings...)
		for _, n := range sortedKeys(sc.Secrets) {
			bindings = append(bindings, map[string]any{"type": "secret_text", "name": n})
		}
		cfOK(w, map[string]any{"bindings": bindings, "compatibility_date": sc.Compat, "observability": map[string]any{"enabled": true}})
	case "secrets":
		switch {
		case len(rest) == 1 && m == "GET":
			var out []map[string]any
			for _, n := range sortedKeys(sc.Secrets) {
				out = append(out, map[string]any{"name": n, "type": "secret_text"})
			}
			cfOK(w, orEmpty(out))
		case len(rest) == 1 && m == "PUT":
			var b struct{ Name, Text, Type string }
			_ = readJSON(r, &b)
			if b.Name == "" {
				cfErr(w, 400, 10021, "secret name required")
				return
			}
			sc.Secrets[b.Name] = b.Text
			cfOK(w, map[string]any{"name": b.Name, "type": "secret_text"})
		case len(rest) == 2 && m == "DELETE":
			delete(sc.Secrets, rest[1])
			cfOK(w, nil)
		}
	case "subdomain":
		if m == "POST" {
			var b struct {
				Enabled bool `json:"enabled"`
			}
			_ = readJSON(r, &b)
			sc.WorkersDev = b.Enabled
		}
		cfOK(w, map[string]any{"enabled": sc.WorkersDev, "previews_enabled": false})
	case "schedules":
		if m == "PUT" {
			var b []struct {
				Cron string `json:"cron"`
			}
			_ = readJSON(r, &b)
			sc.Crons = nil
			for _, x := range b {
				sc.Crons = append(sc.Crons, x.Cron)
			}
		}
		var out []map[string]any
		for _, cr := range sc.Crons {
			out = append(out, map[string]any{"cron": cr, "created_on": sc.Modified.Format(time.RFC3339)})
		}
		cfOK(w, map[string]any{"schedules": orEmpty(out)})
	default:
		cfErr(w, 404, 7003, "Could not route")
	}
}

func (s *Server) cfR2(w http.ResponseWriter, r *http.Request, rest []string) {
	c := s.cf
	m := r.Method
	if len(rest) == 0 {
		switch m {
		case "GET":
			var out []map[string]any
			for _, n := range sortedKeys(c.buckets) {
				out = append(out, map[string]any{"name": n, "creation_date": c.buckets[n].Created.Format(time.RFC3339)})
			}
			cfOK(w, map[string]any{"buckets": orEmpty(out)})
		case "POST":
			var b struct {
				Name string `json:"name"`
			}
			_ = readJSON(r, &b)
			if _, ok := c.buckets[b.Name]; ok {
				cfErr(w, 409, 10004, "The bucket you tried to create already exists, and you own it.")
				return
			}
			c.buckets[b.Name] = &cfBucket{Name: b.Name, Created: s.now().UTC(), Objects: map[string][]byte{}}
			cfOK(w, map[string]any{"name": b.Name, "creation_date": s.now().UTC().Format(time.RFC3339), "location": "ENAM", "storage_class": "Standard"})
		}
		return
	}
	b := c.buckets[rest[0]]
	if b == nil {
		cfErr(w, 404, 10006, "The specified bucket does not exist.")
		return
	}
	if len(rest) == 1 {
		switch m {
		case "GET":
			cfOK(w, map[string]any{"name": b.Name, "creation_date": b.Created.Format(time.RFC3339), "location": "ENAM", "storage_class": "Standard"})
		case "DELETE":
			if len(b.Objects) > 0 {
				cfErr(w, 409, 10008, "The bucket you tried to delete is not empty.")
				return
			}
			delete(c.buckets, rest[0])
			cfOK(w, nil)
		}
		return
	}
	switch rest[1] {
	case "cors":
		cfOK(w, nil)
	case "objects":
		if len(rest) == 2 && m == "GET" {
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range b.Objects {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var out []map[string]any
			for _, k := range keys {
				out = append(out, map[string]any{"key": k, "size": len(b.Objects[k]), "etag": newID("", 8)})
			}
			cfPaged(w, orEmpty(out), map[string]any{"cursor": "", "is_truncated": false})
			return
		}
		key := strings.Join(rest[2:], "/")
		switch m {
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			b.Objects[key] = body
			cfOK(w, map[string]any{"key": key, "size": fmt.Sprint(len(body)), "etag": newID("", 8), "uploaded": s.now().UTC().Format(time.RFC3339)})
		case "GET":
			obj, ok := b.Objects[key]
			if !ok {
				cfErr(w, 404, 10007, "The specified key does not exist.")
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(200)
			w.Write(obj)
		case "DELETE":
			delete(b.Objects, key)
			cfOK(w, nil)
		}
	}
}

func (s *Server) cfKV(w http.ResponseWriter, r *http.Request, rest []string) {
	c := s.cf
	switch {
	case len(rest) == 0 && r.Method == "GET":
		var out []map[string]any
		for _, id := range sortedKeys(c.kv) {
			out = append(out, map[string]any{"id": id, "title": c.kv[id].Title, "supports_url_encoding": true})
		}
		cfPaged(w, orEmpty(out), map[string]any{"page": 1, "per_page": 100, "total_pages": 1, "count": len(out)})
	case len(rest) == 0 && r.Method == "POST":
		var b struct {
			Title string `json:"title"`
		}
		_ = readJSON(r, &b)
		for _, ns := range c.kv {
			if ns.Title == b.Title {
				cfErr(w, 400, 10014, "A namespace with this account ID and title already exists.")
				return
			}
		}
		ns := &cfKV{ID: newID("", 16), Title: b.Title, Values: map[string]kvVal{}}
		c.kv[ns.ID] = ns
		cfOK(w, map[string]any{"id": ns.ID, "title": ns.Title, "supports_url_encoding": true})
	case len(rest) == 1:
		ns := c.kv[rest[0]]
		if ns == nil {
			cfErr(w, 404, 10013, "namespace not found")
			return
		}
		if r.Method == "DELETE" {
			delete(c.kv, rest[0])
			cfOK(w, nil)
			return
		}
		cfOK(w, map[string]any{"id": ns.ID, "title": ns.Title})
	default:
		cfErr(w, 404, 7003, "Could not route")
	}
}

func (s *Server) cfD1(w http.ResponseWriter, r *http.Request, rest []string) {
	c := s.cf
	switch {
	case len(rest) == 0 && r.Method == "GET":
		name := r.URL.Query().Get("name")
		var out []map[string]any
		for _, id := range sortedKeys(c.d1) {
			if name == "" || c.d1[id].Name == name {
				out = append(out, map[string]any{"uuid": id, "name": c.d1[id].Name, "num_tables": 0})
			}
		}
		cfOK(w, orEmpty(out))
	case len(rest) == 0 && r.Method == "POST":
		var b struct {
			Name string `json:"name"`
		}
		_ = readJSON(r, &b)
		db := &cfD1{UUID: newID("", 16), Name: b.Name}
		c.d1[db.UUID] = db
		cfOK(w, map[string]any{"uuid": db.UUID, "name": db.Name})
	case len(rest) >= 1:
		db := c.d1[rest[0]]
		if db == nil {
			cfErr(w, 404, 7404, "database not found")
			return
		}
		if len(rest) == 2 && rest[1] == "query" {
			cfOK(w, []map[string]any{{"results": []any{}, "success": true, "meta": map[string]any{}}})
			return
		}
		if r.Method == "DELETE" {
			delete(c.d1, rest[0])
			cfOK(w, nil)
			return
		}
		cfOK(w, map[string]any{"uuid": db.UUID, "name": db.Name, "num_tables": 0, "file_size": 12288})
	}
}

func (s *Server) cfQueues(w http.ResponseWriter, r *http.Request, rest []string) {
	c := s.cf
	switch {
	case len(rest) == 0 && r.Method == "GET":
		var out []map[string]any
		for _, id := range sortedKeys(c.queues) {
			out = append(out, map[string]any{"queue_id": id, "queue_name": c.queues[id].Name})
		}
		cfOK(w, orEmpty(out))
	case len(rest) == 0 && r.Method == "POST":
		var b struct {
			QueueName string `json:"queue_name"`
		}
		_ = readJSON(r, &b)
		q := &cfQueue{ID: newID("", 16), Name: b.QueueName}
		c.queues[q.ID] = q
		cfOK(w, map[string]any{"queue_id": q.ID, "queue_name": q.Name})
	case len(rest) == 1 && r.Method == "DELETE":
		delete(c.queues, rest[0])
		cfOK(w, nil)
	}
}

func (s *Server) cfDNSRoute(w http.ResponseWriter, r *http.Request, zoneID string, rest []string) {
	var z *cfZone
	for _, x := range s.cf.zones {
		if x.ID == zoneID {
			z = x
		}
	}
	if z == nil {
		cfErr(w, 404, 7003, "zone not found")
		return
	}
	switch {
	case len(rest) == 0 && r.Method == "GET":
		q := r.URL.Query()
		var out []*cfDNS
		for _, id := range sortedKeys(z.Records) {
			rec := z.Records[id]
			if (q.Get("type") == "" || rec.Type == q.Get("type")) && (q.Get("name") == "" || rec.Name == q.Get("name")) {
				out = append(out, rec)
			}
		}
		if out == nil {
			out = []*cfDNS{}
		}
		cfOK(w, out)
	case len(rest) == 0 && r.Method == "POST":
		var rec cfDNS
		_ = readJSON(r, &rec)
		if !strings.HasSuffix(rec.Name, z.Name) {
			rec.Name = rec.Name + "." + z.Name
		}
		rec.ID = newID("", 16)
		z.Records[rec.ID] = &rec
		cfOK(w, &rec)
	case len(rest) == 1:
		rec := z.Records[rest[0]]
		if rec == nil {
			cfErr(w, 404, 81044, "Record does not exist.")
			return
		}
		switch r.Method {
		case "GET":
			cfOK(w, rec)
		case "PUT", "PATCH":
			var upd cfDNS
			_ = readJSON(r, &upd)
			upd.ID = rec.ID
			z.Records[rec.ID] = &upd
			cfOK(w, &upd)
		case "DELETE":
			delete(z.Records, rec.ID)
			cfOK(w, map[string]any{"id": rec.ID})
		}
	}
}

// DNSHas reports whether a zone has a record (used by simulated Resend verification).
func (s *Server) dnsHasLocked(name, typ, content string) bool {
	for _, z := range s.cf.zones {
		for _, rec := range z.Records {
			if rec.Name == name && rec.Type == typ && strings.Trim(rec.Content, `"`) == strings.Trim(content, `"`) {
				return true
			}
		}
	}
	return false
}

// ---- fault helpers ----

func (s *Server) cfRemoveBinding(script, binding string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.firstScriptLocked(script)
	if sc == nil {
		return "", fmt.Errorf("no worker")
	}
	for i, b := range sc.Bindings {
		if binding == "" || b["name"] == binding {
			n := fmt.Sprint(b["name"])
			sc.Bindings = append(sc.Bindings[:i], sc.Bindings[i+1:]...)
			return "Removed binding " + n + " from " + sc.Name + " outside Backplane", nil
		}
	}
	return "", fmt.Errorf("binding %s not found", binding)
}

func (s *Server) cfRemoveSecret(script, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.firstScriptLocked(script)
	if sc == nil {
		return "", fmt.Errorf("no worker")
	}
	delete(sc.Secrets, name)
	return "Deleted secret " + name + " from " + sc.Name, nil
}

func (s *Server) cfCorruptSecret(script, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.firstScriptLocked(script)
	if sc == nil {
		return "", fmt.Errorf("no worker")
	}
	sc.Secrets[name] = "whsec_" + newID("", 16)
	return "Changed " + name + " on " + sc.Name + " to a wrong value", nil
}

func (s *Server) cfDeleteBucket(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for n := range s.cf.buckets {
		if name == "" || n == name {
			delete(s.cf.buckets, n)
			return "Deleted bucket " + n + " outside Backplane", nil
		}
	}
	return "", fmt.Errorf("no bucket")
}

func (s *Server) cfDeleteWorker(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.firstScriptLocked(name)
	if sc == nil {
		return "", fmt.Errorf("no worker")
	}
	delete(s.cf.scripts, sc.Name)
	return "Deleted Worker " + sc.Name, nil
}

func (s *Server) firstScriptLocked(name string) *cfScript {
	if name != "" {
		return s.cf.scripts[name]
	}
	for _, n := range sortedKeys(s.cf.scripts) {
		return s.cf.scripts[n]
	}
	return nil
}
