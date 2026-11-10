package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	ac "delphi-local-api/artifactcatalog"
)

const prototypeEvaluatorVersion = "prototype-v" + ac.PrototypeSchemaVersion

type PrototypeCatalogResponse struct {
	RepositoryID        string             `json:"repository_id"`
	PublicationBranch   string             `json:"publication_branch"`
	BindingRevision     int64              `json:"binding_revision"`
	Revision            string             `json:"revision"`
	EvaluatorVersion    string             `json:"evaluator_version"`
	InventoryDigest     string             `json:"inventory_digest"`
	KnowledgeGeneration string             `json:"knowledge_generation_id,omitempty"`
	KnowledgeDigest     string             `json:"knowledge_digest"`
	Items               []PrototypeSummary `json:"items"`
	navigation          []ac.Item
}

type PrototypeSummary struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Root        string  `json:"root"`
	Status      string  `json:"status"`
	DetailURL   string  `json:"detail_url"`
}

type PrototypeReadResponse struct {
	RepositoryID        string              `json:"repository_id"`
	PublicationBranch   string              `json:"publication_branch"`
	BindingRevision     int64               `json:"binding_revision"`
	Revision            string              `json:"revision"`
	EvaluatorVersion    string              `json:"evaluator_version"`
	InventoryDigest     string              `json:"inventory_digest"`
	KnowledgeGeneration string              `json:"knowledge_generation_id,omitempty"`
	KnowledgeDigest     string              `json:"knowledge_digest"`
	Item                ac.Item             `json:"item"`
	FileURLs            []PrototypeFileLink `json:"file_urls"`
}

type PrototypeFileLink struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

type prototypeGitSource struct {
	workspace string
	source    ac.Source
	sha       string
	transient bool
	release   func()
}

type prototypeGitReader interface {
	BranchHead(context.Context, RepositoryBinding) (string, error)
	OpenRevision(context.Context, string, string, RepositoryBinding, string) (prototypeGitSource, error)
}

func (s prototypeGitSource) close() {
	if s.transient {
		_ = os.RemoveAll(s.workspace)
	}
	if s.release != nil {
		s.release()
	}
}

var prototypeRepositoryLocks sync.Map

func (v gitDesignSystemVerifier) BranchHead(ctx context.Context, binding RepositoryBinding) (string, error) {
	if binding.State != "configured" || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) || v.repositoryURL == nil {
		return "", ErrUnavailable
	}
	remote := v.repositoryURL(binding.RepositoryID)
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n") {
		return "", ErrUnavailable
	}
	head, err := v.git(ctx, "", true, "ls-remote", "--exit-code", remote, "refs/heads/"+binding.PublicationBranch)
	if err != nil {
		return "", ErrUnavailable
	}
	fields := strings.Fields(string(head))
	if len(fields) != 2 || fields[1] != "refs/heads/"+binding.PublicationBranch || !validFullGitSHA(fields[0]) {
		return "", ErrUnavailable
	}
	return fields[0], nil
}

func (v gitDesignSystemVerifier) OpenRevision(ctx context.Context, companyID, projectID string, binding RepositoryBinding, revision string) (prototypeGitSource, error) {
	if !validID(companyID) || !validID(projectID) || binding.State != "configured" || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) || !validFullGitSHA(revision) || v.repositoryURL == nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	if v.prototypeCacheRoot != "" {
		return v.openCachedPrototypeRevision(ctx, companyID, projectID, binding, revision)
	}
	workspace, err := os.MkdirTemp("", ".builder-prototype-read-*")
	if err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(workspace)
		}
	}()
	if err := os.Chmod(workspace, 0o700); err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "init", "--quiet"); err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	remote := v.repositoryURL(binding.RepositoryID)
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n") {
		return prototypeGitSource{}, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "remote", "add", "origin", remote); err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, true, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", "refs/heads/"+binding.PublicationBranch); err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	head, err := v.git(ctx, workspace, false, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	sha := strings.TrimSpace(string(head))
	if !validFullGitSHA(sha) || sha != revision {
		return prototypeGitSource{}, ErrUnavailable
	}
	source, err := ac.NewGitTree(workspace, sha)
	if err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	failed = false
	return prototypeGitSource{workspace: workspace, source: source, sha: sha, transient: true}, nil
}

