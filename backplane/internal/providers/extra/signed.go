package extra

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"safisolutions.org/backplane/internal/core"
	"safisolutions.org/backplane/internal/httpx"
	"safisolutions.org/backplane/internal/providers"
)

// ================= Auth0 (client-credentials token) =================

type auth0 struct{}

var auth0Info = connectMaturity("auth0", "Auth0", "Enterprise sign-in, organizations and role-based access.", "Auth", 2,
	[]core.Capability{core.CapAuth},
	[]providers.Field{{Key: "domain", Label: "Tenant domain", Required: true, Placeholder: "your-tenant.us.auth0.com"}, {Key: "client_id", Label: "Client ID", Required: true},
		{Key: "token", Label: "Client secret", Secret: true, Required: true}},
	[]providers.GuideStep{{Title: "Create a Machine-to-Machine application", Body: "Applications → Create Application → Machine to Machine, authorize it for the Auth0 Management API with read:clients, read:connections and read:stats.", Link: "https://manage.auth0.com/", Label: "Open Auth0"}},
	"https://manage.auth0.com/", "https://auth0.com/docs/api/management/v2", []string{"Applications", "Connections", "Active users"})

func (auth0) Info() providers.Info { return auth0Info }

type tokenCache struct {
	mu  sync.Mutex
	tok string
	exp time.Time
}

func (auth0) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	domain := strings.TrimSuffix(strings.TrimPrefix(c.Setting("domain"), "https://"), "/")
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	base := conn.Base("auth0", "https://"+domain)
	tokens := httpx.New("auth0", base)
	tokens.Log = opts.Log
	cache := &tokenCache{}
	cl := httpx.New("auth0", base+"/api/v2")
	cl.Log = opts.Log
	cl.MaxAttempts = 3
	cl.Auth = func(r *http.Request) {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if cache.tok == "" || time.Now().After(cache.exp) {
			resp, err := tokens.Do(r.Context(), httpx.Request{Method: "POST", Path: "/oauth/token", NoAuth: true, Idempotent: true,
				JSON: map[string]string{"grant_type": "client_credentials", "client_id": c.Setting("client_id"), "client_secret": secret, "audience": "https://" + domain + "/api/v2/"}})
			if err == nil {
				var t struct {
					AccessToken string `json:"access_token"`
					ExpiresIn   int    `json:"expires_in"`
				}
				if json.Unmarshal(resp.Body, &t) == nil {
					cache.tok, cache.exp = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn-60)*time.Second)
				}
			}
		}
		if cache.tok != "" {
			r.Header.Set("Authorization", "Bearer "+cache.tok)
		}
	}
	conn.Client = cl
	return conn, nil
}

func (auth0) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	var clients []struct {
		Name     string `json:"name"`
		ClientID string `json:"client_id"`
	}
	if _, err := get(ctx, c, "/clients?fields=name,client_id&include_fields=true", &clients); err != nil {
		return fail("auth0", "list applications", err, start), nil
	}
	d := c.Connection.Setting("domain")
	return ok(fmt.Sprintf("Connected to %s. %d application(s).", d, len(clients)), start, d, d), nil
}

func (auth0) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var out []providers.Discovered
	var clients []struct {
		Name     string `json:"name"`
		ClientID string `json:"client_id"`
		AppType  string `json:"app_type"`
	}
	if _, err := get(ctx, c, "/clients?fields=name,client_id,app_type&include_fields=true", &clients); err != nil {
		return nil, err
	}
	for _, cl := range clients {
		out = append(out, providers.Discovered{Provider: "auth0", Kind: "auth0.application", ID: cl.ClientID, Name: cl.Name, Detail: cl.AppType})
	}
	var conns []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Strategy string `json:"strategy"`
	}
	if _, err := get(ctx, c, "/connections", &conns); err == nil {
		for _, x := range conns {
			out = append(out, providers.Discovered{Provider: "auth0", Kind: "auth0.connection", ID: x.ID, Name: x.Name, Detail: x.Strategy})
		}
	}
	return out, nil
}

