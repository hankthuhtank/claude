package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
)

// Placeholders let blueprint props refer to things that only exist later or
// must never be written down:
//
//	{{out:KEY.OUTPUT}}     non-secret output of another resource
//	{{secret:KEY.OUTPUT}}  secret output of another resource (from the vault)
//	{{conn:PROVIDER}}      the connection's main credential
//	{{conn:PROVIDER.FIELD}} another credential field of the connection
//	{{gen:NAME}}           a random secret generated once per environment
//	{{param:NAME}}         a blueprint parameter
//	{{code:PATH}}          a generated code file
//	{{codet:PATH}}         a code template, its non-secret placeholders resolved
//	{{codever:PATH}}       short digest of a code file (the Worker's CODE_VERSION)
//	{{env}} {{project}}    environment and project ids
var placeholder = regexp.MustCompile(`\{\{\s*([a-z]+)(?::([^}]+?))?\s*\}\}`)

// resolveMode controls how secrets appear in the output.
type resolveMode int

const (
	forApply resolveMode = iota // real values, in memory only
	forHash                     // secrets replaced by a digest, safe to persist/compare
)

// Unresolved is returned when a reference points at an output that does not
// exist yet (its resource will be created earlier in the same run).
type Unresolved struct{ Ref string }

func (u *Unresolved) Error() string { return "waiting on " + u.Ref }

type resolver struct {
	e    *Engine
	p    *core.Project
	env  string
	man  *core.Manifest
	mode resolveMode
	// genCreate controls whether missing {{gen:}} secrets are created.
	genCreate bool
	// public forbids secret-bearing placeholders (used inside code templates).
	public bool
	// escape is applied to each substituted value (JSON files need their
	// values escaped so a quote in a business name can't break the file).
	escape func(string) string
}

// RenderPublic resolves the non-secret placeholders in the code template rel
// (for exports). Secret-bearing placeholders are refused, so the output is
// safe to share or commit.
func (e *Engine) RenderPublic(p *core.Project, env, rel, text string) (string, error) {
	man, err := e.Store.LoadManifest(p.ID, env)
	if err != nil {
		return "", err
	}
	r := &resolver{e: e, p: p, env: env, man: man, mode: forApply, public: true, escape: escaperFor(rel)}
	return r.str(text)
}

// escaperFor returns the value escaper for a code template's file type.
func escaperFor(rel string) func(string) string {
	if strings.HasSuffix(rel, ".json") || strings.HasSuffix(rel, ".jsonc") {
		return jsonEscape
	}
	return nil
}

// jsonEscape escapes v for use inside a JSON string literal.
func jsonEscape(v string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	out := strings.TrimSuffix(buf.String(), "\n")
	return out[1 : len(out)-1]
}

func digestOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

