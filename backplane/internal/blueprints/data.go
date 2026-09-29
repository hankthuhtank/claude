package blueprints

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"safisolutions.org/backplane/internal/core"
)

// TableSpec describes one table of a data template.
type TableSpec struct {
	Name    string
	Purpose string
	Columns []string // SQL column definitions (id, owner_id and created_at are added)
	// Access: owner (rows belong to the signed-in user), public-read (anyone
	// may read, owner writes), members (via a membership table), server
	// (only the Worker's server key; no browser access).
	Access   string
	Members  string // for Access=members: "<membership table>.<fk column>"
	Extra    string // extra SQL appended after the table
	Realtime bool
}

func has(t Template, c core.Capability) bool {
	for _, x := range t.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

var dataTables = map[string][]TableSpec{
	"saas": {
		{Name: "profiles", Purpose: "User profiles", Access: "owner", Columns: []string{"display_name text", "avatar_path text"}},
		{Name: "projects", Purpose: "User projects", Access: "owner", Columns: []string{"title text not null", "data jsonb not null default '{}'::jsonb"}},
	},
	"free-saas": {
		{Name: "profiles", Purpose: "User profiles", Access: "owner", Columns: []string{"display_name text"}},
		{Name: "items", Purpose: "Saved items", Access: "owner", Columns: []string{"title text not null", "body text"}},
	},
	"subscription-saas": {
		{Name: "profiles", Purpose: "User profiles", Access: "owner", Columns: []string{"display_name text"}},
		{Name: "subscriptions", Purpose: "Subscription status", Access: "owner-read", Columns: []string{"stripe_customer_id text", "stripe_subscription_id text unique", "status text not null default 'inactive'", "price_id text", "current_period_end timestamptz"}},
		{Name: "projects", Purpose: "User projects", Access: "owner", Columns: []string{"title text not null", "data jsonb not null default '{}'::jsonb"}},
	},
	"membership": {
		{Name: "profiles", Purpose: "Member profiles", Access: "owner", Columns: []string{"display_name text"}},
		{Name: "subscriptions", Purpose: "Membership status", Access: "owner-read", Columns: []string{"stripe_customer_id text", "stripe_subscription_id text unique", "status text not null default 'inactive'", "price_id text", "current_period_end timestamptz"}},
		{Name: "posts", Purpose: "Members-only posts", Access: "server-members", Columns: []string{"title text not null", "body text not null", "published boolean not null default false"}},
	},
	"marketplace": {
		{Name: "profiles", Purpose: "Seller and buyer profiles", Access: "owner", Columns: []string{"display_name text", "is_seller boolean not null default false"}},
		{Name: "listings", Purpose: "Items for sale", Access: "public-read", Columns: []string{"title text not null", "description text", "price_cents bigint not null check (price_cents >= 50)", "currency text not null default 'usd'", "active boolean not null default true"}},
		{Name: "purchases", Purpose: "Buyer orders", Access: "owner", Columns: []string{"listing_id uuid not null", "amount_cents bigint not null default 0", "paid boolean not null default false", "stripe_session_id text unique"}},
	},
	"mobile-app": {
		{Name: "profiles", Purpose: "User profiles", Access: "owner", Columns: []string{"display_name text", "push_token text"}},
		{Name: "entries", Purpose: "Synced user data", Access: "owner", Realtime: true, Columns: []string{"kind text not null", "payload jsonb not null default '{}'::jsonb", "updated_at timestamptz not null default now()"}},
	},
	"crm": {
		{Name: "contacts", Purpose: "Contacts", Access: "owner", Columns: []string{"name text not null", "email text", "phone text", "company text", "stage text not null default 'lead'"}},
		{Name: "deals", Purpose: "Deals", Access: "owner", Columns: []string{"contact_id uuid", "title text not null", "value_cents bigint not null default 0", "status text not null default 'open'"}},
		{Name: "activities", Purpose: "Calls, emails and notes", Access: "owner", Columns: []string{"contact_id uuid", "kind text not null", "note text", "due_at timestamptz"}},
	},
	"client-portal": {
		{Name: "projects", Purpose: "Client projects", Access: "owner", Columns: []string{"title text not null", "status text not null default 'active'"}},
		{Name: "documents", Purpose: "Shared documents", Access: "owner", Columns: []string{"project_id uuid", "title text not null", "storage_path text"}},
		{Name: "invoices", Purpose: "Invoices", Access: "owner-read", Columns: []string{"number text not null", "amount_cents bigint not null", "due_date date", "paid boolean not null default false"}},
	},
	"contractor-portal": {
		{Name: "jobs", Purpose: "Jobs", Access: "owner", Columns: []string{"title text not null", "address text", "scheduled_for timestamptz", "status text not null default 'scheduled'"}},
		{Name: "job_photos", Purpose: "Job-site photos", Access: "owner", Columns: []string{"job_id uuid not null", "storage_path text not null", "caption text"}},
		{Name: "signoffs", Purpose: "Customer sign-offs", Access: "owner", Columns: []string{"job_id uuid not null", "signed_by text not null", "signed_at timestamptz not null default now()"}},
	},
	"booking": {
		{Name: "services", Purpose: "Bookable services", Access: "public-read", Columns: []string{"title text not null", "minutes int not null default 30", "deposit_cents bigint not null default 0", "active boolean not null default true"}},
		{Name: "bookings", Purpose: "Appointments", Access: "owner", Columns: []string{"service_id uuid", "starts_at timestamptz not null", "email text", "status text not null default 'pending'", "paid boolean not null default false", "reminder_sent_at timestamptz", "stripe_session_id text unique"}},
	},
	"restaurant": {
		{Name: "menu_items", Purpose: "Menu", Access: "public-read", Columns: []string{"name text not null", "price_cents bigint not null", "available boolean not null default true"}},
		{Name: "orders", Purpose: "Customer orders", Access: "owner", Realtime: true, Columns: []string{"items jsonb not null default '[]'::jsonb", "total_cents bigint not null default 0", "status text not null default 'received'", "paid boolean not null default false", "stripe_session_id text unique"}},
	},
	"file-sharing": {
		{Name: "files", Purpose: "Uploaded files", Access: "owner", Columns: []string{"name text not null", "storage_path text not null", "bytes bigint not null default 0"}},
		{Name: "shares", Purpose: "Expiring share links", Access: "owner", Columns: []string{"file_id uuid not null", "token_hash text not null unique", "expires_at timestamptz not null", "downloads int not null default 0"}},
	},
	"chat": {
		{Name: "rooms", Purpose: "Chat rooms", Access: "owner", Columns: []string{"title text not null"}},
		{Name: "room_members", Purpose: "Room membership", Access: "owner", Columns: []string{"room_id uuid not null", "member_id uuid not null"},
			Extra: "create unique index if not exists room_members_unique on public.room_members (room_id, member_id);"},
		{Name: "messages", Purpose: "Messages", Access: "members", Members: "room_members.room_id", Realtime: true, Columns: []string{"room_id uuid not null", "body text not null check (length(body) <= 4000)"}},
	},
	"game": {
		{Name: "players", Purpose: "Player profiles", Access: "owner", Columns: []string{"nickname text not null"}},
		{Name: "saves", Purpose: "Save games", Access: "owner", Columns: []string{"slot int not null default 1", "state jsonb not null default '{}'::jsonb"}},
		{Name: "scores", Purpose: "Leaderboard", Access: "public-read", Realtime: true, Columns: []string{"board text not null default 'global'", "score bigint not null"}},
	},
	"api-service": {
		{Name: "api_keys", Purpose: "Developer API keys", Access: "owner", Columns: []string{"label text not null", "key_hash text not null unique", "key_hint text not null", "revoked boolean not null default false", "rate_per_minute int not null default 60"}},
		{Name: "api_usage", Purpose: "Usage log", Access: "owner-read", Columns: []string{"api_key_id uuid", "path text not null", "status int not null", "at timestamptz not null default now()"}},
	},
	"webhook-relay": {
		{Name: "sources", Purpose: "Webhook sources", Access: "owner", Columns: []string{"name text not null", "signing_secret text not null", "forward_url text"}},
		{Name: "deliveries", Purpose: "Received webhooks", Access: "owner-read", Columns: []string{"source_id uuid not null", "payload jsonb not null", "verified boolean not null default false", "forwarded_status int", "forwarded_at timestamptz"}},
	},
	"newsletter": {
		{Name: "subscribers", Purpose: "Subscribers", Access: "server", Columns: []string{"email text not null unique", "status text not null default 'pending'", "confirm_token_hash text", "confirmed_at timestamptz"}},
	},
	"support": {
		{Name: "tickets", Purpose: "Tickets", Access: "owner", Columns: []string{"subject text not null", "status text not null default 'open'", "priority text not null default 'normal'"}},
		{Name: "ticket_messages", Purpose: "Ticket replies", Access: "owner", Columns: []string{"ticket_id uuid not null", "body text not null", "from_staff boolean not null default false"}},
	},
	"ai-rag": {
		{Name: "documents", Purpose: "Source documents", Access: "owner", Columns: []string{"title text not null", "storage_path text"}},
		{Name: "chunks", Purpose: "Embedded chunks", Access: "owner", Columns: []string{"document_id uuid not null", "content text not null", "embedding vector(1536)"},
			Extra: "create index if not exists chunks_embedding_idx on public.chunks using hnsw (embedding vector_cosine_ops);"},
	},
	"admin-dashboard": {
		{Name: "staff_roles", Purpose: "Staff roles", Access: "server", Columns: []string{"user_id uuid not null unique", "role text not null check (role in ('viewer','editor','admin'))"}},
		{Name: "records", Purpose: "Operational records", Access: "owner", Columns: []string{"title text not null", "status text not null default 'new'", "data jsonb not null default '{}'::jsonb"}},
		{Name: "audit_log", Purpose: "Audit log", Access: "server", Columns: []string{"actor uuid", "action text not null", "target text", "at timestamptz not null default now()"}},
	},
}

func dataQuestions(t Template) []Question {
	q := []Question{{Key: "app_url", Label: "Your app's web address", Kind: "url", Help: "Used for sign-in redirects, e.g. https://app.example.com."}}
	if has(t, core.CapEmail) || has(t, core.CapAuth) {
		q = append(q, Question{Key: "domain", Label: "Email domain", Kind: "domain", Required: has(t, core.CapEmail), Help: "Sign-in and notification emails come from this domain."},
			Question{Key: "business_name", Label: "Business or app name (shown in emails)", Kind: "text"})
	}
	if has(t, core.CapPayments) {
		label := "Price"
		if t.ID == "subscription-saas" || t.ID == "membership" {
			label = "Monthly price"
		}
		q = append(q, Question{Key: "price", Label: label, Kind: "money", Default: 19.0}, Question{Key: "currency", Label: "Currency", Kind: "select", Default: "usd", Options: []string{"usd", "eur", "gbp", "cad", "aud"}})
	}
	q = append(q, Question{Key: "include_github", Label: "Keep the code in GitHub and deploy automatically", Kind: "toggle", Default: true},
		Question{Key: "region", Label: "Database region", Kind: "select", Default: "us-east-1", Advanced: true, Options: []string{"us-east-1", "us-west-1", "eu-west-1", "eu-central-1", "ap-southeast-1", "ap-northeast-1", "sa-east-1"}},
		Question{Key: "workers_subdomain", Label: "workers.dev subdomain (only if your account has none)", Kind: "text", Advanced: true})
	return q
}

func dataFlow(t Template) []string {
	flow := []string{"Your app"}
	if has(t, core.CapAuth) {
		flow = append(flow, "Supabase Auth")
	}
	flow = append(flow, "Supabase Postgres (RLS)")
	if has(t, core.CapStorage) {
		flow = append(flow, "Supabase Storage")
	}
	if has(t, core.CapPayments) {
		flow = append(flow, "Stripe → Worker webhook")
	}
	if has(t, core.CapEmail) {
		flow = append(flow, "Resend")
	}
	return flow
}

// schemaSQL generates the template's schema with row-level security.
func schemaSQL(t Template) (string, []string, int) {
	tables := dataTables[t.ID]
	var b strings.Builder
	fmt.Fprintf(&b, "-- %s schema — generated by Backplane. Row level security is on for every table.\n\n", t.Name)
	b.WriteString("create extension if not exists pgcrypto;\n")
	if has(t, core.CapVector) {
		b.WriteString("create extension if not exists vector;\n")
	}
	b.WriteString("\n")
	policies := 0
	var names []string
	for _, tb := range tables {
		names = append(names, tb.Name)
		fmt.Fprintf(&b, "create table if not exists public.%s (\n  id uuid primary key default gen_random_uuid(),\n", tb.Name)
		if tb.Access != "server" {
			b.WriteString("  owner_id uuid not null default auth.uid() references auth.users (id) on delete cascade,\n")
		}
		for _, c := range tb.Columns {
			fmt.Fprintf(&b, "  %s,\n", c)
		}
		b.WriteString("  created_at timestamptz not null default now()\n);\n")
		fmt.Fprintf(&b, "alter table public.%s enable row level security;\n", tb.Name)
		if tb.Access != "server" {
			fmt.Fprintf(&b, "create index if not exists %s_owner_idx on public.%s (owner_id);\n", tb.Name, tb.Name)
		}
		pol := func(name, cmd, using, check string) {
			fmt.Fprintf(&b, "drop policy if exists \"%s\" on public.%s;\n", name, tb.Name)
			fmt.Fprintf(&b, "create policy \"%s\" on public.%s for %s to authenticated", name, tb.Name, cmd)
			if using != "" {
				fmt.Fprintf(&b, " using (%s)", using)
			}
			if check != "" {
				fmt.Fprintf(&b, " with check (%s)", check)
			}
			b.WriteString(";\n")
			policies++
		}
		own := "owner_id = (select auth.uid())"
		switch tb.Access {
		case "owner":
			pol(tb.Name+": owners read", "select", own, "")
			pol(tb.Name+": owners insert", "insert", "", own)
			pol(tb.Name+": owners update", "update", own, own)
			pol(tb.Name+": owners delete", "delete", own, "")
		case "owner-read":
			pol(tb.Name+": owners read", "select", own, "")
		case "public-read":
			fmt.Fprintf(&b, "drop policy if exists \"%s: anyone reads\" on public.%s;\n", tb.Name, tb.Name)
			fmt.Fprintf(&b, "create policy \"%s: anyone reads\" on public.%s for select to anon, authenticated using (true);\n", tb.Name, tb.Name)
			policies++
			pol(tb.Name+": owners insert", "insert", "", own)
			pol(tb.Name+": owners update", "update", own, own)
			pol(tb.Name+": owners delete", "delete", own, "")
		case "members":
			parts := strings.SplitN(tb.Members, ".", 2)
			member := fmt.Sprintf("exists (select 1 from public.%s m where m.%s = %s.room_id and m.member_id = (select auth.uid()))", parts[0], parts[1], tb.Name)
			pol(tb.Name+": members read", "select", member+" or "+own, "")
			pol(tb.Name+": members write", "insert", "", "("+member+") and "+own)
			pol(tb.Name+": authors delete", "delete", own, "")
		case "server-members":
			active := "exists (select 1 from public.subscriptions s where s.owner_id = (select auth.uid()) and s.status in ('active','trialing'))"
			pol(tb.Name+": active members read published", "select", "published and "+active, "")
		}
		if tb.Extra != "" {
			b.WriteString(tb.Extra + "\n")
		}
		if tb.Realtime {
			fmt.Fprintf(&b, "do $$ begin\n  if not exists (select 1 from pg_publication_tables where pubname = 'supabase_realtime' and schemaname = 'public' and tablename = '%s') then\n    alter publication supabase_realtime add table public.%s;\n  end if;\nend $$;\n", tb.Name, tb.Name)
		}
		b.WriteString("\n")
	}
	if has(t, core.CapVector) {
		b.WriteString(`-- Similarity search that respects row level security (security invoker).
create or replace function public.match_chunks(query_embedding vector(1536), match_count int default 5)
returns table (id uuid, document_id uuid, content text, similarity float)
language sql stable security invoker set search_path = public as $$
  select c.id, c.document_id, c.content, 1 - (c.embedding <=> query_embedding) as similarity
  from public.chunks c
  where c.embedding is not null
  order by c.embedding <=> query_embedding
  limit match_count;
$$;

`)
	}
	b.WriteString(`-- Synthetic health tests write here (server key only) and nowhere else.
create table if not exists public.backplane_probe (id text primary key, note text, created_at timestamptz not null default now());
alter table public.backplane_probe enable row level security;
create table if not exists public.bp_events (id bigint generated always as identity primary key, at timestamptz not null default now(), source text not null, kind text not null, message text not null, ref text, is_probe boolean not null default false);
alter table public.bp_events enable row level security;
`)
	names = append(names, "backplane_probe", "bp_events")
	return b.String(), names, policies
}

// isolationTable picks the first owner-scoped table for the data-isolation test.
func isolationTable(t Template) (string, string) {
	for _, tb := range dataTables[t.ID] {
		if tb.Access == "owner" {
			for _, c := range tb.Columns {
				f := strings.Fields(c)
				if len(f) >= 2 && strings.Contains(c, "not null") && !strings.Contains(c, "default") && (f[1] == "text") {
					return tb.Name, f[0]
				}
			}
			return tb.Name, ""
		}
	}
	return "", ""
}

func buildData(t Template, projectName string, a Answers) (core.Blueprint, error) {
	slug := Slug(a.str("slug", projectName))
	name := func(suffix string) string { return "{{param:slug}}-{{envshort}}" + suffix }
	appURL := strings.TrimSuffix(a.str("app_url", ""), "/")
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(a.str("domain", ""), "https://"), "http://"), "/"))
	business := a.str("business_name", projectName)
	withEmail := has(t, core.CapEmail) && domain != ""
	if has(t, core.CapEmail) && domain == "" {
		return core.Blueprint{}, fmt.Errorf("an email domain is required for this template")
	}
	withAuth := has(t, core.CapAuth)
	withStorage := has(t, core.CapStorage)
	withPay := has(t, core.CapPayments)
	withGitHub := a.boolean("include_github", true)
	subscription := t.ID == "subscription-saas" || t.ID == "membership"
	cents := int64(math.Round(a.num("price", 19) * 100))
	isoTable, isoColumn := isolationTable(t)
	params := map[string]any{
		"project_name": projectName, "slug": slug, "app_url": appURL, "domain": domain, "business_name": business,
		"from_email": business + " <notifications@" + domain + ">", "region": a.str("region", "us-east-1"), "include_github": withGitHub,
		"github_repo": a.str("github_repo", slug+"-backend"), "workers_subdomain": a.str("workers_subdomain", ""), "compatibility_date": CompatibilityDate,
		"price_cents": cents, "currency": a.str("currency", "usd"), "template": t.ID, "isolation_table": isoTable, "isolation_column": isoColumn,
		"subscription": subscription, "product_name": projectName,
	}
	bp := core.Blueprint{Version: 1, TemplateID: t.ID, Params: params}
	bp.Components = []core.Component{{Key: "app", Label: "Your app", Role: "Web or mobile client", External: true, Order: 0}}
	order := 1
	if withPay {
		bp.Components = append(bp.Components, core.Component{Key: "stripe", Label: "Stripe", Role: "Payments", Capability: core.CapPayments, Provider: "stripe", Order: order,
			Resources: []string{"product", "price", "webhook"}, Breaks: []string{"Payments", "Subscription status updates"}})
		order++
	}
	bp.Components = append(bp.Components, core.Component{Key: "api", Label: "Cloudflare Worker", Role: "Server-side API", Capability: core.CapAPI, Provider: "cloudflare", Order: order,
		Resources: []string{"subdomain", "probes", "api", "api_webhook_secret"}, Breaks: []string{"Server-side tasks", "Webhooks"}})
	order++
	dbBreaks := []string{"Reading and saving data"}
	if withAuth {
		dbBreaks = append(dbBreaks, "Sign-up and sign-in")
	}
	bp.Components = append(bp.Components, core.Component{Key: "db", Label: "Supabase", Role: "Database & sign-in", Capability: core.CapDatabase, Provider: "supabase", Order: order,
		Resources: []string{"db", "schema", "db_key"}, Breaks: dbBreaks})
	order++
	if withStorage {
		bp.Components = append(bp.Components, core.Component{Key: "storage", Label: "Supabase Storage", Role: "Files", Capability: core.CapStorage, Provider: "supabase", Order: order,
			Resources: []string{"files"}, Breaks: []string{"File uploads and downloads"}})
		order++
	}
	if withEmail {
		bp.Components = append(bp.Components, core.Component{Key: "email", Label: "Resend", Role: "Email", Capability: core.CapEmail, Provider: "resend", Order: order,
			Resources: []string{"email_domain", "email_key"}, Breaks: []string{"Sign-in and notification emails"}})
		order++
	}
	if withGitHub {
		bp.Components = append(bp.Components, core.Component{Key: "github", Label: "GitHub", Role: "Code & deploys", Capability: core.CapCICD, Provider: "github", Order: order,
			Resources: []string{"repo", "code", "gh_cf_token", "gh_cf_account"}, Breaks: []string{"Automatic deploys"}})
	}
	R := func(r core.ResourceSpec) { bp.Resources = append(bp.Resources, r) }
	R(core.ResourceSpec{Key: "subdomain", Kind: "cloudflare.workers_subdomain", Provider: "cloudflare", Component: "api", Name: "workers.dev", Title: "workers.dev address", Keep: true,
		Props: map[string]any{"subdomain": "{{param:workers_subdomain}}"}})
	R(core.ResourceSpec{Key: "probes", Kind: "cloudflare.kv_namespace", Provider: "cloudflare", Component: "api", Name: name("-probes"), Title: "KV namespace for probes and rate limits",
		Props: map[string]any{"title": name("-probes")}})
	R(core.ResourceSpec{Key: "db", Kind: "supabase.project", Provider: "supabase", Component: "db", Name: name(""), Title: "Supabase project",
		Props: map[string]any{"name": name(""), "region": "{{param:region}}", "db_pass": "{{gen:db_password}}"}, Count: []core.CountItem{{N: 1, Noun: "project"}}})
	sql, tables, policies := schemaSQL(t)
	_ = sql
	R(core.ResourceSpec{Key: "schema", Kind: "supabase.migration", Provider: "supabase", Component: "db", Name: Slug(t.ID) + "_schema", Title: "Tables and row-level security",
		DependsOn: []string{"db"}, Props: map[string]any{"name": strings.ReplaceAll(t.ID, "-", "_") + "_schema", "project_ref": "{{out:db.ref}}", "sql": "{{code:supabase/schema.sql}}",
			"tables": toAny(tables), "policies": policies}, Count: []core.CountItem{{N: len(tables), Noun: "table"}, {N: policies, Noun: "RLS policy"}}})
	R(core.ResourceSpec{Key: "db_key", Kind: "supabase.api_key", Provider: "supabase", Component: "db", Name: "backplane_worker", Title: "Dedicated secret key for the Worker",
		DependsOn: []string{"db"}, Props: map[string]any{"name": "backplane_worker", "project_ref": "{{out:db.ref}}"}})
	if withStorage {
		R(core.ResourceSpec{Key: "files", Kind: "supabase.storage_bucket", Provider: "supabase", Component: "storage", Name: "files", Title: "Private storage bucket “files”",
			DependsOn: []string{"db"}, Props: map[string]any{"name": "files", "public": false, "project_ref": "{{out:db.ref}}", "file_size_limit": 52428800}})
	}
	secrets := map[string]any{"SUPABASE_SECRET_KEY": "{{secret:db_key.key}}", "BACKPLANE_PROBE_TOKEN": "{{gen:probe_token}}", "SIGNING_SECRET": "{{gen:signing_secret}}"}
	vars := map[string]any{"WORKER_NAME": name("-api"), "SUPABASE_URL": "{{out:db.url}}", "APP_URL": "{{param:app_url}}", "TEMPLATE": t.ID, "CODE_VERSION": "{{codever:worker/src/index.js}}",
		"BUSINESS_NAME": "{{param:business_name}}"}
	deps := []string{"subdomain", "probes", "schema", "db_key"}
	if withEmail {
		R(core.ResourceSpec{Key: "email_domain", Kind: "resend.domain", Provider: "resend", Component: "email", Name: domain, Title: "Sending domain " + domain,
			Props: map[string]any{"name": "{{param:domain}}", "auto_dns": true, "wait_seconds": 90}})
		R(core.ResourceSpec{Key: "email_key", Kind: "resend.api_key", Provider: "resend", Component: "email", Name: name("-worker"), Title: "Send-only email key",
			Props: map[string]any{"name": name("-worker"), "domain_id": "{{out:email_domain.id}}"}})
		secrets["RESEND_API_KEY"] = "{{secret:email_key.token}}"
		vars["FROM_EMAIL"] = "{{param:from_email}}"
		deps = append(deps, "email_key")
	}
	if withAuth {
		settings := map[string]any{"site_url": appURL, "uri_allow_list": appURL + "/**", "external_email_enabled": true, "mailer_autoconfirm": false, "password_min_length": 10}
		props := map[string]any{"project_ref": "{{out:db.ref}}", "settings": settings}
		authDeps := []string{"db"}
		if withEmail {
			settings["smtp_host"], settings["smtp_port"], settings["smtp_user"] = "smtp.resend.com", "465", "resend"
			settings["smtp_admin_email"], settings["smtp_sender_name"] = "notifications@"+domain, business
			props["smtp_pass"] = "{{secret:email_key.token}}"
			authDeps = append(authDeps, "email_key")
		}
		R(core.ResourceSpec{Key: "auth", Kind: "supabase.auth_config", Provider: "supabase", Component: "db", Name: "Auth settings", Title: "Sign-up and sign-in settings", DependsOn: authDeps, Props: props})
		for i := range bp.Components {
			if bp.Components[i].Key == "db" {
				bp.Components[i].Resources = append(bp.Components[i].Resources, "auth")
			}
		}
	}
	if withPay {
		R(core.ResourceSpec{Key: "product", Kind: "stripe.product", Provider: "stripe", Component: "stripe", Name: projectName, Title: "Stripe product “" + projectName + "”",
			Props: map[string]any{"name": "{{param:project_name}}"}})
		priceProps := map[string]any{"product": "{{out:product.id}}", "unit_amount": cents, "currency": "{{param:currency}}"}
		title := "One-time price " + moneyLabel(cents, a.str("currency", "usd"))
		if subscription {
			priceProps["interval"] = "month"
			title = "Monthly price " + moneyLabel(cents, a.str("currency", "usd"))
		}
		R(core.ResourceSpec{Key: "price", Kind: "stripe.price", Provider: "stripe", Component: "stripe", Name: projectName, Title: title, Props: priceProps})
		secrets["STRIPE_SECRET_KEY"] = "{{conn:stripe.worker_key}}"
		vars["PRICE_ID"] = "{{out:price.id}}"
		vars["BILLING_MODE"] = pick(subscription, "subscription", "payment")
		deps = append(deps, "price")
	}
	bindings := []any{map[string]any{"type": "kv_namespace", "name": "PROBES", "namespace_id": "{{out:probes.id}}", "purpose": "Probes and rate limits"}}
	R(core.ResourceSpec{Key: "api", Kind: "cloudflare.worker", Provider: "cloudflare", Component: "api", Name: name("-api"), Title: "Worker API", DependsOn: deps,
		Props: map[string]any{"name": name("-api"), "code": "{{code:worker/src/index.js}}", "compatibility_date": CompatibilityDate, "bindings": bindings, "vars": vars, "secrets": secrets,
			"crons": []any{"*/30 * * * *"}, "workers_dev": true, "subdomain_resource": "subdomain",
			"purposes": map[string]any{"SUPABASE_SECRET_KEY": "Server-side data access", "PROBES": "Probes", "RESEND_API_KEY": "Notification emails", "STRIPE_SECRET_KEY": "Payments"}}})
	if withPay {
		events := []any{"checkout.session.completed", "checkout.session.expired", "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted", "invoice.payment_failed"}
		R(core.ResourceSpec{Key: "webhook", Kind: "stripe.webhook_endpoint", Provider: "stripe", Component: "stripe", Name: "Worker webhook", Title: "Webhook → Worker", DependsOn: []string{"api"},
			Props: map[string]any{"url": "{{out:api.url}}/stripe/webhook", "events": events, "description": "Backplane: {{param:project_name}} ({{env}})", "purpose": "Payment and subscription updates"}})
		R(core.ResourceSpec{Key: "api_webhook_secret", Kind: "cloudflare.worker_secret", Provider: "cloudflare", Component: "api", Name: "STRIPE_WEBHOOK_SECRET", Title: "Worker secret STRIPE_WEBHOOK_SECRET",
			DependsOn: []string{"api", "webhook"}, Props: map[string]any{"script": "{{out:api.name}}", "name": "STRIPE_WEBHOOK_SECRET", "value": "{{secret:webhook.secret}}", "purpose": "Payment updates"}, Count: []core.CountItem{}})
	}
	if withGitHub {
		R(core.ResourceSpec{Key: "repo", Kind: "github.repo", Provider: "github", Component: "github", Name: "{{param:github_repo}}", Title: "Private repository",
			Props: map[string]any{"name": "{{param:github_repo}}", "description": "Backend for " + projectName + " — generated and monitored by Backplane"}})
		R(core.ResourceSpec{Key: "code", Kind: "github.files", Provider: "github", Component: "github", Name: "main", Title: "Commit generated code to main", DependsOn: []string{"repo", "api"},
			Props: map[string]any{"repo": "{{out:repo.full_name}}", "branch": "main", "message": "Backplane: generated backend for {{param:project_name}} ({{env}})",
				"files": map[string]any{"worker/src/index.js": "{{code:worker/src/index.js}}", "worker/wrangler.jsonc": "{{codet:worker/wrangler.jsonc}}",
					"supabase/migrations/0001_schema.sql": "{{code:supabase/schema.sql}}", ".github/workflows/deploy.yml": "{{code:.github/workflows/deploy.yml}}",
					"README.md": "{{codet:README.md}}", ".gitignore": "{{code:.gitignore}}"}}})
		R(core.ResourceSpec{Key: "gh_cf_token", Kind: "github.actions_secret", Provider: "github", Component: "github", Name: "CLOUDFLARE_API_TOKEN", Title: "Deploy secret CLOUDFLARE_API_TOKEN",
			DependsOn: []string{"repo"}, Props: map[string]any{"repo": "{{out:repo.full_name}}", "name": "CLOUDFLARE_API_TOKEN", "value": "{{conn:cloudflare.deploy_token}}"}})
		R(core.ResourceSpec{Key: "gh_cf_account", Kind: "github.actions_secret", Provider: "github", Component: "github", Name: "CLOUDFLARE_ACCOUNT_ID", Title: "Deploy secret CLOUDFLARE_ACCOUNT_ID",
			DependsOn: []string{"repo"}, Props: map[string]any{"repo": "{{out:repo.full_name}}", "name": "CLOUDFLARE_ACCOUNT_ID", "value": "{{connset:cloudflare.account_id}}"}})
	}
	L := func(l core.LinkSpec) { bp.Links = append(bp.Links, l) }
	L(core.LinkSpec{Key: "app_db", From: "app", To: "db", Label: "Supabase client (RLS)", Kind: "http", Check: "", Critical: true, Breaks: dbBreaks})
	L(core.LinkSpec{Key: "api_db", From: "api", To: "db", Label: "Server key (PostgREST)", Kind: "sql", Check: "worker_probe:supabase", Critical: true, Breaks: []string{"Server-side tasks"}})
	if withStorage {
		L(core.LinkSpec{Key: "app_storage", From: "app", To: "storage", Label: "Signed uploads", Kind: "http", Breaks: []string{"File uploads and downloads"}})
	}
	if withEmail {
		L(core.LinkSpec{Key: "api_email", From: "api", To: "email", Label: "Notifications", Kind: "email", Check: "worker_probe:resend", Breaks: []string{"Sign-in and notification emails"}})
		if withAuth {
			L(core.LinkSpec{Key: "db_email", From: "db", To: "email", Label: "Sign-in emails (SMTP)", Kind: "smtp", Check: "supabase_smtp", Critical: true, Breaks: []string{"Sign-in and notification emails"}})
		}
	}
	if withPay {
		L(core.LinkSpec{Key: "app_api", From: "app", To: "api", Label: "Checkout / billing", Kind: "http", Check: "checkout_data", Critical: true, Breaks: []string{"Payments"}})
		L(core.LinkSpec{Key: "api_stripe", From: "api", To: "stripe", Label: "Creates checkout", Kind: "http", Check: "worker_probe:stripe", Critical: true, Breaks: []string{"Payments"}})
		L(core.LinkSpec{Key: "stripe_api", From: "stripe", To: "api", Label: pick(subscription, "customer.subscription.*", "checkout.session.completed"), Kind: "webhook", Check: "stripe_webhook", Critical: true, Breaks: []string{"Subscription status updates"}})
	}
	if withGitHub {
		L(core.LinkSpec{Key: "github_api", From: "github", To: "api", Label: "Deploy on push", Kind: "deploy", Check: "github_deploy", Breaks: []string{"Automatic deploys"}})
	}
	if isoTable != "" && withAuth {
		bp.Scenarios = []core.ScenarioSpec{{Key: "isolation", Title: "Each user sees only their own data", Check: "data_isolation",
			Steps: []string{"Create two temporary users", "User A saves a record", "User A reads it back", "User B cannot see it", "A visitor without an account cannot see it", "Temporary users removed"}}}
	}
	bp.Generated = []core.GeneratedFileSpec{{Path: "worker/src/index.js", Role: "generated", Language: "javascript"}, {Path: "worker/wrangler.jsonc", Role: "system-config", Language: "jsonc"},
		{Path: "supabase/schema.sql", Role: "generated", Language: "sql"}, {Path: "README.md", Role: "generated", Language: "markdown"}}
	bp.Notes = append(bp.Notes, "Every table has row-level security; the generated policies let each signed-in user reach only their own rows.")
	return bp, bp.ValidateGraph()
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func dataFiles(p *core.Project, t Template) map[string]string {
	sql, _, _ := schemaSQL(t)
	files := map[string]string{
		"worker/src/index.js":   strings.ReplaceAll(Asset("data/worker.js"), "{{PROJECT_NAME}}", p.Name),
		"worker/wrangler.jsonc": WranglerConfig(&p.Blueprint),
		"supabase/schema.sql":   sql,
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — backend\n\nGenerated and monitored by **Backplane**. Template: %s.\n\n", p.Name, t.Name)
	b.WriteString("## Tables (row-level security on)\n\n")
	for _, tb := range dataTables[t.ID] {
		fmt.Fprintf(&b, "- `%s` — %s (%s)\n", tb.Name, tb.Purpose, accessLabel(tb.Access))
	}
	b.WriteString("\n## Connecting your app\n\nUse the Supabase client with your project's **publishable** key. Row-level security makes sure each signed-in user only reaches their own rows. Server-side work (webhooks, emails, scheduled jobs) runs in the Worker in `worker/`.\n")
	files["README.md"] = b.String()
	return files
}

func accessLabel(a string) string {
	switch a {
	case "owner":
		return "each user reads and writes only their own rows"
	case "owner-read":
		return "users read their own rows; only the server writes"
	case "public-read":
		return "anyone reads; owners write their own rows"
	case "members":
		return "members of the room read and write"
	case "server-members":
		return "active subscribers read published rows"
	case "server":
		return "server only"
	}
	return a
}

// PriceLabel formats a price param for summaries.
func PriceLabel(bp *core.Blueprint) string {
	c, _ := strconv.ParseInt(bp.Param("price_cents"), 10, 64)
	return moneyLabel(c, bp.Param("currency"))
}
