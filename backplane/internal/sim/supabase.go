package sim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type sbKey struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	APIKey string `json:"api_key"`
	Prefix string `json:"prefix"`
}

type sbColumn struct {
	Name    string
	Default string
}

type sbTable struct {
	Name     string
	RLS      bool
	Columns  []sbColumn
	Unique   [][]string
	Policies map[string]string // name -> definition
	Rows     []map[string]any
}

type sbUser struct {
	ID, Email, Password string
	Meta                map[string]any
}

type sbProject struct {
	ID, Ref, Name, OrgSlug, Region, Status string
	Created                                time.Time
	Keys                                   []sbKey
	Migrations                             []map[string]string
	Tables                                 map[string]*sbTable
	Funcs                                  map[string]bool
	Auth                                   map[string]any
	Buckets                                map[string]map[string]any
	Secrets                                map[string]string
	Users                                  map[string]*sbUser
	Tokens                                 map[string]string
	polls                                  int
}

type sbState struct {
	orgs     []map[string]string
	projects map[string]*sbProject
	// StartPolls is how many status reads a new project reports COMING_UP.
	StartPolls int
}

func newSB() *sbState {
	return &sbState{orgs: []map[string]string{{"id": "org_practice", "slug": "practice-org", "name": "Practice Organization"}}, projects: map[string]*sbProject{}, StartPolls: 2}
}

func jwtLike() string { return "eyJhbGciOiJIUzI1NiJ9." + newID("", 24) + "." + newID("", 20) }

func (s *Server) sbProjectLocked(ref string) *sbProject { return s.sb.projects[ref] }