// The cache is owned by the Go container, never by a registered Project's
// checkout. Git's immutable objects are retained across container restarts;
// a new branch SHA fetches only objects absent from this repository.
func (v gitDesignSystemVerifier) openCachedPrototypeRevision(ctx context.Context, companyID, projectID string, binding RepositoryBinding, revision string) (prototypeGitSource, error) {
	root := v.prototypeCacheRoot
	if !filepath.IsAbs(root) || ac.RejectSymlinkComponents(root) != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	remote := v.repositoryURL(binding.RepositoryID)
	if remote == "" || strings.ContainsAny(remote, "\x00\r\n") {
		return prototypeGitSource{}, ErrUnavailable
	}
	key := sha256.Sum256([]byte(companyID + "\x00" + projectID + "\x00" + binding.RepositoryID + "\x00" + remote))
	workspace := filepath.Join(root, hex.EncodeToString(key[:]))
	lockValue, _ := prototypeRepositoryLocks.LoadOrStore(workspace, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	failed := true
	defer func() {
		if failed {
			lock.Unlock()
		}
	}()
	if err := os.Mkdir(workspace, 0o700); err != nil && !os.IsExist(err) {
		return prototypeGitSource{}, ErrUnavailable
	}
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return prototypeGitSource{}, ErrUnavailable
	}
	gitPath := filepath.Join(workspace, ".git")
	if _, err := os.Lstat(gitPath); os.IsNotExist(err) {
		if _, err := v.git(ctx, workspace, false, "init", "--quiet"); err != nil {
			return prototypeGitSource{}, ErrUnavailable
		}
		if _, err := v.git(ctx, workspace, false, "remote", "add", "origin", remote); err != nil {
			return prototypeGitSource{}, ErrUnavailable
		}
	} else if err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	gitInfo, err := os.Lstat(gitPath)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return prototypeGitSource{}, ErrUnavailable
	}
	configuredRemote, err := v.git(ctx, workspace, false, "remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(string(configuredRemote)) != remote {
		return prototypeGitSource{}, ErrUnavailable
	}
	if _, err := v.git(ctx, workspace, false, "cat-file", "-e", revision+"^{commit}"); err != nil {
		branchRef := "refs/heads/" + binding.PublicationBranch
		remoteRef := "refs/remotes/origin/" + binding.PublicationBranch
		if _, err := v.git(ctx, workspace, true, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", "+"+branchRef+":"+remoteRef); err != nil {
			return prototypeGitSource{}, ErrUnavailable
		}
		fetched, err := v.git(ctx, workspace, false, "rev-parse", "--verify", remoteRef+"^{commit}")
		if err != nil || strings.TrimSpace(string(fetched)) != revision {
			return prototypeGitSource{}, ErrUnavailable
		}
		// This is a private, rebuildable single-writer cache. Expire the old
		// tracking-ref reflog before GC so repeated Prototype publications do
		// not retain every superseded Git object in the Docker volume.
		if _, err := v.git(ctx, workspace, false, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all"); err != nil {
			return prototypeGitSource{}, ErrUnavailable
		}
		if _, err := v.git(ctx, workspace, false, "gc", "--prune=now"); err != nil {
			return prototypeGitSource{}, ErrUnavailable
		}
	}
	source, err := ac.NewGitTree(workspace, revision)
	if err != nil {
		return prototypeGitSource{}, ErrUnavailable
	}
	failed = false
	return prototypeGitSource{workspace: workspace, source: source, sha: revision, release: lock.Unlock}, nil
}

