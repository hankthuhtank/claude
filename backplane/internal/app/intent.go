package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"safisolutions.org/backplane/internal/blueprints"
)

// The plain-English builder turns a description ("sell my photo presets for
// $15 and text me when someone buys") into a preset, add-ons and answers.
// It only ever proposes: the result fills the same guided form the user
// reviews, and nothing is created until a plan is approved.
//
// Two engines produce the same Design:
//   - offline: keyword and pattern matching, instant and private;
//   - Claude (optional): the description alone is sent to Anthropic's API
//     with a JSON schema that only allows real preset and add-on ids.

// Design is a proposed backend.
type Design struct {
	TemplateID   string         `json:"templateId"`
	TemplateName string         `json:"templateName"`
	Industry     string         `json:"industry"`
	Confidence   float64        `json:"confidence"`
	AddOns       []string       `json:"addOns"`
	Answers      map[string]any `json:"answers"`
	ProjectName  string         `json:"projectName"`
	Heard        []Heard        `json:"heard"`
	Alternatives []Alternative  `json:"alternatives"`
	Unsupported  []string       `json:"unsupported,omitempty"`
	Questions    []string       `json:"questions,omitempty"`
	Explanation  string         `json:"explanation"`
	Source       string         `json:"source"` // offline | claude
	Model        string         `json:"model,omitempty"`
	Note         string         `json:"note,omitempty"`
}

// Heard explains one thing the builder understood.
type Heard struct {
	Phrase string `json:"phrase"`
	Means  string `json:"means"`
}

// Alternative is a runner-up preset.
type Alternative struct {
	TemplateID string  `json:"templateId"`
	Name       string  `json:"name"`
	Why        string  `json:"why"`
	Score      float64 `json:"score"`
}

// ---- offline interpreter ----

type cue struct {
	re     *regexp.Regexp
	weight float64
	means  string
}

func cues(weight float64, means string, patterns ...string) []cue {
	out := make([]cue, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, cue{re: regexp.MustCompile(`(?i)\b(?:` + p + `)\b`), weight: weight, means: means})
	}
	return out
}

