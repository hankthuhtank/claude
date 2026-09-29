package app

import (
	"context"
	"sort"
	"strings"

	"safisolutions.org/backplane/internal/blueprints"
	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
)

// ProductsView is the backend's Products tab: what it sells, at what price,
// and whether each price is live in Stripe yet.
type ProductsView struct {
	Spec     *blueprints.ProductsSpec `json:"spec"`
	Currency string                   `json:"currency"`
	Items    []ProductRow             `json:"items"`
	Removed  []string                 `json:"removed,omitempty"` // live in Stripe, no longer sold (the next build retires them)
	Pending  bool                     `json:"pending"`           // saved changes not built yet
	Built    bool                     `json:"built"`
	API      string                   `json:"api,omitempty"`   // the Worker's address
	Order    string                   `json:"order,omitempty"` // where customers order: /checkout, /book, /order, /billing/checkout
}

// ProductRow is one item and its prices.
type ProductRow struct {
	blueprints.Item
	Prices []PriceRow `json:"prices"`
}

// PriceRow is one price of an item in this environment.
type PriceRow struct {
	Interval string `json:"interval"`
	Amount   int64  `json:"amount"`               // what the backend should charge
	Live     int64  `json:"liveAmount,omitempty"` // what Stripe charges now
	PriceID  string `json:"priceId,omitempty"`
	Status   string `json:"status"` // live | changed | new
}

// Products describes what a backend sells in one environment.
func (a *App) Products(ctx context.Context, p DashboardParams) (*ProductsView, error) {
	pr, err := a.project(p.ProjectID)
	if err != nil {
		return nil, err
	}
	env, err := a.envOf(pr, p.Env)
	if err != nil {
		return nil, err
	}
	man, err := a.Store.LoadManifest(pr.ID, env)
	if err != nil {
		return nil, err
	}
	t, _ := blueprints.Get(pr.TemplateID)
	v := &ProductsView{Spec: t.ProductsSpec(), Currency: strings.ToLower(orStr(pr.Blueprint.Param("currency"), "usd")), Items: []ProductRow{}}
	if v.Spec == nil {
		return v, nil
	}
	want := map[string]bool{}
	for _, it := range blueprints.CatalogItems(&pr.Blueprint) {
		row := ProductRow{Item: it}
		for _, iv := range it.Intervals() {
			key := blueprints.PriceKey(it.Key, iv)
			want[key] = true
			price := PriceRow{Interval: iv, Amount: it.CentsFor(iv), Status: "new"}
			if st := man.Resources[key]; st != nil && st.Status != core.StateDeleted {
				v.Built = true
				price.PriceID = st.ID
				price.Live = providers.Int(st.Applied, "unit_amount")
				price.Status = "live"
				if price.Live != price.Amount {
					price.Status = "changed"
				}
			}
			if price.Status != "live" {
				v.Pending = true
			}
			row.Prices = append(row.Prices, price)
		}
		v.Items = append(v.Items, row)
	}
	var gone []string
	for key, st := range man.Resources {
		if st.Kind == "stripe.price" && st.Status != core.StateDeleted && !want[key] {
			gone = append(gone, orStr(st.Name, key))
			v.Pending = true
		}
	}
	sort.Strings(gone)
	v.Removed = gone
	for _, st := range man.Resources {
		if st.Kind == "cloudflare.worker" {
			v.API = st.Output("url")
		}
	}
	switch {
	case pr.Blueprint.Param("order_path") != "":
		v.Order = pr.Blueprint.Param("order_path")
	case t.Engine == "commerce":
		v.Order = "/checkout"
	default:
		v.Order = "/billing/checkout"
	}
	return v, nil
}
