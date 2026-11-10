package knowledge_status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"delphi-local-api/internal/codec"
	"delphi-local-api/internal/source"
)

// CanonicalReference identifies API content, never a Foundation path.
type CanonicalReference struct {
	Family        string `json:"family"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

type CanonicalManifest struct {
	ProjectID    string               `json:"project_id"`
	GenerationID string               `json:"generation_id,omitempty"`
	Active       bool                 `json:"active"`
	State        string               `json:"state"`
	Documents    []CanonicalReference `json:"documents"`
}

// CanonicalOrigin is stored only with a derived visual artifact. It records
// which API revisions the author used; it is not a second knowledge authority.
type CanonicalOrigin struct {
	ProjectID    string               `json:"project_id"`
	GenerationID string               `json:"generation_id"`
	Documents    []CanonicalReference `json:"documents"`
}

func ReadCanonicalOrigin(foundation, path string) *CanonicalOrigin {
	data, err := source.ReadBounded(filepath.Join(foundation, path), MaxDocumentBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var envelope struct {
		Origin json.RawMessage `json:"knowledge_origin"`
	}
	if err != nil || codec.ValidateJSON(data, 0) != nil || json.Unmarshal(data, &envelope) != nil {
		return &CanonicalOrigin{}
	}
	if len(envelope.Origin) == 0 || bytes.Equal(envelope.Origin, []byte("null")) {
		return nil
	}
	var origin CanonicalOrigin
	decoder := json.NewDecoder(bytes.NewReader(envelope.Origin))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&origin) != nil {
		return &CanonicalOrigin{}
	}
	return &origin
}

type CanonicalRecord struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	GenerationID  string          `json:"generation_id"`
	Revision      int64           `json:"revision"`
	ContentDigest string          `json:"content_digest"`
	Payload       json.RawMessage `json:"payload"`
}

var canonicalID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func canonicalFamily(family string) bool {
	switch family {
	case "project_mandate", "domain_catalog", "project_constitution", "strategic_roadmap", "module", "endpoint_contract", "policy":
		return true
	}
	return false
}

// ConfiguredCanonicalManifest selects authority afresh for this exact Project.
// An explicitly local backend (no registry URL/cutover) retains legacy mode.
// With a configured API, only a valid pending manifest selects legacy mode;
// transport failures and malformed responses can never enable Markdown reads.
func ConfiguredCanonicalManifest(ctx context.Context, companyID, projectID string) (*CanonicalManifest, error) {
	address := os.Getenv("DELPHI_BUILDER_REGISTRY_URL")
	if address == "" && os.Getenv("DELPHI_BUILDER_REGISTRY_CUTOVER") != "true" {
		return nil, nil
	}
	var manifest CanonicalManifest
	if err := canonicalGet(ctx, address, companyID, projectID, "/manifest", &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(projectID); err != nil {
		return nil, err
	}
	if !manifest.Active {
		return nil, nil
	}
	return &manifest, nil
}

func (m CanonicalManifest) Validate(projectID string) error {
	if m.ProjectID != projectID || m.Documents == nil || len(m.Documents) > MaxInputs {
		return canonicalInvalid()
	}
	if !m.Active {
		if m.State != "pending" || m.GenerationID != "" || len(m.Documents) != 0 {
			return canonicalInvalid()
		}
		return nil
	}
	if m.State != "active" || !canonicalID.MatchString(m.GenerationID) {
		return canonicalInvalid()
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, ref := range m.Documents {
		key := ref.Family + "/" + ref.ID
		if !canonicalFamily(ref.Family) || !canonicalID.MatchString(ref.ID) || ref.Revision < 1 || seen[key] {
			return canonicalInvalid()
		}
		if _, err := decodeDigest(ref.ContentDigest); err != nil {
			return canonicalInvalid()
		}
		seen[key] = true
		counts[ref.Family]++
	}
	for _, family := range []string{"project_mandate", "domain_catalog", "project_constitution", "strategic_roadmap"} {
		if counts[family] != 1 {
			return canonicalInvalid()
		}
	}
	return nil
}

func canonicalInvalid() error {
	return errors.New("TEACH: canonical knowledge manifest is invalid; restore the Project-scoped API and retry; Markdown fallback is unavailable")
}

func canonicalGet(ctx context.Context, address, companyID, projectID, suffix string, out any) error {
	base, err := url.Parse(address)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" && base.Path != "/" || !canonicalID.MatchString(companyID) || !canonicalID.MatchString(projectID) {
		return errors.New("TEACH: configure the private Builder registry URL and exact Company/Project identities before reading canonical knowledge")
	}
	base.Path = "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/knowledge" + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return canonicalInvalid()
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return errors.New("TEACH: canonical knowledge API is unavailable; restore it and retry the same Project; Markdown fallback is unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxAggregateBytes+1))
	if err != nil || len(data) > MaxAggregateBytes || response.StatusCode != http.StatusOK || codec.ValidateJSON(data, 0) != nil || json.Unmarshal(data, out) != nil {
		return errors.New("TEACH: canonical knowledge API rejected the read or returned invalid content; restore the Project-scoped API and retry; Markdown fallback is unavailable")
	}
	return nil
}

func ReadCanonicalRecord(ctx context.Context, companyID string, manifest CanonicalManifest, ref CanonicalReference) (CanonicalRecord, error) {
	var record CanonicalRecord
	found := false
	for _, entry := range manifest.Documents {
		if entry == ref {
			found = true
			break
		}
	}
	if !found || manifest.Validate(manifest.ProjectID) != nil || !manifest.Active {
		return record, canonicalInvalid()
	}
	err := canonicalGet(ctx, os.Getenv("DELPHI_BUILDER_REGISTRY_URL"), companyID, manifest.ProjectID, "/"+ref.Family+"/"+ref.ID, &record)
	if err != nil {
		return record, err
	}
	if record.ID != ref.ID || record.ProjectID != manifest.ProjectID || record.GenerationID != manifest.GenerationID || record.Revision != ref.Revision || record.ContentDigest != ref.ContentDigest || len(record.Payload) == 0 {
		return CanonicalRecord{}, errors.New("TEACH: canonical knowledge changed during the read; reload the current manifest and retry without Markdown fallback")
	}
	return record, nil
}

func (m CanonicalManifest) Origin() CanonicalOrigin {
	refs := append([]CanonicalReference(nil), m.Documents...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].Family+"/"+refs[i].ID < refs[j].Family+"/"+refs[j].ID })
	return CanonicalOrigin{m.ProjectID, m.GenerationID, refs}
}

func (m CanonicalManifest) Digest() string {
	data, _ := json.Marshal(m.Origin())
	return DigestBytes(append([]byte("builder-canonical-knowledge-origin-v1\x00"), data...))
}

// EvaluateCanonicalOrigin uses only explicit IDs, revisions and API digests.
// Missing origin is actionable review debt, never proof of a current artifact.
func EvaluateCanonicalOrigin(manifest CanonicalManifest, origin *CanonicalOrigin) TargetStatus {
	digest := manifest.Digest()
	result := TargetStatus{Status: "review_required", ReasonCode: "review_missing", ObservedSourceDigest: &digest}
	if origin == nil {
		return result
	}
	prior := CanonicalManifest{ProjectID: origin.ProjectID, GenerationID: origin.GenerationID, Documents: origin.Documents, Active: true, State: "active"}
	if err := prior.Validate(manifest.ProjectID); err != nil {
		result.Status, result.ReasonCode = "unverifiable", "invalid_origin"
		return result
	}
	reviewed := prior.Digest()
	result.ReviewedSourceDigest = &reviewed
	if reviewed == digest {
		result.Status, result.ReasonCode = "current", "match"
	} else {
		result.ReasonCode = "source_changed"
	}
	return result
}

// ResolveDocumentID resolves exact globally unique record IDs in this Project's
// active generation. It never derives a family or path from an identifier.
func (m CanonicalManifest) ResolveDocumentID(id string) (CanonicalReference, error) {
	var found CanonicalReference
	for _, ref := range m.Documents {
		if ref.ID == id {
			if found.ID != "" {
				return found, fmt.Errorf("TEACH: document_id %q is ambiguous; choose an unambiguous ID from this Project's manifest", id)
			}
			found = ref
		}
	}
	if !canonicalID.MatchString(id) || found.ID == "" {
		return found, errors.New("TEACH: document_id is absent from this Project's active knowledge manifest; use an exact API ID")
	}
	return found, nil
}

func IsCanonicalMarkdown(path string) bool {
	if !IsMarkdownPath(path) {
		return false
	}
	stem := strings.TrimSuffix(path, filepath.Ext(path))
	switch strings.ToLower(stem) {
	case "project_mandate", "domain_entities", "project_constitution", "system_roadmap", "endpoints_mvp_contracts":
		return true
	}
	return strings.HasPrefix(path, "modules/") || strings.HasPrefix(path, "policies/") || strings.HasPrefix(path, "domain_entities_sections/")
}
