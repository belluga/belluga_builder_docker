package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	ac "delphi-local-api/artifactcatalog"
)

const (
	landingManifestPath    = "design/landing/manifest.json"
	landingManifestVersion = "2"
	landingMaxManifest     = 256 << 10
	landingMaxFiles        = 128
	landingMaxFileBytes    = 2 << 20
	landingMaxTotalBytes   = 32 << 20
	landingCacheEntries    = 128
	landingCSP             = "default-src 'self' data:; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; script-src 'self'; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"
)

type LandingKnowledgeOrigin struct {
	ProjectID    string                   `json:"project_id"`
	GenerationID string                   `json:"generation_id"`
	Documents    []CanonicalManifestEntry `json:"documents"`
}

type LandingFileLink struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

type LandingCatalogResponse struct {
	CompanyID         string                 `json:"company_id"`
	ProjectID         string                 `json:"project_id"`
	RepositoryID      string                 `json:"repository_id"`
	PublicationBranch string                 `json:"publication_branch"`
	BindingRevision   int64                  `json:"binding_revision"`
	Revision          string                 `json:"revision"`
	InventoryDigest   string                 `json:"inventory_digest"`
	ManifestVersion   string                 `json:"manifest_version"`
	EntryPoint        string                 `json:"entry_point"`
	KnowledgeOrigin   LandingKnowledgeOrigin `json:"knowledge_origin"`
	Files             []LandingFileLink      `json:"files"`
	allowedPaths      map[string]struct{}
}

type landingFileResponse struct {
	Data            []byte
	ContentType     string
	ETag            string
	Revision        string
	InventoryDigest string
}

type landingReader interface {
	ReadLanding(context.Context, string, string) (LandingCatalogResponse, error)
	ReadLandingFile(context.Context, string, string, int64, string, string) (landingFileResponse, error)
}

type landingManifestV2 struct {
	SchemaVersion string `json:"schema_version"`
	Visual        struct {
		Path            string                 `json:"path"`
		ProjectID       string                 `json:"project_id"`
		CompanyID       string                 `json:"company_id"`
		ArtifactFiles   []landingManifestFile  `json:"artifact_files"`
		KnowledgeOrigin LandingKnowledgeOrigin `json:"knowledge_origin"`
	} `json:"local_visual_artifact"`
}

type landingManifestFile struct {
	Path   string `json:"path"`
	Digest string `json:"content_digest"`
}

func (m *MongoStore) readLandingBinding(ctx context.Context, companyID, projectID string) (RepositoryBinding, error) {
	if m.landingBindingReader != nil {
		return m.landingBindingReader(ctx, companyID, projectID)
	}
	return m.ReadRepositoryBinding(ctx, companyID, projectID)
}

func (m *MongoStore) readLandingManifest(ctx context.Context, companyID, projectID string) (CanonicalManifest, error) {
	if m.landingManifestReader != nil {
		return m.landingManifestReader(ctx, companyID, projectID)
	}
	return m.CanonicalManifest(ctx, companyID, projectID, "")
}

func (m *MongoStore) ReadLanding(ctx context.Context, companyID, projectID string) (LandingCatalogResponse, error) {
	if !validID(companyID) || !validID(projectID) {
		return LandingCatalogResponse{}, ErrInvalid
	}
	binding, err := m.readLandingBinding(ctx, companyID, projectID)
	if err != nil {
		return LandingCatalogResponse{}, err
	}
	if binding.State != "configured" || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) {
		return LandingCatalogResponse{}, teachCanonical("landing", "binding", "configure this Project's exact artifact Git repository and publication branch", ErrNotFound)
	}
	manifest, err := m.readLandingManifest(ctx, companyID, projectID)
	if err != nil {
		return LandingCatalogResponse{}, err
	}
	origin, err := landingOriginFromManifest(manifest)
	if err != nil {
		return LandingCatalogResponse{}, err
	}
	if m.prototypeGit == nil {
		return LandingCatalogResponse{}, ErrUnavailable
	}
	// The single remote branch-head resolution is the Landing refresh boundary.
	revision, err := m.prototypeGit.BranchHead(ctx, binding)
	if err != nil || !validFullGitSHA(revision) {
		return LandingCatalogResponse{}, ErrUnavailable
	}
	key := landingCatalogKey(companyID, projectID)
	if cached, ok := m.landingCatalogGet(key); ok && cached.Revision == revision && landingBindingMatchesCatalog(binding, cached) && sameLandingOrigin(cached.KnowledgeOrigin, origin) {
		return cached, nil
	}
	catalog, err := m.readLandingAtRevision(ctx, companyID, projectID, binding, revision, manifest)
	if err != nil {
		return LandingCatalogResponse{}, err
	}
	m.landingCatalogPut(key, catalog)
	return catalog, nil
}