// templateCues map phrases people actually use to presets.
var templateCues = map[string][]cue{
	"software-store": append(cues(3, "selling software", `sell (?:my |our )?(?:app|software|program|plugin|tool|game)s?`, `software (?:store|shop|sales)`, `desktop app`, `installer`, `\.exe`, `mac app`, `windows app`),
		cues(1.5, "software download", `download(?:able)? (?:app|software)`, `app downloads?`)...),
	"digital-downloads": append(cues(3, "a digital download", `e-?books?`, `pdfs?`, `digital (?:products?|downloads?|goods|files?)`, `presets?`, `templates?`, `courses? files?`, `beats?`, `sample packs?`, `fonts?`, `printables?`, `worksheets?`),
		cues(1.5, "delivering files after purchase", `downloads?`, `instant delivery`)...),
	"photography-store": cues(3, "selling photos", `photos?`, `photography`, `prints?`, `stock images?`, `wallpapers?`, `lightroom`),
	"license-server":    cues(3, "license keys", `license keys?`, `licen[cs]e(?:s|ing)?`, `activation (?:keys?|codes?)`, `serial (?:keys?|numbers?)`, `product keys?`),
	"ecommerce": append(cues(3, "physical products with shipping", `ship(?:ping|ped)?`, `physical (?:products?|goods|items)`, `t-?shirts?`, `merch(?:andise)?`, `apparel`, `clothing`, `candles?`, `jewel(?:le)?ry`, `handmade`, `inventory`, `deliver(?:y|ies) (?:to|address)`),
		cues(1.5, "an online store", `online (?:store|shop)`, `e-?commerce`, `shop(?:ify)?`, `storefront`)...),
	"saas":              cues(2.5, "a software-as-a-service app", `saas`, `web app`, `software as a service`, `dashboard app`, `b2b (?:app|tool)`),
	"free-saas":         cues(3, "a free app with accounts", `free (?:app|tool|web app|saas)`, `no payments?`, `side project`),
	"subscription-saas": cues(3, "recurring billing", `subscriptions?`, `per month`, `monthly (?:plan|fee|price|billing)`, `/mo\b`, `recurring`, `billing portal`, `(?:pro|premium) plan`, `tiers?`),
	"membership":        cues(3, "members-only content", `members?(?:hip)?s?(?: only| area| site)?`, `paywall(?:ed)?`, `exclusive content`, `patrons?`, `community access`),
	"marketplace":       cues(3, "a marketplace", `marketplace`, `buyers? and sellers?`, `vendors?`, `sellers?`, `listings?`, `commission`),
	"mobile-app":        cues(3, "a mobile app backend", `mobile app`, `i(?:os|phone) app`, `android`, `react native`, `flutter`, `expo`),
	"crm":               cues(3, "customer relationship management", `crm`, `contacts?`, `leads?`, `deals?`, `pipeline`, `sales team`, `follow[- ]ups?`, `customers? list`),
	"client-portal":     cues(3, "a client portal", `client portal`, `clients? (?:can )?(?:log ?in|see|view|upload)`, `share (?:files|invoices|reports) with (?:my )?clients`, `agency`, `accountant`, `invoices?`),
	"contractor-portal": cues(3, "contractors and job sites", `contractors?`, `job ?sites?`, `crews?`, `field (?:work|teams?|service)`, `work orders?`, `estimates?`, `plumb(?:er|ing)`, `electricians?`, `landscap(?:er|ing)`, `roofing`, `construction`),
	"booking":           cues(3, "bookings and appointments", `book(?:ing|ings)?`, `appointments?`, `reservations?`, `schedul(?:e|ing)`, `calendar`, `sessions? with`, `salon`, `barber`, `tutor(?:ing)?`, `lessons?`, `deposits?`),
	"restaurant":        cues(3, "food ordering", `restaurant`, `food (?:orders?|ordering)`, `menu`, `take ?out`, `pick-?up orders?`, `kitchen`, `café|cafe`, `coffee shop`, `food truck`, `bakery`, `pizza`),
	"file-sharing":      cues(3, "file sharing", `file sharing`, `share files`, `upload(?:s|ing)? (?:and|&) shar(?:e|ing)`, `dropbox`, `transfer (?:big |large )?files`),
	"chat":              cues(3, "realtime chat", `chat`, `messag(?:es|ing)`, `real-?time`, `live (?:updates?|feed)`, `rooms?`, `dm`),
	"game":              cues(3, "a game backend", `games?`, `leaderboards?`, `high ?scores?`, `players?`, `multiplayer`, `save games?`),
	"api-service":       cues(3, "an API product", `api (?:service|product|keys?)`, `public api`, `developers? (?:use|call)`, `rate limit(?:ed|s)?`, `usage[- ]based`),
	"webhook-relay":     cues(3, "webhook plumbing", `webhooks?`, `relay`, `forward (?:events|requests)`, `zapier`, `integrations?`),
	"newsletter":        cues(3, "a newsletter", `newsletter`, `mailing list`, `email list`, `subscribers?`, `double opt-?in`, `substack`),
	"support":           cues(3, "a support desk", `support (?:tickets?|desk|system)`, `help ?desk`, `tickets?`, `customer support`, `complaints?`),
	"ai-rag":            cues(3, "AI answers from your documents", `ai`, `chatbot`, `rag`, `embeddings?`, `vector`, `semantic search`, `ask (?:questions|my docs)`, `knowledge base`, `gpt|claude|llm`),
	"admin-dashboard":   cues(3, "an internal admin backend", `admin (?:panel|dashboard|backend)`, `internal tools?`, `back ?office`, `staff (?:only|dashboard)`, `ops dashboard`),
}