func (r *resolver) lookup(kind, arg string) (string, error) {
	if r.public && (kind == "secret" || kind == "gen" || kind == "conn" || kind == "code" || kind == "codet") {
		return "", fmt.Errorf("code templates may not contain %s placeholders", kind)
	}
	switch kind {
	case "env":
		return r.env, nil
	case "envshort":
		return EnvShort(r.env), nil
	case "project":
		return r.p.ID, nil
	case "param":
		return r.p.Blueprint.Param(arg), nil
	case "out":
		key, out, _ := strings.Cut(arg, ".")
		st := r.man.Resources[key]
		if st == nil || st.Status == core.StateDeleted {
			return "", &Unresolved{Ref: arg}
		}
		v := st.Output(out)
		if v == "" && out == "id" {
			v = st.ID
		}
		if v == "" && out == "name" {
			v = st.Name
		}
		if v == "" {
			return "", &Unresolved{Ref: arg}
		}
		return v, nil
	case "secret":
		key, out, _ := strings.Cut(arg, ".")
		v, err := r.e.Vault.GetString(ResourceSecretKey(r.p.ID, r.env, key, out))
		if err != nil || v == "" {
			return "", &Unresolved{Ref: "secret " + arg}
		}
		if r.mode == forHash {
			return digestOf(v), nil
		}
		return v, nil
	case "gen":
		k := GenSecretKey(r.p.ID, r.env, arg)
		v, err := r.e.Vault.GetString(k)
		if err != nil || v == "" {
			if !r.genCreate {
				if r.mode == forHash {
					return "sha256:pending-" + arg, nil
				}
				return "", &Unresolved{Ref: "generated " + arg}
			}
			v = providers.RandomToken(32)
			if err := r.e.Vault.PutString(k, v, "Generated secret "+arg+" ("+r.p.Name+", "+r.env+")"); err != nil {
				return "", err
			}
		}
		if r.mode == forHash {
			return digestOf(v), nil
		}
		return v, nil
	case "conn":
		prov, field, _ := strings.Cut(arg, ".")
		id := r.p.Connections[r.env][prov]
		if id == "" {
			return "", &core.Problem{Title: "No " + providers.DisplayName(prov) + " connection", Provider: prov, Code: "auth",
				Summary: "Connect " + providers.DisplayName(prov) + " for the " + r.env + " environment first."}
		}
		c, err := r.e.Connection(id)
		if err != nil {
			return "", err
		}
		key := c.SecretRef
		if field != "" {
			key = ConnKey(id, field)
		}
		v, err := r.e.Vault.GetString(key)
		if err != nil || v == "" {
			if field != "" {
				// Optional sub-credential (e.g. a restricted worker key):
				// fall back to the main credential.
				v, err = r.e.Vault.GetString(c.SecretRef)
			}
			if err != nil || v == "" {
				return "", &core.Problem{Title: providers.DisplayName(prov) + " credential missing", Provider: prov, Code: "auth", Summary: "Re-enter the credential on the Connections screen."}
			}
		}
		if r.mode == forHash {
			return digestOf(v), nil
		}
		return v, nil
	case "connset":
		prov, field, _ := strings.Cut(arg, ".")
		id := r.p.Connections[r.env][prov]
		if id == "" {
			return "", &Unresolved{Ref: "connection " + prov}
		}
		c, err := r.e.Connection(id)
		if err != nil {
			return "", err
		}
		v := c.Setting(field)
		if v == "" && field == "account_id" {
			v = c.AccountID
		}
		if v == "" {
			return "", &Unresolved{Ref: "setting " + arg}
		}
		return v, nil
	case "codet":
		// A code template: placeholders inside the file are resolved, but only
		// non-secret kinds are allowed so secrets can never reach a repository.
		raw, err := r.e.ReadCode(r.p.ID, arg)
		if err != nil {
			return "", &core.Problem{Title: "Generated code missing", Code: "invalid", Summary: "The file " + arg + " is missing. Regenerate the code from the Code screen."}
		}
		safe := &resolver{e: r.e, p: r.p, env: r.env, man: r.man, mode: r.mode, public: true, escape: escaperFor(arg)}
		v, err := safe.str(raw)
		if err != nil {
			return "", err
		}
		if r.mode == forHash {
			return digestOf(v), nil
		}
		return v, nil
	case "codever":
		v, err := r.e.ReadCode(r.p.ID, arg)
		if err != nil {
			return "", &core.Problem{Title: "Generated code missing", Code: "invalid", Summary: "The file " + arg + " is missing from this project's code folder. Regenerate the code from the Code screen."}
		}
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])[:12], nil
	case "code":
		v, err := r.e.ReadCode(r.p.ID, arg)
		if err != nil {
			return "", &core.Problem{Title: "Generated code missing", Code: "invalid", Summary: "The file " + arg + " is missing from this project's code folder. Regenerate the code from the Code screen."}
		}
		if r.mode == forHash {
			return digestOf(v), nil
		}
		return v, nil
	}
	return "", fmt.Errorf("unknown placeholder %q", kind)
}

