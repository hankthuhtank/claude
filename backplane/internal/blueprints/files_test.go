package blueprints

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"safisolutions.org/backplane/internal/core"
)

var placeholderRe = regexp.MustCompile(`\{\{\s*([a-z]+)(?::([^}]+?))?\s*\}\}`)

// TestWranglerConfigMatchesWorker checks, for every preset, that the
// committed wrangler.jsonc is valid JSON once resolved (even when a value
// contains quotes), and carries exactly the Worker's vars, bindings and crons.
func TestWranglerConfigMatchesWorker(t *testing.T) {
	for _, tpl := range Catalog {
		answers := Answers{"domain": "joes-shop.com", "business_name": `Joe's "Best" Shop`}
		if spec := tpl.ProductsSpec(); spec != nil {
			answers["products"] = []any{map[string]any{"name": `Joe's "Best" {{Thing}}`, "price": 12.5}, map[string]any{"name": "Second item", "price": 30.0, "billing": spec.Billing[len(spec.Billing)-1]}}
		}
		bp, err := Build(tpl, `Joe's "Best" Shop`, answers)
		if err != nil {
			t.Fatalf("%s: build: %v", tpl.ID, err)
		}
		files, err := GenerateFiles(&core.Project{Name: `Joe's "Best" Shop`, Blueprint: bp})
		if err != nil {
			t.Fatalf("%s: files: %v", tpl.ID, err)
		}
		raw := files["worker/wrangler.jsonc"]
		if raw == "" {
			t.Fatalf("%s: no wrangler.jsonc", tpl.ID)
		}
		var lines []string
		for _, l := range strings.Split(raw, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "//") {
				lines = append(lines, l)
			}
		}
		// Resolve every placeholder to a hostile value, escaped the way the
		// codet resolver escapes values for JSON files.
		body := placeholderRe.ReplaceAllStringFunc(strings.Join(lines, "\n"), func(string) string {
			b, _ := json.Marshal(`a "quoted" \ value`)
			return string(b[1 : len(b)-1])
		})
		var cfg struct {
			Name     string            `json:"name"`
			Vars     map[string]string `json:"vars"`
			KV       []map[string]any  `json:"kv_namespaces"`
			R2       []map[string]any  `json:"r2_buckets"`
			Triggers struct {
				Crons []string `json:"crons"`
			} `json:"triggers"`
		}
		if err := json.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatalf("%s: wrangler.jsonc is not valid JSON: %v\n%s", tpl.ID, err, body)
		}
		var worker *core.ResourceSpec
		for i := range bp.Resources {
			if bp.Resources[i].Kind == "cloudflare.worker" {
				worker = &bp.Resources[i]
			}
		}
		vars, _ := worker.Props["vars"].(map[string]any)
		if len(cfg.Vars) != len(vars) {
			t.Errorf("%s: wrangler has %d vars, Worker has %d", tpl.ID, len(cfg.Vars), len(vars))
		}
		for k := range vars {
			if _, ok := cfg.Vars[k]; !ok {
				t.Errorf("%s: var %s missing from wrangler.jsonc", tpl.ID, k)
			}
		}
		bindings, _ := worker.Props["bindings"].([]any)
		if len(cfg.KV)+len(cfg.R2) != len(bindings) {
			t.Errorf("%s: wrangler has %d bindings, Worker has %d", tpl.ID, len(cfg.KV)+len(cfg.R2), len(bindings))
		}
		crons, _ := worker.Props["crons"].([]any)
		if len(cfg.Triggers.Crons) != len(crons) {
			t.Errorf("%s: wrangler has %d crons, Worker has %d", tpl.ID, len(cfg.Triggers.Crons), len(crons))
		}
		shipping := bp.Param("fulfillment_mode") == "shipping"
		if shipping && (strings.Contains(raw, "{{out:downloads.") || len(cfg.R2) > 0) {
			t.Errorf("%s: shipping preset must not reference the download bucket\n%s", tpl.ID, raw)
		}
		for _, secret := range []string{"{{secret:", "{{conn:", "{{gen:"} {
			if strings.Contains(raw, secret) {
				t.Errorf("%s: wrangler.jsonc contains a secret placeholder %s", tpl.ID, secret)
			}
		}
	}
}
