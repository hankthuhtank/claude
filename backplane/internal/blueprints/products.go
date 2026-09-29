package blueprints

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"safisolutions.org/backplane/internal/core"
)

// Products & prices: every preset that takes payments sells a list of items
// (products, plans, services, menu items). Each item becomes one Stripe
// product with one price per billing interval, and the Worker gets a catalog
// that maps item keys to those prices. Browsers only ever send an item key
// and a quantity; the amount always comes from Stripe.

// Billing intervals.
const (
	BillOnce  = "once"
	BillMonth = "month"
	BillYear  = "year"
)

// MaxItems caps how many items one backend sells (Stripe allows more; a
// longer list belongs in a database-backed catalog).
const MaxItems = 40

// ProductsSpec configures the "Products & prices" question for a preset.
type ProductsSpec struct {
	Noun        string   `json:"noun"`        // "product", "plan", "service", "menu item"
	Billing     []string `json:"billing"`     // allowed intervals; the first is the default
	Yearly      bool     `json:"yearly"`      // monthly items may add a yearly price
	Quantity    bool     `json:"quantity"`    // customers may buy more than one / fill a cart
	MaxQuantity int      `json:"maxQuantity"` // per line
	PriceLabel  string   `json:"priceLabel"`  // "Price", "Deposit"
	Placeholder string   `json:"placeholder"` // example item name
	Minutes     bool     `json:"minutes"`     // items have a duration (bookable services)
}

// Item is one thing customers can buy.
type Item struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Price       float64 `json:"price"`
	Billing     string  `json:"billing"`
	YearlyPrice float64 `json:"yearlyPrice,omitempty"` // monthly items: optional yearly price
	Minutes     int     `json:"minutes,omitempty"`     // bookable services
}

// Cents is the item's main price in the smallest currency unit.
func (it Item) Cents() int64 { return toCents(it.Price) }

// Intervals lists the billing intervals the item is sold at, main one first.
func (it Item) Intervals() []string {
	out := []string{it.Billing}
	if it.Billing == BillMonth && it.YearlyPrice > 0 {
		out = append(out, BillYear)
	}
	return out
}

// CentsFor is the price for one interval.
func (it Item) CentsFor(interval string) int64 {
	if interval == BillYear && it.Billing == BillMonth {
		return toCents(it.YearlyPrice)
	}
	return it.Cents()
}

func toCents(v float64) int64 { return int64(math.Round(v * 100)) }

// ProductKey and PriceKey are the blueprint resource keys for an item.
func ProductKey(item string) string            { return "product_" + item }
func PriceKey(item, interval string) string     { return "price_" + item + "_" + interval }
func productsQuestion(spec ProductsSpec) Question {
	label := "Products & prices"
	switch spec.Noun {
	case "plan":
		label = "Plans & prices"
	case "membership tier":
		label = "Membership tiers & prices"
	case "service":
		label = "Services & deposits"
	case "menu item":
		label = "Menu & prices"
	}
	return Question{Key: "products", Label: label, Kind: "products", Required: true, Products: &spec,
		Help: "Checkout only accepts these items. Prices are set here and checked on the server, so customers can't change what they pay. You can change them after the build on the backend's Products tab."}
}

var itemKeyRE = regexp.MustCompile(`[^a-z0-9]+`)

// itemKey turns a name into a short, stable key ("Pro plan" → "pro_plan").
func itemKey(name string) string {
	k := strings.Trim(itemKeyRE.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if len(k) > 24 {
		k = strings.Trim(k[:24], "_")
	}
	if k == "" {
		k = "item"
	}
	if k[0] >= '0' && k[0] <= '9' {
		k = "i" + k
	}
	return k
}

var validKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// cleanText removes placeholder braces and control characters from text a
// person typed, so it can never be read as a {{placeholder}}.
func cleanText(s string, max int) string {
	s = strings.NewReplacer("{{", "{", "}}", "}").Replace(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, s)
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

func anyFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(t), "$€£")), 64)
		return f, err == nil
	}
	return 0, false
}

func anyStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// Items reads and validates the products list from a preset's answers. Older
// projects that answered "product_name" and "price" become a one-item list.
func Items(t Template, a Answers, fallbackName string) ([]Item, error) {
	spec := t.ProductsSpec()
	if spec == nil {
		return nil, nil
	}
	var raw []any
	switch v := a["products"].(type) {
	case []any:
		raw = v
	case []map[string]any:
		for _, m := range v {
			raw = append(raw, m)
		}
	case []Item:
		for _, it := range v {
			raw = append(raw, map[string]any{"key": it.Key, "name": it.Name, "description": it.Description, "price": it.Price, "billing": it.Billing, "yearlyPrice": it.YearlyPrice, "minutes": it.Minutes})
		}
	case string:
		_ = json.Unmarshal([]byte(v), &raw)
	}
	if len(raw) == 0 {
		// Answers from before products & prices existed.
		if _, ok := a["price"]; ok {
			name := a.str("product_name", fallbackName)
			raw = []any{map[string]any{"key": "main", "name": name, "price": a["price"]}}
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("add at least one %s with a price", spec.Noun)
	}
	if len(raw) > MaxItems {
		return nil, fmt.Errorf("up to %d %ss can be sold from one backend", MaxItems, spec.Noun)
	}
	allowed := map[string]bool{}
	for _, b := range spec.Billing {
		allowed[b] = true
	}
	used := map[string]bool{}
	var out []Item
	for i, x := range raw {
		m, _ := x.(map[string]any)
		if m == nil {
			continue
		}
		it := Item{Name: cleanText(anyStr(m, "name"), 80), Description: cleanText(anyStr(m, "description"), 240)}
		row := fmt.Sprintf("%s %d", spec.Noun, i+1)
		if it.Name == "" {
			return nil, fmt.Errorf("%s needs a name", row)
		}
		row = "“" + it.Name + "”"
		p, ok := anyFloat(m["price"])
		if !ok || p < 0.5 {
			return nil, fmt.Errorf("%s needs a %s of at least 0.50 (Stripe's minimum charge)", row, strings.ToLower(orDefault(spec.PriceLabel, "price")))
		}
		if p > 999999 {
			return nil, fmt.Errorf("%s: %s is too large", row, strings.ToLower(orDefault(spec.PriceLabel, "price")))
		}
		it.Price = math.Round(p*100) / 100
		it.Billing = strings.ToLower(anyStr(m, "billing", "interval"))
		switch it.Billing {
		case "":
			it.Billing = spec.Billing[0]
		case "one-time", "one_time", "onetime", "payment":
			it.Billing = BillOnce
		case "monthly":
			it.Billing = BillMonth
		case "yearly", "annual", "annually":
			it.Billing = BillYear
		}
		if !allowed[it.Billing] {
			return nil, fmt.Errorf("%s: this preset sells %s", row, billingList(spec.Billing))
		}
		if y, ok := anyFloat(firstOf(m, "yearlyPrice", "yearly_price")); ok && y > 0 {
			if !spec.Yearly || it.Billing != BillMonth {
				return nil, fmt.Errorf("%s: a yearly price can only be added to a monthly %s", row, spec.Noun)
			}
			if y < 0.5 {
				return nil, fmt.Errorf("%s: the yearly price must be at least 0.50", row)
			}
			it.YearlyPrice = math.Round(y*100) / 100
		}
		if spec.Minutes {
			if mins, ok := anyFloat(m["minutes"]); ok && mins > 0 {
				it.Minutes = int(mins)
			}
			if it.Minutes <= 0 {
				it.Minutes = 30
			}
		}
		key := strings.ToLower(anyStr(m, "key"))
		if !validKeyRE.MatchString(key) {
			key = itemKey(it.Name)
		}
		for base, n := key, 2; used[key]; n++ {
			key = fmt.Sprintf("%s_%d", strings.TrimRight(base[:min(len(base), 28)], "_"), n)
		}
		used[key] = true
		it.Key = key
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("add at least one %s with a price", spec.Noun)
	}
	return out, nil
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func billingList(b []string) string {
	words := map[string]string{BillOnce: "one-time", BillMonth: "monthly", BillYear: "yearly"}
	var parts []string
	for _, x := range b {
		parts = append(parts, words[x])
	}
	if len(parts) == 1 {
		return parts[0] + " items only"
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1] + " items"
}

// IntervalLabel is "one-time", "/ month" or "/ year" for summaries.
func IntervalLabel(interval string) string {
	switch interval {
	case BillMonth:
		return "/ month"
	case BillYear:
		return "/ year"
	}
	return "one-time"
}

// ItemsAnswer converts items back to the answer format (the UI's list).
func ItemsAnswer(items []Item) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{"key": it.Key, "name": it.Name, "price": it.Price, "billing": it.Billing}
		if it.Description != "" {
			m["description"] = it.Description
		}
		if it.YearlyPrice > 0 {
			m["yearlyPrice"] = it.YearlyPrice
		}
		if it.Minutes > 0 {
			m["minutes"] = it.Minutes
		}
		out = append(out, m)
	}
	return out
}

