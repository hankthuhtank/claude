package blueprints

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"safisolutions.org/backplane/internal/core"
)

// CompatibilityDate is the Workers runtime date generated code targets.
const CompatibilityDate = "2026-09-01"

// Answers are the user's replies to a template's questions.
type Answers map[string]any

func (a Answers) str(k, def string) string {
	switch v := a[k].(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case bool:
		return strconv.FormatBool(v)
	}
	return def
}

func (a Answers) boolean(k string, def bool) bool {
	switch v := a[k].(type) {
	case bool:
		return v
	case string:
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func (a Answers) num(k string, def float64) float64 {
	switch v := a[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(v), "$"), 64); err == nil {
			return f
		}
	}
	return def
}

// Defaults fills a template's answers with defaults.
func Defaults(t Template, a Answers) Answers {
	out := Answers{}
	for _, q := range t.Questions {
		if q.Default != nil {
			out[q.Key] = q.Default
		}
	}
	for k, v := range a {
		out[k] = v
	}
	return out
}

// Build creates the blueprint for a template.
func Build(t Template, projectName string, a Answers) (core.Blueprint, error) {
	a = Defaults(t, a)
	var bp core.Blueprint
	var err error
	switch t.Engine {
	case "commerce":
		bp, err = buildCommerce(t, projectName, a)
	case "data":
		bp, err = buildData(t, projectName, a)
	default:
		return core.Blueprint{}, fmt.Errorf("template %s has no builder", t.ID)
	}
	if err != nil {
		return bp, err
	}
	applyAddOns(&bp, a)
	return bp, bp.ValidateGraph()
}

// applyAddOns attaches chosen task add-ons as monitored components.
func applyAddOns(bp *core.Blueprint, a Answers) {
	var ids []string
	switch v := a["add_ons"].(type) {
	case []any:
		for _, x := range v {
			ids = append(ids, fmt.Sprint(x))
		}
	case []string:
		ids = v
	case string:
		for _, x := range strings.Split(v, ",") {
			if strings.TrimSpace(x) != "" {
				ids = append(ids, strings.TrimSpace(x))
			}
		}
	}
	for i, id := range ids {
		ad, ok := AddOnByID(id)
		if !ok || bp.ComponentByKey("addon_"+id) != nil {
			continue
		}
		bp.Components = append(bp.Components, core.Component{Key: "addon_" + id, Label: providerLabel(ad.Provider), Role: ad.Task, Capability: ad.Capability,
			Provider: ad.Provider, Order: 90 + i, Breaks: []string{ad.Task}})
	}
	if len(ids) > 0 {
		bp.Params["add_ons"] = strings.Join(ids, ",")
	}
}

func providerLabel(id string) string {
	switch id {
	case "twilio":
		return "Twilio"
	case "sentry":
		return "Sentry"
	case "posthog":
		return "PostHog"
	case "cloudinary":
		return "Cloudinary"
	case "upstash":
		return "Upstash"
	case "vercel":
		return "Vercel"
	case "clerk":
		return "Clerk"
	}
	return id
}

