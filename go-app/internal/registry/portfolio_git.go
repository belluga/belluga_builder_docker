package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"

	ac "delphi-local-api/artifactcatalog"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	portfolioPath            = "project_portfolio.json"
	portfolioCoverPath       = "project_portfolio/cover.png"
	portfolioMaxFileBytes    = 2 << 20
	portfolioMaxCacheEntries = 64
)

type PortfolioFileLink struct {
	Path        string `json:"path"`
	Digest      string `json:"content_digest"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

type ProjectPortfolio struct {
	CompanyID         string              `json:"company_id"`
	ProjectID         string              `json:"project_id"`
	State             string              `json:"state"`
	Teach             string              `json:"teach,omitempty"`
	RepositoryID      string              `json:"repository_id,omitempty"`
	PublicationBranch string              `json:"publication_branch,omitempty"`
	BindingRevision   int64               `json:"binding_revision,omitempty"`
	Revision          string              `json:"revision,omitempty"`
	InventoryDigest   string              `json:"inventory_digest,omitempty"`
	SchemaVersion     string              `json:"schema_version,omitempty"`
	Summary           string              `json:"summary,omitempty"`
	Files             []PortfolioFileLink `json:"files,omitempty"`
	fileData          map[string][]byte   `json:"-"`
}

type PortfolioCatalogResponse struct {
	CompanyID string             `json:"company_id"`
	Projects  []ProjectPortfolio `json:"projects"`
}

type portfolioFileResponse struct {
	Data                                                 []byte
	ContentType, ETag, Revision, InventoryDigest, Digest string
}
type portfolioCachedFile struct {
	data                []byte
	digest, contentType string
}
type portfolioSummaryV1 struct {
	SchemaVersion string `json:"schema_version"`
	Summary       string `json:"summary"`
}

func (m *MongoStore) ReadPortfolioCatalog(ctx context.Context, companyID string) (PortfolioCatalogResponse, error) {
	if !validID(companyID) {
		return PortfolioCatalogResponse{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return PortfolioCatalogResponse{}, err
	}
	rows, err := m.client.Database(company.DatabaseName).Collection(projectsCollection).Find(ctx, bson.D{{Key: "company_id", Value: companyID}})
	if err != nil {
		return PortfolioCatalogResponse{}, ErrUnavailable
	}
	defer rows.Close(ctx)
	result := PortfolioCatalogResponse{CompanyID: companyID, Projects: []ProjectPortfolio{}}
	for rows.Next(ctx) {
		var record projectDocument
		if rows.Decode(&record) != nil || record.ObjectID.IsZero() || record.CompanyID != companyID || !validID(record.ID) {
			return PortfolioCatalogResponse{}, ErrUnavailable
		}
		item, readErr := m.ReadProjectPortfolio(ctx, companyID, record.ID)
		if readErr != nil {
			item = ProjectPortfolio{CompanyID: companyID, ProjectID: record.ID, State: portfolioErrorState(readErr), Teach: portfolioTeach(readErr)}
		}
		result.Projects = append(result.Projects, item)
	}
	if rows.Err() != nil {
		return PortfolioCatalogResponse{}, ErrUnavailable
	}
	return result, nil
}

func portfolioErrorState(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "not_authored"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	default:
		return "unavailable"
	}
}

func portfolioTeach(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "TEACH: Publish project_portfolio.json v1 at the bound Git revision. Project snapshots and Markdown are not fallbacks."
	case errors.Is(err, ErrInvalid):
		return "TEACH: Correct project_portfolio.json to the strict v1 summary contract."
	default:
		return "TEACH: Portfolio Git content could not be verified; no snapshot or Markdown fallback was consulted."
	}
}

func (m *MongoStore) ReadProjectPortfolio(ctx context.Context, companyID, projectID string) (ProjectPortfolio, error) {
	if !validID(companyID) || !validID(projectID) {
		return ProjectPortfolio{}, ErrInvalid
	}
	project, err := m.Project(ctx, companyID, projectID)
	if err != nil {
		return ProjectPortfolio{}, err
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil {
		return ProjectPortfolio{}, err
	}
	if binding.State != "configured" || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) {
		return ProjectPortfolio{}, ErrNotFound
	}
	if m.prototypeGit == nil {
		return ProjectPortfolio{}, ErrUnavailable
	}
	revision, err := m.prototypeGit.BranchHead(ctx, binding)
	if err != nil || !validFullGitSHA(revision) {
		return ProjectPortfolio{}, ErrUnavailable
	}
	opened, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, binding, revision)
	if err != nil {
		return ProjectPortfolio{}, ErrUnavailable
	}
	defer opened.close()
	item, err := evaluatePortfolioSource(opened.source, companyID, projectID, binding, revision)
	if err != nil {
		return ProjectPortfolio{}, err
	}
	if err := opened.source.VerifyStable(); err != nil {
		return ProjectPortfolio{}, ErrUnavailable
	}
	if err := m.fencePortfolio(ctx, companyID, projectID, binding, project); err != nil {
		return ProjectPortfolio{}, err
	}
	cacheKey := portfolioCacheKey(companyID, projectID, binding.Revision, revision, item.InventoryDigest)
	m.portfolioMu.Lock()
	if m.portfolioFiles == nil {
		m.portfolioFiles = make(map[string]map[string]portfolioCachedFile)
	}
	if len(m.portfolioFiles) >= portfolioMaxCacheEntries {
		m.portfolioFiles = make(map[string]map[string]portfolioCachedFile)
	}
	files := make(map[string]portfolioCachedFile, len(item.fileData))
	for _, link := range item.Files {
		files[link.Path] = portfolioCachedFile{data: append([]byte(nil), item.fileData[link.Path]...), digest: link.Digest, contentType: link.ContentType}
	}
	m.portfolioFiles[cacheKey] = files
	m.portfolioMu.Unlock()
	for i := range item.Files {
		item.Files[i].URL = fmt.Sprintf("/internal/v1/companies/%s/projects/%s/portfolio/%d/%s/%s/files/%s", companyID, projectID, binding.Revision, revision, strings.TrimPrefix(item.InventoryDigest, "sha256:"), item.Files[i].Path)
	}
	item.fileData = nil
	return item, nil
}

func (m *MongoStore) fencePortfolio(ctx context.Context, companyID, projectID string, binding RepositoryBinding, project Project) error {
	current, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil || current != binding {
		return ErrConflict
	}
	latest, err := m.Project(ctx, companyID, projectID)
	if err != nil || latest.CompanyID != companyID || latest.ID != projectID || latest.BindingRevision != project.BindingRevision {
		return ErrConflict
	}
	return nil
}

func evaluatePortfolioSource(source ac.Source, companyID, projectID string, binding RepositoryBinding, revision string) (ProjectPortfolio, error) {
	if !validID(companyID) || !validID(projectID) || binding.State != "configured" || !validFullGitSHA(revision) {
		return ProjectPortfolio{}, ErrInvalid
	}
	data, mode, err := source.Read(portfolioPath, portfolioMaxFileBytes)
	hasSummary := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ProjectPortfolio{}, ErrUnavailable
	}
	var summary portfolioSummaryV1
	files := []PortfolioFileLink{}
	records := []ac.DigestRecord{}
	fileData := map[string][]byte{}
	if hasSummary {
		if mode != ac.ModeRegular || len(data) == 0 || len(data) > portfolioMaxFileBytes || !utf8.Valid(data) || decodeStrictPortfolio(data, &summary) != nil || summary.SchemaVersion != "1" || !utf8.ValidString(summary.Summary) || strings.TrimSpace(summary.Summary) == "" || utf8.RuneCountInString(summary.Summary) > 240 {
			return ProjectPortfolio{}, ErrInvalid
		}
		files = append(files, PortfolioFileLink{Path: portfolioPath, Digest: portfolioDigest(data), ContentType: "application/json"})
		records = append(records, ac.DigestRecord{SourceID: binding.RepositoryID, Path: portfolioPath, Content: data})
		fileData[portfolioPath] = append([]byte(nil), data...)
	}
	cover, coverMode, coverErr := source.Read(portfolioCoverPath, portfolioMaxFileBytes)
	if coverErr == nil {
		if coverMode != ac.ModeRegular || len(cover) == 0 || len(cover) > portfolioMaxFileBytes {
			return ProjectPortfolio{}, ErrInvalid
		}
		if _, err := png.Decode(bytes.NewReader(cover)); err != nil {
			return ProjectPortfolio{}, ErrInvalid
		}
		files = append(files, PortfolioFileLink{Path: portfolioCoverPath, Digest: portfolioDigest(cover), ContentType: "image/png"})
		fileData[portfolioCoverPath] = append([]byte(nil), cover...)
		records = append(records, ac.DigestRecord{SourceID: binding.RepositoryID, Path: portfolioCoverPath, Content: cover})
	} else if !errors.Is(coverErr, fs.ErrNotExist) {
		return ProjectPortfolio{}, ErrUnavailable
	}
	if len(files) == 0 {
		return ProjectPortfolio{}, ErrNotFound
	}
	inventory, err := ac.InventoryDigest(binding.RepositoryID, records)
	if err != nil {
		return ProjectPortfolio{}, ErrInvalid
	}
	result := ProjectPortfolio{CompanyID: companyID, ProjectID: projectID, State: "available", RepositoryID: binding.RepositoryID, PublicationBranch: binding.PublicationBranch, BindingRevision: binding.Revision, Revision: revision, InventoryDigest: inventory, Files: files, fileData: fileData}
	if hasSummary {
		result.SchemaVersion = "1"
		result.Summary = summary.Summary
	}
	return result, nil
}

func decodeStrictPortfolio(data []byte, target *portfolioSummaryV1) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := validateLandingJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func (m *MongoStore) ReadProjectPortfolioFile(ctx context.Context, companyID, projectID string, bindingRevision int64, revision, inventoryDigest, requestedPath string) (portfolioFileResponse, error) {
	if !validID(companyID) || !validID(projectID) || bindingRevision < 0 || !validFullGitSHA(revision) || !validSourceDigest(inventoryDigest) || ac.ValidateRelativePath(requestedPath) != nil || portfolioMIMEType(requestedPath) == "" {
		return portfolioFileResponse{}, ErrInvalid
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil || binding.State != "configured" || binding.Revision != bindingRevision {
		return portfolioFileResponse{}, ErrConflict
	}
	key := portfolioCacheKey(companyID, projectID, bindingRevision, revision, inventoryDigest)
	m.portfolioMu.Lock()
	cached := m.portfolioFiles[key][requestedPath]
	m.portfolioMu.Unlock()
	if cached.data == nil {
		return portfolioFileResponse{}, ErrConflict
	}
	if len(cached.data) > portfolioMaxFileBytes || portfolioDigest(cached.data) != cached.digest || cached.contentType != portfolioMIMEType(requestedPath) {
		return portfolioFileResponse{}, ErrUnavailable
	}
	return portfolioFileResponse{Data: append([]byte(nil), cached.data...), ContentType: cached.contentType, Digest: cached.digest, ETag: `"` + strings.TrimPrefix(cached.digest, "sha256:") + `"`, Revision: revision, InventoryDigest: inventoryDigest}, nil
}

func portfolioCacheKey(companyID, projectID string, bindingRevision int64, revision, inventory string) string {
	return fmt.Sprintf("%s/%s/%d/%s/%s", companyID, projectID, bindingRevision, revision, inventory)
}
func portfolioDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func portfolioMIMEType(path string) string {
	switch path {
	case portfolioPath:
		return "application/json"
	case portfolioCoverPath:
		return "image/png"
	default:
		return ""
	}
}