// catalogVar renders the Worker's CATALOG variable: item keys, names and
// amounts are fixed now, price ids are placeholders resolved at deploy time.
func catalogVar(items []Item, currency string) string {
	type entry struct {
		Key         string            `json:"key"`
		Name        string            `json:"name"`
		Description string            `json:"description,omitempty"`
		Minutes     int               `json:"minutes,omitempty"`
		Billing     string            `json:"billing"`
		Prices      map[string]string `json:"prices"`
		Amounts     map[string]int64  `json:"amounts"`
	}
	cat := struct {
		Currency string  `json:"currency"`
		Items    []entry `json:"items"`
	}{Currency: strings.ToLower(currency)}
	for _, it := range items {
		e := entry{Key: it.Key, Name: it.Name, Description: it.Description, Minutes: it.Minutes, Billing: it.Billing, Prices: map[string]string{}, Amounts: map[string]int64{}}
		for _, iv := range it.Intervals() {
			e.Prices[iv] = "{{out:" + PriceKey(it.Key, iv) + ".id}}"
			e.Amounts[iv] = it.CentsFor(iv)
		}
		cat.Items = append(cat.Items, e)
	}
	return jsonText(cat)
}

// addCatalog adds a Stripe product and its prices for every item, and
// returns the price resource keys (the Worker depends on them).
func addCatalog(bp *core.Blueprint, items []Item, currency, component string) []string {
	var keys []string
	stripeComp := bp.ComponentByKey(component)
	for _, it := range items {
		pk := ProductKey(it.Key)
		bp.Resources = append(bp.Resources, core.ResourceSpec{Key: pk, Kind: "stripe.product", Provider: "stripe", Component: component, Name: it.Name,
			Title: "Stripe product “" + it.Name + "”", Props: map[string]any{"name": it.Name, "description": it.Description, "metadata": map[string]any{"backplane_item": it.Key}}})
		if stripeComp != nil {
			stripeComp.Resources = append(stripeComp.Resources, pk)
		}
		for _, iv := range it.Intervals() {
			cents := it.CentsFor(iv)
			props := map[string]any{"product": "{{out:" + pk + ".id}}", "unit_amount": cents, "currency": "{{param:currency}}",
				"nickname": it.Name + " " + IntervalLabel(iv), "metadata": map[string]any{"backplane_item": it.Key, "backplane_interval": iv}}
			if iv != BillOnce {
				props["interval"] = iv
			}
			title := "Price " + moneyLabel(cents, currency) + " " + IntervalLabel(iv) + " — " + it.Name
			key := PriceKey(it.Key, iv)
			bp.Resources = append(bp.Resources, core.ResourceSpec{Key: key, Kind: "stripe.price", Provider: "stripe", Component: component, Name: it.Name, Title: title, Props: props})
			if stripeComp != nil {
				stripeComp.Resources = append(stripeComp.Resources, key)
			}
			keys = append(keys, key)
		}
	}
	return keys
}

// catalogSummary is "Candle $24 · Soap $8 (+3 more)" for notes and emails.
func catalogSummary(items []Item, currency string) string {
	var parts []string
	for i, it := range items {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(items)-3))
			break
		}
		p := it.Name + " " + moneyLabel(it.Cents(), currency)
		if it.Billing != BillOnce {
			p += IntervalLabel(it.Billing)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " · ")
}

// CatalogItems returns a project's items as built into its blueprint.
func CatalogItems(bp *core.Blueprint) []Item {
	var items []Item
	if s := bp.Param("catalog_items"); s != "" {
		_ = json.Unmarshal([]byte(s), &items)
	}
	return items
}
