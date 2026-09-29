package engine

import (
	"sort"
	"strings"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
)

// Pricing notes, checked 2026-09-29 against public pricing pages and several
// 2026 write-ups. They are estimates for small businesses, never guarantees;
// the UI says so next to every number.
var pricing = map[string]core.CostLine{
	"cloudflare": {Estimate: "$0 – $5 / month",
		Basis:   "Workers Free: 100,000 requests/day, 10 ms CPU per request. Workers Paid: $5/month minimum with 10M requests and 30M CPU-ms included, then $0.30 per extra million requests. R2: 10 GB storage, 1M writes and 10M reads free each month, no egress fees. KV free: 100k reads and 1k writes per day. D1 free: 5M rows read and 100k rows written per day, 5 GB.",
		Upgrade: "Workers Paid ($5/month) once you pass ~100,000 requests a day or need more than 10 ms CPU per request.",
		Source:  "cloudflare.com/plans/developer-platform (2026)"},
	"supabase": {Estimate: "Free tier, or $25 / month",
		Basis:   "Free: 2 active projects, 500 MB database, 1 GB file storage, 50,000 monthly active users; projects pause after 1 week without activity. Pro: $25/month per project with 8 GB database and 100,000 MAU included.",
		Upgrade: "Pro ($25/month) above 500 MB of data, for daily backups, or so production never pauses.",
		Source:  "supabase.com/pricing (2026)"},
	"stripe": {Estimate: "No monthly fee — 2.9% + 30¢ per card payment",
		Basis:   "US standard pricing for online card payments. International cards +1.5%, currency conversion +1%, disputes $15.",
		Upgrade: "Nothing to upgrade; fees scale with sales. Stripe Tax and Billing add their own percentage fees if enabled.",
		Source:  "stripe.com/pricing (2026)"},
	"resend": {Estimate: "Free tier, or $20 / month",
		Basis:   "Free: 3,000 emails/month and 100/day. Pro: $20/month for 50,000 emails; pay-as-you-go overage since Dec 2025.",
		Upgrade: "Pro ($20/month) once you send more than 100 emails on a busy day.",
		Source:  "resend.com/pricing (2026)"},
	"github": {Estimate: "$0",
		Basis:   "Private repositories are free; the Free plan includes 2,000 Actions minutes/month for private repos (public repos unlimited). A deploy with Wrangler uses about 1 minute.",
		Upgrade: "Only if you run far more than ~60 deploys a day.",
		Source:  "github.com/pricing (2026)"},
	"vercel":      {Estimate: "Free (Hobby) or $20 / user / month (Pro)", Basis: "Hobby is for non-commercial use; commercial sites need Pro.", Upgrade: "Pro for any commercial project.", Source: "vercel.com/pricing (2026)"},
	"netlify":     {Estimate: "Free, or $19+ / month", Basis: "Free plan with credit-based usage limits.", Upgrade: "When bandwidth or build credits run out.", Source: "netlify.com/pricing (2026)"},
	"neon":        {Estimate: "Free tier, then usage-based", Basis: "Free plan with scale-to-zero compute and limited storage per project.", Upgrade: "Launch plan when you need more compute hours or storage.", Source: "neon.com/pricing (2026)"},
	"upstash":     {Estimate: "Free tier, then pay per request", Basis: "Redis and QStash free tiers with daily/monthly request caps.", Upgrade: "Pay-as-you-go above the free caps.", Source: "upstash.com/pricing (2026)"},
	"clerk":       {Estimate: "Free tier, then per monthly active user", Basis: "Free plan includes a generous monthly active user allowance.", Upgrade: "Pro when you pass the free MAU allowance or need premium features.", Source: "clerk.com/pricing (2026)"},
	"twilio":      {Estimate: "Pay per message", Basis: "US SMS is billed per segment plus carrier fees; A2P 10DLC registration is required for US business texting.", Upgrade: "Costs scale with messages sent.", Source: "twilio.com/sms/pricing (2026)"},
	"sentry":      {Estimate: "Free (Developer), then $26+ / month", Basis: "Developer plan for one user with a monthly error quota.", Upgrade: "Team plan for more users or events.", Source: "sentry.io/pricing (2026)"},
	"posthog":     {Estimate: "Free tier, then usage-based", Basis: "Generous monthly free allowance of analytics events and flag requests.", Upgrade: "Automatic usage billing above the free allowance.", Source: "posthog.com/pricing (2026)"},
	"cloudinary":  {Estimate: "Free tier (credits), then $89+ / month", Basis: "Free plan with monthly credits covering storage, transformations and bandwidth.", Upgrade: "Plus plan when credits run out.", Source: "cloudinary.com/pricing (2026)"},
	"planetscale": {Estimate: "Paid plans", Basis: "PlanetScale has no free hobby tier; smallest plans are usage-based.", Upgrade: "—", Source: "planetscale.com/pricing (2026)"},
	"auth0":       {Estimate: "Free tier, then per MAU", Basis: "Free plan with a monthly active user allowance.", Upgrade: "Essentials when you need more MAU or features.", Source: "auth0.com/pricing (2026)"},
	"firebase":    {Estimate: "Spark (free) or Blaze (pay as you go)", Basis: "Cloud Functions require the Blaze plan.", Upgrade: "Blaze for Functions or above free quotas.", Source: "firebase.google.com/pricing (2026)"},
	"aws":         {Estimate: "Usage-based", Basis: "Most services have a free tier for the first 12 months or an always-free allowance.", Upgrade: "Varies by service.", Source: "aws.amazon.com/pricing (2026)"},
}

// EstimateCostsFor is EstimateCosts for a project. Practice backends run on
// the built-in simulator, so they cost nothing and say so instead of showing
// real-world price tables.
func EstimateCostsFor(p *core.Project) []core.CostLine {
	if p.Practice {
		var names []string
		for _, prov := range p.Blueprint.Providers() {
			names = append(names, providers.DisplayName(prov))
		}
		return []core.CostLine{{Provider: "Practice", Estimate: "Free — simulated",
			Basis:   "Runs on Backplane's built-in simulator of " + strings.Join(names, ", ") + " on this computer. No accounts, no charges.",
			Upgrade: "Build the same preset without “Practice run” to see real monthly estimates."}}
	}
	return EstimateCosts(&p.Blueprint)
}

// EstimateCosts lists an estimate for every provider in the blueprint.
func EstimateCosts(bp *core.Blueprint) []core.CostLine {
	var out []core.CostLine
	for _, p := range bp.Providers() {
		c, ok := pricing[p]
		if !ok {
			continue
		}
		c.Provider = providers.DisplayName(p)
		// Tailor the Stripe line to the product price when known.
		if p == "stripe" {
			if amt := bp.Param("price_cents"); amt != "" && amt != "0" {
				c.Basis = "On a " + moneyParam(bp) + " sale you keep about " + netAfterStripe(bp) + " after the standard US card fee. " + c.Basis
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.Compare(out[i].Provider, out[j].Provider) < 0 })
	return out
}

func moneyParam(bp *core.Blueprint) string {
	cents := parseInt(bp.Param("price_cents"))
	return "$" + fmtCents(cents)
}

func netAfterStripe(bp *core.Blueprint) string {
	cents := parseInt(bp.Param("price_cents"))
	fee := cents*29/1000 + 30
	return "$" + fmtCents(cents-fee)
}

func parseInt(s string) int64 {
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

func fmtCents(c int64) string {
	neg := c < 0
	if neg {
		c = -c
	}
	s := itoa(c/100) + "." + pad2(c%100)
	if neg {
		return "-" + s
	}
	return s
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}