// ================= Firebase / Google (service-account JWT) =================

type firebase struct{}

var firebaseInfo = connectMaturity("firebase", "Firebase", "Auth, Firestore, Functions, Storage, Hosting and Messaging.", "App platform", 2,
	[]core.Capability{core.CapAuth, core.CapDatabase, core.CapFunctions, core.CapStorage, core.CapPush, core.CapAnalytics, core.CapDeployment},
	[]providers.Field{{Key: "token", Label: "Service account JSON", Secret: true, Required: true, Help: "Paste the whole JSON key file for a service account with the Firebase Viewer role."}},
	[]providers.GuideStep{{Title: "Create a service account key", Body: "Firebase console → Project settings → Service accounts → Generate new private key. Give the account the Firebase Viewer role for monitoring.", Link: "https://console.firebase.google.com/", Label: "Open Firebase console"}},
	"https://console.firebase.google.com/", "https://firebase.google.com/docs/reference/firebase-management/rest", []string{"Project", "Web, Android and iOS apps", "Firestore databases", "Hosting sites"})

func (firebase) Info() providers.Info { return firebaseInfo }

type serviceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func googleToken(ctx context.Context, sa serviceAccount, scope string, log func(httpx.LogEvent)) (string, time.Time, error) {
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", time.Time{}, fmt.Errorf("the service account private key is not valid PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not read the private key: %w", err)
	}
	key, okRSA := keyAny.(*rsa.PrivateKey)
	if !okRSA {
		return "", time.Time{}, fmt.Errorf("expected an RSA private key")
	}
	now := time.Now()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	aud := sa.TokenURI
	if aud == "" {
		aud = "https://oauth2.googleapis.com/token"
	}
	claims, _ := json.Marshal(map[string]any{"iss": sa.ClientEmail, "scope": scope, "aud": aud, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	unsigned := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", time.Time{}, err
	}
	cl := httpx.New("firebase", "")
	cl.Log = log
	resp, err := cl.Do(ctx, httpx.Request{Method: "POST", Path: aud, Form: url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {unsigned + "." + enc.EncodeToString(sig)}}, Idempotent: true})
	if err != nil {
		return "", time.Time{}, err
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(resp.Body, &t); err != nil {
		return "", time.Time{}, err
	}
	return t.AccessToken, now.Add(time.Duration(t.ExpiresIn-60) * time.Second), nil
}

func (firebase) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	var sa serviceAccount
	if err := json.Unmarshal([]byte(secret), &sa); err != nil || sa.ClientEmail == "" {
		return nil, &core.Problem{Title: "Not a service account key", Provider: "firebase", Code: "invalid", Summary: "Paste the complete JSON file Google gives you when you generate a private key."}
	}
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("firebase", conn.Base("firebase", "https://firebase.googleapis.com"))
	cl.Log = opts.Log
	cl.MaxAttempts = 3
	cache := &tokenCache{}
	cl.Auth = func(r *http.Request) {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if cache.tok == "" || time.Now().After(cache.exp) {
			if tok, exp, err := googleToken(r.Context(), sa, "https://www.googleapis.com/auth/cloud-platform.read-only https://www.googleapis.com/auth/firebase.readonly", opts.Log); err == nil {
				cache.tok, cache.exp = tok, exp
			}
		}
		if cache.tok != "" {
			r.Header.Set("Authorization", "Bearer "+cache.tok)
		}
	}
	conn.Client = cl
	if conn.Connection.Settings == nil {
		conn.Connection.Settings = map[string]string{}
	}
	conn.Connection.Settings["project_id"] = sa.ProjectID
	return conn, nil
}

