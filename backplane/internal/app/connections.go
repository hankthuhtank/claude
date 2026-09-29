package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"safisolutions.org/backplane/internal/blueprints"
	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/engine"
	"safisolutions.org/backplane/internal/providers"
	"safisolutions.org/backplane/internal/providers/stripe"
	"safisolutions.org/backplane/internal/vault"
)

// ConnectionView is a connection as shown in the UI (never the secret).
type ConnectionView struct {
	core.Connection
	Hint   string            `json:"hint"`
	Fields map[string]string `json:"fieldHints"` // secret field -> hint
	UsedBy []string          `json:"usedBy"`     // "Project (env)"
	Info   providers.Info    `json:"providerInfo"`
}

// CatalogResult lists templates and capabilities.
type CatalogResult struct {
	Templates    []blueprints.Template `json:"templates"`
	Industries   []blueprints.Industry `json:"industries"`
	AddOns       []blueprints.AddOn    `json:"addOns"`
	Capabilities []core.CapabilityInfo `json:"capabilities"`
	Emails       []map[string]any      `json:"emails"`
}

// Catalog returns the template catalogue.
func (a *App) Catalog(ctx context.Context) (*CatalogResult, error) {
	return &CatalogResult{Templates: blueprints.Catalog, Industries: blueprints.Industries, AddOns: blueprints.AddOns, Capabilities: core.Capabilities, Emails: blueprints.SystemEmails()}, nil
}

// Providers lists every provider with its maturity and guide.
func (a *App) Providers(ctx context.Context) ([]providers.Info, error) { return a.Reg.All(), nil }

