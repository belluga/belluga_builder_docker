package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxOverviewProxyJSON = 256 << 10
const maxOverviewProxyPNG = 2 << 20

var overviewProxySlots = map[string]bool{"desktop": true, "mobile": true, "problem": true, "proposal": true, "impact": true, "audience": true}

type overviewProxyMedia struct {
	Slot   string `json:"slot"`
	Digest string `json:"digest"`
	Href   string `json:"href"`
}
type overviewProxyResponse struct {
	State           string               `json:"state"`
	CompanyID       string               `json:"company_id"`
	ProjectID       string               `json:"project_id"`
	Revision        int64                `json:"revision,omitempty"`
	Content         json.RawMessage      `json:"content,omitempty"`
	Media           []overviewProxyMedia `json:"media,omitempty"`
	KnowledgeOrigin json.RawMessage      `json:"knowledge_origin,omitempty"`
	Source          json.RawMessage      `json:"source,omitempty"`
	Teach           string               `json:"teach,omitempty"`
}

func (p *registeredPreview) overviewAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	companyID, projectID, revision, slot, mediaRequest, ok := parseOverviewProxyRequest(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "overview_request_invalid", "teach": "TEACH: Informe Company e Project válidos; media exige revisão positiva e slot fixo."})
		return
	}
	base := strings.TrimRight(strings.TrimSpace(p.registryURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(env("DELPHI_BUILDER_REGISTRY_URL", "http://builder-registry:8080")), "/")
	}
	registered, err := p.registryProjectRegistered(r, base, companyID, projectID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "overview_unavailable", "teach": "TEACH: Registry identity verification is unavailable; retry the current Overview."})
		return
	}
	if !registered {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project_not_found", "teach": "TEACH: This Company and Project are not registered in Registry."})
		return
	}
	if mediaRequest {
		p.overviewMedia(w, r, base, companyID, projectID, revision, slot)
		return
	}
	endpoint := overviewRegistryPath(companyID, projectID, "")
	response, err := p.designSystemGet(r, endpoint)
	if err != nil {
		overviewProxyError(w, http.StatusServiceUnavailable, "overview_unavailable", "TEACH: Registry Overview is unavailable; retry the current read.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		overviewProxyRegistryStatus(w, response)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxOverviewProxyJSON+1))
	if err != nil || len(data) > maxOverviewProxyJSON || rejectDuplicateJSONKeys(data) != nil {
		overviewProxyError(w, 503, "overview_response_invalid", "TEACH: Registry returned an invalid or oversized Overview response.")
		return
	}
	var result overviewProxyResponse
	if json.Unmarshal(data, &result) != nil || !validOverviewProxyResult(result, companyID, projectID, 0) {
		overviewProxyError(w, 503, "overview_response_invalid", "TEACH: Registry Overview identity, state, revision, content or media metadata did not validate.")
		return
	}
	for i := range result.Media {
		result.Media[i].Href = fmt.Sprintf("/api/overview/media/%s/%s/%d/%s", url.PathEscape(companyID), url.PathEscape(projectID), result.Revision, url.PathEscape(result.Media[i].Slot))
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(result)
}

func parseOverviewProxyRequest(r *http.Request) (companyID, projectID string, revision int64, slot string, media, ok bool) {
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) > 0 || len(r.URL.RawQuery) > 2048 {
		return
	}
	if r.URL.Path == "/api/overview" {
		q := r.URL.Query()
		var a, b bool
		companyID, a = oneQueryValue(q, "company_id")
		projectID, b = oneQueryValue(q, "project_id")
		ok = a && b && len(q) == 2 && validRegistrationID(companyID) && validRegistrationID(projectID)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/overview/media/") || len(r.URL.RawQuery) > 0 {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/overview/media/"), "/")
	if len(parts) != 4 {
		return
	}
	var err error
	companyID, _ = url.PathUnescape(parts[0])
	projectID, _ = url.PathUnescape(parts[1])
	revision, err = strconv.ParseInt(parts[2], 10, 64)
	slot, _ = url.PathUnescape(parts[3])
	media = true
	ok = err == nil && revision > 0 && validRegistrationID(companyID) && validRegistrationID(projectID) && overviewProxySlots[slot]
	return
}

func overviewRegistryPath(companyID, projectID, suffix string) string {
	return "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/overview" + suffix
}
func overviewProxyError(w http.ResponseWriter, status int, code, teach string) {
	writeJSON(w, status, map[string]string{"error": code, "teach": teach})
}
func overviewProxyRegistryStatus(w http.ResponseWriter, response *http.Response) {
	status, code, teach := 503, "overview_unavailable", "TEACH: Registry Overview is unavailable; retry the current read."
	if response.StatusCode == 404 {
		status, code, teach = 404, "overview_not_found", "TEACH: No current Overview revision is authored for this Project."
	}
	if response.StatusCode == 409 {
		status, code, teach = 409, "overview_selection_changed", "TEACH: Overview selection changed during the read; reload the current revision."
	}
	overviewProxyError(w, status, code, teach)
}