func (r *resolver) str(s string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	var firstErr error
	out := placeholder.ReplaceAllStringFunc(s, func(m string) string {
		sub := placeholder.FindStringSubmatch(m)
		v, err := r.lookup(sub[1], strings.TrimSpace(sub[2]))
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if r.escape != nil {
			return r.escape(v)
		}
		return v
	})
	return out, firstErr
}

func (r *resolver) value(v any) (any, error) {
	switch t := v.(type) {
	case string:
		return r.str(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			y, err := r.value(x)
			if err != nil {
				return nil, err
			}
			out[k] = y
		}
		return out, nil
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, x := range t {
			y, err := r.str(x)
			if err != nil {
				return nil, err
			}
			out[k] = y
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			y, err := r.value(x)
			if err != nil {
				return nil, err
			}
			out[i] = y
		}
		return out, nil
	case []string:
		out := make([]any, len(t))
		for i, x := range t {
			y, err := r.str(x)
			if err != nil {
				return nil, err
			}
			out[i] = y
		}
		return out, nil
	case []map[string]any:
		out := make([]any, len(t))
		for i, x := range t {
			y, err := r.value(x)
			if err != nil {
				return nil, err
			}
			out[i] = y
		}
		return out, nil
	}
	return v, nil
}

// resolveSpec returns a copy of spec with placeholders resolved.
func (e *Engine) resolveSpec(p *core.Project, env string, man *core.Manifest, spec *core.ResourceSpec, mode resolveMode, genCreate bool) (*core.ResourceSpec, error) {
	r := &resolver{e: e, p: p, env: env, man: man, mode: mode, genCreate: genCreate}
	props, err := r.value(spec.Props)
	if err != nil {
		return nil, err
	}
	cp := *spec
	if m, ok := props.(map[string]any); ok {
		cp.Props = m
	}
	cp.Name, _ = r.str(spec.Name)
	return &cp, nil
}

// desiredHash is the hash of a spec's resolved props with secrets digested.
func (e *Engine) desiredHash(p *core.Project, env string, man *core.Manifest, spec *core.ResourceSpec) (string, error) {
	rs, err := e.resolveSpec(p, env, man, spec, forHash, false)
	if err != nil {
		return "", err
	}
	return core.HashProps(rs.Props), nil
}

// referencedKeys lists resource keys a spec refers to through placeholders.
func referencedKeys(spec *core.ResourceSpec) []string {
	seen := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			for _, m := range placeholder.FindAllStringSubmatch(t, -1) {
				if m[1] == "out" || m[1] == "secret" {
					k, _, _ := strings.Cut(strings.TrimSpace(m[2]), ".")
					seen[k] = true
				}
			}
		case map[string]any:
			for _, x := range t {
				walk(x)
			}
		case map[string]string:
			for _, x := range t {
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		case []string:
			for _, x := range t {
				walk(x)
			}
		case []map[string]any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(spec.Props)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// safeApplied returns props suitable for persisting (secrets digested).
func (e *Engine) safeApplied(p *core.Project, env string, man *core.Manifest, spec *core.ResourceSpec) map[string]any {
	rs, err := e.resolveSpec(p, env, man, spec, forHash, false)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	for k, v := range rs.Props {
		if k == "code" || k == "html" || k == "sql" || k == "files" || k == "content" || k == "content_base64" {
			if s, ok := v.(string); ok {
				out[k] = digestOf(s)
				continue
			}
			if m, ok := v.(map[string]any); ok {
				d := map[string]any{}
				for fk, fv := range m {
					d[fk] = digestOf(fmt.Sprint(fv))
				}
				out[k] = d
				continue
			}
		}
		out[k] = v
	}
	return out
}

// EnvShort abbreviates an environment for resource names (prod, stg, dev).
func EnvShort(env string) string {
	switch strings.ToLower(env) {
	case "production", "prod", "live":
		return "prod"
	case "staging", "stage", "stg":
		return "stg"
	case "development", "dev":
		return "dev"
	}
	s := strings.ToLower(env)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 6 {
		out = out[:6]
	}
	if out == "" {
		out = "env"
	}
	return out
}
