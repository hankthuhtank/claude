// Package blueprints turns a template and a few plain answers into a
// complete blueprint (components, resources, links, tests) and generates the
// backend code, schema, emails and CI configuration that go with it.
package blueprints

import (
	"embed"
	"regexp"
	"strings"

	"safisolutions.org/backplane/internal/core"
)

//go:embed assets
var assets embed.FS

// Asset reads an embedded asset.
func Asset(path string) string {
	b, err := assets.ReadFile("assets/" + path)
	if err != nil {
		return ""
	}
	return string(b)
}

// Build levels. Every template states honestly what Backplane builds.
const (
	LevelFull = "full" // everything provisioned, backend code generated, end-to-end tested
	LevelCore = "core" // data, auth, storage, email and an API Worker; your app logic plugs in
)

// Question is one field of the guided customisation form.
type Question struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Kind     string   `json:"kind"` // text money number select toggle file email domain url
	Default  any      `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
	Advanced bool     `json:"advanced,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
}

// Template is a reusable backend recipe.
type Template struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Category     string            `json:"category"`
	Summary      string            `json:"summary"`
	Example      string            `json:"example"`
	Capabilities []core.Capability `json:"capabilities"`
	Providers    []string          `json:"providers"`
	Level        string            `json:"level"`
	LevelNote    string            `json:"levelNote"`
	Engine       string            `json:"engine"` // commerce | subscriptions | data
	Mode         string            `json:"mode,omitempty"`
	Flow         []string          `json:"flow"` // the readable pipeline
	Questions    []Question        `json:"questions"`
	Tables       []TableSpec       `json:"-"`
	Industry     string            `json:"industry"`
	Stack        []StackRole       `json:"stack"`  // which server does which job
	AddOns       []string          `json:"addOns"` // ids of optional task add-ons that suit it
}

// StackRole says which server handles one job in a preset.
type StackRole struct {
	Task     string `json:"task"`     // "Takes payments"
	Provider string `json:"provider"` // "stripe"
	Service  string `json:"service"`  // "Stripe Checkout + webhooks"
	Why      string `json:"why"`      // one plain sentence
}

// Industry groups presets by the kind of business they serve.
type Industry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	// Servers is the typical backend line-up for this kind of business.
	Servers string `json:"servers"`
}

// Industries in display order.
var Industries = []Industry{
	{"retail", "Retail & e-commerce", "Physical products, orders, shipping details, marketplaces and food ordering.", "Payments + order database + confirmation email; realtime for kitchens and stock"},
	{"digital", "Digital products", "Software, downloads, photos, templates and license keys.", "Payments + private file storage + signed expiring links + delivery email"},
	{"subscriptions", "Subscriptions & memberships", "Recurring plans, member-only content and newsletters.", "Recurring billing + customer portal + accounts + status kept in your database"},
	{"services", "Services & appointments", "Bookings, deposits, reminders, job sites and client work.", "Scheduling database + deposits + reminder jobs + email or SMS"},
	{"crm", "Customer relationships & support", "Contacts, deals, tickets, back-office dashboards.", "Team database with row-level security + email + audit trail"},
	{"apps", "Apps & software platforms", "SaaS products, mobile apps, games and public APIs.", "Accounts + per-user data + storage + a server-side API"},
	{"community", "Community & realtime", "Chat rooms and live updates.", "Realtime database channels + membership rules"},
	{"automation", "Data, files & automation", "File sharing, webhook plumbing and AI search over your documents.", "Storage + queues/webhooks + vector search"},
}

// AddOn is an optional extra server for one task. Add-ons are connected and
// monitored as part of the project's health; Backplane does not provision
// inside them yet, and the UI says so.
type AddOn struct {
	ID         string          `json:"id"`
	Task       string          `json:"task"`
	Provider   string          `json:"provider"`
	Capability core.Capability `json:"capability"`
	Why        string          `json:"why"`
}