func (m *MongoStore) ReadLandingFile(ctx context.Context, companyID, projectID string, bindingRevision int64, revision, requestedPath string) (landingFileResponse, error) {
	if !validID(companyID) || !validID(projectID) || bindingRevision < 0 || !validFullGitSHA(revision) || ac.ValidateRelativePath(requestedPath) != nil {
		return landingFileResponse{}, ErrInvalid
	}
	binding, err := m.readLandingBinding(ctx, companyID, projectID)
	if err != nil {
		return landingFileResponse{}, err
	}
	if binding.State != "configured" || binding.Revision != bindingRevision {
		return landingFileResponse{}, ErrConflict
	}
	manifest, err := m.readLandingManifest(ctx, companyID, projectID)
	if err != nil {
		return landingFileResponse{}, err
	}
	origin, err := landingOriginFromManifest(manifest)
	if err != nil {
		return landingFileResponse{}, err
	}
	key := landingCatalogKey(companyID, projectID)
	catalog, cached := m.landingCatalogGet(key)
	if !cached || catalog.Revision != revision || catalog.BindingRevision != bindingRevision || !landingBindingMatchesCatalog(binding, catalog) || !sameLandingOrigin(catalog.KnowledgeOrigin, origin) {
		catalog, err = m.readLandingAtRevision(ctx, companyID, projectID, binding, revision, manifest)
		if err != nil {
			return landingFileResponse{}, err
		}
		m.landingCatalogPut(key, catalog)
	}
	if _, ok := catalog.allowedPaths[requestedPath]; !ok {
		return landingFileResponse{}, ErrNotFound
	}
	cacheKey := landingFileCacheKey(companyID, projectID, catalog, requestedPath)
	if cachedFile, ok := m.prototypeFileCache.get(cacheKey); ok {
		return landingFileResponse{Data: cachedFile.Data, ContentType: cachedFile.ContentType, ETag: cachedFile.ETag, Revision: cachedFile.Revision, InventoryDigest: cachedFile.InventoryDigest}, nil
	}
	if m.prototypeGit == nil {
		return landingFileResponse{}, ErrUnavailable
	}
	gitBinding := binding
	gitSource, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, gitBinding, revision)
	if err != nil {
		return landingFileResponse{}, ErrUnavailable
	}
	defer gitSource.close()
	data, mode, err := gitSource.source.Read(requestedPath, landingMaxFileBytes)
	if err != nil || mode != ac.ModeRegular || len(data) > landingMaxFileBytes {
		return landingFileResponse{}, ErrUnavailable
	}
	if err := gitSource.source.VerifyStable(); err != nil {
		return landingFileResponse{}, ErrUnavailable
	}
	file := landingFileResponseFor(data, requestedPath, catalog)
	m.prototypeFileCache.put(cacheKey, prototypeFileResponse{Data: file.Data, ContentType: file.ContentType, ETag: file.ETag, Revision: file.Revision, InventoryDigest: file.InventoryDigest})
	return file, nil
}