var addOnCues = map[string][]cue{
	"sms":       cues(1, "text messages (Twilio)", `texts?`, `sms`, `text messages?`, `text (?:me|them|customers|reminders?)`, `phone (?:alerts?|verification)`),
	"errors":    cues(1, "error monitoring (Sentry)", `error (?:monitoring|tracking|reports?)`, `crash(?:es| reports?)?`, `sentry`, `bug reports?`),
	"analytics": cues(1, "product analytics (PostHog)", `analytics`, `funnels?`, `track (?:users|usage|conversions?)`, `feature flags?`, `a/b tests?`, `posthog`),
	"media":     cues(1, "image and video processing (Cloudinary)", `resiz(?:e|ing) (?:images?|photos?)`, `thumbnails?`, `image processing`, `video (?:processing|transcod\w*)`, `cloudinary`, `watermarks?`),
	"cache":     cues(1, "cache & rate limits (Upstash)", `cache|caching`, `redis`, `rate[- ]limit(?:s|ing)?`, `delayed jobs?`, `queue(?:s|d)?`, `upstash`),
	"hosting":   cues(1, "website hosting (Vercel)", `host(?:ing)? (?:my |the )?(?:site|website|frontend|landing page)`, `vercel`, `next\.?js`, `landing page`),
	"auth_plus": cues(1, "enterprise sign-in (Clerk)", `sso`, `saml`, `single sign-on`, `enterprise (?:login|sign-?in)`, `organizations? (?:and|&) teams?`, `clerk`),
}

var unsupportedCues = []struct {
	re   *regexp.Regexp
	note string
}{
	{regexp.MustCompile(`(?i)\b(?:push notifications?|apns|fcm)\b`), "Mobile push notifications are not set up by Backplane yet (email and SMS are)."},
	{regexp.MustCompile(`(?i)\b(?:crypto(?:currency)?|bitcoin|ethereum|nft|web3|blockchain)\b`), "Crypto payments are not supported; Stripe handles cards, wallets and bank payments."},
	{regexp.MustCompile(`(?i)\b(?:live ?stream(?:ing)?|video streaming|twitch)\b`), "Live video streaming needs a dedicated streaming service Backplane does not provision."},
	{regexp.MustCompile(`(?i)\b(?:phone calls?|call center|voip)\b`), "Voice calls are not provisioned (Twilio SMS is available as an add-on)."},
	{regexp.MustCompile(`(?i)\b(?:train(?:ing)? (?:a |my own )?(?:model|ai)|fine-?tun\w*)\b`), "Training or fine-tuning AI models is outside Backplane; the AI preset searches your own documents."},
	{regexp.MustCompile(`(?i)\b(?:hipaa|phi|medical records)\b`), "Regulated health data (HIPAA) needs signed agreements with each provider before use."},
}

