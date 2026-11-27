package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const githubAdminAssertion = "Cf-Access-Jwt-Assertion"
const githubAdminPath = "/admin/github"

var githubAdminSetupPath = regexp.MustCompile(`^/apps/[a-z0-9][a-z0-9-]*/installations/new$`)

type githubAdminProxy struct {
	origin   *url.URL
	registry *url.URL
	client   *http.Client
	webRoot  string
}

func newGitHubAdminProxy(webRoot string) *githubAdminProxy {
	origin, err := url.Parse(env("BUILDER_GITHUB_ADMIN_ORIGIN", ""))
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.String() != "https://"+origin.Host {
		return nil
	}
	registry, err := url.Parse((&registeredPreview{}).knowledgeBase())
	if err != nil || (registry.Scheme != "http" && registry.Scheme != "https") || registry.Host == "" || registry.User != nil || registry.Path != "" || registry.RawQuery != "" || registry.Fragment != "" {
		return nil
	}
	return &githubAdminProxy{origin: origin, registry: registry, webRoot: webRoot, client: &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func registerGitHubAdmin(mux *http.ServeMux, proxy *githubAdminProxy) {
	// Reserve the namespace even when remote administration is disabled.
	mux.HandleFunc(githubAdminPath, proxy.page)
	registerBuilderOnboarding(mux, proxy)
	mux.HandleFunc(githubAdminPath+"/", func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, "not_found") })
	const roles = githubAdminPath + "/companies/{company}/projects/{project}"
	const op = roles + "/operations"
	mux.HandleFunc("GET "+roles, proxy.operation(""))
	mux.HandleFunc("GET "+roles+"/pending-installation", proxy.operation("/pending-installation"))
	mux.HandleFunc("POST "+op, proxy.operation("/operations"))
	mux.HandleFunc("GET "+op+"/{operation}", proxy.operation("/operations/{operation}"))
	mux.HandleFunc("GET "+op+"/{operation}/repositories", proxy.operation("/operations/{operation}/repositories"))
	mux.HandleFunc("POST "+op+"/{operation}/finalize", proxy.operation("/operations/{operation}/finalize"))
	mux.HandleFunc("POST "+op+"/{operation}/abandon", proxy.operation("/operations/{operation}/abandon"))
	mux.HandleFunc("GET "+githubAdminPath+"/callback", proxy.callback(false))
	mux.HandleFunc("GET "+githubAdminPath+"/oauth/callback", proxy.callback(true))
}

func (p *githubAdminProxy) admit(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if p == nil || !strings.EqualFold(r.Host, p.origin.Host) {
		writeError(w, http.StatusNotFound, "not_found")
		return false
	}
	values := r.Header.Values(githubAdminAssertion)
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 8192 {
		writeError(w, http.StatusUnauthorized, "github_operator_authentication_required")
		return false
	}
	if len(r.URL.RawQuery) > 4096 {
		writeError(w, http.StatusBadRequest, "github_invalid_query")
		return false
	}
	return true
}

func githubAdminRegistryPath(company, project string) string {
	return "/internal/v1/companies/" + company + "/projects/" + project + "/github-connections"
}