func (m *MongoStore) readLandingAtRevision(ctx context.Context, companyID, projectID string, binding RepositoryBinding, revision string, knowledge CanonicalManifest) (LandingCatalogResponse, error) {
	if binding.State != "configured" || !validFullGitSHA(revision) || m.prototypeGit == nil {
		return LandingCatalogResponse{}, ErrUnavailable
	}
	origin, err := landingOriginFromManifest(knowledge)
	if err != nil {
		return LandingCatalogResponse{}, err
	}
	source, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, binding, revision)
	if err != nil {
		return LandingCatalogResponse{}, ErrUnavailable
	}
	defer source.close()
	catalog, err := evaluateLandingSource(source.source, companyID, projectID, binding.RepositoryID, binding, revision, origin)
	if err != nil {
		return LandingCatalogResponse{}, err
	}
	if err := source.source.VerifyStable(); err != nil {
		return LandingCatalogResponse{}, ErrUnavailable
	}
	for i := range catalog.Files {
		catalog.Files[i].URL = fmt.Sprintf("/internal/v1/companies/%s/projects/%s/landing/%d/%s/files/%s", companyID, projectID, binding.Revision, revision, catalog.Files[i].Path)
	}
	if err := m.fenceLanding(ctx, companyID, projectID, binding, origin); err != nil {
		return LandingCatalogResponse{}, err
	}
	return catalog, nil
}

func (m *MongoStore) fenceLanding(ctx context.Context, companyID, projectID string, expected RepositoryBinding, origin LandingKnowledgeOrigin) error {
	current, err := m.readLandingBinding(ctx, companyID, projectID)
	if err != nil || !sameRepositoryBinding(expected, current) {
		return ErrConflict
	}
	manifest, err := m.readLandingManifest(ctx, companyID, projectID)
	if err != nil {
		return err
	}
	currentOrigin, err := landingOriginFromManifest(manifest)
	if err != nil || !sameLandingOrigin(origin, currentOrigin) {
		return ErrConflict
	}
	return nil
}