// ListConnections returns saved connections with hints and usage.
func (a *App) ListConnections(ctx context.Context) ([]ConnectionView, error) {
	cs, err := a.Store.LoadConnections()
	if err != nil {
		return nil, err
	}
	projects, _ := a.Store.ListProjects()
	out := make([]ConnectionView, 0, len(cs))
	for _, c := range cs {
		v := ConnectionView{Connection: c, Fields: map[string]string{}}
		if s, err := a.Vault.GetString(c.SecretRef); err == nil {
			v.Hint = vault.Hint(s)
		}
		if p, ok := a.Reg.Provider(c.Provider); ok {
			v.Info = p.Info()
			for _, f := range v.Info.Fields {
				if f.Secret && f.Key != "token" {
					if s, err := a.Vault.GetString(engine.ConnKey(c.ID, f.Key)); err == nil {
						v.Fields[f.Key] = vault.Hint(s)
					}
				}
			}
		}
		for _, p := range projects {
			for env, links := range p.Connections {
				if links[c.Provider] == c.ID {
					v.UsedBy = append(v.UsedBy, p.Name+" ("+env+")")
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// AddConnectionParams creates a connection.
type AddConnectionParams struct {
	Provider string            `json:"provider"`
	Label    string            `json:"label"`
	Fields   map[string]string `json:"fields"`
	Practice bool              `json:"practice"`
}

// AddConnectionResult is the saved connection and its first verification.
type AddConnectionResult struct {
	Connection ConnectionView          `json:"connection"`
	Verify     *providers.VerifyResult `json:"verify"`
}

// AddConnection stores credentials in the vault, verifies them immediately
// and saves the connection (even when verification warns, so the user can fix
// permissions without re-typing).
func (a *App) AddConnection(ctx context.Context, p AddConnectionParams) (*AddConnectionResult, error) {
	prov, ok := a.Reg.Provider(p.Provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider %s", p.Provider)
	}
	info := prov.Info()
	if info.Maturity == providers.MaturityPlanned {
		return nil, fmt.Errorf("%s is not available yet", info.Name)
	}
	secret := strings.TrimSpace(p.Fields["token"])
	if secret == "" {
		return nil, fmt.Errorf("the %s credential is required", info.Name)
	}
	id := engine.NewID("conn")
	c := core.Connection{ID: id, Provider: p.Provider, Label: strings.TrimSpace(p.Label), AuthMethod: "token", SecretRef: engine.ConnKey(id, "token"),
		Settings: map[string]string{}, CreatedAt: time.Now().UTC(), Practice: p.Practice, Status: core.HealthUnknown}
	for _, f := range info.Fields {
		v := strings.TrimSpace(p.Fields[f.Key])
		if f.Key == "token" || v == "" {
			continue
		}
		if f.Secret {
			if err := a.Vault.PutString(engine.ConnKey(id, f.Key), v, info.Name+" "+f.Label); err != nil {
				return nil, err
			}
			c.Settings["has_"+f.Key] = "true"
			continue
		}
		c.Settings[f.Key] = v
	}
	if p.Provider == "stripe" {
		c.Mode = stripe.ModeOf(secret)
		c.Settings["mode"] = c.Mode
		if wk := strings.TrimSpace(p.Fields["worker_key"]); wk != "" && stripe.ModeOf(wk) != c.Mode {
			return nil, fmt.Errorf("the Worker key is a %s key but the main key is %s — use keys from the same mode", stripe.ModeOf(wk), c.Mode)
		}
	}
	if err := a.Vault.PutString(c.SecretRef, secret, info.Name+" credential"); err != nil {
		return nil, err
	}
	if c.Label == "" {
		c.Label = info.Name
		if c.Mode != "" {
			c.Label += " (" + c.Mode + ")"
		}
	}
	vr := a.verifyInto(ctx, &c)
	cs, err := a.Store.LoadConnections()
	if err != nil {
		return nil, err
	}
	cs = append(cs, c)
	if err := a.Store.SaveConnections(cs); err != nil {
		return nil, err
	}
	a.log("info", "", "", fmt.Sprintf("Connected %s (%s): %s", info.Name, c.Label, vr.Summary))
	views, _ := a.ListConnections(ctx)
	for _, v := range views {
		if v.ID == c.ID {
			return &AddConnectionResult{Connection: v, Verify: vr}, nil
		}
	}
	return &AddConnectionResult{Verify: vr}, nil
}

// verifyInto runs Level 1 verification and updates the connection record.
func (a *App) verifyInto(ctx context.Context, c *core.Connection) *providers.VerifyResult {
	if c.Practice && !a.practiceRunning() {
		c.Status, c.StatusNote, c.VerifiedAt = core.HealthOff, practiceOffNote, nil
		return &providers.VerifyResult{Health: core.HealthOff, Summary: practiceOffNote}
	}
	prov, _ := a.Reg.Provider(c.Provider)
	conn, err := a.Engine.OpenConn(*c, core.LogEntry{})
	var vr *providers.VerifyResult
	if err != nil {
		p := providers.Translate(c.Provider, "open the connection", err)
		vr = &providers.VerifyResult{Health: core.HealthFail, Summary: p.Summary, Problem: p}
	} else {
		vr, err = prov.Verify(ctx, conn)
		if err != nil {
			p := providers.Translate(c.Provider, "verify the connection", err)
			vr = &providers.VerifyResult{Health: core.HealthFail, Summary: p.Summary, Problem: p}
		}
	}
	now := time.Now().UTC()
	c.Status, c.StatusNote, c.VerifiedAt = vr.Health, vr.Summary, &now
	c.Warnings, c.Details = vr.Warnings, vr.Details
	if vr.AccountID != "" {
		c.AccountID = vr.AccountID
	}
	if vr.AccountName != "" {
		c.AccountName = vr.AccountName
	}
	if vr.Mode != "" && c.Provider == "stripe" {
		c.Mode = vr.Mode
	}
	if len(vr.Scopes) > 0 {
		c.Scopes = vr.Scopes
	}
	if c.Settings == nil {
		c.Settings = map[string]string{}
	}
	for k, v := range vr.Settings {
		if c.Settings[k] == "" {
			c.Settings[k] = v
		}
	}
	return vr
}

// UpdateConnectionParams replaces credential fields or settings.
type UpdateConnectionParams struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Fields map[string]string `json:"fields"`
}

// UpdateConnection rotates credentials or changes settings, then re-verifies.
func (a *App) UpdateConnection(ctx context.Context, p UpdateConnectionParams) (*AddConnectionResult, error) {
	cs, err := a.Store.LoadConnections()
	if err != nil {
		return nil, err
	}
	for i := range cs {
		if cs[i].ID != p.ID {
			continue
		}
		c := &cs[i]
		prov, _ := a.Reg.Provider(c.Provider)
		info := prov.Info()
		if p.Label != "" {
			c.Label = p.Label
		}
		for _, f := range info.Fields {
			v, present := p.Fields[f.Key]
			if !present {
				continue
			}
			v = strings.TrimSpace(v)
			switch {
			case f.Key == "token" && v != "":
				if err := a.Vault.PutString(c.SecretRef, v, info.Name+" credential"); err != nil {
					return nil, err
				}
				if c.Provider == "stripe" {
					c.Mode = stripe.ModeOf(v)
					c.Settings["mode"] = c.Mode
				}
			case f.Secret && v != "":
				if err := a.Vault.PutString(engine.ConnKey(c.ID, f.Key), v, info.Name+" "+f.Label); err != nil {
					return nil, err
				}
				c.Settings["has_"+f.Key] = "true"
			case f.Secret && v == "":
				_ = a.Vault.Delete(engine.ConnKey(c.ID, f.Key))
				delete(c.Settings, "has_"+f.Key)
			case !f.Secret:
				if c.Settings == nil {
					c.Settings = map[string]string{}
				}
				c.Settings[f.Key] = v
			}
		}
		vr := a.verifyInto(ctx, c)
		if err := a.Store.SaveConnections(cs); err != nil {
			return nil, err
		}
		a.log("info", "", "", "Updated connection "+c.Label+": "+vr.Summary)
		views, _ := a.ListConnections(ctx)
		for _, v := range views {
			if v.ID == c.ID {
				return &AddConnectionResult{Connection: v, Verify: vr}, nil
			}
		}
	}
	return nil, fmt.Errorf("connection not found")
}

// IDParams carries an id.
type IDParams struct {
	ID string `json:"id"`
}

// DeleteConnection removes a connection and its secrets. Connections used by
// a project environment cannot be removed until they are replaced.
func (a *App) DeleteConnection(ctx context.Context, p IDParams) (bool, error) {
	views, err := a.ListConnections(ctx)
	if err != nil {
		return false, err
	}
	for _, v := range views {
		if v.ID == p.ID && len(v.UsedBy) > 0 {
			return false, fmt.Errorf("still used by %s — assign another connection first", strings.Join(v.UsedBy, ", "))
		}
	}
	cs, _ := a.Store.LoadConnections()
	var keep []core.Connection
	for _, c := range cs {
		if c.ID != p.ID {
			keep = append(keep, c)
		}
	}
	if err := a.Vault.DeletePrefix("conn/" + p.ID + "/"); err != nil {
		return false, err
	}
	return true, a.Store.SaveConnections(keep)
}

// VerifyConnection re-runs Level 1 for one connection.
func (a *App) VerifyConnection(ctx context.Context, p IDParams) (*AddConnectionResult, error) {
	cs, err := a.Store.LoadConnections()
	if err != nil {
		return nil, err
	}
	for i := range cs {
		if cs[i].ID == p.ID {
			vr := a.verifyInto(ctx, &cs[i])
			if err := a.Store.SaveConnections(cs); err != nil {
				return nil, err
			}
			views, _ := a.ListConnections(ctx)
			for _, v := range views {
				if v.ID == p.ID {
					return &AddConnectionResult{Connection: v, Verify: vr}, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("connection not found")
}

// pickConnection chooses a sensible default connection for a provider in an
// environment: practice matches practice, and for Stripe live keys go to
// production while test keys go everywhere else.
func (a *App) pickConnection(provider, env string, practice bool) string {
	cs, _ := a.Store.LoadConnections()
	best, bestScore := "", -1
	for _, c := range cs {
		if c.Provider != provider || c.Practice != practice {
			continue
		}
		score := 1
		if c.Status == core.HealthOK {
			score += 2
		}
		if provider == "stripe" {
			if core.IsProduction(env) == (c.Mode == "live") {
				score += 4
			}
		}
		if score > bestScore {
			best, bestScore = c.ID, score
		}
	}
	return best
}