func canonicalManifestForPrototype(manifest CanonicalManifest, projectID string) (*ac.CanonicalManifest, string, error) {
	if manifest.ProjectID != projectID || manifest.State != "active" && manifest.State != "pending" || manifest.Active != (manifest.State == "active") {
		return nil, "", ErrUnavailable
	}
	converted := &ac.CanonicalManifest{
		ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID,
		Active: manifest.Active, State: manifest.State,
		Documents: make([]ac.CanonicalReference, 0, len(manifest.Documents)),
	}
	for _, item := range manifest.Documents {
		converted.Documents = append(converted.Documents, ac.CanonicalReference{
			Family: string(item.Family), ID: item.ID, Revision: item.Revision, ContentDigest: item.ContentDigest,
		})
	}
	data, err := json.Marshal(converted)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	digest := sha256.Sum256(data)
	if !manifest.Active {
		return nil, hex.EncodeToString(digest[:]), nil
	}
	return converted, hex.EncodeToString(digest[:]), nil
}

// prototypeSnapshot checks the branch only when opening the current catalog.
// Revision-pinned detail and file reads use the active, already verified Mongo
// projection; a later catalog reload is the explicit refresh boundary.
func (m *MongoStore) prototypeSnapshot(ctx context.Context, companyID, projectID, requestedRevision string) (PrototypeCatalogResponse, error) {
	if !validID(companyID) || !validID(projectID) {
		return PrototypeCatalogResponse{}, ErrInvalid
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("binding", err)
	}
	if binding.State != "configured" || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) || binding.Revision < 0 {
		return PrototypeCatalogResponse{}, teachCanonical("", "binding", "configure this Project's exact artifact Git repository and publication branch", ErrNotFound)
	}
	manifest, err := m.CanonicalManifest(ctx, companyID, projectID, "")
	if err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("knowledge", err)
	}
	knowledge, knowledgeDigest, err := canonicalManifestForPrototype(manifest, projectID)
	if err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("knowledge_contract", err)
	}
	reader := m.prototypeGit
	if reader == nil {
		return PrototypeCatalogResponse{}, ErrUnavailable
	}
	head, err := prototypeReadRevision(ctx, reader, binding, requestedRevision)
	if err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("branch_head", err)
	}
	if !validFullGitSHA(head) {
		return PrototypeCatalogResponse{}, ErrUnavailable
	}
	if indexed, found, readErr := m.readActivePrototypeProjection(ctx, companyID, projectID, binding, head, manifest.GenerationID, knowledgeDigest); readErr != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("active_projection", readErr)
	} else if found {
		if err := m.fencePrototypeIdentity(ctx, companyID, projectID, indexed); err != nil {
			return PrototypeCatalogResponse{}, prototypeReadFailure("indexed_fence", err)
		}
		return indexed, nil
	}
	if requestedRevision != "" {
		return PrototypeCatalogResponse{}, ErrConflict
	}
	gitSource, err := reader.OpenRevision(ctx, companyID, projectID, binding, head)
	if err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("git_revision", err)
	}
	defer gitSource.close()
	evaluation := ac.EvaluateWithKnowledge(gitSource.source, projectID, knowledge)
	if evaluation.Outcome != "go" || evaluation.Revision == nil || *evaluation.Revision != gitSource.sha || evaluation.InventoryDigest == nil {
		if len(evaluation.Diagnostics) > 0 {
			log.Printf("prototype evaluation rejected (outcome=%s, code=%s)", evaluation.Outcome, evaluation.Diagnostics[0].Code)
		} else {
			log.Printf("prototype evaluation rejected (outcome=%s, diagnostics=0)", evaluation.Outcome)
		}
		return PrototypeCatalogResponse{}, prototypeEvaluationError(evaluation)
	}
	if err := gitSource.source.VerifyStable(); err != nil {
		log.Printf("prototype Git source stability check failed (%T)", err)
		return PrototypeCatalogResponse{}, ErrUnavailable
	}
	navigation := append([]ac.Item(nil), evaluation.Items...)
	items := make([]PrototypeSummary, 0, len(navigation))
	for _, item := range navigation {
		items = append(items, PrototypeSummary{
			ID: item.ID, Name: item.Name, Description: item.Description, Root: item.Root, Status: item.Status,
			DetailURL: fmt.Sprintf("/internal/v1/companies/%s/projects/%s/prototypes/%s/%s", companyID, projectID, gitSource.sha, item.ID),
		})
	}
	catalog := PrototypeCatalogResponse{
		RepositoryID: binding.RepositoryID, PublicationBranch: binding.PublicationBranch,
		BindingRevision: binding.Revision, Revision: gitSource.sha,
		EvaluatorVersion: prototypeEvaluatorVersion, InventoryDigest: *evaluation.InventoryDigest,
		KnowledgeGeneration: manifest.GenerationID, KnowledgeDigest: knowledgeDigest, Items: items, navigation: navigation,
	}
	if err := m.persistPrototypeProjection(ctx, companyID, projectID, catalog); err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("persist_projection", err)
	}
	if err := m.fencePrototypeCurrent(ctx, companyID, projectID, catalog); err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("new_fence", err)
	}
	m.primePrototypeFiles(ctx, companyID, projectID, catalog, gitSource.source)
	return catalog, nil
}

