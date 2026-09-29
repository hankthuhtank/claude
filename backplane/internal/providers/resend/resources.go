package resend

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// Handlers returns every Resend resource handler.
func Handlers() []providers.Handler {
	return []providers.Handler{domainH{}, keyH{}, templateH{}, webhookH{}, segmentH{}}
}

func rconn(s *providers.Session) (*providers.Conn, error) { return s.Conn("resend") }

// ================= Domain =================

type domainH struct{}

func (domainH) Kind() string { return KindDomain }

// RootOf guesses the registrable root of a host ("mail.example.co.uk" is
// imperfect, but the DNS manager confirms the real zone before writing).
func RootOf(host string) string {
	parts := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}

// dnsRecords converts Resend's records into fully-qualified DNS records.
func dnsRecords(d *Domain, zone string) []providers.DNSRecord {
	var out []providers.DNSRecord
	for _, r := range d.Records {
		name := r.Name
		if zone != "" && !strings.HasSuffix(name, zone) {
			if name == "" || name == "@" {
				name = zone
			} else {
				name = name + "." + zone
			}
		}
		out = append(out, providers.DNSRecord{Type: r.Type, Name: name, Content: r.Value, Priority: r.Priority, Purpose: r.Record})
	}
	return out
}

func getDomain(ctx context.Context, c *providers.Conn, id string) (*Domain, error) {
	var d Domain
	if err := Call(ctx, c, "GET", "/domains/"+id, nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (domainH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(providers.Str(spec.Props, "name"))
	var dom *Domain
	adopted := false
	if st != nil && st.ID != "" {
		if d, err := getDomain(ctx, c, st.ID); err == nil {
			dom = d
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	if dom == nil {
		var list listDomains
		if err := Call(ctx, c, "GET", "/domains", nil, &list); err != nil {
			return nil, err
		}
		for i := range list.Data {
			if strings.EqualFold(list.Data[i].Name, name) {
				dom = &list.Data[i]
				adopted = st == nil
			}
		}
	}
	created := false
	if dom == nil {
		body := map[string]any{"name": name}
		if r := providers.Str(spec.Props, "region"); r != "" {
			body["region"] = r
		}
		s.Say("Adding sending domain %s to Resend", name)
		var d Domain
		if err := Call(ctx, c, "POST", "/domains", body, &d); err != nil {
			return nil, err
		}
		dom, created = &d, true
	}
	if full, err := getDomain(ctx, c, dom.ID); err == nil {
		dom = full
	}
	next := providers.Touch(spec, st, dom.ID, dom.Name)
	if adopted {
		next.CreatedBy = "adopted"
	}
	if next.Applied == nil {
		next.Applied = map[string]any{}
	}
	// Publish DNS through the connected DNS provider when it hosts the zone.
	dnsNote := ""
	if dom.Status != "verified" && providers.Bool(spec.Props, "auto_dns") && s.DNS != nil {
		if zone, ok := s.DNS.Zone(ctx, name); ok {
			recs := dnsRecords(dom, zone)
			s.Say("Publishing %d DNS records for %s on Cloudflare", len(recs), name)
			ids, err := s.DNS.Ensure(ctx, zone, recs)
			if err != nil {
				return nil, err
			}
			next.Applied["dns_zone"] = zone
			next.Applied["dns_record_ids"] = ids
			dnsNote = "DNS records published automatically."
		} else {
			dnsNote = "Add the DNS records shown below at your DNS host."
		}
	} else if dom.Status != "verified" {
		dnsNote = "Add the DNS records shown below at your DNS host."
	}
	if dom.Status != "verified" {
		_ = Call(ctx, c, "POST", "/domains/"+dom.ID+"/verify", map[string]any{}, nil)
		wait := time.Duration(providers.Int(spec.Props, "wait_seconds")) * time.Second
		if wait == 0 {
			wait = 90 * time.Second
		}
		deadline := time.Now().Add(wait)
		for dom.Status != "verified" && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(s.PollEvery()):
			}
			if d, err := getDomain(ctx, c, dom.ID); err == nil {
				if d.Status != dom.Status {
					s.Say("Resend domain %s is %s", name, strings.ReplaceAll(d.Status, "_", " "))
				}
				dom = d
			}
		}
	}
	recsJSON, _ := json.Marshal(dom.Records)
	next.SetOutput("id", dom.ID)
	next.SetOutput("name", dom.Name)
	next.SetOutput("status", dom.Status)
	next.SetOutput("records", string(recsJSON))
	if dom.Status != "verified" {
		next.Note = "Domain verification pending (DNS can take up to 72 hours). " + dnsNote + " Emails to customers start working once it verifies; Backplane keeps checking."
	}
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func (domainH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	d, err := getDomain(ctx, c, st.ID)
	if err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	var failed []string
	for _, r := range d.Records {
		if r.Status == "failed" || r.Status == "temporary_failure" {
			failed = append(failed, r.Record+" "+r.Type+" "+r.Name)
		}
	}
	return &providers.Observation{Exists: true, ID: d.ID, Name: d.Name, Props: map[string]any{"status": d.Status, "failed_records": failed},
		Summary: "domain " + strings.ReplaceAll(d.Status, "_", " ")}, nil
}

func (domainH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	status := providers.Str(obs.Props, "status")
	if status == "verified" {
		return nil
	}
	sev := core.HealthWarn
	if status == "failed" || status == "partially_failed" {
		sev = core.HealthFail
	}
	actual := strings.ReplaceAll(status, "_", " ")
	if f := providers.List(obs.Props, "failed_records"); len(f) > 0 {
		actual += " (failing: " + strings.Join(f, "; ") + ")"
	}
	return []core.DriftItem{{Resource: spec.Key, Kind: spec.Kind, Provider: "resend", Field: "domain verification", Expected: "verified", Actual: actual,
		Severity: sev, Breaks: []string{"Emails to customers (receipts, download links, sign-in emails)"}, Recommended: "Publish the DNS records and re-verify", FixID: "reapply:" + spec.Key}}
}

func (domainH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	if st.CreatedBy == "adopted" {
		return nil
	}
	c, err := rconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "DELETE", "/domains/"+st.ID, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Send-only API key =================

type keyH struct{}

func (keyH) Kind() string { return KindAPIKey }

type keyItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func listKeys(ctx context.Context, c *providers.Conn) ([]keyItem, error) {
	var out struct {
		Data []keyItem `json:"data"`
	}
	err := Call(ctx, c, "GET", "/api-keys", nil, &out)
	return out.Data, err
}

func (keyH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	domainID := providers.Str(spec.Props, "domain_id")
	keys, err := listKeys(ctx, c)
	if err != nil {
		return nil, err
	}
	if st != nil && st.ID != "" && st.Output("domain_id") == domainID && !providers.Bool(spec.Props, "__rotate") {
		for _, k := range keys {
			if k.ID == st.ID {
				if v, err := s.Secret(spec.Key, "token"); err == nil && v != "" {
					return &providers.ApplyResult{State: providers.Touch(spec, st, k.ID, k.Name)}, nil
				}
			}
		}
	}
	// A key we can no longer read is useless: remove our old one first.
	for _, k := range keys {
		if k.Name == name || (st != nil && k.ID == st.ID) {
			_ = Call(ctx, c, "DELETE", "/api-keys/"+k.ID, nil, nil)
		}
	}
	body := map[string]any{"name": name, "permission": "sending_access"}
	if domainID != "" {
		body["domain_id"] = domainID
	}
	s.Say("Creating a send-only Resend key limited to the sending domain")
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := Call(ctx, c, "POST", "/api-keys", body, &out); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, out.ID, name)
	next.SetOutput("domain_id", domainID)
	next.SetOutput("permission", "sending_access")
	return &providers.ApplyResult{State: next, Secrets: map[string]string{"token": out.Token}, Created: true}, nil
}

func (keyH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	keys, err := listKeys(ctx, c)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.ID == st.ID {
			return &providers.Observation{Exists: true, ID: k.ID, Name: k.Name}, nil
		}
	}
	return &providers.Observation{Exists: false}, nil
}

func (keyH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (keyH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := rconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "DELETE", "/api-keys/"+st.ID, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Template =================

type templateH struct{}

func (templateH) Kind() string { return KindTemplate }

type template struct {
	ID                     string           `json:"id"`
	Name                   string           `json:"name"`
	Alias                  string           `json:"alias"`
	Subject                string           `json:"subject"`
	HTML                   string           `json:"html"`
	Status                 string           `json:"status"`
	HasUnpublishedVersions bool             `json:"has_unpublished_versions"`
	Variables              []map[string]any `json:"variables"`
}

func templateBody(spec *core.ResourceSpec) map[string]any {
	body := map[string]any{"name": providers.Str(spec.Props, "name"), "subject": providers.Str(spec.Props, "subject"), "html": providers.Str(spec.Props, "html")}
	if a := providers.Str(spec.Props, "alias"); a != "" {
		body["alias"] = a
	}
	if t := providers.Str(spec.Props, "text"); t != "" {
		body["text"] = t
	}
	if f := providers.Str(spec.Props, "from"); f != "" {
		body["from"] = f
	}
	var vars []map[string]any
	for _, v := range providers.Objects(spec.Props, "variables") {
		vars = append(vars, v)
	}
	if len(vars) > 0 {
		body["variables"] = vars
	}
	return body
}

func (templateH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	body := templateBody(spec)
	id := ""
	created := false
	if st != nil && st.ID != "" {
		if err := Call(ctx, c, "PATCH", "/templates/"+st.ID, body, nil); err == nil {
			id = st.ID
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	if id == "" {
		// Adopt a template that already carries our alias.
		if a := providers.Str(spec.Props, "alias"); a != "" {
			var t template
			if err := Call(ctx, c, "GET", "/templates/"+a, nil, &t); err == nil && t.ID != "" {
				if err := Call(ctx, c, "PATCH", "/templates/"+t.ID, body, nil); err != nil {
					return nil, err
				}
				id = t.ID
			}
		}
	}
	if id == "" {
		s.Say("Creating email template “%s”", body["name"])
		var out struct {
			ID string `json:"id"`
		}
		if err := Call(ctx, c, "POST", "/templates", body, &out); err != nil {
			return nil, err
		}
		id, created = out.ID, true
	}
	if err := Call(ctx, c, "POST", "/templates/"+id+"/publish", map[string]any{}, nil); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, id, providers.Str(spec.Props, "name"))
	next.SetOutput("id", id)
	next.SetOutput("alias", providers.Str(spec.Props, "alias"))
	if next.Applied == nil {
		next.Applied = map[string]any{}
	}
	next.Applied["html_digest"] = core.HashBytes([]byte(providers.Str(spec.Props, "html")))[:12]
	return &providers.ApplyResult{State: next, Created: created}, nil
}

func (templateH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var t template
	if err := Call(ctx, c, "GET", "/templates/"+st.ID, nil, &t); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: t.ID, Name: t.Name, Props: map[string]any{"status": t.Status, "unpublished": t.HasUnpublishedVersions,
		"html_digest": core.HashBytes([]byte(t.HTML))[:12]}, Summary: t.Status}, nil
}

func (templateH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	var items []core.DriftItem
	if providers.Str(obs.Props, "status") != "published" {
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "resend", Field: "template status", Expected: "published", Actual: providers.Str(obs.Props, "status"),
			Severity: core.HealthFail, Breaks: []string{"“" + providers.Str(spec.Props, "name") + "” emails fail to send"}, Recommended: "Publish the template", FixID: "reapply:" + spec.Key})
	}
	if st != nil && st.Applied != nil && providers.Str(st.Applied, "html_digest") != "" && providers.Str(obs.Props, "html_digest") != providers.Str(st.Applied, "html_digest") {
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "resend", Field: "template content", Expected: "Backplane version", Actual: "Edited in Resend",
			Severity: core.HealthWarn, Breaks: []string{"Nothing is broken — someone customised the email in Resend. Re-applying would overwrite their edit."}, Recommended: "Keep the edit (no action) or restore Backplane's version", FixID: "reapply:" + spec.Key})
	}
	return items
}