func validOverviewProxyResult(v overviewProxyResponse, companyID, projectID string, expected int64) bool {
	if v.CompanyID != companyID || v.ProjectID != projectID {
		return false
	}
	switch v.State {
	case "not_authored":
		return v.Revision == 0 && len(v.Content) == 0 && len(v.Media) == 0 && v.Teach != ""
	case "invalid":
		return v.Revision >= 0 && (expected == 0 || v.Revision == 0 || v.Revision == expected) && v.Teach != ""
	case "stale", "available":
	default:
		return false
	}
	if v.Revision <= 0 || (expected > 0 && v.Revision != expected) || len(v.Content) == 0 || len(v.Media) < 2 || len(v.Media) > 6 || len(v.KnowledgeOrigin) == 0 || len(v.Source) == 0 {
		return false
	}
	if rejectDuplicateJSONKeys(v.Content) != nil || rejectDuplicateJSONKeys(v.KnowledgeOrigin) != nil || rejectDuplicateJSONKeys(v.Source) != nil {
		return false
	}
	var content struct {
		SchemaVersion string `json:"schema_version"`
		Tagline       string `json:"tagline"`
		Summary       string `json:"summary"`
		DesktopAlt    string `json:"desktop_alt"`
		MobileAlt     string `json:"mobile_alt"`
		Sections      map[string]struct {
			Heading  string   `json:"heading"`
			Body     string   `json:"body"`
			Points   []string `json:"points"`
			ImageAlt *string  `json:"image_alt"`
		} `json:"sections"`
	}
	if json.Unmarshal(v.Content, &content) != nil || content.SchemaVersion != "1" || len(content.Sections) != 4 || content.Tagline == "" || content.Summary == "" || content.DesktopAlt == "" || content.MobileAlt == "" {
		return false
	}
	for _, name := range []string{"problem", "proposal", "impact", "audience"} {
		section, ok := content.Sections[name]
		if !ok || section.Heading == "" || section.Body == "" || section.Points == nil || len(section.Points) > 3 {
			return false
		}
		for _, point := range section.Points {
			if strings.TrimSpace(point) == "" {
				return false
			}
		}
	}
	seen := map[string]bool{}
	desktop, mobile := false, false
	for _, m := range v.Media {
		if !overviewProxySlots[m.Slot] || seen[m.Slot] || len(m.Digest) != 71 || !strings.HasPrefix(m.Digest, "sha256:") || !validSHA256(strings.TrimPrefix(m.Digest, "sha256:")) {
			return false
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(m.Digest, "sha256:")); err != nil {
			return false
		}
		seen[m.Slot] = true
		desktop = desktop || m.Slot == "desktop"
		mobile = mobile || m.Slot == "mobile"
	}
	return desktop && mobile
}

func (p *registeredPreview) overviewMedia(w http.ResponseWriter, r *http.Request, base, companyID, projectID string, revision int64, slot string) {
	path := overviewRegistryPath(companyID, projectID, "/"+strconv.FormatInt(revision, 10))
	meta, err := p.designSystemGet(r, path)
	if err != nil {
		overviewProxyError(w, 503, "overview_media_unavailable", "TEACH: Exact Overview revision is temporarily unavailable.")
		return
	}
	defer meta.Body.Close()
	if meta.StatusCode != 200 {
		overviewProxyRegistryStatus(w, meta)
		return
	}
	data, err := io.ReadAll(io.LimitReader(meta.Body, maxOverviewProxyJSON+1))
	if err != nil || len(data) > maxOverviewProxyJSON || rejectDuplicateJSONKeys(data) != nil {
		overviewProxyError(w, 503, "overview_response_invalid", "TEACH: Exact Registry revision metadata is invalid.")
		return
	}
	var result overviewProxyResponse
	if json.Unmarshal(data, &result) != nil || !validOverviewProxyResult(result, companyID, projectID, revision) || (result.State != "available" && result.State != "stale") {
		overviewProxyError(w, 503, "overview_response_invalid", "TEACH: Exact Registry revision or media metadata did not validate.")
		return
	}
	var declared *overviewProxyMedia
	for i := range result.Media {
		if result.Media[i].Slot == slot {
			declared = &result.Media[i]
			break
		}
	}
	if declared == nil {
		overviewProxyError(w, 404, "overview_media_not_found", "TEACH: This exact Overview revision does not declare that media slot.")
		return
	}
	media, err := p.designSystemGet(r, overviewRegistryPath(companyID, projectID, "/"+strconv.FormatInt(revision, 10)+"/media/"+url.PathEscape(slot)))
	if err != nil {
		overviewProxyError(w, 503, "overview_media_unavailable", "TEACH: Exact Overview media is temporarily unavailable.")
		return
	}
	defer media.Body.Close()
	body, err := io.ReadAll(io.LimitReader(media.Body, maxOverviewProxyPNG+1))
	if err != nil || media.StatusCode != 200 || len(body) == 0 || len(body) > maxOverviewProxyPNG || media.Header.Get("Content-Type") != "image/png" {
		overviewProxyError(w, 503, "overview_media_invalid", "TEACH: Registry media is not a bounded PNG.")
		return
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != declared.Digest || media.Header.Get("ETag") != "\""+digest+"\"" {
		overviewProxyError(w, 503, "overview_media_invalid", "TEACH: Registry media digest does not match the exact revision.")
		return
	}
	cfg, e := png.DecodeConfig(strings.NewReader(string(body)))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 16<<20 {
		overviewProxyError(w, 503, "overview_media_invalid", "TEACH: Registry PNG dimensions exceed the supported limits.")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("ETag", "\""+digest+"\"")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
