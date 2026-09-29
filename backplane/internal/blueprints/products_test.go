package blueprints

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestItemsValidation(t *testing.T) {
	store, _ := Get("software-store")
	shop, _ := Get("ecommerce")
	saas, _ := Get("subscription-saas")
	cases := []struct {
		name    string
		tpl     Template
		answers Answers
		wantErr string
		check   func(t *testing.T, items []Item)
	}{
		{"no products", store, Answers{}, "add at least one", nil},
		{"no name", store, Answers{"products": []any{map[string]any{"price": 10.0}}}, "needs a name", nil},
		{"too cheap", store, Answers{"products": []any{map[string]any{"name": "A", "price": 0.2}}}, "at least 0.50", nil},
		{"physical goods are one-time", shop, Answers{"products": []any{map[string]any{"name": "Candle", "price": 20.0, "billing": "month"}}}, "one-time items only", nil},
		{"yearly needs monthly", saas, Answers{"products": []any{map[string]any{"name": "Pro", "price": 190.0, "billing": "year", "yearlyPrice": 100.0}}}, "monthly", nil},
		{"legacy answers", store, Answers{"product_name": "Pixel Presets", "price": 29.0}, "", func(t *testing.T, items []Item) {
			if len(items) != 1 || items[0].Key != "main" || items[0].Name != "Pixel Presets" || items[0].Cents() != 2900 || items[0].Billing != BillOnce {
				t.Fatalf("legacy item: %+v", items)
			}
		}},
		{"keys are stable and unique", saas, Answers{"products": []any{
			map[string]any{"name": "Pro plan", "price": "19", "yearlyPrice": 190.0},
			map[string]any{"name": "Pro Plan!", "price": 29.0},
			map[string]any{"key": "team", "name": "Renamed team plan", "price": 49.0, "billing": "yearly"},
		}}, "", func(t *testing.T, items []Item) {
			if items[0].Key != "pro_plan" || items[1].Key != "pro_plan_2" || items[2].Key != "team" {
				t.Fatalf("keys: %s %s %s", items[0].Key, items[1].Key, items[2].Key)
			}
			if got := items[0].Intervals(); len(got) != 2 || got[1] != BillYear || items[0].CentsFor(BillYear) != 19000 {
				t.Fatalf("monthly + yearly: %v", got)
			}
			if items[2].Billing != BillYear {
				t.Fatalf("billing: %s", items[2].Billing)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := Items(tc.tpl, tc.answers, "Backend")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, items)
		})
	}
}

// No preset may name what's for sale on the person's behalf.
func TestNoDefaultProductName(t *testing.T) {
	for _, tpl := range Catalog {
		for _, q := range tpl.Questions {
			if q.Key == "product_name" || q.Key == "price" {
				t.Errorf("%s still asks %s", tpl.ID, q.Key)
			}
			if s, ok := q.Default.(string); ok && strings.EqualFold(s, "My App") {
				t.Errorf("%s: %s defaults to %q", tpl.ID, q.Key, s)
			}
		}
		if tpl.Mode == "shipping" {
			for _, q := range tpl.Questions {
				if q.Kind == "file" || strings.HasPrefix(q.Key, "download") || q.Key == "max_downloads" {
					t.Errorf("%s (physical goods) asks about downloads: %s", tpl.ID, q.Key)
				}
			}
		}
	}
}

func TestCatalogBuildsOneStripePricePerInterval(t *testing.T) {
	tpl, _ := Get("software-store")
	bp, err := Build(tpl, "Presets", Answers{"domain": "p.com", "products": []any{
		map[string]any{"name": "Personal", "price": 29.0},
		map[string]any{"name": "Pro", "price": 9.0, "billing": "month"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"product_personal", "price_personal_once", "product_pro", "price_pro_month"}
	for _, k := range want {
		if bp.ResourceByKey(k) == nil {
			t.Errorf("missing resource %s", k)
		}
	}
	var worker map[string]any
	for _, r := range bp.Resources {
		if r.Kind == "cloudflare.worker" {
			worker = r.Props
			for _, k := range []string{"price_personal_once", "price_pro_month"} {
				if !contains(r.DependsOn, k) {
					t.Errorf("Worker does not depend on %s", k)
				}
			}
		}
	}
	vars := worker["vars"].(map[string]any)
	raw := strings.NewReplacer("{{out:price_personal_once.id}}", "price_1", "{{out:price_pro_month.id}}", "price_2").Replace(vars["CATALOG"].(string))
	var cat struct {
		Items []struct {
			Key     string            `json:"key"`
			Billing string            `json:"billing"`
			Prices  map[string]string `json:"prices"`
			Amounts map[string]int64  `json:"amounts"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &cat); err != nil {
		t.Fatalf("CATALOG is not JSON: %v\n%s", err, raw)
	}
	if len(cat.Items) != 2 || cat.Items[0].Prices["once"] != "price_1" || cat.Items[1].Amounts["month"] != 900 || cat.Items[1].Billing != "month" {
		t.Fatalf("catalog: %+v", cat)
	}
	var events []any
	for _, r := range bp.Resources {
		if r.Kind == "stripe.webhook_endpoint" {
			events = r.Props["events"].([]any)
		}
	}
	found := false
	for _, e := range events {
		found = found || e == "customer.subscription.deleted"
	}
	if !found {
		t.Error("a store selling subscriptions must listen for canceled subscriptions")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