func prototypeReadRevision(ctx context.Context, reader prototypeGitReader, binding RepositoryBinding, requestedRevision string) (string, error) {
	if requestedRevision != "" {
		if !validFullGitSHA(requestedRevision) {
			return "", ErrInvalid
		}
		return requestedRevision, nil
	}
	return reader.BranchHead(ctx, binding)
}

func (m *MongoStore) primePrototypeFiles(ctx context.Context, companyID, projectID string, catalog PrototypeCatalogResponse, source ac.Source) {
	if m.prototypeFileCache == nil {
		return
	}
	paths := make([]string, 0, len(prototypeAllowedPaths(catalog)))
	for path := range prototypeAllowedPaths(catalog) {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		data, mode, err := source.Read(path, ac.MaxFileBytes)
		if err != nil || mode != ac.ModeRegular || len(data) > ac.MaxFileBytes {
			continue
		}
		m.prototypeFileCache.put(prototypeFileCacheKey(companyID, projectID, catalog, path), prototypeFileResponseFor(data, path, catalog))
	}
}

func (m *MongoStore) warmPrototypeFiles(ctx context.Context, companyID, projectID string, catalog PrototypeCatalogResponse) error {
	if m.prototypeFileCache == nil {
		return ErrUnavailable
	}
	for path := range prototypeAllowedPaths(catalog) {
		if !m.prototypeFileCache.has(prototypeFileCacheKey(companyID, projectID, catalog, path)) {
			binding := RepositoryBinding{State: "configured", RepositoryID: catalog.RepositoryID, PublicationBranch: catalog.PublicationBranch, Revision: catalog.BindingRevision}
			source, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, binding, catalog.Revision)
			if err != nil {
				return err
			}
			defer source.close()
			m.primePrototypeFiles(ctx, companyID, projectID, catalog, source.source)
			if err := source.source.VerifyStable(); err != nil {
				return ErrUnavailable
			}
			return nil
		}
	}
	return nil
}

func prototypeReadFailure(stage string, err error) error {
	log.Printf("prototype catalog unavailable at %s (%T)", stage, err)
	return err
}

func (m *MongoStore) fencePrototypeBinding(ctx context.Context, companyID, projectID string, catalog PrototypeCatalogResponse) error {
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil || !prototypeBindingMatchesCatalog(binding, catalog) {
		return ErrConflict
	}
	return nil
}

func prototypeBindingMatchesCatalog(binding RepositoryBinding, catalog PrototypeCatalogResponse) bool {
	return binding.State == "configured" && binding.RepositoryID == catalog.RepositoryID && binding.PublicationBranch == catalog.PublicationBranch && binding.Revision == catalog.BindingRevision
}

func (m *MongoStore) fencePrototypeIdentity(ctx context.Context, companyID, projectID string, catalog PrototypeCatalogResponse) error {
	if err := m.fencePrototypeBinding(ctx, companyID, projectID, catalog); err != nil {
		return err
	}
	manifest, err := m.CanonicalManifest(ctx, companyID, projectID, "")
	if err != nil {
		return err
	}
	_, knowledgeDigest, err := canonicalManifestForPrototype(manifest, projectID)
	if err != nil || manifest.GenerationID != catalog.KnowledgeGeneration || knowledgeDigest != catalog.KnowledgeDigest {
		return ErrConflict
	}
	return nil
}