func (firebase) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	pid := c.Connection.Setting("project_id")
	var p struct {
		ProjectID   string `json:"projectId"`
		DisplayName string `json:"displayName"`
	}
	if _, err := get(ctx, c, "/v1beta1/projects/"+pid, &p); err != nil {
		return fail("firebase", "read the Firebase project", err, start), nil
	}
	r := ok("Connected to "+firstNonEmpty(p.DisplayName, p.ProjectID)+".", start, p.ProjectID, firstNonEmpty(p.DisplayName, p.ProjectID))
	r.Settings["project_id"] = p.ProjectID
	return r, nil
}

func (firebase) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	pid := c.Connection.Setting("project_id")
	var out []providers.Discovered
	var apps struct {
		Apps []struct {
			AppID       string `json:"appId"`
			DisplayName string `json:"displayName"`
			Platform    string `json:"platform"`
		} `json:"apps"`
	}
	if _, err := get(ctx, c, "/v1beta1/projects/"+pid+":searchApps", &apps); err != nil {
		return nil, err
	}
	for _, a := range apps.Apps {
		out = append(out, providers.Discovered{Provider: "firebase", Kind: "firebase.app", ID: a.AppID, Name: firstNonEmpty(a.DisplayName, a.AppID), Detail: strings.ToLower(a.Platform)})
	}
	return out, nil
}

// ================= AWS (SigV4) =================

type aws struct{}

var awsInfo = providers.Info{ID: "aws", Name: "AWS", Category: "Cloud", Phase: 3, Maturity: providers.MaturityConnect,
	Tagline:      "Lambda, S3, DynamoDB, SES, SQS and more — kept behind Backplane's simpler model.",
	Capabilities: []core.Capability{core.CapFunctions, core.CapStorage, core.CapDatabase, core.CapEmail, core.CapQueue, core.CapDNS, core.CapSecrets, core.CapMonitoring},
	Fields: []providers.Field{{Key: "access_key_id", Label: "Access key ID", Required: true, Placeholder: "AKIA…"}, {Key: "token", Label: "Secret access key", Secret: true, Required: true},
		{Key: "region", Label: "Region", Placeholder: "us-east-1"}},
	Guide:    []providers.GuideStep{{Title: "Create a read-only IAM user", Body: "IAM → Users → Create user with the ReadOnlyAccess policy, then Security credentials → Create access key (Other). Backplane only reads for now.", Link: "https://console.aws.amazon.com/iam/home#/users", Label: "Open AWS IAM"}},
	TokenURL: "https://console.aws.amazon.com/iam/home#/users", DocsURL: "https://docs.aws.amazon.com/general/latest/gr/signing-aws-api-requests.html",
	Discovers: []string{"Account identity", "Lambda functions", "S3 buckets"},
	OAuthNote: "Phase 3 preview: Backplane verifies the identity and discovers resources; provisioning on AWS comes later."}

func (aws) Info() providers.Info { return awsInfo }

func (aws) Connect(c core.Connection, secret string, opts providers.ConnectOptions) (*providers.Conn, error) {
	conn := &providers.Conn{Connection: c, Secret: secret, BaseURLs: opts.BaseURLs}
	cl := httpx.New("aws", "")
	cl.Log = opts.Log
	cl.MaxAttempts = 3
	cl.Auth = func(r *http.Request) {
		signV4(r, c.Setting("access_key_id"), secret, regionOf(c), serviceOf(r.URL.Host))
	}
	conn.Client = cl
	return conn, nil
}

func regionOf(c core.Connection) string {
	if r := c.Setting("region"); r != "" {
		return r
	}
	return "us-east-1"
}

func serviceOf(host string) string {
	switch {
	case strings.HasPrefix(host, "sts."):
		return "sts"
	case strings.HasPrefix(host, "lambda."):
		return "lambda"
	case strings.HasPrefix(host, "s3.") || host == "s3.amazonaws.com":
		return "s3"
	}
	return strings.SplitN(host, ".", 2)[0]
}