func buildCommerce(t Template, projectName string, a Answers) (core.Blueprint, error) {
	slug := Slug(a.str("slug", projectName)) // stored at creation so renames never rename resources
	items, err := Items(t, a, projectName)
	if err != nil {
		return core.Blueprint{}, err
	}
	spec := t.ProductsSpec()
	currency := a.str("currency", "usd")
	business := cleanText(a.str("business_name", projectName), 80)
	// One item: emails and pages name it. Several: they name the business
	// and list what was bought.
	product := business
	if len(items) == 1 {
		product = items[0].Name
	}
	domain := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(a.str("domain", ""), "https://"), "http://"))
	domain = strings.TrimSuffix(domain, "/")
	if domain == "" {
		return core.Blueprint{}, fmt.Errorf("an email domain is required (receipts are sent from it)")
	}
	cents := items[0].Cents()
	mode := t.Mode
	license := "none"
	if mode == "license" {
		license = "key"
	}
	fromEmail := a.str("from_email", "orders@"+domain)
	support := a.str("support_email", fromEmail)
	site := strings.TrimSuffix(a.str("site_origin", ""), "/")
	fileKey := "releases/" + slug + "/product.bin"
	if f := a.str("product_file", ""); f != "" {
		fileKey = "releases/" + slug + "/" + sanitizeFile(filepath.Base(f))
	}
	accounts := a.boolean("accounts", true) && mode != "shipping"
	withGitHub := a.boolean("include_github", true)
	repo := a.str("github_repo", slug+"-backend")
	params := map[string]any{
		"project_name": projectName, "slug": slug, "product_name": product, "price_cents": cents, "currency": currency, "catalog_items": ItemsAnswer(items),
		"business_name": business, "domain": domain, "from_email": business + " <" + fromEmail + ">", "support_email": support,
		"site_origin": site, "success_url": a.str("success_url", ""), "cancel_url": a.str("cancel_url", site),
		"download_ttl_hours": strconv.Itoa(int(a.num("download_ttl_hours", 72))), "max_downloads": strconv.Itoa(int(a.num("max_downloads", 10))),
		"license_mode": license, "fulfillment_mode": mode, "product_file_key": fileKey, "product_file": a.str("product_file", ""),
		"accounts": accounts, "include_github": withGitHub, "github_repo": repo, "region": a.str("region", "us-east-1"),
		"workers_subdomain": a.str("workers_subdomain", ""), "compatibility_date": CompatibilityDate, "ship_countries": a.str("ship_countries", "US,CA"),
	}
	name := func(suffix string) string { return "{{param:slug}}-{{envshort}}" + suffix }
	bp := core.Blueprint{Version: 1, TemplateID: t.ID, Params: params}

	// ── Components, top to bottom in the rack ──
	bp.Components = []core.Component{
		{Key: "customer", Label: "Customer checkout", Role: "Buy button on your site", External: true, Order: 0},
		{Key: "stripe", Label: "Stripe", Role: "Takes the payment", Capability: core.CapPayments, Provider: "stripe", Order: 1,
			Resources: []string{"webhook"}, Breaks: []string{"Payments", "Refunds", "Webhook configuration"}},
		{Key: "api", Label: "Cloudflare Worker", Role: "Your API", Capability: core.CapAPI, Provider: "cloudflare", Order: 2,
			Resources: []string{"subdomain", "api", "api_webhook_secret", "api_resend_secret"}, Breaks: []string{"Checkout", "Order processing", "Download delivery"}},
		{Key: "db", Label: "Supabase", Role: "Orders database", Capability: core.CapDatabase, Provider: "supabase", Order: 3,
			Resources: []string{"db", "schema", "db_key"}, Breaks: []string{"Order records", "Download access checks"}},
	}
	if mode != "shipping" {
		bp.Components = append(bp.Components, core.Component{Key: "storage", Label: "R2", Role: "Secure downloads", Capability: core.CapStorage, Provider: "cloudflare", Order: 4,
			Resources: []string{"downloads", "probes", "product_file"}, Breaks: []string{"Download delivery"}})
	} else {
		bp.Components[2].Resources = append(bp.Components[2].Resources, "probes")
	}
	bp.Components = append(bp.Components, core.Component{Key: "email", Label: "Resend", Role: "Receipts & links", Capability: core.CapEmail, Provider: "resend", Order: 5,
		Resources: []string{"email_domain", "email_key", "email_purchase", "email_webhook"}, Breaks: []string{"Receipt emails"}})
	if accounts {
		bp.Components[3].Resources = append(bp.Components[3].Resources, "auth")
		bp.Components[3].Breaks = append(bp.Components[3].Breaks, "Customer accounts")
	}
	if withGitHub {
		bp.Components = append(bp.Components, core.Component{Key: "github", Label: "GitHub", Role: "Code & deploys", Capability: core.CapCICD, Provider: "github", Order: 6,
			Resources: []string{"repo", "code", "gh_cf_token", "gh_cf_account"}, Breaks: []string{"Automatic deploys"}})
	}

	// ── Resources ──
	R := func(r core.ResourceSpec) { bp.Resources = append(bp.Resources, r) }
	R(core.ResourceSpec{Key: "subdomain", Kind: "cloudflare.workers_subdomain", Provider: "cloudflare", Component: "api", Name: "workers.dev",
		Title: "workers.dev address", Keep: true, Props: map[string]any{"subdomain": "{{param:workers_subdomain}}", "why": "Every Worker needs a public address; this reuses your account's workers.dev subdomain."}})
	R(core.ResourceSpec{Key: "probes", Kind: "cloudflare.kv_namespace", Provider: "cloudflare", Component: pick(mode == "shipping", "api", "storage"), Name: name("-probes"),
		Title: "KV namespace for delivery probes", Props: map[string]any{"title": name("-probes"), "why": "Lets Backplane prove Stripe really delivers events to your Worker."}})
	if mode != "shipping" {
		R(core.ResourceSpec{Key: "downloads", Kind: "cloudflare.r2_bucket", Provider: "cloudflare", Component: "storage", Name: name("-downloads"),
			Title: "Private R2 bucket for downloads", Props: map[string]any{"name": name("-downloads"), "why": "Holds the file customers buy. It is private — files leave only through signed, expiring links."}})
		if f := a.str("product_file", ""); f != "" {
			R(core.ResourceSpec{Key: "product_file", Kind: "cloudflare.r2_object", Provider: "cloudflare", Component: "storage", Name: fileKey,
				Title: "Upload " + filepath.Base(f), Props: map[string]any{"bucket": "{{out:downloads.name}}", "key": fileKey, "path": f, "sha256": a.str("product_file_sha", ""), "content_type": "application/octet-stream"}})
		}
	}
	R(core.ResourceSpec{Key: "db", Kind: "supabase.project", Provider: "supabase", Component: "db", Name: name(""),
		Title: "Supabase project", Keep: a.str("existing_ref", "") != "", Adopt: a.str("existing_ref", "") != "",
		Props: map[string]any{"name": name(""), "region": "{{param:region}}", "db_pass": "{{gen:db_password}}", "existing_ref": a.str("existing_ref", ""),
			"why": "Postgres database for orders, downloads and licenses."}, Count: []core.CountItem{{N: 1, Noun: "project"}}})
	tables := []string{"orders", "downloads", "licenses", "webhook_events", "email_log", "bp_events", "backplane_probe"}
	R(core.ResourceSpec{Key: "schema", Kind: "supabase.migration", Provider: "supabase", Component: "db", Name: "commerce_schema",
		Title: "Tables, indexes and row-level security", DependsOn: []string{"db"},
		Props: map[string]any{"name": "commerce_schema", "project_ref": "{{out:db.ref}}", "sql": "{{code:supabase/schema.sql}}", "tables": tables, "policies": 3,
			"columns":  map[string]any{"orders": "id,stripe_session_id,email,status,amount_total,currency", "downloads": "order_id,object_key,expires_at,revoked"},
			"purposes": map[string]any{"orders": "Order records", "downloads": "Download access checks", "webhook_events": "Duplicate-payment protection", "email_log": "Receipt tracking"},
			"why":      "Every table has row-level security on; customers can read only their own purchases."},
		Count: []core.CountItem{{N: len(tables), Noun: "table"}, {N: 3, Noun: "RLS policy"}}})
	R(core.ResourceSpec{Key: "db_key", Kind: "supabase.api_key", Provider: "supabase", Component: "db", Name: "backplane_worker",
		Title: "Dedicated secret key for the Worker", DependsOn: []string{"db"},
		Props: map[string]any{"name": "backplane_worker", "project_ref": "{{out:db.ref}}", "why": "A separate key you can revoke without touching anything else."},
		Count: []core.CountItem{{N: 1, Noun: "secret key"}}})
	priceKeys := addCatalog(&bp, items, currency, "stripe")
	first := PriceKey(items[0].Key, items[0].Billing)
	R(core.ResourceSpec{Key: "email_domain", Kind: "resend.domain", Provider: "resend", Component: "email", Name: domain, Title: "Sending domain " + domain,
		Props: map[string]any{"name": "{{param:domain}}", "auto_dns": true, "wait_seconds": 90, "why": "Receipts come from your own domain, with SPF and DKIM so they reach inboxes."}})
	R(core.ResourceSpec{Key: "email_key", Kind: "resend.api_key", Provider: "resend", Component: "email", Name: name("-worker"), Title: "Send-only email key for the Worker",
		Props: map[string]any{"name": name("-worker"), "domain_id": "{{out:email_domain.id}}", "why": "The Worker can only send from your domain — it cannot read contacts or change settings."}})
	R(core.ResourceSpec{Key: "email_purchase", Kind: "resend.template", Provider: "resend", Component: "email", Name: "Purchase confirmation", Title: "Email template: purchase confirmation",
		Props: map[string]any{"name": "{{param:slug}} {{envshort}} purchase", "alias": "{{param:slug}}-{{envshort}}-purchase", "subject": "Your {{{PRODUCT_NAME}}} download",
			"html": "{{code:emails/purchase.html}}", "variables": templateVars("purchase")}})

	secrets := map[string]any{
		"STRIPE_SECRET_KEY":       "{{conn:stripe.worker_key}}",
		"SUPABASE_SECRET_KEY":     "{{secret:db_key.key}}",
		"RESEND_API_KEY":          "{{secret:email_key.token}}",
		"DOWNLOAD_SIGNING_SECRET": "{{gen:download_signing}}",
		"BACKPLANE_PROBE_TOKEN":   "{{gen:probe_token}}",
	}
	vars := map[string]any{
		"WORKER_NAME": name("-api"), "PRODUCT_NAME": "{{param:product_name}}", "PRODUCT_FILE_KEY": "{{param:product_file_key}}", "PRICE_ID": "{{out:" + first + ".id}}",
		"CATALOG": catalogVar(items, currency), "MAX_QUANTITY": strconv.Itoa(max(1, spec.MaxQuantity)), "CART": strconv.FormatBool(spec.Quantity),
		"SUPABASE_URL": "{{out:db.url}}", "FROM_EMAIL": "{{param:from_email}}", "SUPPORT_EMAIL": "{{param:support_email}}", "BUSINESS_NAME": "{{param:business_name}}",
		"SITE_ORIGIN": "{{param:site_origin}}", "SUCCESS_URL": "{{param:success_url}}", "CANCEL_URL": "{{param:cancel_url}}", "DOWNLOAD_TTL_HOURS": "{{param:download_ttl_hours}}",
		"MAX_DOWNLOADS": "{{param:max_downloads}}", "LICENSE_MODE": "{{param:license_mode}}", "FULFILLMENT_MODE": "{{param:fulfillment_mode}}",
		"SHIP_COUNTRIES": "{{param:ship_countries}}", "RESEND_TEMPLATE_PURCHASE": "{{out:email_purchase.id}}", "CODE_VERSION": "{{codever:worker/src/index.js}}",
	}
	bindings := []any{map[string]any{"type": "kv_namespace", "name": "PROBES", "namespace_id": "{{out:probes.id}}", "purpose": "Stripe delivery probes"}}
	deps := append([]string{"subdomain", "probes", "schema", "db_key", "email_key", "email_purchase"}, priceKeys...)
	if mode != "shipping" {
		bindings = append(bindings, map[string]any{"type": "r2_bucket", "name": "DOWNLOADS", "bucket_name": "{{out:downloads.name}}", "purpose": "Secure download delivery"})
		deps = append(deps, "downloads")
	}
	R(core.ResourceSpec{Key: "api", Kind: "cloudflare.worker", Provider: "cloudflare", Component: "api", Name: name("-api"), Title: "Worker API (checkout, webhooks, downloads)",
		DependsOn: deps,
		Props: map[string]any{"name": name("-api"), "code": "{{code:worker/src/index.js}}", "compatibility_date": CompatibilityDate, "bindings": bindings, "vars": vars, "secrets": secrets,
			"crons": []any{"17 3 * * *"}, "workers_dev": true, "subdomain_resource": "subdomain",
			"purposes": map[string]any{"DOWNLOADS": "Secure download delivery", "PROBES": "Stripe delivery probes", "STRIPE_SECRET_KEY": "Checkout", "SUPABASE_SECRET_KEY": "Order records",
				"RESEND_API_KEY": "Receipt emails", "DOWNLOAD_SIGNING_SECRET": "Download links", "BACKPLANE_PROBE_TOKEN": "Health checks", "PRICE_ID": "Checkout", "CATALOG": "Checkout", "SUPABASE_URL": "Order records"},
			"why": "Runs checkout, verifies Stripe's signature on every payment event, records orders, issues signed download links and sends the email."},
		Count: []core.CountItem{{N: 1, Noun: "Worker"}, {N: len(bindings), Noun: "binding"}, {N: len(secrets) + 2, Noun: "secret"}}})
	webhookEvents := []any{"checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.expired", "charge.refunded"}
	if hasRecurring(items) {
		// Subscriptions: a canceled subscription ends access to downloads and licenses.
		webhookEvents = append(webhookEvents, "customer.subscription.deleted")
	}
	R(core.ResourceSpec{Key: "webhook", Kind: "stripe.webhook_endpoint", Provider: "stripe", Component: "stripe", Name: "Worker webhook", Title: "Webhook → Worker",
		DependsOn: []string{"api"},
		Props: map[string]any{"url": "{{out:api.url}}/stripe/webhook", "events": webhookEvents, "description": "Backplane: {{param:project_name}} ({{env}})", "purpose": "Payment notifications",
			"why": "Stripe tells the Worker the moment a payment succeeds, is refunded, or a checkout expires."}})
	R(core.ResourceSpec{Key: "api_webhook_secret", Kind: "cloudflare.worker_secret", Provider: "cloudflare", Component: "api", Name: "STRIPE_WEBHOOK_SECRET",
		Title: "Worker secret STRIPE_WEBHOOK_SECRET", DependsOn: []string{"api", "webhook"},
		Props: map[string]any{"script": "{{out:api.name}}", "name": "STRIPE_WEBHOOK_SECRET", "value": "{{secret:webhook.secret}}", "purpose": "Payment notifications"}, Count: []core.CountItem{}})
	R(core.ResourceSpec{Key: "email_webhook", Kind: "resend.webhook", Provider: "resend", Component: "email", Name: "Delivery events", Title: "Delivery-event webhook → Worker",
		DependsOn: []string{"api"},
		Props:     map[string]any{"endpoint": "{{out:api.url}}/resend/webhook", "events": []any{"email.delivered", "email.bounced", "email.complained", "email.delivery_delayed", "email.failed"}}})
	R(core.ResourceSpec{Key: "api_resend_secret", Kind: "cloudflare.worker_secret", Provider: "cloudflare", Component: "api", Name: "RESEND_WEBHOOK_SECRET",
		Title: "Worker secret RESEND_WEBHOOK_SECRET", DependsOn: []string{"api", "email_webhook"},
		Props: map[string]any{"script": "{{out:api.name}}", "name": "RESEND_WEBHOOK_SECRET", "value": "{{secret:email_webhook.secret}}", "purpose": "Bounce tracking"}, Count: []core.CountItem{}})
	if accounts {
		R(core.ResourceSpec{Key: "auth", Kind: "supabase.auth_config", Provider: "supabase", Component: "db", Name: "Auth settings", Title: "Customer sign-in, emails via Resend",
			DependsOn: []string{"db", "email_key"},
			Props: map[string]any{"project_ref": "{{out:db.ref}}", "smtp_pass": "{{secret:email_key.token}}",
				"settings": map[string]any{"site_url": site, "uri_allow_list": site + "/**", "external_email_enabled": true, "mailer_autoconfirm": false, "password_min_length": 10,
					"smtp_host": "smtp.resend.com", "smtp_port": "465", "smtp_user": "resend", "smtp_admin_email": fromEmail, "smtp_sender_name": business},
				"why": "Customers can sign in to see past purchases. Verification emails go through Resend, not Supabase's rate-limited default."}})
	}
	if withGitHub {
		R(core.ResourceSpec{Key: "repo", Kind: "github.repo", Provider: "github", Component: "github", Name: repo, Title: "Private repository " + repo,
			Props: map[string]any{"name": "{{param:github_repo}}", "description": "Backend for " + projectName + " — generated and monitored by Backplane"}})
		files := map[string]any{
			"worker/src/index.js":                   "{{code:worker/src/index.js}}",
			"worker/wrangler.jsonc":                 "{{codet:worker/wrangler.jsonc}}",
			"supabase/migrations/0001_commerce.sql": "{{code:supabase/schema.sql}}",
			".github/workflows/deploy.yml":          "{{code:.github/workflows/deploy.yml}}",
			"emails/purchase.html":                  "{{code:emails/purchase.html}}",
			"README.md":                             "{{codet:README.md}}",
			".gitignore":                            "{{code:.gitignore}}",
		}
		R(core.ResourceSpec{Key: "code", Kind: "github.files", Provider: "github", Component: "github", Name: "main", Title: "Commit generated code to main",
			DependsOn: append([]string{"repo", "api", "probes", "email_purchase"}, priceKeys...),
			Props:     map[string]any{"repo": "{{out:repo.full_name}}", "branch": "main", "files": files, "message": "Backplane: generated backend for {{param:project_name}} ({{env}})"}})
		R(core.ResourceSpec{Key: "gh_cf_token", Kind: "github.actions_secret", Provider: "github", Component: "github", Name: "CLOUDFLARE_API_TOKEN", Title: "Deploy secret CLOUDFLARE_API_TOKEN",
			DependsOn: []string{"repo"}, Props: map[string]any{"repo": "{{out:repo.full_name}}", "name": "CLOUDFLARE_API_TOKEN", "value": "{{conn:cloudflare.deploy_token}}"}})
		R(core.ResourceSpec{Key: "gh_cf_account", Kind: "github.actions_secret", Provider: "github", Component: "github", Name: "CLOUDFLARE_ACCOUNT_ID", Title: "Deploy secret CLOUDFLARE_ACCOUNT_ID",
			DependsOn: []string{"repo"}, Props: map[string]any{"repo": "{{out:repo.full_name}}", "name": "CLOUDFLARE_ACCOUNT_ID", "value": "{{connset:cloudflare.account_id}}"}})
	}

	// ── Links (every one is tested) ──
	L := func(l core.LinkSpec) { bp.Links = append(bp.Links, l) }
	L(core.LinkSpec{Key: "buy", From: "customer", To: "api", Label: "Buy → /checkout", Kind: "http", Check: "checkout", Critical: true, Breaks: []string{"Checkout"}})
	L(core.LinkSpec{Key: "api_stripe", From: "api", To: "stripe", Label: "Creates Checkout Session", Kind: "http", Check: "worker_probe:stripe", Critical: true, Breaks: []string{"Checkout"}})
	L(core.LinkSpec{Key: "stripe_api", From: "stripe", To: "api", Label: "checkout.session.completed", Kind: "webhook", Check: "stripe_webhook", Critical: true, Breaks: []string{"Order processing", "Receipt emails", "Download delivery"}})
	L(core.LinkSpec{Key: "api_db", From: "api", To: "db", Label: "Orders (PostgREST)", Kind: "sql", Check: "worker_probe:supabase", Critical: true, Breaks: []string{"Order records", "Download access checks"}})
	if mode != "shipping" {
		L(core.LinkSpec{Key: "api_storage", From: "api", To: "storage", Label: "R2 binding DOWNLOADS", Kind: "binding", Check: "worker_probe:r2", Critical: true, Breaks: []string{"Download delivery"}})
	}
	L(core.LinkSpec{Key: "api_email", From: "api", To: "email", Label: "Receipt + download link", Kind: "email", Check: "worker_probe:resend", Critical: true, Breaks: []string{"Receipt emails"}})
	L(core.LinkSpec{Key: "email_api", From: "email", To: "api", Label: "Delivery events", Kind: "webhook", Check: "resend_webhook", Breaks: []string{"Bounce tracking"}})
	if accounts {
		L(core.LinkSpec{Key: "db_email", From: "db", To: "email", Label: "Sign-in emails (SMTP)", Kind: "smtp", Check: "supabase_smtp", Breaks: []string{"Customer accounts"}})
	}
	if withGitHub {
		L(core.LinkSpec{Key: "github_api", From: "github", To: "api", Label: "Deploy on push", Kind: "deploy", Check: "github_deploy", Breaks: []string{"Automatic deploys"}})
	}
	if mode != "shipping" {
		bp.Scenarios = []core.ScenarioSpec{{Key: "purchase", Title: "Customer buys and downloads", Check: "store_purchase",
			Steps: []string{"Create test customer", "Test payment", "Stripe webhook fires", "Worker receives event", "Order written to database", "Temporary download generated", "Email process triggered", "Everything verified"}}}
	} else {
		bp.Scenarios = []core.ScenarioSpec{{Key: "purchase", Title: "Customer orders a shipped product", Check: "store_purchase",
			Steps: []string{"Create test customer", "Test payment", "Stripe webhook fires", "Worker receives event", "Order written to database", "Shipping details recorded", "Email process triggered", "Everything verified"}}}
	}
	bp.Generated = []core.GeneratedFileSpec{
		{Path: "worker/src/index.js", Role: "generated", Language: "javascript"},
		{Path: "worker/wrangler.jsonc", Role: "system-config", Language: "jsonc"},
		{Path: "supabase/schema.sql", Role: "generated", Language: "sql"},
		{Path: "emails/purchase.html", Role: "generated", Language: "html"},
		{Path: ".github/workflows/deploy.yml", Role: "system-config", Language: "yaml"},
		{Path: "README.md", Role: "generated", Language: "markdown"},
		{Path: ".gitignore", Role: "system-config", Language: "text"},
	}
	bp.Notes = append(bp.Notes, "Sells: "+catalogSummary(items, currency)+". Checkout takes an item and a quantity; the price always comes from Stripe, never from the browser.",
		"Stripe events carry a verified signature; the Worker rejects anything else.",
		"Every database table has row-level security enabled.")
	if mode == "shipping" {
		return bp, bp.ValidateGraph()
	}
	bp.Notes = append(bp.Notes,
		"Download links are HMAC-signed, expire after "+params["download_ttl_hours"].(string)+" hours and are revoked automatically on refund.")
	return bp, bp.ValidateGraph()
}

func hasRecurring(items []Item) bool {
	for _, it := range items {
		if it.Billing != BillOnce {
			return true
		}
	}
	return false
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func sanitizeFile(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "product.bin"
	}
	return b.String()
}

func moneyLabel(cents int64, currency string) string {
	s := fmt.Sprintf("%d.%02d", cents/100, cents%100)
	s = strings.TrimSuffix(s, ".00")
	switch strings.ToLower(currency) {
	case "usd":
		return "$" + s
	case "eur":
		return "€" + s
	case "gbp":
		return "£" + s
	}
	return s + " " + strings.ToUpper(currency)
}