// AddOns is the add-on library.
var AddOns = []AddOn{
	{"sms", "Text-message reminders and codes", "twilio", core.CapSMS, "Appointment reminders, order-ready texts and phone verification."},
	{"errors", "Error monitoring", "sentry", core.CapMonitoring, "Crashes in your app or site are reported with the exact line and user impact."},
	{"analytics", "Product analytics & feature flags", "posthog", core.CapAnalytics, "See where people drop off; turn features on for some users first."},
	{"media", "Image & video processing", "cloudinary", core.CapMedia, "Automatic resizing, thumbnails and fast delivery of photos and video."},
	{"cache", "Cache, rate limits & scheduled messages", "upstash", core.CapCache, "Fast counters, rate limiting and delayed jobs via Redis and QStash."},
	{"hosting", "Website hosting", "vercel", core.CapDeployment, "Host the site or app that talks to this backend; deployments are monitored."},
	{"auth_plus", "Enterprise sign-in (SSO, organizations)", "clerk", core.CapAuth, "SAML/SSO and organization management when customers are companies."},
}

// AddOnByID finds an add-on.
func AddOnByID(id string) (AddOn, bool) {
	for _, a := range AddOns {
		if a.ID == id {
			return a, true
		}
	}
	return AddOn{}, false
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// Slug makes a resource-safe short name.
func Slug(s string) string {
	s = strings.Trim(slugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 24 {
		s = strings.Trim(s[:24], "-")
	}
	if s == "" {
		s = "backend"
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "bp-" + s
	}
	return s
}

var commonCommerce = []Question{
	{Key: "product_name", Label: "What are you selling?", Kind: "text", Default: "My App", Required: true},
	{Key: "price", Label: "Price", Kind: "money", Default: 29.0, Required: true, Help: "One-time price customers pay."},
	{Key: "currency", Label: "Currency", Kind: "select", Default: "usd", Options: []string{"usd", "eur", "gbp", "cad", "aud"}},
	{Key: "business_name", Label: "Business name (shown in emails)", Kind: "text", Default: ""},
	{Key: "domain", Label: "Email domain", Kind: "domain", Required: true, Help: "Receipts are sent from this domain, e.g. example.com. Backplane adds the DNS records if the domain is on your Cloudflare account."},
	{Key: "from_email", Label: "Send receipts from", Kind: "email", Help: "Defaults to orders@ your domain."},
	{Key: "support_email", Label: "Support email", Kind: "email", Help: "Customers reply here."},
	{Key: "site_origin", Label: "Your website", Kind: "url", Help: "Where the Buy button lives, e.g. https://example.com. Allows your site to call the API."},
	{Key: "accounts", Label: "Let customers sign in to see past purchases", Kind: "toggle", Default: true},
	{Key: "include_github", Label: "Keep the code in GitHub and deploy automatically", Kind: "toggle", Default: true},
	{Key: "download_ttl_hours", Label: "Download link lifetime (hours)", Kind: "number", Default: 72, Advanced: true},
	{Key: "max_downloads", Label: "Downloads allowed per purchase", Kind: "number", Default: 10, Advanced: true},
	{Key: "region", Label: "Database region", Kind: "select", Default: "us-east-1", Advanced: true,
		Options: []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2", "ca-central-1", "eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-central-2", "eu-north-1", "ap-south-1", "ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-east-1", "sa-east-1"}},
	{Key: "github_repo", Label: "GitHub repository name", Kind: "text", Advanced: true},
	{Key: "workers_subdomain", Label: "workers.dev subdomain (only if your account has none)", Kind: "text", Advanced: true},
}

func withFile(label string) []Question {
	q := append([]Question{}, commonCommerce...)
	return append(q[:2], append([]Question{{Key: "product_file", Label: label, Kind: "file", Help: "Optional now — you can upload it later from the dashboard."}}, q[2:]...)...)
}

var storeFlow = []string{"Customer Checkout", "Stripe", "Webhook", "Cloudflare Worker", "Supabase Orders", "Resend Confirmation", "R2 Secure Download"}

// Catalog lists every template, in display order.
var Catalog = []Template{
	{ID: "software-store", Name: "Software Store", Category: "Sell", Engine: "commerce", Mode: "download", Level: LevelFull,
		Summary:      "Sell a desktop app or file for a one-time price and deliver it with an expiring, private download link.",
		Example:      "People pay $29 once, get an email and a temporary download link.",
		Capabilities: []core.Capability{core.CapPayments, core.CapWebhooks, core.CapAPI, core.CapDatabase, core.CapStorage, core.CapEmail, core.CapAuth, core.CapCICD},
		Providers:    []string{"stripe", "cloudflare", "supabase", "resend", "github"}, Flow: storeFlow, Questions: withFile("Installer or file customers download")},
	{ID: "digital-downloads", Name: "Digital Downloads", Category: "Sell", Engine: "commerce", Mode: "download", Level: LevelFull,
		Summary:      "Ebooks, templates, presets, audio packs — pay once, download privately.",
		Example:      "Sell a $12 template pack with a link that expires after 3 days.",
		Capabilities: []core.Capability{core.CapPayments, core.CapWebhooks, core.CapAPI, core.CapDatabase, core.CapStorage, core.CapEmail},
		Providers:    []string{"stripe", "cloudflare", "supabase", "resend", "github"}, Flow: storeFlow, Questions: withFile("The file customers download")},
	{ID: "photography-store", Name: "Photography Store", Category: "Sell", Engine: "commerce", Mode: "download", Level: LevelFull,
		Summary:      "Sell full-resolution photos or collections; buyers get private originals.",
		Example:      "Sell a $49 full-resolution wedding gallery download.",
		Capabilities: []core.Capability{core.CapPayments, core.CapWebhooks, core.CapAPI, core.CapDatabase, core.CapStorage, core.CapEmail, core.CapMedia},
		Providers:    []string{"stripe", "cloudflare", "supabase", "resend", "github"}, Flow: storeFlow, Questions: withFile("Full-resolution file or zip")},
	{ID: "license-server", Name: "License Server", Category: "Sell", Engine: "commerce", Mode: "license", Level: LevelFull,
		Summary:      "Sell software with license keys your app can validate against your own API.",
		Example:      "Each purchase issues a license key; the app calls /license/validate.",
		Capabilities: []core.Capability{core.CapPayments, core.CapWebhooks, core.CapAPI, core.CapDatabase, core.CapStorage, core.CapEmail},
		Providers:    []string{"stripe", "cloudflare", "supabase", "resend", "github"}, Flow: []string{"Customer Checkout", "Stripe", "Webhook", "Cloudflare Worker", "Supabase Licenses", "Resend License Email", "App validates key"},
		Questions: withFile("Installer customers download")},
	{ID: "ecommerce", Name: "E-commerce Store", Category: "Sell", Engine: "commerce", Mode: "shipping", Level: LevelFull,
		Summary:      "Sell physical products: checkout with shipping address, order records and confirmation emails.",
		Example:      "Sell a $35 candle; collect the shipping address and email a confirmation.",
		Capabilities: []core.Capability{core.CapPayments, core.CapWebhooks, core.CapAPI, core.CapDatabase, core.CapEmail},
		Providers:    []string{"stripe", "cloudflare", "supabase", "resend", "github"}, Flow: []string{"Customer Checkout", "Stripe (address)", "Webhook", "Cloudflare Worker", "Supabase Orders", "Resend Confirmation"},
		Questions: commonCommerce},
	{ID: "saas", Name: "SaaS", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "Accounts, teams, per-user data with row-level security, file uploads and transactional email.",
		Example:      "Users sign up, verify email, create projects and upload files.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapStorage, core.CapEmail, core.CapAPI}, Providers: []string{"supabase", "cloudflare", "resend", "github"}},
	{ID: "free-saas", Name: "Free SaaS", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "Sign-up, sign-in and private per-user data — no payments.",
		Example:      "A free tool where people save their own items.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapEmail}, Providers: []string{"supabase", "resend", "cloudflare"}},
	{ID: "subscription-saas", Name: "Subscription SaaS", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "Monthly or yearly plans with a customer portal; subscription status kept in your database.",
		Example:      "$19/month Pro plan with a self-serve billing portal.",
		Capabilities: []core.Capability{core.CapAuth, core.CapPayments, core.CapWebhooks, core.CapDatabase, core.CapEmail, core.CapAPI}, Providers: []string{"supabase", "stripe", "cloudflare", "resend", "github"}},
	{ID: "membership", Name: "Membership Site", Category: "Community", Engine: "data", Level: LevelCore,
		Summary:      "Paid members-only content with accounts and recurring billing.",
		Example:      "Members pay $9/month to read premium posts.",
		Capabilities: []core.Capability{core.CapAuth, core.CapPayments, core.CapWebhooks, core.CapDatabase, core.CapEmail}, Providers: []string{"supabase", "stripe", "cloudflare", "resend"}},
	{ID: "marketplace", Name: "Marketplace", Category: "Sell", Engine: "data", Level: LevelCore,
		Summary:      "Sellers list items, buyers purchase; data model with seller/buyer isolation. Stripe Connect payouts are set up in Stripe.",
		Example:      "Local makers list products; buyers order from them.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapStorage, core.CapPayments, core.CapEmail}, Providers: []string{"supabase", "stripe", "cloudflare", "resend"}},
	{ID: "mobile-app", Name: "Mobile Application", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "Auth, per-user data and file storage for an iOS/Android app.",
		Example:      "A habit tracker app with accounts and synced data.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapStorage, core.CapRealtime}, Providers: []string{"supabase", "cloudflare"}},
	{ID: "crm", Name: "CRM", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Contacts, companies, deals and activities with team access.",
		Example:      "Track leads, calls and deals for a small sales team.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapEmail}, Providers: []string{"supabase", "resend", "cloudflare"}},
	{ID: "client-portal", Name: "Client Portal", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Clients sign in to see their projects, files and invoices — and only theirs.",
		Example:      "An agency shares deliverables with each client privately.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapStorage, core.CapEmail}, Providers: []string{"supabase", "resend", "cloudflare"}},
	{ID: "contractor-portal", Name: "Contractor Portal", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Jobs, schedules, photos and sign-offs for field crews and customers.",
		Example:      "Crews upload job-site photos; customers approve completed work.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapStorage, core.CapEmail}, Providers: []string{"supabase", "resend", "cloudflare"}},
	{ID: "booking", Name: "Booking System", Category: "Services", Engine: "data", Level: LevelCore,
		Summary:      "Customers book appointments, pay deposits and get confirmations and reminders.",
		Example:      "Book a haircut, pay a $10 deposit, get a reminder the day before.",
		Capabilities: []core.Capability{core.CapDatabase, core.CapPayments, core.CapWebhooks, core.CapEmail, core.CapScheduler}, Providers: []string{"supabase", "stripe", "cloudflare", "resend"}},
	{ID: "restaurant", Name: "Restaurant Ordering", Category: "Services", Engine: "data", Level: LevelCore,
		Summary:      "Menu, online orders, payment and a live kitchen order feed.",
		Example:      "Order pickup online; the kitchen screen updates instantly.",
		Capabilities: []core.Capability{core.CapDatabase, core.CapPayments, core.CapRealtime, core.CapEmail}, Providers: []string{"supabase", "stripe", "cloudflare", "resend"}},
	{ID: "file-sharing", Name: "File Sharing", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Upload files and share them with expiring private links.",
		Example:      "Send a 2 GB video to a client with a link that expires.",
		Capabilities: []core.Capability{core.CapAuth, core.CapStorage, core.CapDatabase, core.CapEmail}, Providers: []string{"supabase", "cloudflare", "resend"}},
	{ID: "chat", Name: "Realtime Chat", Category: "Community", Engine: "data", Level: LevelCore,
		Summary:      "Rooms, members and messages delivered live, with per-room access control.",
		Example:      "Customers chat with support in real time.",
		Capabilities: []core.Capability{core.CapAuth, core.CapRealtime, core.CapDatabase}, Providers: []string{"supabase", "cloudflare"}},
	{ID: "game", Name: "Game Backend", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "Player accounts, saves, leaderboards and live match state.",
		Example:      "Save progress and post high scores from a mobile game.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapRealtime, core.CapCache}, Providers: []string{"supabase", "cloudflare"}},
	{ID: "api-service", Name: "API Service", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "A public API with API keys, usage logging and rate limits.",
		Example:      "Offer a data API to developers with per-key limits.",
		Capabilities: []core.Capability{core.CapAPI, core.CapDatabase, core.CapCache}, Providers: []string{"cloudflare", "supabase"}},
	{ID: "webhook-relay", Name: "Webhook Relay", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Receive webhooks, verify signatures, store them and forward them reliably.",
		Example:      "Collect webhooks from three tools and forward them to one app.",
		Capabilities: []core.Capability{core.CapWebhooks, core.CapQueue, core.CapDatabase}, Providers: []string{"cloudflare", "supabase"}},
	{ID: "newsletter", Name: "Newsletter", Category: "Community", Engine: "data", Level: LevelCore,
		Summary:      "Double opt-in sign-up, subscriber list in Resend and broadcast-ready audience.",
		Example:      "People subscribe on your site and confirm by email.",
		Capabilities: []core.Capability{core.CapEmail, core.CapDatabase, core.CapAPI}, Providers: []string{"resend", "cloudflare", "supabase"}},
	{ID: "support", Name: "Support / Ticket System", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Customers open tickets, staff reply, everyone gets email updates.",
		Example:      "A help desk with ticket numbers and email replies.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapEmail}, Providers: []string{"supabase", "resend", "cloudflare"}},
	{ID: "ai-rag", Name: "AI / RAG Application", Category: "Software products", Engine: "data", Level: LevelCore,
		Summary:      "Documents, chunks and pgvector embeddings for retrieval-augmented answers.",
		Example:      "Answer customer questions from your own documents.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase, core.CapVector, core.CapStorage}, Providers: []string{"supabase", "cloudflare"}},
	{ID: "admin-dashboard", Name: "Admin Dashboard Backend", Category: "Operations", Engine: "data", Level: LevelCore,
		Summary:      "Roles, audit log and admin-only access to operational data.",
		Example:      "Staff manage records; every change is logged.",
		Capabilities: []core.Capability{core.CapAuth, core.CapDatabase}, Providers: []string{"supabase", "cloudflare"}},
}

var industryOf = map[string]string{
	"software-store": "digital", "digital-downloads": "digital", "photography-store": "digital", "license-server": "digital",
	"ecommerce": "retail", "marketplace": "retail", "restaurant": "retail",
	"subscription-saas": "subscriptions", "membership": "subscriptions", "newsletter": "subscriptions",
	"booking": "services", "contractor-portal": "services", "client-portal": "services",
	"crm": "crm", "support": "crm", "admin-dashboard": "crm",
	"saas": "apps", "free-saas": "apps", "mobile-app": "apps", "game": "apps", "api-service": "apps",
	"chat":         "community",
	"file-sharing": "automation", "webhook-relay": "automation", "ai-rag": "automation",
}

var addOnsOf = map[string][]string{
	"software-store": {"errors", "analytics", "hosting"}, "digital-downloads": {"analytics", "hosting"}, "photography-store": {"media", "hosting"},
	"license-server": {"errors", "analytics"}, "ecommerce": {"sms", "analytics", "media", "hosting"}, "marketplace": {"media", "analytics", "errors"},
	"restaurant": {"sms", "analytics"}, "subscription-saas": {"errors", "analytics", "auth_plus", "hosting"}, "membership": {"analytics", "hosting"},
	"newsletter": {"analytics"}, "booking": {"sms", "analytics"}, "contractor-portal": {"sms", "media"}, "client-portal": {"auth_plus", "errors"},
	"crm": {"sms", "analytics"}, "support": {"sms", "errors"}, "admin-dashboard": {"auth_plus", "errors"}, "saas": {"errors", "analytics", "auth_plus", "hosting"},
	"free-saas": {"analytics", "hosting"}, "mobile-app": {"errors", "analytics", "cache"}, "game": {"cache", "errors", "analytics"},
	"api-service": {"cache", "errors"}, "chat": {"errors", "cache"}, "file-sharing": {"media", "errors"}, "webhook-relay": {"cache", "errors"}, "ai-rag": {"cache", "errors"},
}

// stackFor derives "which server does which job" from a preset's capabilities.
func stackFor(t *Template) []StackRole {
	var out []StackRole
	add := func(c core.Capability, r StackRole) {
		if has(*t, c) {
			out = append(out, r)
		}
	}
	add(core.CapPayments, StackRole{"Takes payments", "stripe", "Stripe Checkout, prices and webhooks", "Cards, wallets and receipts without handling card data yourself."})
	if t.Engine == "commerce" {
		out = append(out, StackRole{"Runs your API", "cloudflare", "Cloudflare Worker", "Verifies every payment event, records orders and hands out signed links — close to every customer."})
	} else {
		out = append(out, StackRole{"Runs server-side jobs", "cloudflare", "Cloudflare Worker (+ cron)", "Webhooks, billing, reminders and anything that must not run in a browser."})
	}
	out = append(out, StackRole{"Stores your data", "supabase", "Supabase Postgres with row-level security", "A real SQL database where each user can reach only their own rows."})
	add(core.CapAuth, StackRole{"Signs people in", "supabase", "Supabase Auth", "Email/password, magic links and OAuth, with your own email domain."})
	if t.Engine == "commerce" && t.Mode != "shipping" {
		out = append(out, StackRole{"Delivers files", "cloudflare", "Cloudflare R2 (private)", "No egress fees; files leave only through signed, expiring links."})
	} else {
		add(core.CapStorage, StackRole{"Stores files", "supabase", "Supabase Storage (private bucket)", "Uploads protected by the same rules as your data."})
	}
	add(core.CapRealtime, StackRole{"Pushes live updates", "supabase", "Supabase Realtime", "Screens update the moment a row changes."})
	add(core.CapVector, StackRole{"Searches by meaning", "supabase", "pgvector in Postgres", "AI answers from your own documents, respecting row-level security."})
	add(core.CapEmail, StackRole{"Sends email", "resend", "Resend (your domain, SPF + DKIM)", "Receipts and sign-in emails that land in inboxes, with delivery tracking."})
	out = append(out, StackRole{"Keeps the code & deploys", "github", "GitHub + Actions", "Your backend's code lives in your account and redeploys on every change."})
	return out
}

func init() {
	for i := range Catalog {
		t := &Catalog[i]
		t.Industry = industryOf[t.ID]
		t.AddOns = addOnsOf[t.ID]
		t.Stack = stackFor(t)
		if t.Level == LevelFull {
			t.LevelNote = "Backplane builds everything, writes the backend code, and proves the full customer journey with an end-to-end test."
		} else {
			t.LevelNote = "Backplane builds the database (with row-level security), sign-in, storage, email and a monitored API Worker, and proves data isolation end to end. Your app's own screens and business logic plug into it."
		}
		if len(t.Questions) == 0 {
			t.Questions = dataQuestions(*t)
		}
		if len(t.Flow) == 0 {
			t.Flow = dataFlow(*t)
		}
	}
}

// Get returns a template by id.
func Get(id string) (Template, bool) {
	for _, t := range Catalog {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