var (
	priceRE    = regexp.MustCompile(`(?i)(?:(?:\$|usd\s?|€|£|eur\s?|gbp\s?|cad\s?|aud\s?)\s?(\d{1,6}(?:[.,]\d{1,2})?)|(\d{1,6}(?:[.,]\d{1,2})?)\s?(?:dollars|bucks|usd|eur|euros?|gbp|pounds|cad|aud))`)
	emailRE    = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@([a-z0-9\-]+\.)+[a-z]{2,24}\b`)
	domainRE   = regexp.MustCompile(`(?i)\b((?:[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|co|app|dev|shop|store|ai|us|uk|ca|au|de|fr|es|it|nl|xyz|me|tech|studio|design|art|photo|photography|online|site|biz|info))\b`)
	urlRE      = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
	nameRE     = regexp.MustCompile(`(?i)\b(?:called|named|brand(?:ed)? as|business is)\s+["“']?([A-Z0-9][\w&' .\-]{1,40}?)["”']?(?:[,.;!]|\s+(?:and|that|which|where|for|to)\b|$)`)
	quotedRE   = regexp.MustCompile(`["“]([^"”]{2,40})["”]`)
	sellWhatRE = regexp.MustCompile(`(?i)\bsell(?:ing)?\s+(?:my|our|a|an|the)?\s*([a-z][a-z0-9 '\-]{2,40}?)(?:\s+(?:for|at|to|online|on|with|and)\b|[,.;!]|$)`)
)

func interpretOffline(text string) *Design {
	d := &Design{Answers: map[string]any{}, Source: "offline"}
	scores := map[string]float64{}
	reasons := map[string][]string{}
	for id, cs := range templateCues {
		for _, c := range cs {
			if m := c.re.FindString(text); m != "" {
				scores[id] += c.weight
				reasons[id] = append(reasons[id], strings.ToLower(strings.TrimSpace(m)))
			}
		}
	}
	// Price per month pulls toward subscriptions; shipping words beat downloads.
	lower := strings.ToLower(text)
	if strings.Contains(lower, "per month") || strings.Contains(lower, "/month") || strings.Contains(lower, "a month") || strings.Contains(lower, "monthly") {
		scores["subscription-saas"] += 1
		scores["membership"] += 0.5
	}
	if scores["ecommerce"] >= 3 {
		scores["digital-downloads"] -= 1.5
		scores["software-store"] -= 1
	}
	type ranked struct {
		id    string
		score float64
	}
	var rs []ranked
	for id, s := range scores {
		if s > 0 {
			rs = append(rs, ranked{id, s})
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].score != rs[j].score {
			return rs[i].score > rs[j].score
		}
		return catalogIndex(rs[i].id) < catalogIndex(rs[j].id)
	})
	if len(rs) == 0 {
		d.TemplateID = "saas"
		d.Confidence = 0.15
		d.Questions = append(d.Questions, "What does the business do, and how does money come in (one-time sales, subscriptions, or none)?")
	} else {
		d.TemplateID = rs[0].id
		second := 0.0
		if len(rs) > 1 {
			second = rs[1].score
		}
		d.Confidence = math.Round(math.Min(0.95, 0.35+0.6*(rs[0].score-second)/(rs[0].score+1))*100) / 100
		for _, r := range reasons[rs[0].id] {
			d.Heard = append(d.Heard, Heard{Phrase: r, Means: meaning(rs[0].id)})
		}
		for _, r := range rs[1:] {
			if len(d.Alternatives) == 3 {
				break
			}
			t, _ := blueprints.Get(r.id)
			d.Alternatives = append(d.Alternatives, Alternative{TemplateID: r.id, Name: t.Name, Score: math.Round(r.score*10) / 10,
				Why: "Mentions " + strings.Join(uniq(reasons[r.id]), ", ")})
		}
	}
	for id, cs := range addOnCues {
		for _, c := range cs {
			if m := c.re.FindString(text); m != "" {
				if !contains(d.AddOns, id) {
					d.AddOns = append(d.AddOns, id)
					d.Heard = append(d.Heard, Heard{Phrase: strings.ToLower(m), Means: c.means})
				}
				break
			}
		}
	}
	sort.Strings(d.AddOns)
	for _, u := range unsupportedCues {
		if u.re.MatchString(text) {
			d.Unsupported = append(d.Unsupported, u.note)
		}
	}
	extractAnswers(text, d)
	finishDesign(d)
	return d
}

func extractAnswers(text string, d *Design) {
	if m := priceRE.FindStringSubmatch(text); m != nil {
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		if v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64); err == nil && v >= 0.5 {
			d.Answers["price"] = v
			d.Heard = append(d.Heard, Heard{Phrase: strings.TrimSpace(m[0]), Means: fmt.Sprintf("price %.2f", v)})
		}
		cur := strings.ToLower(m[0])
		switch {
		case strings.Contains(cur, "€") || strings.Contains(cur, "eur"):
			d.Answers["currency"] = "eur"
		case strings.Contains(cur, "£") || strings.Contains(cur, "gbp") || strings.Contains(cur, "pound"):
			d.Answers["currency"] = "gbp"
		case strings.Contains(cur, "cad"):
			d.Answers["currency"] = "cad"
		case strings.Contains(cur, "aud"):
			d.Answers["currency"] = "aud"
		}
	}
	emails := emailRE.FindAllString(text, -1)
	if len(emails) > 0 {
		d.Answers["support_email"] = strings.ToLower(emails[0])
		d.Heard = append(d.Heard, Heard{Phrase: emails[0], Means: "support email"})
	}
	withoutEmails := emailRE.ReplaceAllString(text, " ")
	if u := urlRE.FindString(withoutEmails); u != "" {
		u = strings.TrimRight(u, ".,;)")
		d.Answers["site_origin"] = u
		d.Answers["app_url"] = u
		d.Heard = append(d.Heard, Heard{Phrase: u, Means: "your website"})
	}
	if dm := domainRE.FindString(urlRE.ReplaceAllStringFunc(withoutEmails, func(u string) string {
		h := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		return " " + strings.SplitN(h, "/", 2)[0] + " "
	})); dm != "" {
		dm = strings.TrimPrefix(strings.ToLower(dm), "www.")
		d.Answers["domain"] = dm
		d.Heard = append(d.Heard, Heard{Phrase: dm, Means: "email domain"})
	} else if len(emails) > 0 {
		if _, host, ok := strings.Cut(strings.ToLower(emails[0]), "@"); ok && !freeMail[host] {
			d.Answers["domain"] = host
		}
	}
	if m := nameRE.FindStringSubmatch(text); m != nil {
		d.ProjectName = strings.TrimSpace(m[1])
	} else if m := quotedRE.FindStringSubmatch(text); m != nil {
		d.ProjectName = strings.TrimSpace(m[1])
	}
	if d.ProjectName != "" {
		d.Answers["business_name"] = d.ProjectName
	}
	if m := sellWhatRE.FindStringSubmatch(text); m != nil {
		what := strings.TrimSpace(m[1])
		if what != "" && !stopWord[what] {
			d.Answers["product_name"] = titleWords(what)
		}
	}
	if strings.Contains(strings.ToLower(text), "no github") || strings.Contains(strings.ToLower(text), "without github") {
		d.Answers["include_github"] = false
	}
}