func evaluateLandingSource(source ac.Source, companyID, projectID, repositoryID string, binding RepositoryBinding, revision string, knowledge LandingKnowledgeOrigin) (LandingCatalogResponse, error) {
	manifestBytes, mode, err := source.Read(landingManifestPath, landingMaxManifest)
	if errors.Is(err, os.ErrNotExist) {
		return LandingCatalogResponse{}, teachCanonical("landing", "manifest", "publish design/landing/manifest.json with schema_version 2, declared file digests, and the current API knowledge_origin at the bound Git revision; root project_landing.manifest.json and Markdown are not runtime fallbacks", ErrNotFound)
	}
	if err != nil || mode != ac.ModeRegular || len(manifestBytes) > landingMaxManifest {
		return LandingCatalogResponse{}, teachCanonical("landing", "manifest", "publish a regular design/landing/manifest.json with schema_version 2 within the 256 KiB limit at the bound Git revision", ErrInvalid)
	}
	var version struct {
		SchemaVersion string `json:"schema_version"`
	}
	if json.Unmarshal(manifestBytes, &version) == nil && version.SchemaVersion != landingManifestVersion {
		return LandingCatalogResponse{}, teachCanonical("landing", "manifest", "replace the unsupported design/landing/manifest.json with schema_version 2 containing local_visual_artifact, declared file digests, and current API knowledge_origin; root legacy manifest and Markdown are not served", ErrInvalid)
	}
	var manifest landingManifestV2
	if err := decodeLandingManifest(manifestBytes, &manifest); err != nil || manifest.SchemaVersion != landingManifestVersion || manifest.Visual.Path != "design/landing/index.html" || manifest.Visual.CompanyID != companyID || manifest.Visual.ProjectID != projectID || !sameLandingOrigin(manifest.Visual.KnowledgeOrigin, knowledge) || len(manifest.Visual.ArtifactFiles) == 0 || len(manifest.Visual.ArtifactFiles) > landingMaxFiles {
		return LandingCatalogResponse{}, teachCanonical("landing", "manifest", "use the strict schema_version 2 design/landing/manifest.json with exact Company/Project identity, visual artifact_files with sha256 content_digest values, and the complete current API knowledge_origin", ErrInvalid)
	}
	allowed := make(map[string]struct{}, len(manifest.Visual.ArtifactFiles))
	paths := make([]string, 0, len(manifest.Visual.ArtifactFiles))
	records := make([]ac.DigestRecord, 0, len(manifest.Visual.ArtifactFiles))
	var total int64
	for _, file := range manifest.Visual.ArtifactFiles {
		if ac.ValidateRelativePath(file.Path) != nil || !strings.HasPrefix(file.Path, "design/landing/") || landingMIMEType(file.Path) == "" || !validSourceDigest(file.Digest) {
			return LandingCatalogResponse{}, teachCanonical("landing", "artifact_files", "declare only safe, supported design/landing files with sha256 digests", ErrInvalid)
		}
		if _, duplicate := allowed[file.Path]; duplicate {
			return LandingCatalogResponse{}, ErrInvalid
		}
		data, fileMode, readErr := source.Read(file.Path, landingMaxFileBytes)
		if readErr != nil || fileMode != ac.ModeRegular || len(data) > landingMaxFileBytes {
			return LandingCatalogResponse{}, teachCanonical("landing", file.Path, "keep each Landing artifact a regular file within the 2 MiB limit", ErrInvalid)
		}
		sum := sha256.Sum256(data)
		if file.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return LandingCatalogResponse{}, teachCanonical("landing", file.Path, "refresh the declared content_digest for the exact Git bytes", ErrConflict)
		}
		total += int64(len(data))
		if total > landingMaxTotalBytes {
			return LandingCatalogResponse{}, teachCanonical("landing", "artifact_files", "keep total Landing bytes at or below 32 MiB", ErrInvalid)
		}
		allowed[file.Path] = struct{}{}
		paths = append(paths, file.Path)
		records = append(records, ac.DigestRecord{SourceID: repositoryID, Path: file.Path, Content: data})
	}
	if _, admitted := allowed[manifest.Visual.Path]; !admitted {
		return LandingCatalogResponse{}, ErrInvalid
	}
	inventory, err := ac.InventoryDigest(repositoryID, records)
	if err != nil {
		return LandingCatalogResponse{}, ErrInvalid
	}
	sort.Strings(paths)
	links := make([]LandingFileLink, 0, len(paths))
	for _, path := range paths {
		links = append(links, LandingFileLink{Path: path})
	}
	return LandingCatalogResponse{
		CompanyID: companyID, ProjectID: projectID, RepositoryID: repositoryID,
		PublicationBranch: binding.PublicationBranch, BindingRevision: binding.Revision,
		Revision: revision, InventoryDigest: inventory, ManifestVersion: landingManifestVersion,
		EntryPoint: manifest.Visual.Path, KnowledgeOrigin: manifest.Visual.KnowledgeOrigin,
		Files: links, allowedPaths: allowed,
	}, nil
}

func decodeLandingManifest(data []byte, manifest *landingManifestV2) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("manifest is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := validateLandingJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(manifest); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func validateLandingJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := validateLandingJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := validateLandingJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func landingOriginFromManifest(manifest CanonicalManifest) (LandingKnowledgeOrigin, error) {
	if manifest.State != "active" || !manifest.Active || manifest.GenerationID == "" || manifest.ProjectID == "" || len(manifest.Documents) == 0 {
		return LandingKnowledgeOrigin{}, teachCanonical("landing", "knowledge_origin", "activate and read the complete Project Knowledge manifest before admitting Landing", ErrUnavailable)
	}
	documents := append([]CanonicalManifestEntry(nil), manifest.Documents...)
	sort.Slice(documents, func(i, j int) bool {
		if documents[i].Family != documents[j].Family {
			return documents[i].Family < documents[j].Family
		}
		return documents[i].ID < documents[j].ID
	})
	return LandingKnowledgeOrigin{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, Documents: documents}, nil
}