func (m *MongoStore) fencePrototypeCurrent(ctx context.Context, companyID, projectID string, catalog PrototypeCatalogResponse) error {
	if err := m.fencePrototypeIdentity(ctx, companyID, projectID, catalog); err != nil {
		return err
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil {
		return err
	}
	reader := m.prototypeGit
	if reader == nil {
		return ErrUnavailable
	}
	head, err := reader.BranchHead(ctx, binding)
	if err != nil {
		return err
	}
	if head != catalog.Revision {
		return ErrConflict
	}
	return nil
}

func prototypeEvaluationError(evaluation ac.Response) error {
	if len(evaluation.Diagnostics) == 0 {
		return ErrUnavailable
	}
	first := evaluation.Diagnostics[0]
	field := "prototypes"
	if first.Path != nil && *first.Path != "" {
		field = *first.Path
	}
	return teachCanonical("", field, "correct the Prototype catalog or manifest at the bound Git branch head; older Prototype formats are not supported", ErrInvalid)
}

func prototypeAllowedPaths(catalog PrototypeCatalogResponse) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, item := range catalog.navigation {
		for _, path := range item.AllowedPaths {
			if ac.ValidateRelativePath(path) == nil {
				allowed[path] = struct{}{}
			}
		}
		for _, related := range item.Related {
			if related.Path != "" && ac.ValidateRelativePath(related.Path) == nil {
				allowed[related.Path] = struct{}{}
			}
		}
		for _, screen := range item.Screens {
			for _, state := range screen.States {
				for _, reference := range state.ApprovedReferences {
					if reference.ApprovalEvidence != "" && ac.ValidateRelativePath(reference.ApprovalEvidence) == nil {
						allowed[reference.ApprovalEvidence] = struct{}{}
					}
				}
			}
		}
	}
	return allowed
}

type prototypeFileResponse struct {
	Data            []byte
	ContentType     string
	ETag            string
	Revision        string
	InventoryDigest string
}

func (m *MongoStore) ReadPrototypes(ctx context.Context, companyID, projectID string) (PrototypeCatalogResponse, error) {
	m.prototypeHot.replace(companyID, projectID, nil)
	catalog, err := m.prototypeSnapshot(ctx, companyID, projectID, "")
	if err != nil {
		return PrototypeCatalogResponse{}, err
	}
	if err := m.warmPrototypeFiles(ctx, companyID, projectID, catalog); err != nil {
		return PrototypeCatalogResponse{}, prototypeReadFailure("warm_files", err)
	}
	m.prototypeHot.replace(companyID, projectID, &catalog)
	return catalog, nil
}

func (m *MongoStore) ReadPrototype(ctx context.Context, companyID, projectID, revision, prototypeID string) (PrototypeReadResponse, error) {
	if !validFullGitSHA(revision) || !validStableArtifactID(prototypeID) {
		return PrototypeReadResponse{}, ErrInvalid
	}
	hot, found := m.prototypeHot.get(companyID, projectID, revision)
	catalog := hot.catalog
	if found {
		if err := m.fencePrototypeIdentity(ctx, companyID, projectID, catalog); err != nil {
			return PrototypeReadResponse{}, err
		}
	} else {
		var err error
		catalog, err = m.prototypeSnapshot(ctx, companyID, projectID, revision)
		if err != nil {
			return PrototypeReadResponse{}, err
		}
	}
	if catalog.Revision != revision {
		return PrototypeReadResponse{}, ErrConflict
	}
	for _, item := range catalog.navigation {
		if item.ID == prototypeID {
			links := make([]PrototypeFileLink, 0)
			for path := range prototypeAllowedPaths(PrototypeCatalogResponse{navigation: []ac.Item{item}}) {
				links = append(links, PrototypeFileLink{Path: path, URL: fmt.Sprintf("/internal/v1/companies/%s/projects/%s/prototypes/%s/files/%s", companyID, projectID, revision, path)})
			}
			sort.Slice(links, func(i, j int) bool { return links[i].Path < links[j].Path })
			return PrototypeReadResponse{
				RepositoryID: catalog.RepositoryID, PublicationBranch: catalog.PublicationBranch,
				BindingRevision: catalog.BindingRevision, Revision: catalog.Revision,
				EvaluatorVersion: catalog.EvaluatorVersion, InventoryDigest: catalog.InventoryDigest,
				KnowledgeGeneration: catalog.KnowledgeGeneration, KnowledgeDigest: catalog.KnowledgeDigest,
				Item: item, FileURLs: links,
			}, nil
		}
	}
	return PrototypeReadResponse{}, ErrNotFound
}