func (s *Server) supabaseAPI(w http.ResponseWriter, r *http.Request, rest string) {
	if !s.auth(r, "supabase") {
		writeJSON(w, 401, map[string]string{"message": "Unauthorized"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := segs(rest)
	m := r.Method
	switch {
	case eq(p, "v1", "organizations") && m == "GET":
		writeJSON(w, 200, s.sb.orgs)
		return
	case eq(p, "v1", "projects") && m == "GET":
		var out []map[string]any
		for _, ref := range sortedKeys(s.sb.projects) {
			out = append(out, s.sbProjectJSON(s.sb.projects[ref]))
		}
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
		return
	case eq(p, "v1", "projects") && m == "POST":
		var b struct {
			Name            string         `json:"name"`
			OrgSlug         string         `json:"organization_slug"`
			DBPass          string         `json:"db_pass"`
			RegionSelection map[string]any `json:"region_selection"`
		}
		_ = readJSON(r, &b)
		if b.Name == "" || b.OrgSlug == "" || len(b.DBPass) < 8 {
			writeJSON(w, 400, map[string]string{"message": "name, organization_slug and db_pass are required"})
			return
		}
		active := 0
		for _, pr := range s.sb.projects {
			if pr.Status != "INACTIVE" && pr.Status != "REMOVED" {
				active++
			}
		}
		if active >= 2 {
			writeJSON(w, 402, map[string]string{"message": "The following organization members have reached their maximum limits for the number of active free projects (2)."})
			return
		}
		ref := strings.ToLower(newID("", 10))[:20]
		pr := &sbProject{ID: ref, Ref: ref, Name: b.Name, OrgSlug: b.OrgSlug, Region: fmt.Sprint(b.RegionSelection["code"]), Status: "COMING_UP", Created: s.now().UTC(),
			Tables: map[string]*sbTable{}, Funcs: map[string]bool{}, Auth: map[string]any{"site_url": "http://localhost:3000", "external_email_enabled": true, "mailer_autoconfirm": false, "password_min_length": 6},
			Buckets: map[string]map[string]any{}, Secrets: map[string]string{}, Users: map[string]*sbUser{}, Tokens: map[string]string{}, polls: s.sb.StartPolls}
		pr.Keys = []sbKey{
			{ID: "anon", Name: "anon", Type: "legacy", APIKey: jwtLike()},
			{ID: "service_role", Name: "service_role", Type: "legacy", APIKey: jwtLike()},
			{ID: newID("", 8), Name: "default", Type: "publishable", APIKey: "sb_publishable_" + newID("", 16), Prefix: "sb_publishable_"},
		}
		s.sb.projects[ref] = pr
		writeJSON(w, 201, s.sbProjectJSON(pr))
		return
	}
	if len(p) >= 3 && p[0] == "v1" && p[1] == "projects" {
		pr := s.sb.projects[p[2]]
		if pr == nil {
			writeJSON(w, 404, map[string]string{"message": "Project not found"})
			return
		}
		s.sbProjectRoute(w, r, pr, p[3:])
		return
	}
	writeJSON(w, 404, map[string]string{"message": "Not found"})
}

func (s *Server) sbProjectJSON(pr *sbProject) map[string]any {
	return map[string]any{"id": pr.ID, "ref": pr.Ref, "name": pr.Name, "organization_id": "org_practice", "organization_slug": pr.OrgSlug,
		"region": pr.Region, "status": pr.Status, "created_at": pr.Created.Format(time.RFC3339),
		"database": map[string]any{"host": "db." + pr.Ref + ".supabase.co", "version": "17.4", "postgres_engine": "17", "release_channel": "ga"}}
}

func (s *Server) sbProjectRoute(w http.ResponseWriter, r *http.Request, pr *sbProject, rest []string) {
	m := r.Method
	if len(rest) == 0 {
		switch m {
		case "GET":
			if pr.Status == "COMING_UP" {
				if pr.polls <= 0 {
					pr.Status = "ACTIVE_HEALTHY"
				} else {
					pr.polls--
				}
			}
			writeJSON(w, 200, s.sbProjectJSON(pr))
		case "DELETE":
			delete(s.sb.projects, pr.Ref)
			writeJSON(w, 200, map[string]any{"id": pr.ID, "ref": pr.Ref, "name": pr.Name})
		}
		return
	}
	if pr.Status == "INACTIVE" && rest[0] != "restore" {
		writeJSON(w, 400, map[string]string{"message": "Project is paused. Restore it to continue."})
		return
	}
	switch rest[0] {
	case "restore":
		pr.Status, pr.polls = "COMING_UP", 1
		writeJSON(w, 200, map[string]any{})
	case "pause":
		pr.Status = "INACTIVE"
		writeJSON(w, 200, map[string]any{})
	case "health":
		var out []map[string]any
		for _, svc := range strings.Split(r.URL.Query().Get("services"), ",") {
			out = append(out, map[string]any{"name": svc, "healthy": pr.Status == "ACTIVE_HEALTHY", "status": map[bool]string{true: "ACTIVE_HEALTHY", false: "COMING_UP"}[pr.Status == "ACTIVE_HEALTHY"]})
		}
		writeJSON(w, 200, out)
	case "api-keys":
		if m == "POST" {
			var b struct{ Type, Name, Description string }
			_ = readJSON(r, &b)
			if !regexp.MustCompile(`^[a-z_][a-z0-9_]+$`).MatchString(b.Name) || len(b.Name) < 4 {
				writeJSON(w, 400, map[string]string{"message": "name must match ^[a-z_][a-z0-9_]+$"})
				return
			}
			prefix := "sb_secret_"
			if b.Type == "publishable" {
				prefix = "sb_publishable_"
			}
			k := sbKey{ID: newID("", 8), Name: b.Name, Type: b.Type, APIKey: prefix + newID("", 16), Prefix: prefix}
			pr.Keys = append(pr.Keys, k)
			out := k
			if r.URL.Query().Get("reveal") != "true" {
				out.APIKey = prefix + "··········"
			}
			writeJSON(w, 201, out)
			return
		}
		if len(rest) == 2 && m == "DELETE" {
			for i, k := range pr.Keys {
				if k.ID == rest[1] {
					pr.Keys = append(pr.Keys[:i], pr.Keys[i+1:]...)
					writeJSON(w, 200, k)
					return
				}
			}
			writeJSON(w, 404, map[string]string{"message": "API key not found"})
			return
		}
		reveal := r.URL.Query().Get("reveal") == "true"
		out := make([]sbKey, len(pr.Keys))
		for i, k := range pr.Keys {
			out[i] = k
			if !reveal && k.Type == "secret" {
				out[i].APIKey = k.Prefix + "··········"
			}
		}
		writeJSON(w, 200, out)
	case "database":
		if len(rest) >= 2 && rest[1] == "migrations" {
			if m == "GET" {
				out := pr.Migrations
				if out == nil {
					out = []map[string]string{}
				}
				writeJSON(w, 200, out)
				return
			}
			var b struct{ Query, Name string }
			_ = readJSON(r, &b)
			if err := s.sbExec(pr, b.Query); err != nil {
				writeJSON(w, 400, map[string]string{"message": "Failed to run sql query: " + err.Error()})
				return
			}
			pr.Migrations = append(pr.Migrations, map[string]string{"version": s.now().UTC().Format("20060102150405"), "name": b.Name})
			writeJSON(w, 200, map[string]any{})
			return
		}
		if len(rest) >= 2 && rest[1] == "query" {
			var b struct{ Query string }
			_ = readJSON(r, &b)
			rows, err := s.sbQuery(pr, b.Query, len(rest) == 3)
			if err != nil {
				writeJSON(w, 400, map[string]string{"message": err.Error()})
				return
			}
			writeJSON(w, 201, rows)
			return
		}
	case "config":
		if len(rest) == 2 && rest[1] == "auth" {
			if m == "PATCH" {
				var b map[string]any
				_ = readJSON(r, &b)
				for k, v := range b {
					if k == "smtp_pass" {
						pr.Auth["smtp_pass_set"] = true
						continue
					}
					pr.Auth[k] = v
				}
			}
			out := map[string]any{}
			for k, v := range pr.Auth {
				out[k] = v
			}
			writeJSON(w, 200, out)
			return
		}
	case "storage":
		var out []map[string]any
		for _, id := range sortedKeys(pr.Buckets) {
			out = append(out, pr.Buckets[id])
		}
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	case "secrets":
		switch m {
		case "GET":
			var out []map[string]string
			for _, k := range sortedKeys(pr.Secrets) {
				out = append(out, map[string]string{"name": k, "value": "··········"})
			}
			if out == nil {
				out = []map[string]string{}
			}
			writeJSON(w, 200, out)
		case "POST":
			var b []struct{ Name, Value string }
			_ = readJSON(r, &b)
			for _, x := range b {
				pr.Secrets[x.Name] = x.Value
			}
			writeJSON(w, 201, map[string]any{})
		}
	case "functions":
		writeJSON(w, 200, []any{})
	default:
		writeJSON(w, 404, map[string]string{"message": "Not found"})
	}
}

// ---- a very small SQL model: enough to run generated schemas ----

var (
	reCreateTable = regexp.MustCompile(`(?is)create table if not exists public\.(\w+)\s*\((.*?)\);`)
	reAddColumn   = regexp.MustCompile(`(?i)alter table public\.(\w+) add column if not exists (\w+)([^;]*);`)
	reEnableRLS   = regexp.MustCompile(`(?i)alter table public\.(\w+) enable row level security`)
	reDisableRLS  = regexp.MustCompile(`(?i)alter table public\.(\w+) disable row level security`)
	reCreatePol   = regexp.MustCompile(`(?is)create policy "([^"]+)" on public\.(\w+)(.*?);`)
	reDropPol     = regexp.MustCompile(`(?i)drop policy if exists "([^"]+)" on public\.(\w+)`)
	reFunc        = regexp.MustCompile(`(?i)create or replace function public\.(\w+)`)
)

func splitTopLevel(body string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, ch := range body {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, body[start:])
	return parts
}

func (s *Server) sbExec(pr *sbProject, sql string) error {
	for _, m := range reCreateTable.FindAllStringSubmatch(sql, -1) {
		name := m[1]
		if _, ok := pr.Tables[name]; ok {
			continue
		}
		t := &sbTable{Name: name, Policies: map[string]string{}}
		for _, raw := range splitTopLevel(m[2]) {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "unique") {
				inner := line[strings.Index(line, "(")+1 : strings.LastIndex(line, ")")]
				var cols []string
				for _, c := range strings.Split(inner, ",") {
					cols = append(cols, strings.TrimSpace(c))
				}
				t.Unique = append(t.Unique, cols)
				continue
			}
			if strings.HasPrefix(lower, "primary key") || strings.HasPrefix(lower, "constraint") || strings.HasPrefix(lower, "check") {
				continue
			}
			f := strings.Fields(line)
			col := sbColumn{Name: f[0]}
			if i := strings.Index(lower, " default "); i >= 0 {
				d := line[i+9:]
				for _, stop := range []string{" references ", " check ", " not null", " unique", " primary key"} {
					if j := strings.Index(strings.ToLower(d), stop); j >= 0 {
						d = d[:j]
					}
				}
				col.Default = strings.TrimSpace(d)
			}
			if strings.Contains(lower, " unique") || strings.Contains(lower, "primary key") {
				t.Unique = append(t.Unique, []string{f[0]})
			}
			t.Columns = append(t.Columns, col)
		}
		pr.Tables[name] = t
	}
	for _, m := range reAddColumn.FindAllStringSubmatch(sql, -1) {
		t := pr.Tables[m[1]]
		if t == nil {
			continue
		}
		exists := false
		for _, c := range t.Columns {
			if c.Name == m[2] {
				exists = true
			}
		}
		if !exists {
			col := sbColumn{Name: m[2]}
			if i := strings.Index(strings.ToLower(m[3]), " default "); i >= 0 {
				col.Default = strings.TrimSpace(m[3][i+9:])
			}
			t.Columns = append(t.Columns, col)
		}
	}
	for _, m := range reEnableRLS.FindAllStringSubmatch(sql, -1) {
		if t := pr.Tables[m[1]]; t != nil {
			t.RLS = true
		}
	}
	for _, m := range reDisableRLS.FindAllStringSubmatch(sql, -1) {
		if t := pr.Tables[m[1]]; t != nil {
			t.RLS = false
		}
	}
	for _, m := range reDropPol.FindAllStringSubmatch(sql, -1) {
		if t := pr.Tables[m[2]]; t != nil {
			delete(t.Policies, m[1])
		}
	}
	for _, m := range reCreatePol.FindAllStringSubmatch(sql, -1) {
		if t := pr.Tables[m[2]]; t != nil {
			t.Policies[m[1]] = strings.ToLower(m[3])
		}
	}
	for _, m := range reFunc.FindAllStringSubmatch(sql, -1) {
		pr.Funcs[m[1]] = true
	}
	return nil
}

func (s *Server) sbQuery(pr *sbProject, q string, readOnly bool) ([]map[string]any, error) {
	lq := strings.ToLower(q)
	switch {
	case strings.Contains(lq, "relrowsecurity"):
		var out []map[string]any
		for _, n := range sortedKeys(pr.Tables) {
			out = append(out, map[string]any{"name": n, "rls": pr.Tables[n].RLS})
		}
		return orEmptyRows(out), nil
	case strings.Contains(lq, "from pg_policies"):
		n := 0
		for _, t := range pr.Tables {
			n += len(t.Policies)
		}
		return []map[string]any{{"n": n}}, nil
	case strings.Contains(lq, "information_schema.columns"):
		var out []map[string]any
		for _, n := range sortedKeys(pr.Tables) {
			for _, c := range pr.Tables[n].Columns {
				out = append(out, map[string]any{"table_name": n, "column_name": c.Name})
			}
		}
		return orEmptyRows(out), nil
	}
	if readOnly {
		return nil, fmt.Errorf("cannot execute %s in a read-only transaction", strings.Fields(q)[0])
	}
	return []map[string]any{}, s.sbExec(pr, q)
}

func orEmptyRows(v []map[string]any) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	return v
}

// ---- project-level APIs: PostgREST, Auth, Storage ----

type sbRole struct {
	service bool
	uid     string
	anon    bool
}

func (s *Server) sbRoleFor(pr *sbProject, r *http.Request) (sbRole, bool) {
	key := r.Header.Get("apikey")
	var k *sbKey
	for i := range pr.Keys {
		if pr.Keys[i].APIKey == key {
			k = &pr.Keys[i]
		}
	}
	if k == nil {
		return sbRole{}, false
	}
	if k.Type == "secret" || k.Name == "service_role" {
		return sbRole{service: true}, true
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if uid, ok := pr.Tokens[tok]; ok {
		return sbRole{uid: uid}, true
	}
	return sbRole{anon: true}, true
}

func (s *Server) supabaseProject(w http.ResponseWriter, r *http.Request, rest string) {
	p := segs(rest)
	if len(p) < 2 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pr := s.sb.projects[p[0]]
	if pr == nil {
		http.Error(w, "project not found", 404)
		return
	}
	if pr.Status != "ACTIVE_HEALTHY" {
		writeJSON(w, 503, map[string]string{"message": "Project is not available (" + pr.Status + ")"})
		return
	}
	role, ok := s.sbRoleFor(pr, r)
	switch {
	case p[1] == "auth" && len(p) >= 3:
		s.sbAuth(w, r, pr, role, ok, p[3:])
	case !ok:
		writeJSON(w, 401, map[string]string{"message": "Invalid API key", "hint": "Double check your Supabase `anon` or `service_role` API key."})
	case p[1] == "rest" && len(p) >= 4:
		if p[3] == "rpc" && len(p) == 5 {
			s.sbRPC(w, r, pr, role, p[4])
			return
		}
		s.sbREST(w, r, pr, role, p[3])
	case p[1] == "storage" && len(p) >= 4 && p[3] == "bucket":
		if !role.service {
			writeJSON(w, 403, map[string]string{"message": "new row violates row-level security policy"})
			return
		}
		id := ""
		if len(p) > 4 {
			id = p[4]
		}
		switch r.Method {
		case "GET":
			if b, ok := pr.Buckets[id]; ok {
				writeJSON(w, 200, b)
			} else {
				writeJSON(w, 404, map[string]string{"statusCode": "404", "error": "Bucket not found", "message": "Bucket not found"})
			}
		case "POST", "PUT":
			var b map[string]any
			_ = readJSON(r, &b)
			bid := fmt.Sprint(b["id"])
			if id != "" {
				bid = id
			}
			if _, exists := pr.Buckets[bid]; exists && r.Method == "POST" {
				writeJSON(w, 409, map[string]string{"statusCode": "409", "error": "Duplicate", "message": "The resource already exists"})
				return
			}
			pr.Buckets[bid] = map[string]any{"id": bid, "name": bid, "public": b["public"] == true}
			writeJSON(w, 200, map[string]any{"name": bid})
		case "DELETE":
			delete(pr.Buckets, id)
			writeJSON(w, 200, map[string]any{"message": "Successfully deleted"})
		}
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) sbAuth(w http.ResponseWriter, r *http.Request, pr *sbProject, role sbRole, keyOK bool, rest []string) {
	if !keyOK {
		writeJSON(w, 401, map[string]string{"message": "Invalid API key"})
		return
	}
	switch {
	case eq(rest, "admin", "users") && r.Method == "POST":
		if !role.service {
			writeJSON(w, 403, map[string]any{"code": 403, "error_code": "not_admin", "msg": "User not allowed"})
			return
		}
		var b struct {
			Email    string         `json:"email"`
			Password string         `json:"password"`
			Meta     map[string]any `json:"user_metadata"`
		}
		_ = readJSON(r, &b)
		for _, u := range pr.Users {
			if strings.EqualFold(u.Email, b.Email) {
				writeJSON(w, 422, map[string]any{"code": 422, "error_code": "email_exists", "msg": "A user with this email address has already been registered"})
				return
			}
		}
		u := &sbUser{ID: uuid(), Email: b.Email, Password: b.Password, Meta: b.Meta}
		pr.Users[u.ID] = u
		writeJSON(w, 200, map[string]any{"id": u.ID, "email": u.Email, "aud": "authenticated", "role": "authenticated"})
	case len(rest) == 3 && rest[0] == "admin" && rest[1] == "users" && r.Method == "DELETE":
		if !role.service {
			writeJSON(w, 403, map[string]any{"msg": "User not allowed"})
			return
		}
		id := rest[2]
		if _, ok := pr.Users[id]; !ok {
			writeJSON(w, 404, map[string]any{"code": 404, "error_code": "user_not_found", "msg": "User not found"})
			return
		}
		delete(pr.Users, id)
		for tok, uid := range pr.Tokens {
			if uid == id {
				delete(pr.Tokens, tok)
			}
		}
		for _, t := range pr.Tables { // on delete cascade
			var keep []map[string]any
			for _, row := range t.Rows {
				if fmt.Sprint(row["owner_id"]) != id {
					keep = append(keep, row)
				}
			}
			t.Rows = keep
		}
		writeJSON(w, 200, map[string]any{})
	case eq(rest, "token") && r.Method == "POST":
		var b struct{ Email, Password string }
		_ = readJSON(r, &b)
		for _, u := range pr.Users {
			if strings.EqualFold(u.Email, b.Email) && u.Password == b.Password {
				tok := jwtLike()
				pr.Tokens[tok] = u.ID
				writeJSON(w, 200, map[string]any{"access_token": tok, "token_type": "bearer", "expires_in": 3600, "user": map[string]any{"id": u.ID, "email": u.Email}})
				return
			}
		}
		writeJSON(w, 400, map[string]any{"code": 400, "error_code": "invalid_credentials", "msg": "Invalid login credentials"})
	case eq(rest, "user") && r.Method == "GET":
		if role.uid == "" {
			writeJSON(w, 401, map[string]any{"code": 401, "error_code": "no_authorization", "msg": "This endpoint requires a valid Bearer token"})
			return
		}
		u := pr.Users[role.uid]
		writeJSON(w, 200, map[string]any{"id": u.ID, "email": u.Email, "aud": "authenticated"})
	default:
		writeJSON(w, 404, map[string]any{"msg": "Not found"})
	}
}

func uuid() string {
	h := newID("", 16)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// filter is one PostgREST condition.
type filter struct{ col, op, val string }

func parseFilters(r *http.Request) []filter {
	var out []filter
	for k, vs := range r.URL.Query() {
		switch k {
		case "select", "order", "limit", "on_conflict", "offset":
			continue
		}
		for _, v := range vs {
			op, val, _ := strings.Cut(v, ".")
			out = append(out, filter{k, op, val})
		}
	}
	return out
}

func cmp(a any, b string) int {
	af, aerr := strconv.ParseFloat(fmt.Sprint(a), 64)
	bf, berr := strconv.ParseFloat(b, 64)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	}
	return strings.Compare(fmt.Sprint(a), b)
}

func rowMatches(row map[string]any, fs []filter) bool {
	for _, f := range fs {
		v, present := row[f.col]
		sv := fmt.Sprint(v)
		if !present || v == nil {
			sv = ""
		}
		switch f.op {
		case "eq":
			if sv != f.val {
				return false
			}
		case "neq":
			if sv == f.val {
				return false
			}
		case "is":
			if f.val == "null" && present && v != nil {
				return false
			}
		case "lt":
			if cmp(v, f.val) >= 0 {
				return false
			}
		case "lte":
			if cmp(v, f.val) > 0 {
				return false
			}
		case "gt":
			if cmp(v, f.val) <= 0 {
				return false
			}
		case "gte":
			if cmp(v, f.val) < 0 {
				return false
			}
		case "ilike":
			if !strings.EqualFold(sv, strings.ReplaceAll(f.val, "*", "")) {
				return false
			}
		case "in":
			found := false
			for _, x := range strings.Split(strings.Trim(f.val, "()"), ",") {
				if x == sv {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// visible applies row-level security for non-service roles.
func (t *sbTable) visible(role sbRole, row map[string]any, cmd string) bool {
	if role.service || !t.RLS {
		return true
	}
	for _, def := range t.Policies {
		forCmd := strings.Contains(def, "for "+cmd) || strings.Contains(def, "for all")
		if !forCmd {
			continue
		}
		if role.anon {
			if strings.Contains(def, "to anon") && strings.Contains(def, "using (true)") {
				return true
			}
			continue
		}
		if strings.Contains(def, "using (true)") {
			return true
		}
		if strings.Contains(def, "owner_id = (select auth.uid())") && fmt.Sprint(row["owner_id"]) == role.uid {
			return true
		}
		if strings.Contains(def, "user_id = (select auth.uid())") && fmt.Sprint(row["user_id"]) == role.uid {
			return true
		}
	}
	return false
}

func (t *sbTable) canInsert(role sbRole) bool {
	if role.service || !t.RLS {
		return true
	}
	if role.anon {
		return false
	}
	for _, def := range t.Policies {
		if strings.Contains(def, "for insert") {
			return true
		}
	}
	return false
}

func (t *sbTable) defaults(role sbRole, now time.Time) map[string]any {
	out := map[string]any{}
	for _, c := range t.Columns {
		d := strings.ToLower(c.Default)
		switch {
		case d == "":
		case strings.HasPrefix(d, "gen_random_uuid"):
			out[c.Name] = uuid()
		case strings.HasPrefix(d, "now()"):
			out[c.Name] = now.UTC().Format(time.RFC3339Nano)
		case strings.HasPrefix(d, "auth.uid()"):
			if role.uid != "" {
				out[c.Name] = role.uid
			}
		case d == "true" || d == "false":
			out[c.Name] = d == "true"
		case strings.HasPrefix(d, "'{}'"):
			out[c.Name] = map[string]any{}
		case strings.HasPrefix(d, "'[]'"):
			out[c.Name] = []any{}
		case strings.HasPrefix(d, "'"):
			out[c.Name] = strings.Trim(strings.SplitN(c.Default, "::", 2)[0], "'")
		default:
			if n, err := strconv.ParseFloat(d, 64); err == nil {
				out[c.Name] = n
			}
		}
	}
	return out
}

func (s *Server) sbREST(w http.ResponseWriter, r *http.Request, pr *sbProject, role sbRole, table string) {
	t := pr.Tables[table]
	if t == nil {
		writeJSON(w, 404, map[string]any{"code": "PGRST205", "message": "Could not find the table 'public." + table + "' in the schema cache"})
		return
	}
	prefer := r.Header.Get("Prefer")
	fs := parseFilters(r)
	switch r.Method {
	case "GET":
		var rows []map[string]any
		for _, row := range t.Rows {
			if rowMatches(row, fs) && t.visible(role, row, "select") {
				rows = append(rows, s.project(pr, row, r.URL.Query().Get("select")))
			}
		}
		if lim, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && lim < len(rows) {
			rows = rows[:lim]
		}
		writeJSON(w, 200, orEmptyRows(rows))
	case "POST":
		if !t.canInsert(role) {
			status := 403
			if role.anon {
				status = 401
			}
			writeJSON(w, status, map[string]any{"code": "42501", "message": "new row violates row-level security policy for table \"" + table + "\""})
			return
		}
		var body any
		_ = readJSON(r, &body)
		var items []map[string]any
		switch b := body.(type) {
		case []any:
			for _, x := range b {
				if m, ok := x.(map[string]any); ok {
					items = append(items, m)
				}
			}
		case map[string]any:
			items = append(items, b)
		}
		conflictCols := strings.Split(r.URL.Query().Get("on_conflict"), ",")
		var out []map[string]any
		for _, it := range items {
			row := t.defaults(role, s.now())
			for k, v := range it {
				row[k] = v
			}
			if role.uid != "" && t.hasColumn("owner_id") {
				row["owner_id"] = role.uid
			}
			if idx := t.conflict(row, conflictCols); idx >= 0 {
				switch {
				case strings.Contains(prefer, "resolution=ignore-duplicates"):
					continue
				case strings.Contains(prefer, "resolution=merge-duplicates"):
					for k, v := range it {
						t.Rows[idx][k] = v
					}
					out = append(out, t.Rows[idx])
					continue
				default:
					writeJSON(w, 409, map[string]any{"code": "23505", "message": "duplicate key value violates unique constraint \"" + table + "_key\""})
					return
				}
			}
			for _, fk := range t.Columns {
				if fk.Name == "order_id" && pr.Tables["orders"] != nil && row["order_id"] != nil {
					if pr.Tables["orders"].find("id", fmt.Sprint(row["order_id"])) < 0 {
						writeJSON(w, 409, map[string]any{"code": "23503", "message": "insert or update on table \"" + table + "\" violates foreign key constraint"})
						return
					}
				}
			}
			t.Rows = append(t.Rows, row)
			out = append(out, row)
		}
		if strings.Contains(prefer, "return=representation") {
			writeJSON(w, 201, orEmptyRows(out))
			return
		}
		w.WriteHeader(201)
	case "PATCH":
		var body map[string]any
		_ = readJSON(r, &body)
		var out []map[string]any
		for _, row := range t.Rows {
			if rowMatches(row, fs) && t.visible(role, row, "update") {
				for k, v := range body {
					row[k] = v
				}
				out = append(out, row)
			}
		}
		if strings.Contains(prefer, "return=representation") {
			writeJSON(w, 200, orEmptyRows(out))
			return
		}
		w.WriteHeader(204)
	case "DELETE":
		var keep, out []map[string]any
		for _, row := range t.Rows {
			if rowMatches(row, fs) && t.visible(role, row, "delete") {
				out = append(out, row)
				continue
			}
			keep = append(keep, row)
		}
		t.Rows = keep
		// cascade deletes for rows referencing deleted orders
		if table == "orders" {
			ids := map[string]bool{}
			for _, o := range out {
				ids[fmt.Sprint(o["id"])] = true
			}
			for _, ct := range pr.Tables {
				if ct.hasColumn("order_id") {
					var k []map[string]any
					for _, row := range ct.Rows {
						if !ids[fmt.Sprint(row["order_id"])] {
							k = append(k, row)
						}
					}
					ct.Rows = k
				}
			}
		}
		if strings.Contains(prefer, "return=representation") {
			writeJSON(w, 200, orEmptyRows(out))
			return
		}
		w.WriteHeader(204)
	}
}

func (t *sbTable) hasColumn(c string) bool {
	for _, x := range t.Columns {
		if x.Name == c {
			return true
		}
	}
	return false
}

func (t *sbTable) find(col, val string) int {
	for i, row := range t.Rows {
		if fmt.Sprint(row[col]) == val {
			return i
		}
	}
	return -1
}

func (t *sbTable) conflict(row map[string]any, onConflict []string) int {
	sets := t.Unique
	if len(onConflict) > 0 && onConflict[0] != "" {
		sets = [][]string{onConflict}
	}
	for i, existing := range t.Rows {
		for _, cols := range sets {
			same := len(cols) > 0
			for _, c := range cols {
				c = strings.TrimSpace(c)
				if row[c] == nil || fmt.Sprint(existing[c]) != fmt.Sprint(row[c]) {
					same = false
				}
			}
			if same {
				return i
			}
		}
	}
	return -1
}

// project applies a select list, including one level of embedding such as
// "revoked,orders(status)".
func (s *Server) project(pr *sbProject, row map[string]any, sel string) map[string]any {
	if sel == "" || sel == "*" {
		cp := map[string]any{}
		for k, v := range row {
			cp[k] = v
		}
		return cp
	}
	out := map[string]any{}
	for _, part := range splitTopLevel(sel) {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "("); i > 0 {
			rel := part[:i]
			cols := strings.Split(strings.TrimSuffix(part[i+1:], ")"), ",")
			if rt := pr.Tables[rel]; rt != nil {
				fk := strings.TrimSuffix(rel, "s") + "_id"
				if idx := rt.find("id", fmt.Sprint(row[fk])); idx >= 0 {
					emb := map[string]any{}
					for _, c := range cols {
						emb[strings.TrimSpace(c)] = rt.Rows[idx][strings.TrimSpace(c)]
					}
					out[rel] = emb
				} else {
					out[rel] = nil
				}
			}
			continue
		}
		out[part] = row[part]
	}
	return out
}

func (s *Server) sbRPC(w http.ResponseWriter, r *http.Request, pr *sbProject, role sbRole, fn string) {
	if !pr.Funcs[fn] {
		writeJSON(w, 404, map[string]any{"code": "PGRST202", "message": "Could not find the function public." + fn})
		return
	}
	if !role.service {
		writeJSON(w, 401, map[string]any{"code": "42501", "message": "permission denied for function " + fn})
		return
	}
	var b map[string]any
	_ = readJSON(r, &b)
	if fn == "bp_record_download" {
		if t := pr.Tables["downloads"]; t != nil {
			for _, row := range t.Rows {
				if fmt.Sprint(row["stripe_session_id"]) == fmt.Sprint(b["p_session"]) && fmt.Sprint(row["object_key"]) == fmt.Sprint(b["p_key"]) {
					n, _ := strconv.ParseFloat(fmt.Sprint(row["download_count"]), 64)
					row["download_count"] = n + 1
					row["last_downloaded_at"] = s.now().UTC().Format(time.RFC3339)
				}
			}
		}
	}
	w.WriteHeader(204)
}

// ---- fault helpers ----

func (s *Server) sbPause(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range sortedKeys(s.sb.projects) {
		if target != "" && ref != target {
			continue
		}
		s.sb.projects[ref].Status = "INACTIVE"
		return "Paused Supabase project " + ref + " (as the Free plan does after a week without activity)", nil
	}
	return "", fmt.Errorf("no project")
}

func (s *Server) sbDisableRLS(target, table string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range sortedKeys(s.sb.projects) {
		if target != "" && ref != target {
			continue
		}
		pr := s.sb.projects[ref]
		names := sortedKeys(pr.Tables)
		sort.Strings(names)
		for _, n := range names {
			if table == "" || n == table {
				pr.Tables[n].RLS = false
				return "Disabled row level security on " + n, nil
			}
		}
	}
	return "", fmt.Errorf("no table")
}

// sbRevokeKey deletes a project's secret API key (as if someone revoked it
// in the dashboard).
func (s *Server) sbRevokeKey(target, keyID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range sortedKeys(s.sb.projects) {
		if target != "" && ref != target {
			continue
		}
		pr := s.sb.projects[ref]
		for i, k := range pr.Keys {
			if k.Type == "secret" && (keyID == "" || k.ID == keyID) {
				pr.Keys = append(pr.Keys[:i], pr.Keys[i+1:]...)
				return "Revoked Supabase secret key " + k.Name + " in the dashboard", nil
			}
		}
	}
	return "", fmt.Errorf("no secret key")
}

// sbData exposes rows for the simulated Worker.
func (s *Server) sbLookupKeyLocked(key string) (*sbProject, *sbKey) {
	for _, pr := range s.sb.projects {
		for i := range pr.Keys {
			if pr.Keys[i].APIKey == key {
				return pr, &pr.Keys[i]
			}
		}
	}
	return nil, nil
}

var _ = json.Marshal
