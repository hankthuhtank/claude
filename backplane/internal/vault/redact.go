package vault

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Redactor removes secrets from any text before it is logged, stored or shown.
// It knows every value in the vault plus the shapes of common credentials, so
// even a secret that never passed through the vault (say, one echoed back in a
// provider error) is masked.
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Add registers a secret value. Very short values are ignored because masking
// "abc" everywhere would destroy readable logs without adding safety.
func (r *Redactor) Add(v string) {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.values {
		if x == v {
			return
		}
	}
	r.values = append(r.values, v)
	// Longest first so a secret containing another is masked whole.
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(sk|rk|pk)_(live|test)_[A-Za-z0-9]{8,}`),                           // Stripe keys
	regexp.MustCompile(`\bwhsec_[A-Za-z0-9+/=]{8,}`),                                         // webhook secrets (Stripe, Svix)
	regexp.MustCompile(`\bre_[A-Za-z0-9_]{16,}`),                                             // Resend
	regexp.MustCompile(`\bsbp_[A-Za-z0-9]{16,}`),                                             // Supabase personal tokens
	regexp.MustCompile(`\bsb_(secret|publishable)_[A-Za-z0-9_\-]{8,}`),                       // Supabase API keys
	regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),                           // GitHub classic tokens
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),                                     // GitHub fine-grained tokens
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`), // JWTs
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-~+/=]{12,}`),                            // bearer headers
	regexp.MustCompile(`(?i)("?(?:password|passwd|db_pass|secret|api_key|apikey|token|access_token|refresh_token|private_key|signing_secret|client_secret)"?\s*[:=]\s*"?)([^"\s,&}]{6,})`),
	regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:/\s]+:)([^@\s]+)(@)`), // connection strings
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAC[a-f0-9]{32}\b`), // Twilio account SID (paired with tokens)
}

// Mask replaces secrets in s.
func (r *Redactor) Mask(s string) string {
	if s == "" {
		return s
	}
	if r != nil {
		r.mu.RLock()
		for _, v := range r.values {
			if strings.Contains(s, v) {
				s = strings.ReplaceAll(s, v, mask(v))
			}
		}
		r.mu.RUnlock()
	}
	for i, p := range patterns {
		switch i {
		case 8: // bearer: keep the word
			s = p.ReplaceAllString(s, "${1}••••••")
		case 9: // key=value: keep the key
			s = p.ReplaceAllString(s, "${1}••••••")
		case 10: // connection string password
			s = p.ReplaceAllString(s, "${1}••••••${3}")
		default:
			s = p.ReplaceAllStringFunc(s, mask)
		}
	}
	return s
}

// mask keeps a recognisable prefix (sk_live_, whsec_) and the last four
// characters so users can tell keys apart without seeing them.
func mask(v string) string {
	prefix := ""
	for _, p := range []string{"sk_live_", "sk_test_", "rk_live_", "rk_test_", "pk_live_", "pk_test_", "whsec_", "re_", "sbp_", "sb_secret_", "sb_publishable_", "ghp_", "github_pat_", "eyJ"} {
		if strings.HasPrefix(v, p) {
			prefix = p
			break
		}
	}
	if len(v) <= len(prefix)+8 {
		return prefix + "••••"
	}
	return prefix + "••••" + v[len(v)-4:]
}

// Hint returns a safe identifying hint for a secret, e.g. "sk_test_••••4f2a".
func Hint(v string) string {
	if v == "" {
		return ""
	}
	return mask(v)
}