func (m *MongoStore) ReadPrototypeFile(ctx context.Context, companyID, projectID, revision, requestedPath string) (prototypeFileResponse, error) {
	if !validFullGitSHA(revision) || ac.ValidateRelativePath(requestedPath) != nil {
		return prototypeFileResponse{}, ErrInvalid
	}
	hot, found := m.prototypeHot.get(companyID, projectID, revision)
	catalog := hot.catalog
	if found {
		if err := m.fencePrototypeIdentity(ctx, companyID, projectID, catalog); err != nil {
			return prototypeFileResponse{}, err
		}
	} else {
		var err error
		catalog, err = m.prototypeSnapshot(ctx, companyID, projectID, revision)
		if err != nil {
			return prototypeFileResponse{}, err
		}
	}
	if catalog.Revision != revision {
		return prototypeFileResponse{}, ErrConflict
	}
	allowedPaths := hot.allowed
	if !found {
		allowedPaths = prototypeAllowedPaths(catalog)
	}
	if _, allowed := allowedPaths[requestedPath]; !allowed {
		return prototypeFileResponse{}, ErrNotFound
	}
	cacheKey := prototypeFileCacheKey(companyID, projectID, catalog, requestedPath)
	if cached, found := m.prototypeFileCache.get(cacheKey); found {
		return cached, nil
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil {
		return prototypeFileResponse{}, err
	}
	if m.prototypeGit == nil {
		return prototypeFileResponse{}, ErrUnavailable
	}
	source, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, binding, revision)
	if err != nil {
		return prototypeFileResponse{}, err
	}
	defer source.close()
	m.primePrototypeFiles(ctx, companyID, projectID, catalog, source.source)
	if err := source.source.VerifyStable(); err != nil {
		return prototypeFileResponse{}, ErrUnavailable
	}
	if cached, found := m.prototypeFileCache.get(cacheKey); found {
		return cached, nil
	}
	data, mode, err := source.source.Read(requestedPath, ac.MaxFileBytes)
	if err != nil || mode != ac.ModeRegular || len(data) > ac.MaxFileBytes {
		return prototypeFileResponse{}, ErrUnavailable
	}
	if err := source.source.VerifyStable(); err != nil {
		return prototypeFileResponse{}, ErrUnavailable
	}
	if err := m.fencePrototypeIdentity(ctx, companyID, projectID, catalog); err != nil {
		return prototypeFileResponse{}, err
	}
	response := prototypeFileResponseFor(data, requestedPath, catalog)
	if m.prototypeFileCache != nil {
		m.prototypeFileCache.put(cacheKey, response)
	}
	return response, nil
}

func prototypeFileResponseFor(data []byte, path string, catalog PrototypeCatalogResponse) prototypeFileResponse {
	digest := sha256.Sum256(data)
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return prototypeFileResponse{
		Data: data, ContentType: contentType,
		ETag: fmt.Sprintf("\"%s-%s\"", catalog.Revision, hex.EncodeToString(digest[:])), Revision: catalog.Revision,
		InventoryDigest: catalog.InventoryDigest,
	}
}

func validStableArtifactID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, char := range id[1:] {
		if char < 'a' || char > 'z' {
			if char < '0' || char > '9' {
				if char != '-' {
					return false
				}
			}
		}
	}
	return true
}