var freeMail = map[string]bool{"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "yahoo.com": true, "icloud.com": true, "me.com": true, "aol.com": true, "proton.me": true, "protonmail.com": true, "live.com": true}

var stopWord = map[string]bool{"stuff": true, "things": true, "products": true, "items": true, "it": true, "them": true}

// finishDesign fills template facts, filters answers to real questions and
// writes the explanation.
func finishDesign(d *Design) {
	t, ok := blueprints.Get(d.TemplateID)
	if !ok {
		t, _ = blueprints.Get("saas")
		d.TemplateID = t.ID
	}
	d.TemplateName, d.Industry = t.Name, t.Industry
	keys := map[string]blueprints.Question{}
	for _, q := range t.Questions {
		keys[q.Key] = q
	}
	for k, v := range d.Answers {
		q, ok := keys[k]
		if !ok {
			delete(d.Answers, k)
			continue
		}
		if q.Kind == "select" && len(q.Options) > 0 && !contains(q.Options, fmt.Sprint(v)) {
			delete(d.Answers, k)
		}
	}
	if _, ok := keys["domain"]; ok {
		if _, have := d.Answers["domain"]; !have && keys["domain"].Required {
			d.Questions = append(d.Questions, "Which domain should emails come from (for example example.com)?")
		}
	}
	if _, ok := keys["price"]; ok {
		if _, have := d.Answers["price"]; !have {
			d.Questions = append(d.Questions, "What price should customers pay?")
		}
	}
	valid := d.AddOns[:0]
	for _, id := range d.AddOns {
		if _, ok := blueprints.AddOnByID(id); ok {
			valid = append(valid, id)
		}
	}
	d.AddOns = valid
	if len(d.AddOns) > 0 {
		d.Answers["add_ons"] = append([]string{}, d.AddOns...)
	}
	if d.Explanation == "" {
		var b strings.Builder
		fmt.Fprintf(&b, "%s fits best", t.Name)
		if len(d.Heard) > 0 {
			var phrases []string
			for _, h := range d.Heard {
				phrases = append(phrases, "“"+h.Phrase+"”")
				if len(phrases) == 3 {
					break
				}
			}
			b.WriteString(" because you mentioned " + strings.Join(phrases, ", "))
		}
		b.WriteString(". " + t.Summary)
		d.Explanation = b.String()
	}
}

func meaning(id string) string {
	if cs := templateCues[id]; len(cs) > 0 {
		return cs[0].means
	}
	return id
}

func catalogIndex(id string) int {
	for i, t := range blueprints.Catalog {
		if t.ID == id {
			return i
		}
	}
	return 999
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func titleWords(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// InterpretParams carries a description.
type InterpretParams struct {
	Text  string `json:"text"`
	UseAI bool   `json:"useAI"`
}

// Interpret is the instant, offline reading shown while the user types.
func (a *App) Interpret(ctx context.Context, p InterpretParams) (*Design, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return nil, fmt.Errorf("describe what your business needs")
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	return interpretOffline(text), nil
}

// DesignFromDescription proposes a backend. With UseAI and a saved key it
// asks Claude; otherwise (or if that fails) it uses the offline reader.
func (a *App) DesignFromDescription(ctx context.Context, p InterpretParams) (*Design, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return nil, fmt.Errorf("describe what your business needs")
	}
	if len(text) > 4000 {
		return nil, fmt.Errorf("keep the description under 4,000 characters")
	}
	offline := interpretOffline(text)
	if !p.UseAI {
		return offline, nil
	}
	key, err := a.Vault.GetString(aiKeyRef)
	if err != nil || key == "" {
		offline.Note = "Claude is not set up (Settings → Plain-English assistant), so the offline reader was used."
		return offline, nil
	}
	model := a.Store.LoadSettings().AIModel
	d, err := designWithClaude(ctx, key, model, text)
	if err != nil {
		offline.Note = "Claude could not help this time (" + err.Error() + "), so the offline reader was used."
		a.log("warn", "", "", "Plain-English assistant: "+err.Error())
		return offline, nil
	}
	return d, nil
}

// ---- Claude assist ----

const aiKeyRef = "ai/anthropic"

// aiModels are the models the assistant may use; each supports structured
// outputs, the effort setting and server-side refusal fallbacks.
var aiModels = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1"}

func aiModel(m string) string {
	if contains(aiModels, m) {
		return m
	}
	return aiModels[0]
}

func designSchema() json.RawMessage {
	ids := make([]string, 0, len(blueprints.Catalog))
	for _, t := range blueprints.Catalog {
		ids = append(ids, t.ID)
	}
	addOns := make([]string, 0, len(blueprints.AddOns))
	for _, ad := range blueprints.AddOns {
		addOns = append(addOns, ad.ID)
	}
	nullable := func(t string) map[string]any {
		return map[string]any{"anyOf": []any{map[string]any{"type": t}, map[string]any{"type": "null"}}}
	}
	answerProps := map[string]any{
		"product_name":  nullable("string"),
		"price":         nullable("number"),
		"currency":      map[string]any{"anyOf": []any{map[string]any{"type": "string", "enum": []string{"usd", "eur", "gbp", "cad", "aud"}}, map[string]any{"type": "null"}}},
		"business_name": nullable("string"),
		"domain":        nullable("string"),
		"support_email": nullable("string"),
		"site_origin":   nullable("string"),
		"app_url":       nullable("string"),
		"accounts":      nullable("boolean"),
	}
	req := make([]string, 0, len(answerProps))
	for k := range answerProps {
		req = append(req, k)
	}
	sort.Strings(req)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"template_id":  map[string]any{"type": "string", "enum": ids},
			"alternatives": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": ids}},
			"add_ons":      map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": addOns}},
			"project_name": nullable("string"),
			"answers":      map[string]any{"type": "object", "properties": answerProps, "required": req, "additionalProperties": false},
			"explanation":  map[string]any{"type": "string"},
			"heard": map[string]any{"type": "array", "items": map[string]any{"type": "object",
				"properties": map[string]any{"phrase": map[string]any{"type": "string"}, "means": map[string]any{"type": "string"}},
				"required":   []string{"phrase", "means"}, "additionalProperties": false}},
			"unsupported": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"questions":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required":             []string{"template_id", "alternatives", "add_ons", "project_name", "answers", "explanation", "heard", "unsupported", "questions"},
		"additionalProperties": false,
	}
	bs, _ := json.Marshal(schema)
	return bs
}

func designSystemPrompt() string {
	var b strings.Builder
	b.WriteString(`You help small-business owners set up the backend for their business in Backplane, a desktop app that provisions Cloudflare, Supabase, Stripe, Resend and GitHub from presets.

Read the owner's description and choose the single preset that fits best, plus up to three alternatives. Choose add-ons only when the description asks for that task. Fill an answer only when the description states it (never guess prices, domains or emails; use null otherwise). "explanation" is two plain sentences addressed to the owner saying why the preset fits and what it sets up. "heard" lists the owner's own phrases you relied on and what each means. "unsupported" lists needs no preset or add-on covers, stated honestly. "questions" lists at most three things the owner must still decide.

Presets:
`)
	for _, t := range blueprints.Catalog {
		fmt.Fprintf(&b, "- %s: %s (%s). %s Example: %s\n", t.ID, t.Name, t.Industry, t.Summary, t.Example)
	}
	b.WriteString("\nAdd-ons:\n")
	for _, ad := range blueprints.AddOns {
		fmt.Fprintf(&b, "- %s: %s via %s. %s\n", ad.ID, ad.Task, ad.Provider, ad.Why)
	}
	return b.String()
}

type claudeDesign struct {
	TemplateID   string         `json:"template_id"`
	Alternatives []string       `json:"alternatives"`
	AddOns       []string       `json:"add_ons"`
	ProjectName  *string        `json:"project_name"`
	Answers      map[string]any `json:"answers"`
	Explanation  string         `json:"explanation"`
	Heard        []Heard        `json:"heard"`
	Unsupported  []string       `json:"unsupported"`
	Questions    []string       `json:"questions"`
}

func anthropicClient(key string) anthropic.Client {
	return anthropic.NewClient(option.WithAPIKey(key), option.WithRequestTimeout(90*time.Second), option.WithMaxRetries(2))
}

// designWithClaude sends only the description (no credentials, no project
// data) and accepts only schema-valid preset and add-on ids.
func designWithClaude(ctx context.Context, key, model, text string) (*Design, error) {
	model = aiModel(model)
	client := anthropicClient(key)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := client.Beta.Messages.New(cctx, anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: designSystemPrompt()}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(text))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: designSchema()},
		},
		// A policy decline is retried server-side on Anthropic's
		// recommended fallback model instead of failing the request.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	if err != nil {
		return nil, explainAIError(err)
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return nil, fmt.Errorf("Claude declined this description")
	case anthropic.BetaStopReasonMaxTokens:
		return nil, fmt.Errorf("the answer was cut off")
	}
	var raw string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			raw += tb.Text
		}
	}
	var cd claudeDesign
	if err := json.Unmarshal([]byte(raw), &cd); err != nil {
		return nil, fmt.Errorf("the answer was not valid JSON")
	}
	if _, ok := blueprints.Get(cd.TemplateID); !ok {
		return nil, fmt.Errorf("Claude chose an unknown preset")
	}
	d := &Design{TemplateID: cd.TemplateID, Answers: map[string]any{}, Heard: cd.Heard, Unsupported: cd.Unsupported, Explanation: strings.TrimSpace(cd.Explanation),
		Source: "claude", Model: string(resp.Model), Confidence: 0.9}
	if len(cd.Questions) > 3 {
		cd.Questions = cd.Questions[:3]
	}
	d.Questions = cd.Questions
	if cd.ProjectName != nil {
		d.ProjectName = strings.TrimSpace(*cd.ProjectName)
	}
	for _, id := range cd.AddOns {
		if _, ok := blueprints.AddOnByID(id); ok && !contains(d.AddOns, id) {
			d.AddOns = append(d.AddOns, id)
		}
	}
	for _, id := range cd.Alternatives {
		if t, ok := blueprints.Get(id); ok && id != cd.TemplateID && len(d.Alternatives) < 3 {
			d.Alternatives = append(d.Alternatives, Alternative{TemplateID: id, Name: t.Name, Why: t.Summary})
		}
	}
	for k, v := range cd.Answers {
		if v == nil {
			continue
		}
		switch k {
		case "price":
			if f, ok := v.(float64); ok && f >= 0.5 && f < 1e6 {
				d.Answers[k] = math.Round(f*100) / 100
			}
		case "domain":
			if s, ok := v.(string); ok && domainRE.MatchString(s) {
				d.Answers[k] = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "www.")
			}
		case "support_email":
			if s, ok := v.(string); ok && emailRE.MatchString(s) {
				d.Answers[k] = strings.ToLower(strings.TrimSpace(s))
			}
		case "site_origin", "app_url":
			if s, ok := v.(string); ok && strings.HasPrefix(s, "https://") {
				d.Answers[k] = strings.TrimRight(s, "/")
			}
		default:
			d.Answers[k] = v
		}
	}
	finishDesign(d)
	return d, nil
}