func (templateH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := rconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "DELETE", "/templates/"+st.ID, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Webhook =================

type webhookH struct{}

func (webhookH) Kind() string { return KindWebhook }

type webhook struct {
	ID            string   `json:"id"`
	Endpoint      string   `json:"endpoint"`
	Events        []string `json:"events"`
	Status        string   `json:"status"`
	SigningSecret string   `json:"signing_secret"`
}

func (webhookH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	endpoint := providers.Str(spec.Props, "endpoint")
	events := providers.List(spec.Props, "events")
	sort.Strings(events)
	if st != nil && st.ID != "" {
		err := Call(ctx, c, "PATCH", "/webhooks/"+st.ID, map[string]any{"endpoint": endpoint, "events": events, "status": "enabled"}, nil)
		if err == nil {
			var w webhook
			if err := Call(ctx, c, "GET", "/webhooks/"+st.ID, nil, &w); err != nil {
				return nil, err
			}
			next := providers.Touch(spec, st, w.ID, endpoint)
			res := &providers.ApplyResult{State: next}
			if w.SigningSecret != "" {
				res.Secrets = map[string]string{"secret": w.SigningSecret}
			}
			return res, nil
		} else if !httpx.IsNotFound(err) {
			return nil, err
		}
	}
	s.Say("Creating Resend webhook → %s", endpoint)
	var w webhook
	if err := Call(ctx, c, "POST", "/webhooks", map[string]any{"endpoint": endpoint, "events": events}, &w); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, w.ID, endpoint)
	next.SetOutput("id", w.ID)
	return &providers.ApplyResult{State: next, Secrets: map[string]string{"secret": w.SigningSecret}, Created: true}, nil
}

