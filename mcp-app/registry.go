package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxRegistryError   = 64 << 10
	maxRegistrySuccess = 18 << 20
	maxMCPError        = 256 << 10
)

type registryClient struct {
	base *url.URL
	http *http.Client
}

type registryResponse struct {
	status int
	body   []byte
}

func newRegistryClient(raw string) (registryClient, error) {
	base, err := url.Parse(raw)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Hostname() == "" || base.User != nil || base.Opaque != "" || base.RawQuery != "" ||
		base.ForceQuery || base.Fragment != "" || base.RawPath != "" || (base.Path != "" && base.Path != "/") {
		return registryClient{}, errors.New("Registry origin must be an absolute HTTP(S) origin without credentials, path, query or fragment")
	}
	base.Path = ""
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    2,
		MaxConnsPerHost:        2,
		IdleConnTimeout:        60 * time.Second,
		TLSHandshakeTimeout:    5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		DisableCompression:     true,
	}
	return registryClient{
		base: base,
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c registryClient) request(ctx context.Context, session, method, path string, query url.Values, body json.RawMessage, timeout time.Duration) (registryResponse, error) {
	if !nativeSessionID.MatchString(session) {
		return registryResponse{}, errors.New("invalid Registry conversation identity")
	}
	return c.exchange(ctx, session, method, path, query, body, timeout)
}

func (c registryClient) requestDiscovery(ctx context.Context, path string, query url.Values, timeout time.Duration) (registryResponse, error) {
	const companies = "/internal/v1/companies/"
	if path != companies+"search" {
		company, ok := strings.CutPrefix(path, companies)
		company, projects := strings.CutSuffix(company, "/projects")
		if !ok || !projects || !identityID.MatchString(company) {
			return registryResponse{}, errors.New("invalid Registry discovery path")
		}
	}
	return c.exchange(ctx, "", http.MethodGet, path, query, nil, timeout)
}

func (c registryClient) exchange(ctx context.Context, session, method, path string, query url.Values, body json.RawMessage, timeout time.Duration) (registryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := *c.base
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return registryResponse{}, errors.New("invalid Registry path")
	}
	target.Path, target.RawPath, target.RawQuery = decoded, path, query.Encode()
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), payload)
	if err != nil {
		return registryResponse{}, errors.New("invalid Registry request")
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		req.Header.Set(headerLane, "conversation")
		req.Header.Set(headerProvider, provider)
		req.Header.Set(headerSession, session)
	}
	// The request is attempted once. A buffered body must not give the HTTP
	// transport a replay mechanism, and no idempotency header is forwarded.
	req.GetBody = nil
	response, err := c.http.Do(req)
	if err != nil {
		return registryResponse{}, err
	}
	defer response.Body.Close()
	result := registryResponse{status: response.StatusCode}
	if result.status < 200 || result.status >= 600 || result.status >= 300 && result.status < 400 {
		return result, errors.New("unsupported Registry response status")
	}
	limit := int64(maxRegistrySuccess)
	if result.status >= 400 {
		limit = maxRegistryError
	}
	if response.ContentLength > limit {
		return result, errors.New("Registry response exceeds byte limit")
	}
	result.body, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if int64(len(result.body)) > limit {
		return result, errors.New("Registry response exceeds byte limit")
	}
	result.body = bytes.TrimSpace(result.body)
	if len(result.body) < 2 || result.body[0] != '{' || !json.Valid(result.body) || !utf8.Valid(result.body) {
		return result, errors.New("Registry response is not a JSON object")
	}
	return result, nil
}

func (r registryResponse) ok() bool {
	return r.status >= http.StatusOK && r.status < http.StatusMultipleChoices
}

type registrySession struct {
	Type       string `json:"type"`
	ExternalID string `json:"external_id"`
	Binding    struct {
		CompanyID string `json:"company_id"`
		ProjectID string `json:"project_id"`
	} `json:"binding"`
}

func readRegistrySession(raw []byte, session string) (registrySession, bool) {
	var result registrySession
	err := json.Unmarshal(raw, &result)
	return result, err == nil && result.Type == provider && result.ExternalID == session &&
		identityID.MatchString(result.Binding.CompanyID) && identityID.MatchString(result.Binding.ProjectID)
}

func projectProjection(data []byte, company, project, name string) ([]byte, error) {
	var view struct {
		ID                    string `json:"id"`
		Name                  string `json:"name"`
		CompanyID             string `json:"company_id"`
		RoadmapRevision       *int64 `json:"roadmap_revision,omitempty"`
		BindingRevision       *int64 `json:"binding_revision,omitempty"`
		RegisteredAt          string `json:"registered_at,omitempty"`
		PreparationState      string `json:"preparation_state,omitempty"`
		KnowledgeGenerationID string `json:"knowledge_generation_id,omitempty"`
	}
	if err := json.Unmarshal(data, &view); err != nil || view.CompanyID != company || view.ID != project ||
		strings.TrimSpace(view.Name) == "" || (name != "" && view.Name != name) {
		return nil, errors.New("invalid Project identity in Registry response")
	}
	return json.Marshal(view)
}
