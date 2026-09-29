package providers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
)

// Str reads a string prop.
func Str(props map[string]any, key string) string {
	if props == nil {
		return ""
	}
	switch v := props[key].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		bs, _ := json.Marshal(v)
		return string(bs)
	}
}

// Int reads an integer prop.
func Int(props map[string]any, key string) int64 {
	switch v := props[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

// Bool reads a boolean prop.
func Bool(props map[string]any, key string) bool {
	switch v := props[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	case float64:
		return v != 0
	}
	return false
}

// List reads a []string prop.
func List(props map[string]any, key string) []string {
	switch v := props[key].(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	return nil
}

// Map reads a map[string]string prop.
func Map(props map[string]any, key string) map[string]string {
	out := map[string]string{}
	switch v := props[key].(type) {
	case map[string]string:
		for k, x := range v {
			out[k] = x
		}
	case map[string]any:
		for k, x := range v {
			out[k] = Str(map[string]any{"v": x}, "v")
		}
	}
	return out
}

// Objects reads a []map[string]any prop.
func Objects(props map[string]any, key string) []map[string]any {
	var out []map[string]any
	switch v := props[key].(type) {
	case []map[string]any:
		return v
	case []any:
		for _, x := range v {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// SortedKeys returns map keys sorted.
func SortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RandomToken returns n random bytes hex-encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// StrongPassword returns a password satisfying common database rules.
func StrongPassword(n int) string {
	const sets = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	var b strings.Builder
	for i := 0; i < n; i++ {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(sets))))
		b.WriteByte(sets[k.Int64()])
	}
	// guarantee each class appears
	return "Bp9" + b.String() + "x"
}

// NewState starts a resource state for a spec.
func NewState(spec *core.ResourceSpec, id, name string) *core.ResourceState {
	now := time.Now().UTC()
	return &core.ResourceState{Key: spec.Key, Kind: spec.Kind, Provider: spec.Provider, ID: id, Name: name,
		Outputs: map[string]string{}, Status: core.StateReady, CreatedBy: "backplane", CreatedAt: now, UpdatedAt: now}
}

// Touch updates a state's identity after apply, keeping creation metadata.
func Touch(spec *core.ResourceSpec, st *core.ResourceState, id, name string) *core.ResourceState {
	if st == nil {
		return NewState(spec, id, name)
	}
	cp := *st
	cp.Kind, cp.Provider = spec.Kind, spec.Provider
	if id != "" {
		cp.ID = id
	}
	if name != "" {
		cp.Name = name
	}
	if cp.Outputs == nil {
		cp.Outputs = map[string]string{}
	} else {
		o := map[string]string{}
		for k, v := range cp.Outputs {
			o[k] = v
		}
		cp.Outputs = o
	}
	cp.Status = core.StateReady
	cp.UpdatedAt = time.Now().UTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = cp.UpdatedAt
	}
	if cp.CreatedBy == "" {
		cp.CreatedBy = "backplane"
	}
	return &cp
}

// DriftIf appends a drift item when expected and actual differ.
func DriftIf(items []core.DriftItem, spec *core.ResourceSpec, field, expected, actual string, sev core.Health, breaks []string, recommended string) []core.DriftItem {
	if expected == actual {
		return items
	}
	return append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: spec.Provider, Field: field,
		Expected: orDash(expected), Actual: orDash(actual), Severity: sev, Breaks: breaks, Recommended: recommended, FixID: "reapply:" + spec.Key})
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Translate turns any adapter error into a plain-English Problem. The engine
// adds affected/unaffected capabilities from the architecture graph.
func Translate(provider, action string, err error) *core.Problem {
	if err == nil {
		return nil
	}
	if p, ok := err.(*core.Problem); ok {
		return p
	}
	name := DisplayName(provider)
	e, ok := httpx.AsError(err)
	if !ok {
		return &core.Problem{Title: fmt.Sprintf("%s: %s failed", name, action), Summary: err.Error(), Provider: provider, Code: "error", Technical: err.Error()}
	}
	p := &core.Problem{Provider: provider, Code: e.Kind, HTTPStatus: e.Status, Retryable: e.Retryable,
		Technical: strings.TrimSpace(fmt.Sprintf("%s %s → %d %s %s (request %s) %s", e.Method, e.Path, e.Status, e.Code, e.Message, e.RequestID, e.Body))}
	switch e.Kind {
	case httpx.KindAuth:
		p.Title = strings.ToUpper(name) + " CONNECTION FAILED"
		p.Summary = "The credential stored for " + name + " is no longer accepted. It may have been revoked, rotated, expired, or pasted incompletely."
		p.Fixes = []core.Fix{{ID: "reconnect:" + provider, Label: "Replace the " + name + " credential", Explain: "Open the connection and paste a new key. Backplane re-checks every project that uses it.", Action: "reconnect", Target: provider}}
	case httpx.KindPermission:
		p.Title = name + " refused permission"
		p.Summary = "The " + name + " credential works, but it is not allowed to " + action + ". It is missing a permission Backplane needs."
		if e.Message != "" {
			p.Summary += " " + name + " said: “" + e.Message + "”."
		}
		p.Fixes = []core.Fix{{ID: "reconnect:" + provider, Label: "Create a key with the right permissions", Explain: "The setup guide opens the " + name + " page with the exact permissions pre-selected where the provider supports it.", Action: "guide", Target: provider}}
	case httpx.KindNotFound:
		p.Title = name + ": something expected is missing"
		p.Summary = "Backplane tried to " + action + " but " + name + " says it does not exist. It may have been deleted or renamed outside Backplane."
		p.Fixes = []core.Fix{{ID: "replan", Label: "Re-plan to recreate what's missing", Explain: "A new plan shows exactly what would be created again before anything changes.", Action: "replan", Automatic: false}}
	case httpx.KindConflict:
		p.Title = name + ": already exists"
		p.Summary = "Something with the same name already exists in " + name + ". Backplane will not overwrite resources it did not create."
		p.Fixes = []core.Fix{{ID: "adopt", Label: "Use the existing one, or rename", Explain: "Choose ‘Use existing’ in the plan to adopt it, or change the name.", Action: "replan"}}
	case httpx.KindInvalid:
		p.Title = name + " rejected the request"
		p.Summary = name + " did not accept what Backplane sent while trying to " + action + "."
		if e.Message != "" {
			p.Summary += " It said: “" + e.Message + "”."
		}
	case httpx.KindRateLimit:
		p.Title = name + " is rate limiting"
		p.Summary = name + " asked Backplane to slow down. Backplane waited and retried " + strconv.Itoa(e.Attempts) + " times. Try again in a minute; nothing is broken."
		p.Retryable = true
	case httpx.KindServer:
		p.Title = name + " had a problem"
		p.Summary = name + " returned a server error (" + strconv.Itoa(e.Status) + ") after " + strconv.Itoa(e.Attempts) + " attempts. This is usually temporary on their side."
		p.Retryable = true
		if info, ok := statusPages[provider]; ok {
			p.Fixes = append(p.Fixes, core.Fix{ID: "status:" + provider, Label: "Check " + name + " status", Link: info, Action: "link"})
		}
	case httpx.KindNetwork, httpx.KindTimeout:
		p.Title = "Could not reach " + name
		p.Summary = "Backplane could not get an answer from " + name + " (" + e.Message + "). Check your internet connection, VPN or firewall. If only " + name + " is affected, they may be having an outage."
		p.Retryable = true
		if info, ok := statusPages[provider]; ok {
			p.Fixes = append(p.Fixes, core.Fix{ID: "status:" + provider, Label: "Check " + name + " status", Link: info, Action: "link"})
		}
	case httpx.KindCanceled:
		p.Title = "Canceled"
		p.Summary = "The operation was canceled before " + name + " answered."
	default:
		p.Title = name + ": " + action + " failed"
		p.Summary = e.Message
	}
	return p
}

var statusPages = map[string]string{
	"cloudflare": "https://www.cloudflarestatus.com",
	"supabase":   "https://status.supabase.com",
	"stripe":     "https://status.stripe.com",
	"resend":     "https://resend-status.com",
	"github":     "https://www.githubstatus.com",
	"vercel":     "https://www.vercel-status.com",
	"netlify":    "https://www.netlifystatus.com",
	"neon":       "https://neonstatus.com",
	"upstash":    "https://status.upstash.com",
	"clerk":      "https://status.clerk.com",
	"twilio":     "https://status.twilio.com",
	"sentry":     "https://status.sentry.io",
	"posthog":    "https://status.posthog.com",
}

var displayNames = map[string]string{
	"cloudflare": "Cloudflare", "supabase": "Supabase", "stripe": "Stripe", "resend": "Resend", "github": "GitHub",
	"vercel": "Vercel", "netlify": "Netlify", "firebase": "Firebase", "aws": "AWS", "neon": "Neon",
	"planetscale": "PlanetScale", "upstash": "Upstash", "clerk": "Clerk", "auth0": "Auth0", "twilio": "Twilio",
	"cloudinary": "Cloudinary", "sentry": "Sentry", "posthog": "PostHog", "anthropic": "Anthropic", "opentofu": "OpenTofu",
}

// DisplayName returns a provider's display name.
func DisplayName(id string) string {
	if n, ok := displayNames[id]; ok {
		return n
	}
	if id == "" {
		return "Provider"
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// LogTo adapts an httpx log event into a session log line.
func LogTo(fn func(level, provider, resource, msg string)) func(httpx.LogEvent) {
	return func(ev httpx.LogEvent) {
		if fn == nil {
			return
		}
		level := "debug"
		msg := fmt.Sprintf("%s %s → %d in %dms", ev.Method, ev.URL, ev.Status, ev.Latency.Milliseconds())
		if ev.Err != nil {
			level = "warn"
			msg = fmt.Sprintf("%s %s → %v (attempt %d)", ev.Method, ev.URL, ev.Err, ev.Attempt)
		}
		if ev.RequestID != "" {
			msg += " [" + ev.RequestID + "]"
		}
		fn(level, ev.Provider, ev.Resource, msg)
	}
}
