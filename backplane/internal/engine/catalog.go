package engine

import (
	"encoding/json"
	"math"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/stripe"
)

// CatalogItem is one item a backend sells, as recorded in its blueprint
// (the blueprints package writes it; the engine only reads it).
type CatalogItem struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Billing     string  `json:"billing"`
	YearlyPrice float64 `json:"yearlyPrice,omitempty"`
	Minutes     int     `json:"minutes,omitempty"`
}

// Cents is the item's main price in the smallest currency unit.
func (it CatalogItem) Cents() int64 { return int64(math.Round(it.Price * 100)) }

// Catalog returns the items a blueprint sells.
func Catalog(bp *core.Blueprint) []CatalogItem {
	var items []CatalogItem
	if s := bp.Param("catalog_items"); s != "" {
		_ = json.Unmarshal([]byte(s), &items)
	}
	return items
}

// probePrice picks a built price for no-charge probes: a one-time price when
// there is one (mode "payment"), otherwise a recurring one ("subscription").
func (c *Checker) probePrice() (*core.ResourceSpec, string) {
	var recurring *core.ResourceSpec
	for i := range c.P.Blueprint.Resources {
		r := &c.P.Blueprint.Resources[i]
		if r.Kind != stripe.KindPrice || c.State(r.Key) == nil {
			continue
		}
		if providers.Str(r.Props, "interval") == "" {
			return r, "payment"
		}
		if recurring == nil {
			recurring = r
		}
	}
	if recurring != nil {
		return recurring, "subscription"
	}
	return nil, ""
}
