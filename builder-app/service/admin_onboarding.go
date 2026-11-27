package main

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
)

func registerBuilderOnboarding(mux *http.ServeMux, p *githubAdminProxy) {
	mux.HandleFunc("GET /admin", p.serveOnboardingPage)
	mux.HandleFunc("GET /admin/projects", p.adminProjects)
	const base = "/admin/companies"
	mux.HandleFunc("GET "+base, p.onboardingProxy("", "", false))
	mux.HandleFunc("POST "+base, p.onboardingProxy("", "", true))
	mux.HandleFunc("POST "+base+"/{company}/projects", p.onboardingProxy("/projects", "", false))
	mux.HandleFunc("GET "+base+"/{company}/image", p.companyImage)
	mux.HandleFunc("GET "+base+"/{company}/github-pending-installation", p.onboardingProxy("/github-pending-installation", "", false))
	mux.HandleFunc("POST "+base+"/{company}/github-discovery", p.onboardingProxy("/github-discovery", "", false))
	mux.HandleFunc("GET "+base+"/{company}/github-discovery/{operation}", p.onboardingProxy("/github-discovery", "operation", false))
	mux.HandleFunc("GET "+base+"/{company}/github-discovery/{operation}/repositories", p.onboardingProxy("/github-discovery", "repositories", false))
}
func (p *githubAdminProxy) serveOnboardingPage(w http.ResponseWriter, r *http.Request) {
	if !p.admit(w, r) {
		return
	}
	http.ServeFile(w, r, filepath.Join(p.webRoot, "index.html"))
}
func (p *githubAdminProxy) onboardingProxy(suffix, operationMode string, multipart bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.admit(w, r) {
			return
		}
		path := "/internal/v1/admin/companies"
		if suffix != "" {
			company := r.PathValue("company")
			if !validRegistrationID(company) {
				writeError(w, http.StatusBadRequest, "github_invalid_target")
				return
			}
			path += "/" + company + suffix
		}
		if operationMode != "" {
			op := r.PathValue("operation")
			if !validRegistrationID(op) {
				writeError(w, http.StatusBadRequest, "github_invalid_target")
				return
			}
			path += "/" + op
		}
		if operationMode == "repositories" {
			path += "/repositories"
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		validPage := operationMode == "repositories" && githubAdminSingle(query, "page") != ""
		validCompany := r.Method == http.MethodGet && suffix == "" && validRegistrationID(githubAdminSingle(query, "company_id"))
		if err != nil || len(query) > 0 && (len(query) != 1 || !validPage && !validCompany) {
			writeError(w, http.StatusBadRequest, "github_invalid_query")
			return
		}
		var body []byte
		if r.Method == http.MethodPost {
			origins := r.Header.Values("Origin")
			if len(origins) != 1 || origins[0] != p.origin.String() {
				writeError(w, http.StatusForbidden, "github_origin_denied")
				return
			}
			expected, limit := "application/json", int64(8<<10)
			if multipart {
				expected, limit = "multipart/form-data", 272<<10
			}
			ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || len(r.Header.Values("Content-Type")) != 1 || ct != expected {
				writeError(w, http.StatusUnsupportedMediaType, "github_content_type_required")
				return
			}
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
			if err != nil {
				writeError(w, http.StatusRequestEntityTooLarge, "github_request_too_large")
				return
			}
		}
		status, data := p.call(r, r.Method, path, query, body)
		if data == nil {
			writeError(w, status, "github_admin_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
}
func (p *githubAdminProxy) companyImage(w http.ResponseWriter, r *http.Request) {
	if !p.admit(w, r) {
		return
	}
	company := r.PathValue("company")
	if !validRegistrationID(company) || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "github_invalid_target")
		return
	}
	target := *p.registry
	target.Path = "/internal/v1/admin/companies/" + company + "/image"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_admin_unavailable")
		return
	}
	req.Header.Set(githubAdminAssertion, r.Header.Get(githubAdminAssertion))
	res, err := p.client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_admin_unavailable")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		writeError(w, res.StatusCode, "company_image_unavailable")
		return
	}
	mime := res.Header.Get("Content-Type")
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
		writeError(w, http.StatusBadGateway, "company_image_unavailable")
		return
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (256<<10)+1))
	if err != nil || len(data) == 0 || len(data) > 256<<10 || http.DetectContentType(data) != mime {
		writeError(w, http.StatusBadGateway, "company_image_unavailable")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (p *githubAdminProxy) adminProjects(w http.ResponseWriter, r *http.Request) {
	if !p.admit(w, r) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "github_invalid_query")
		return
	}
	for key, values := range query {
		if len(values) != 1 || key != "company_id" && key != "q" && key != "cursor" {
			writeError(w, http.StatusBadRequest, "github_invalid_query")
			return
		}
	}
	status, body := p.call(r, http.MethodGet, "/internal/v1/admin/projects", query, nil)
	if body == nil {
		writeError(w, status, "github_admin_access_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
