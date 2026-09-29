// Package all assembles every provider adapter into one registry.
package all

import (
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/cloudflare"
	"safisolutions.org/backplane/internal/providers/extra"
	"safisolutions.org/backplane/internal/providers/github"
	"safisolutions.org/backplane/internal/providers/resend"
	"safisolutions.org/backplane/internal/providers/stripe"
	"safisolutions.org/backplane/internal/providers/supabase"
)

// Registry returns a registry with every adapter.
func Registry() *providers.Registry {
	r := providers.NewRegistry()
	r.Add(cloudflare.Provider{}, cloudflare.Handlers()...)
	r.Add(supabase.Provider{}, supabase.Handlers()...)
	r.Add(stripe.Provider{}, stripe.Handlers()...)
	r.Add(resend.Provider{}, resend.Handlers()...)
	r.Add(github.Provider{}, github.Handlers()...)
	for _, p := range extra.Providers() {
		r.Add(p)
	}
	return r
}