func sameLandingOrigin(a, b LandingKnowledgeOrigin) bool {
	if a.ProjectID == "" || a.ProjectID != b.ProjectID || a.GenerationID == "" || a.GenerationID != b.GenerationID || len(a.Documents) == 0 || len(a.Documents) != len(b.Documents) {
		return false
	}
	available := make(map[string]CanonicalManifestEntry, len(b.Documents))
	for _, document := range b.Documents {
		available[landingKnowledgeKey(document)] = document
	}
	selected := make(map[string]struct{}, len(a.Documents))
	for _, document := range a.Documents {
		key := landingKnowledgeKey(document)
		if document.ID == "" || !validSourceDigest(document.ContentDigest) {
			return false
		}
		if _, duplicate := selected[key]; duplicate {
			return false
		}
		selected[key] = struct{}{}
		if available[key] != document {
			return false
		}
	}
	return true
}

func landingKnowledgeKey(document CanonicalManifestEntry) string {
	return string(document.Family) + "\x00" + document.ID
}

func sameRepositoryBinding(a, b RepositoryBinding) bool {
	return a.State == b.State && a.RepositoryID == b.RepositoryID && a.PublicationBranch == b.PublicationBranch && a.Revision == b.Revision
}

func landingBindingMatchesCatalog(binding RepositoryBinding, catalog LandingCatalogResponse) bool {
	return binding.State == "configured" && binding.RepositoryID == catalog.RepositoryID && binding.PublicationBranch == catalog.PublicationBranch && binding.Revision == catalog.BindingRevision
}

func landingMIMEType(path string) string {
	if ac.ValidateRelativePath(path) != nil || !strings.HasPrefix(path, "design/landing/") {
		return ""
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	default:
		return ""
	}
}

func landingFileResponseFor(data []byte, path string, catalog LandingCatalogResponse) landingFileResponse {
	digest := sha256.Sum256(data)
	return landingFileResponse{Data: append([]byte(nil), data...), ContentType: landingMIMEType(path), ETag: fmt.Sprintf("\"%s-%s\"", catalog.Revision, hex.EncodeToString(digest[:])), Revision: catalog.Revision, InventoryDigest: catalog.InventoryDigest}
}

func landingCatalogKey(companyID, projectID string) string { return companyID + "\x00" + projectID }

func landingFileCacheKey(companyID, projectID string, catalog LandingCatalogResponse, path string) string {
	return companyID + "\x00" + projectID + "\x00landing\x00" + catalog.RepositoryID + "\x00" + catalog.PublicationBranch + "\x00" + fmt.Sprint(catalog.BindingRevision) + "\x00" + catalog.Revision + "\x00" + catalog.InventoryDigest + "\x00" + path
}

func (m *MongoStore) landingCatalogGet(key string) (LandingCatalogResponse, bool) {
	m.landingMu.Lock()
	defer m.landingMu.Unlock()
	if m.landingCatalogs == nil {
		return LandingCatalogResponse{}, false
	}
	value, ok := m.landingCatalogs[key]
	return value, ok
}

func (m *MongoStore) landingCatalogPut(key string, value LandingCatalogResponse) {
	m.landingMu.Lock()
	defer m.landingMu.Unlock()
	if m.landingCatalogs == nil {
		m.landingCatalogs = make(map[string]LandingCatalogResponse)
	}
	if _, exists := m.landingCatalogs[key]; !exists {
		m.landingOrder = append(m.landingOrder, key)
	}
	m.landingCatalogs[key] = value
	for len(m.landingOrder) > landingCacheEntries {
		oldest := m.landingOrder[0]
		m.landingOrder = m.landingOrder[1:]
		delete(m.landingCatalogs, oldest)
	}
}