func (webhookH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	var w webhook
	if err := Call(ctx, c, "GET", "/webhooks/"+st.ID, nil, &w); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	sort.Strings(w.Events)
	return &providers.Observation{Exists: true, ID: w.ID, Name: w.Endpoint, Props: map[string]any{"endpoint": w.Endpoint, "status": w.Status, "events": w.Events}}, nil
}

func (webhookH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	if obs == nil || !obs.Exists {
		return nil
	}
	items := providers.DriftIf(nil, spec, "webhook endpoint", providers.Str(spec.Props, "endpoint"), providers.Str(obs.Props, "endpoint"), core.HealthFail,
		[]string{"Bounce and complaint tracking"}, "Point the webhook back at the Worker")
	if s := providers.Str(obs.Props, "status"); s != "" && s != "enabled" {
		items = append(items, core.DriftItem{Resource: spec.Key, Kind: spec.Kind, Provider: "resend", Field: "webhook status", Expected: "enabled", Actual: s,
			Severity: core.HealthFail, Breaks: []string{"Bounce and complaint tracking"}, Recommended: "Re-enable the webhook", FixID: "reapply:" + spec.Key})
	}
	return items
}

func (webhookH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := rconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "DELETE", "/webhooks/"+st.ID, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// ================= Segment =================

type segmentH struct{}

func (segmentH) Kind() string { return KindSegment }

func (segmentH) Apply(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.ApplyResult, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	name := providers.Str(spec.Props, "name")
	if st != nil && st.ID != "" {
		if err := Call(ctx, c, "GET", "/segments/"+st.ID, nil, nil); err == nil {
			return &providers.ApplyResult{State: providers.Touch(spec, st, st.ID, name)}, nil
		}
	}
	var list struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := Call(ctx, c, "GET", "/segments", nil, &list); err == nil {
		for _, x := range list.Data {
			if x.Name == name {
				next := providers.Touch(spec, st, x.ID, name)
				next.SetOutput("id", x.ID)
				return &providers.ApplyResult{State: next}, nil
			}
		}
	}
	s.Say("Creating contact segment “%s”", name)
	var out struct {
		ID string `json:"id"`
	}
	if err := Call(ctx, c, "POST", "/segments", map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	next := providers.Touch(spec, st, out.ID, name)
	next.SetOutput("id", out.ID)
	return &providers.ApplyResult{State: next, Created: true}, nil
}

func (segmentH) Observe(ctx context.Context, s *providers.Session, spec *core.ResourceSpec, st *core.ResourceState) (*providers.Observation, error) {
	c, err := rconn(s)
	if err != nil {
		return nil, err
	}
	if st == nil || st.ID == "" {
		return &providers.Observation{Exists: false}, nil
	}
	if err := Call(ctx, c, "GET", "/segments/"+st.ID, nil, nil); err != nil {
		if httpx.IsNotFound(err) {
			return &providers.Observation{Exists: false}, nil
		}
		return nil, err
	}
	return &providers.Observation{Exists: true, ID: st.ID}, nil
}

func (segmentH) Drift(spec *core.ResourceSpec, st *core.ResourceState, obs *providers.Observation) []core.DriftItem {
	return nil
}

func (segmentH) Delete(ctx context.Context, s *providers.Session, st *core.ResourceState) error {
	c, err := rconn(s)
	if err != nil {
		return err
	}
	err = Call(ctx, c, "DELETE", "/segments/"+st.ID, nil, nil)
	if httpx.IsNotFound(err) {
		return nil
	}
	return err
}

// Summary helper used by the dashboard.
func Summary(n int) string { return fmt.Sprintf("%d templates", n) }