func explainAIError(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		switch apierr.StatusCode {
		case 401:
			return fmt.Errorf("the Anthropic API key was rejected")
		case 403:
			return fmt.Errorf("the API key is not allowed to use this model")
		case 404:
			return fmt.Errorf("the selected model is not available to this key")
		case 429:
			return fmt.Errorf("rate limited by Anthropic — try again in a minute")
		case 400:
			return fmt.Errorf("the request was not accepted (%s)", apierr.Type())
		default:
			if apierr.StatusCode >= 500 {
				return fmt.Errorf("Anthropic's API is busy (HTTP %d)", apierr.StatusCode)
			}
			return fmt.Errorf("HTTP %d from Anthropic", apierr.StatusCode)
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Claude took too long to answer")
	}
	return fmt.Errorf("could not reach Anthropic's API")
}

// SetAIKeyParams stores (or removes, when Key is empty) the Anthropic key.
type SetAIKeyParams struct {
	Key   string `json:"key"`
	Model string `json:"model"`
}

// AIStatus describes the assistant setup.
type AIStatus struct {
	Enabled bool     `json:"enabled"`
	Model   string   `json:"model"`
	Models  []string `json:"models"`
	Hint    string   `json:"hint,omitempty"`
	Privacy string   `json:"privacy"`
}