func (p *githubAdminProxy) call(r *http.Request, method, path string, query url.Values, body []byte) (int, []byte) {
	target := *p.registry
	target.Path = path
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(r.Context(), method, target.String(), bytes.NewReader(body))
	if err != nil {
		return http.StatusBadGateway, nil
	}
	req.Header.Set(githubAdminAssertion, r.Header.Get(githubAdminAssertion))
	if method == http.MethodPost {
		req.Header.Set("Origin", r.Header.Get("Origin"))
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	}
	res, err := p.client.Do(req)
	if err != nil {
		return http.StatusBadGateway, nil
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 600 || res.StatusCode >= 300 && res.StatusCode < 400 {
		return http.StatusBadGateway, nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	if err != nil || len(data) > 256<<10 || !json.Valid(data) || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return http.StatusBadGateway, nil
	}
	return res.StatusCode, data
}

func (p *githubAdminProxy) page(w http.ResponseWriter, r *http.Request) {
	if !p.admit(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "github_method_not_allowed")
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil && githubAdminSingle(q, "new_project") == "1" && validRegistrationID(githubAdminSingle(q, "company_id")) && !q.Has("project_id") {
		p.serveOnboardingPage(w, r)
		return
	}
	if err != nil || !validRegistrationID(githubAdminSingle(q, "company_id")) || !validRegistrationID(githubAdminSingle(q, "project_id")) {
		writeError(w, http.StatusBadRequest, "github_project_required")
		return
	}
	status, _ := p.call(r, http.MethodGet, githubAdminRegistryPath(q.Get("company_id"), q.Get("project_id")), nil, nil)
	if status != http.StatusOK {
		writeError(w, status, "github_admin_access_unavailable")
		return
	}
	http.ServeFile(w, r, filepath.Join(p.webRoot, "index.html"))
}

func (p *githubAdminProxy) operation(suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admit(w, r) {
			return
		}
		company, project, operation := r.PathValue("company"), r.PathValue("project"), r.PathValue("operation")
		if !validRegistrationID(company) || !validRegistrationID(project) || strings.Contains(suffix, "{operation}") && !validRegistrationID(operation) {
			writeError(w, http.StatusBadRequest, "github_invalid_target")
			return
		}
		var body []byte
		if r.Method == http.MethodPost {
			origins := r.Header.Values("Origin")
			if len(origins) != 1 || origins[0] != p.origin.String() {
				writeError(w, http.StatusForbidden, "github_origin_denied")
				return
			}
			ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || len(r.Header.Values("Content-Type")) != 1 || ct != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "github_json_required")
				return
			}
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
			if err != nil {
				writeError(w, http.StatusRequestEntityTooLarge, "github_request_too_large")
				return
			}
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 0 && (!strings.HasSuffix(suffix, "/repositories") || len(query) != 1 || githubAdminSingle(query, "page") == "") {
			writeError(w, http.StatusBadRequest, "github_invalid_query")
			return
		}
		status, data := p.call(r, r.Method, githubAdminRegistryPath(company, project)+strings.ReplaceAll(suffix, "{operation}", operation), query, body)
		if data == nil {
			writeError(w, status, "github_admin_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
}

func githubAdminSingle(q url.Values, key string) string {
	if len(q[key]) != 1 {
		return ""
	}
	return q.Get(key)
}

func (p *githubAdminProxy) callback(oauth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admit(w, r) {
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		key, path := "installation_id", "/internal/v1/admin/github/callback"
		if oauth {
			key, path = "code", "/internal/v1/admin/github/oauth/callback"
		}
		state, value := githubAdminSingle(q, "state"), githubAdminSingle(q, key)
		setupAction := githubAdminSingle(q, "setup_action")
		pending := !oauth && setupAction == "request" && !q.Has("installation_id") && len(q) == 2
		validSetupAction := setupAction == "" || setupAction == "install" || setupAction == "update" || pending
		if err != nil || state == "" || (!pending && value == "") || (!oauth && (len(q["setup_action"]) > 1 || !validSetupAction)) {
			writeError(w, http.StatusBadRequest, "github_callback_invalid")
			return
		}
		upstreamQuery := url.Values{"state": {state}}
		if pending {
			upstreamQuery.Set("setup_action", "request")
		} else {
			upstreamQuery.Set(key, value)
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		status, data := p.call(r, http.MethodGet, path, upstreamQuery, nil)
		if status != http.StatusOK {
			writeError(w, status, "github_callback_rejected")
			return
		}
		var result struct {
			Company          string `json:"company_id"`
			Project          string `json:"project_id"`
			Operation        string `json:"operation_id"`
			AuthorizationURL string `json:"authorization_url"`
			State            string `json:"state"`
		}
		if json.Unmarshal(data, &result) != nil || !validRegistrationID(result.Company) || (result.Project != "" && !validRegistrationID(result.Project)) || !validRegistrationID(result.Operation) || (pending && (result.State != "awaiting_organization_approval" || result.AuthorizationURL != "")) {
			writeError(w, http.StatusBadGateway, "github_callback_unavailable")
			return
		}
		if result.AuthorizationURL != "" {
			if !p.githubRedirectAllowed(result.AuthorizationURL) {
				writeError(w, http.StatusBadGateway, "github_callback_unavailable")
				return
			}
			http.Redirect(w, r, result.AuthorizationURL, http.StatusSeeOther)
			return
		}
		q = url.Values{"company_id": {result.Company}, "operation_id": {result.Operation}}
		if result.Project == "" {
			q.Set("new_project", "1")
		} else {
			q.Set("project_id", result.Project)
		}
		http.Redirect(w, r, githubAdminPath+"?"+q.Encode(), http.StatusSeeOther)
	}
}

func (p *githubAdminProxy) githubRedirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || githubAdminSingle(q, "state") == "" {
		return false
	}
	if githubAdminSetupPath.MatchString(u.Path) {
		return len(q) == 1
	}
	return u.Path == "/login/oauth/authorize" && len(q) == 5 && githubAdminSingle(q, "redirect_uri") == p.origin.String()+githubAdminPath+"/oauth/callback" &&
		githubAdminSingle(q, "code_challenge_method") == "S256" && githubAdminSingle(q, "code_challenge") != "" && githubAdminSingle(q, "client_id") != ""
}
