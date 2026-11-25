package registry

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const githubAccessAssertionHeader = "Cf-Access-Jwt-Assertion"

var errGitHubAccessAssertion = errors.New("Cloudflare Access assertion invalid")

type githubAccessVerifier struct {
	issuer, audience string
	operators        map[string]map[string]bool
	client           *http.Client
	now              func() time.Time
	mu               sync.Mutex
	keys             map[string]*rsa.PublicKey
	fetched          time.Time
}

func newGitHubAccessVerifier(issuer, audience, operatorsJSON string, client *http.Client) (*githubAccessVerifier, error) {
	u, err := url.Parse(issuer)
	if err != nil || !githubCanonicalHTTPSOrigin(u, issuer) || !strings.HasSuffix(u.Host, ".cloudflareaccess.com") || !validSummaryField(audience, 256) {
		return nil, ErrInvalid
	}
	var configured map[string][]string
	if len(operatorsJSON) > 64<<10 || json.Unmarshal([]byte(operatorsJSON), &configured) != nil || len(configured) == 0 {
		return nil, ErrInvalid
	}
	operators := make(map[string]map[string]bool, len(configured))
	for subject, companies := range configured {
		if !validSummaryField(subject, 256) || len(companies) == 0 {
			return nil, ErrInvalid
		}
		set := make(map[string]bool, len(companies))
		for _, company := range companies {
			if !validID(company) || set[company] {
				return nil, ErrInvalid
			}
			set[company] = true
		}
		operators[subject] = set
	}
	if client == nil {
		client = http.DefaultClient
	}
	accessHTTP := *client
	accessHTTP.Jar = nil
	accessHTTP.Timeout = 5 * time.Second
	accessHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &githubAccessVerifier{issuer: issuer, audience: audience, operators: operators, client: &accessHTTP, now: time.Now}, nil
}

func githubCanonicalHTTPSOrigin(u *url.URL, value string) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && strings.ToLower(u.Host) == u.Host && u.User == nil &&
		u.Opaque == "" && value == "https://"+u.Host
}

func (v *githubAccessVerifier) verify(ctx context.Context, assertion string) (githubOperator, error) {
	var zero githubOperator
	if len(assertion) > 8192 {
		return zero, errGitHubAccessAssertion
	}
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return zero, errGitHubAccessAssertion
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return zero, errGitHubAccessAssertion
	}
	var info struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if json.Unmarshal(header, &info) != nil || info.Algorithm != "RS256" || !validSummaryField(info.KeyID, 128) {
		return zero, errGitHubAccessAssertion
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return zero, errGitHubAccessAssertion
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) < 256 || len(signature) > 1024 {
		return zero, errGitHubAccessAssertion
	}
	var claims struct {
		Issuer    string   `json:"iss"`
		Subject   string   `json:"sub"`
		Audience  []string `json:"aud"`
		Expires   int64    `json:"exp"`
		NotBefore int64    `json:"nbf"`
	}
	now := v.now().Unix()
	if json.Unmarshal(payload, &claims) != nil || claims.Issuer != v.issuer ||
		!validSummaryField(claims.Subject, 256) || claims.Expires <= now || claims.NotBefore > now {
		return zero, errGitHubAccessAssertion
	}
	validAudience := false
	for _, audience := range claims.Audience {
		validAudience = validAudience || audience == v.audience
	}
	if !validAudience {
		return zero, errGitHubAccessAssertion
	}
	key, err := v.key(ctx, info.KeyID)
	if err != nil {
		return zero, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil || claims.Expires <= v.now().Unix() {
		return zero, errGitHubAccessAssertion
	}
	return githubOperator{Issuer: claims.Issuer, Subject: claims.Subject}, nil
}

func (v *githubAccessVerifier) permits(operator githubOperator, company string) bool {
	return operator.Issuer == v.issuer && v.operators[operator.Subject][company]
}

func (v *githubAccessVerifier) key(ctx context.Context, id string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	age := v.now().Sub(v.fetched)
	if key := v.keys[id]; key != nil && age < 5*time.Minute {
		return key, nil
	}
	// Unknown key IDs may be rotation or attacker input. Bound re-fetches
	// even if the provider returns an error or a valid set without that key.
	if !v.fetched.IsZero() && age < 30*time.Second {
		if v.keys == nil {
			return nil, ErrUnavailable
		}
		return nil, errGitHubAccessAssertion
	}
	v.fetched = v.now()
	keys, err := v.fetchKeys(ctx)
	if err != nil {
		v.keys = nil
		return nil, err
	}
	v.keys = keys
	if key := keys[id]; key != nil {
		return key, nil
	}
	return nil, errGitHubAccessAssertion
}

func (v *githubAccessVerifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.issuer+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	const limit = 64 << 10
	payload, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(payload) > limit {
		return nil, ErrUnavailable
	}
	var document struct {
		Keys []struct {
			ID        string `json:"kid"`
			Type      string `json:"kty"`
			Algorithm string `json:"alg"`
			Use       string `json:"use"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if json.Unmarshal(payload, &document) != nil || len(document.Keys) > 32 {
		return nil, ErrUnavailable
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, raw := range document.Keys {
		if raw.Type != "RSA" || (raw.Algorithm != "" && raw.Algorithm != "RS256") || (raw.Use != "" && raw.Use != "sig") {
			continue
		}
		nBytes, nErr := base64.RawURLEncoding.DecodeString(raw.Modulus)
		eBytes, eErr := base64.RawURLEncoding.DecodeString(raw.Exponent)
		if nErr != nil || eErr != nil || len(nBytes) > 1024 || len(eBytes) == 0 || len(eBytes) > 4 || !validSummaryField(raw.ID, 128) || keys[raw.ID] != nil {
			return nil, ErrUnavailable
		}
		n, e := new(big.Int).SetBytes(nBytes), new(big.Int).SetBytes(eBytes).Int64()
		if n.BitLen() < 2048 || n.Bit(0) == 0 || e < 3 || e%2 == 0 || e > (1<<31)-1 {
			return nil, ErrUnavailable
		}
		keys[raw.ID] = &rsa.PublicKey{N: n, E: int(e)}
	}
	if len(keys) == 0 {
		return nil, ErrUnavailable
	}
	return keys, nil
}