// signV4 signs a request with AWS Signature Version 4.
func signV4(r *http.Request, keyID, secret, region, service string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	var body []byte
	if r.GetBody != nil {
		if rc, err := r.GetBody(); err == nil {
			b := new(strings.Builder)
			buf := make([]byte, 4096)
			for {
				n, err := rc.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					break
				}
			}
			body = []byte(b.String())
		}
	}
	payloadHash := sha256Hex(body)
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if service == "sts" || service == "s3" {
		region = "us-east-1"
	}
	host := r.URL.Host
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	headers := map[string]string{"host": host, "x-amz-content-sha256": payloadHash, "x-amz-date": amzDate}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		signed = append(signed, "content-type")
		headers["content-type"] = ct
	}
	sort.Strings(signed)
	var canonHeaders strings.Builder
	for _, h := range signed {
		canonHeaders.WriteString(h + ":" + strings.TrimSpace(headers[h]) + "\n")
	}
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var cq []string
	for _, k := range keys {
		for _, v := range q[k] {
			cq = append(cq, awsEscape(k)+"="+awsEscape(v))
		}
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{r.Method, path, strings.Join(cq, "&"), canonHeaders.String(), strings.Join(signed, ";"), payloadHash}, "\n")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	k := hmacSHA([]byte("AWS4"+secret), date)
	k = hmacSHA(k, region)
	k = hmacSHA(k, service)
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+keyID+"/"+scope+", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
}

func awsEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hmacSHA(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func (aws) Verify(ctx context.Context, c *providers.Conn) (*providers.VerifyResult, error) {
	start := time.Now()
	resp, err := c.Client.Do(ctx, httpx.Request{Method: "POST", Path: "https://sts.amazonaws.com/", Form: url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}}, Idempotent: true})
	if err != nil {
		return fail("aws", "check the caller identity", err, start), nil
	}
	var id struct {
		Result struct {
			Arn     string `xml:"Arn"`
			Account string `xml:"Account"`
		} `xml:"GetCallerIdentityResult"`
	}
	if err := xml.Unmarshal(resp.Body, &id); err != nil {
		return fail("aws", "read the caller identity", err, start), nil
	}
	r := ok("Connected to account "+id.Result.Account+" as "+id.Result.Arn+".", start, id.Result.Account, id.Result.Account)
	if strings.HasSuffix(id.Result.Arn, ":root") {
		r.Health = core.HealthWarn
		r.Warnings = append(r.Warnings, "These are root account keys. Create a read-only IAM user instead — root keys can do anything to the account.")
	}
	return r, nil
}

func (aws) Discover(ctx context.Context, c *providers.Conn) ([]providers.Discovered, error) {
	var out []providers.Discovered
	region := regionOf(c.Connection)
	var fns struct {
		Functions []struct {
			FunctionName string `json:"FunctionName"`
			Runtime      string `json:"Runtime"`
		} `json:"Functions"`
	}
	resp, err := c.Client.Do(ctx, httpx.Request{Method: "GET", Path: "https://lambda." + region + ".amazonaws.com/2015-03-31/functions/"})
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(resp.Body, &fns)
	for _, f := range fns.Functions {
		out = append(out, providers.Discovered{Provider: "aws", Kind: "aws.lambda", ID: f.FunctionName, Name: f.FunctionName, Detail: f.Runtime + " · " + region})
	}
	if resp, err := c.Client.Do(ctx, httpx.Request{Method: "GET", Path: "https://s3.amazonaws.com/"}); err == nil {
		var b struct {
			Buckets []struct {
				Name string `xml:"Name"`
			} `xml:"Buckets>Bucket"`
		}
		if xml.Unmarshal(resp.Body, &b) == nil {
			for _, x := range b.Buckets {
				out = append(out, providers.Discovered{Provider: "aws", Kind: "aws.s3", ID: x.Name, Name: x.Name, Detail: "S3 bucket"})
			}
		}
	}
	return out, nil
}