const aiPrivacy = "Only the description you type is sent to Anthropic's API. Credentials, project data and logs never leave this computer through the assistant."

// SetAIKey verifies and saves an Anthropic API key for the plain-English
// assistant (or removes it).
func (a *App) SetAIKey(ctx context.Context, p SetAIKeyParams) (*AIStatus, error) {
	st := a.Store.LoadSettings()
	key := strings.TrimSpace(p.Key)
	if key == "" {
		_ = a.Vault.Delete(aiKeyRef)
		st.AIProvider = ""
		if err := a.Store.SaveSettings(st); err != nil {
			return nil, err
		}
		return &AIStatus{Enabled: false, Model: aiModel(st.AIModel), Models: aiModels, Privacy: aiPrivacy}, nil
	}
	if !strings.HasPrefix(key, "sk-ant-") {
		return nil, fmt.Errorf("that does not look like an Anthropic API key (they start with sk-ant-)")
	}
	model := aiModel(orStr(p.Model, st.AIModel))
	client := anthropicClient(key)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := client.Models.Get(cctx, model, anthropic.ModelGetParams{}); err != nil {
		return nil, explainAIError(err)
	}
	if err := a.Vault.PutString(aiKeyRef, key, "Anthropic API key (plain-English assistant)"); err != nil {
		return nil, err
	}
	st.AIProvider, st.AIModel = "anthropic", model
	if err := a.Store.SaveSettings(st); err != nil {
		return nil, err
	}
	a.log("info", "", "", "Plain-English assistant enabled with "+model)
	return &AIStatus{Enabled: true, Model: model, Models: aiModels, Privacy: aiPrivacy}, nil
}

// AIState reports whether the assistant is configured.
func (a *App) AIState(ctx context.Context) (*AIStatus, error) {
	st := a.Store.LoadSettings()
	s := &AIStatus{Model: aiModel(st.AIModel), Models: aiModels, Privacy: aiPrivacy}
	if v, err := a.Vault.GetString(aiKeyRef); err == nil && v != "" {
		s.Enabled = true
		if len(v) > 12 {
			s.Hint = v[:10] + "…" + v[len(v)-4:]
		}
	}
	return s, nil
}
