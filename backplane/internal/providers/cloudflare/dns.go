package cloudflare

import (
	"context"
	"net/url"
	"strings"

	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// DNS publishes records for other providers (for example Resend's SPF/DKIM
// records) when the domain's zone lives on the connected Cloudflare account.
type DNS struct {
	Conn *providers.Conn
}

// Zone finds the zone that contains host by trying each parent domain.
func (d DNS) Zone(ctx context.Context, host string) (string, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	parts := strings.Split(host, ".")
	for i := 0; i < len(parts)-1; i++ {
		candidate := strings.Join(parts[i:], ".")
		if _, err := ZoneID(ctx, d.Conn, candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func (d DNS) existing(ctx context.Context, zid string, r providers.DNSRecord) ([]dnsRecord, error) {
	var out []dnsRecord
	_, err := api(ctx, d.Conn, httpx.Request{Method: "GET", Path: "/zones/" + zid + "/dns_records",
		Query: url.Values{"type": {r.Type}, "name": {r.Name}}, Quiet: true}, &out)
	return out, err
}

func sameContent(a, b string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(a), `"`), strings.Trim(strings.TrimSpace(b), `"`))
}

// Ensure creates records that are not already present with the same content.
func (d DNS) Ensure(ctx context.Context, zone string, recs []providers.DNSRecord) ([]string, error) {
	zid, err := ZoneID(ctx, d.Conn, zone)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range recs {
		list, err := d.existing(ctx, zid, r)
		if err != nil {
			return ids, err
		}
		found := ""
		for _, x := range list {
			if sameContent(x.Content, r.Content) {
				found = x.ID
			}
		}
		if found != "" {
			ids = append(ids, found)
			continue
		}
		body := dnsRecord{Type: r.Type, Name: r.Name, Content: r.Content, TTL: r.TTL}
		if body.TTL == 0 {
			body.TTL = 1
		}
		if r.Type == "MX" {
			p := r.Priority
			body.Priority = &p
		}
		var out dnsRecord
		if _, err := api(ctx, d.Conn, httpx.Request{Method: "POST", Path: "/zones/" + zid + "/dns_records", JSON: body, Resource: r.Name}, &out); err != nil {
			return ids, err
		}
		ids = append(ids, out.ID)
	}
	return ids, nil
}

// Missing lists records that are not published with the expected content.
func (d DNS) Missing(ctx context.Context, zone string, recs []providers.DNSRecord) ([]providers.DNSRecord, error) {
	zid, err := ZoneID(ctx, d.Conn, zone)
	if err != nil {
		return nil, err
	}
	var missing []providers.DNSRecord
	for _, r := range recs {
		list, err := d.existing(ctx, zid, r)
		if err != nil {
			return nil, err
		}
		ok := false
		for _, x := range list {
			if sameContent(x.Content, r.Content) {
				ok = true
			}
		}
		if !ok {
			missing = append(missing, r)
		}
	}
	return missing, nil
}
