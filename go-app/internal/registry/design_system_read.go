package registry

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"

	ac "belluga-builder-viewer/artifact_catalog"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type DesignSystemReadResponse struct {
	Selection  DesignSystemSelection   `json:"selection"`
	Evaluation ac.DesignSystemResponse `json:"evaluation"`
}

type DesignSystemHistoryRevision struct {
	SHA         string `json:"sha"`
	CommittedAt string `json:"committed_at"`
	Current     bool   `json:"current_selected_revision"`
}

type DesignSystemHistoryResponse struct {
	Selection     DesignSystemSelection         `json:"selection"`
	RepositoryID  string                        `json:"repository_id"`
	Branch        string                        `json:"branch"`
	BranchHeadSHA string                        `json:"branch_head_sha"`
	Revisions     []DesignSystemHistoryRevision `json:"revisions"`
	Truncated     bool                          `json:"truncated"`
}

type DesignSystemAsOfResponse struct {
	Selection     DesignSystemSelection   `json:"selection"`
	Revision      string                  `json:"revision"`
	BranchHeadSHA string                  `json:"branch_head_sha"`
	Current       bool                    `json:"current_selected_revision"`
	Evaluation    ac.DesignSystemResponse `json:"evaluation"`
}

type designSystemAssetResponse struct {
	Data            []byte
	ContentType     string
	ETag            string
	Revision        string
	InventoryDigest string
}

type designSystemGitReader interface {
	ReadDesignSystem(context.Context, string, string, string, string, string, string, string) (ac.DesignSystemResponse, []byte, error)
}

type resolvedDesignSystemSource struct {
	RepositoryID string
	Revision     string
	Branch       string
	Level        string
	Inventory    string
	Selection    DesignSystemSelection
}

func (m *MongoStore) resolveDesignSystemSource(ctx context.Context, companyID, projectID, requestedSHA string) (resolvedDesignSystemSource, error) {
	if !validID(companyID) || !validID(projectID) || requestedSHA != "" && !validFullGitSHA(requestedSHA) {
		return resolvedDesignSystemSource{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return resolvedDesignSystemSource{}, err
	}
	var stored projectDocument
	err = m.client.Database(company.DatabaseName).Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&stored)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return resolvedDesignSystemSource{}, ErrNotFound
	}
	if err != nil {
		return resolvedDesignSystemSource{}, normalizeMongoError(err)
	}
	if stored.CompanyID != companyID || stored.ObjectID.IsZero() {
		return resolvedDesignSystemSource{}, ErrNotFound
	}
	state, err := m.ReadDesignSystemState(ctx, companyID, projectID)
	if err != nil {
		return resolvedDesignSystemSource{}, err
	}
	selection := state.Selection
	bound := stored.Binding != nil && stored.Binding.RepositoryID != "" && stored.Binding.PublicationBranch != ""
	if !designSystemSelectionMatchesBinding(selection, stored.BindingRevision, stored.Binding) {
		return resolvedDesignSystemSource{}, todoTeach("selection", "reload the Project binding after a concurrent repository change", ErrConflict)
	}
	if selection.State == "selected" {
		if !bound || !validBinding(*stored.Binding) || !validFullGitSHA(selection.SHA) || !validSourceDigest(selection.InventoryDigest) {
			return resolvedDesignSystemSource{}, ErrUnavailable
		}
		if requestedSHA != "" && requestedSHA != selection.SHA {
			return resolvedDesignSystemSource{}, todoTeach("revision", "read only the currently selected exact Design System SHA", ErrConflict)
		}
		return resolvedDesignSystemSource{RepositoryID: stored.Binding.RepositoryID, Revision: selection.SHA, Branch: stored.Binding.PublicationBranch, Level: "project", Inventory: selection.InventoryDigest, Selection: selection}, nil
	}
	if m.designSystemSourceMapRoot == "" || stored.Binding == nil {
		return resolvedDesignSystemSource{}, todoTeach("source_bindings", "be available from the private pinned Design System source map", ErrUnavailable)
	}
	bindings, err := (fileDesignSystemSourceMapProvider{root: m.designSystemSourceMapRoot}).Read(ctx, projectID, companyID, *stored.Binding)
	if err != nil {
		return resolvedDesignSystemSource{}, todoTeach("source_bindings", "be available and valid in the private pinned Design System source map", ErrUnavailable)
	}
	// Once a Project repository has ever been bound, its private Foundation slot
	// is masked before validation or remote access. Only never-bound Projects may
	// select that slot; unbind and delete deliberately inherit Company/default.
	if selection.State != "never_bound" {
		bindings.Sources.Project = nil
	}
	if len(ac.ValidateSourceBindingsFor(bindings, "committed", projectID, companyID)) != 0 {
		return resolvedDesignSystemSource{}, todoTeach("source_bindings", "contain only valid pinned source entries after applying Project masking", ErrUnavailable)
	}
	level, source := ac.SelectSourceBinding(bindings)
	if source == nil || source.Revision == nil || !validFullGitSHA(*source.Revision) || !validGitRepositoryIdentity(source.RepositoryID) {
		return resolvedDesignSystemSource{}, todoTeach("design_system_source", "no exact pinned Design System source is available at the selected precedence level", ErrUnavailable)
	}
	if requestedSHA != "" && requestedSHA != *source.Revision {
		return resolvedDesignSystemSource{}, todoTeach("revision", "read only the exact SHA pinned for the selected Design System source", ErrConflict)
	}
	return resolvedDesignSystemSource{RepositoryID: source.RepositoryID, Revision: *source.Revision, Level: level, Selection: selection}, nil
}

func (m *MongoStore) ReadDesignSystem(ctx context.Context, companyID, projectID, requestedSHA string) (DesignSystemReadResponse, error) {
	resolved, err := m.resolveDesignSystemSource(ctx, companyID, projectID, requestedSHA)
	if err != nil {
		return DesignSystemReadResponse{}, err
	}
	evaluation, _, err := m.readResolvedDesignSystem(ctx, projectID, companyID, resolved, "")
	if err != nil {
		return DesignSystemReadResponse{}, err
	}
	if err := m.confirmDesignSystemSelection(ctx, companyID, projectID, resolved.Selection); err != nil {
		return DesignSystemReadResponse{}, err
	}
	return DesignSystemReadResponse{Selection: resolved.Selection, Evaluation: evaluation}, nil
}

func (m *MongoStore) ListDesignSystemHistory(ctx context.Context, companyID, projectID string, limit int) (DesignSystemHistoryResponse, error) {
	selection, binding, err := m.loadBoundDesignSystemHistory(ctx, companyID, projectID)
	if err != nil {
		return DesignSystemHistoryResponse{}, err
	}
	reader, ok := m.designSystemVerifier.(designSystemGitHistoryReader)
	if !ok {
		return DesignSystemHistoryResponse{}, ErrUnavailable
	}
	page, err := reader.ListDesignSystemHistory(ctx, projectID, companyID, binding.RepositoryID, binding.PublicationBranch, limit)
	if err != nil {
		return DesignSystemHistoryResponse{}, err
	}
	if err := m.confirmDesignSystemSelection(ctx, companyID, projectID, selection); err != nil {
		return DesignSystemHistoryResponse{}, err
	}
	page.Selection = selection
	for index := range page.Revisions {
		page.Revisions[index].Current = selection.State == "selected" && page.Revisions[index].SHA == selection.SHA
	}
	return page, nil
}

func (m *MongoStore) ReadDesignSystemAsOf(ctx context.Context, companyID, projectID, revision string) (DesignSystemAsOfResponse, error) {
	if !validFullGitSHA(revision) {
		return DesignSystemAsOfResponse{}, todoTeach("revision", "be an exact full Git commit SHA", ErrInvalid)
	}
	selection, binding, err := m.loadBoundDesignSystemHistory(ctx, companyID, projectID)
	if err != nil {
		return DesignSystemAsOfResponse{}, err
	}
	reader, ok := m.designSystemVerifier.(designSystemGitHistoryReader)
	if !ok {
		return DesignSystemAsOfResponse{}, ErrUnavailable
	}
	evaluation, head, err := reader.ReadDesignSystemAsOf(ctx, projectID, companyID, binding.RepositoryID, binding.PublicationBranch, revision)
	if err != nil {
		return DesignSystemAsOfResponse{}, err
	}
	if err := m.confirmDesignSystemSelection(ctx, companyID, projectID, selection); err != nil {
		return DesignSystemAsOfResponse{}, err
	}
	return DesignSystemAsOfResponse{Selection: selection, Revision: revision, BranchHeadSHA: head,
		Current: selection.State == "selected" && selection.RepositoryID == binding.RepositoryID && selection.SHA == revision, Evaluation: evaluation}, nil
}

func (m *MongoStore) loadBoundDesignSystemHistory(ctx context.Context, companyID, projectID string) (DesignSystemSelection, Binding, error) {
	if !validID(companyID) || !validID(projectID) {
		return DesignSystemSelection{}, Binding{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return DesignSystemSelection{}, Binding{}, err
	}
	var stored projectDocument
	if err := m.client.Database(company.DatabaseName).Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return DesignSystemSelection{}, Binding{}, ErrNotFound
		}
		return DesignSystemSelection{}, Binding{}, normalizeMongoError(err)
	}
	if stored.CompanyID != companyID || stored.ObjectID.IsZero() {
		return DesignSystemSelection{}, Binding{}, ErrNotFound
	}
	state, err := m.ReadDesignSystemState(ctx, companyID, projectID)
	if err != nil {
		return DesignSystemSelection{}, Binding{}, err
	}
	if !designSystemSelectionMatchesBinding(state.Selection, stored.BindingRevision, stored.Binding) {
		return DesignSystemSelection{}, Binding{}, todoTeach("selection", "reload the Project binding after a concurrent repository change", ErrConflict)
	}
	if state.PendingOperation != nil {
		return DesignSystemSelection{}, Binding{}, todoTeach("pending_operation", "be recovered before listing or reading branch history", ErrConflict)
	}
	if stored.Binding == nil || !validBinding(*stored.Binding) || stored.Binding.RepositoryID == "" || stored.Binding.PublicationBranch == "" {
		return DesignSystemSelection{}, Binding{}, todoTeach("repository_binding", "be configured before reading Project Git history; Company/default pins do not imply a branch", ErrUnavailable)
	}
	return state.Selection, *stored.Binding, nil
}

func designSystemSelectionMatchesBinding(selection DesignSystemSelection, bindingRevision int64, binding *Binding) bool {
	if selection.BindingRevision != bindingRevision {
		return false
	}
	bound := binding != nil && binding.RepositoryID != "" && binding.PublicationBranch != ""
	if !bound {
		return selection.RepositoryID == "" && selection.PublicationBranch == ""
	}
	return selection.RepositoryID == binding.RepositoryID && selection.PublicationBranch == binding.PublicationBranch
}

func (m *MongoStore) ReadDesignSystemAsset(ctx context.Context, companyID, projectID, requestedSHA, assetPath string) (designSystemAssetResponse, error) {
	if ac.ValidateRelativePath(assetPath) != nil || len([]byte(assetPath)) > ac.MaxPathBytes {
		return designSystemAssetResponse{}, todoTeach("asset_path", "use a safe relative path declared in the selected Design System", ErrInvalid)
	}
	resolved, err := m.resolveDesignSystemSource(ctx, companyID, projectID, requestedSHA)
	if err != nil {
		return designSystemAssetResponse{}, err
	}
	if err := m.confirmDesignSystemSelection(ctx, companyID, projectID, resolved.Selection); err != nil {
		return designSystemAssetResponse{}, err
	}
	evaluation, data, err := m.readResolvedDesignSystem(ctx, projectID, companyID, resolved, assetPath)
	if err != nil {
		return designSystemAssetResponse{}, err
	}
	if err := m.confirmDesignSystemSelection(ctx, companyID, projectID, resolved.Selection); err != nil {
		return designSystemAssetResponse{}, err
	}
	if evaluation.InventoryDigest == nil {
		return designSystemAssetResponse{}, ErrUnavailable
	}
	sum := sha256.Sum256(data)
	contentType := mime.TypeByExtension(filepath.Ext(assetPath))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return designSystemAssetResponse{Data: data, ContentType: contentType, ETag: fmt.Sprintf("\"%s-%x\"", resolved.Revision, sum[:]), Revision: resolved.Revision, InventoryDigest: *evaluation.InventoryDigest}, nil
}

func (m *MongoStore) confirmDesignSystemSelection(ctx context.Context, companyID, projectID string, expected DesignSystemSelection) error {
	current, err := m.ReadDesignSystemState(ctx, companyID, projectID)
	if err != nil {
		return err
	}
	selected := current.Selection
	if selected.State != expected.State || selected.RepositoryID != expected.RepositoryID || selected.PublicationBranch != expected.PublicationBranch ||
		selected.BindingRevision != expected.BindingRevision || selected.SHA != expected.SHA || selected.InventoryDigest != expected.InventoryDigest {
		return todoTeach("selection", "the Project Design System selection changed during the exact-SHA read; reload and retry", ErrConflict)
	}
	return nil
}

func (m *MongoStore) readResolvedDesignSystem(ctx context.Context, projectID, companyID string, resolved resolvedDesignSystemSource, assetPath string) (ac.DesignSystemResponse, []byte, error) {
	if m.designSystemVerifier == nil {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	reader, ok := m.designSystemVerifier.(designSystemGitReader)
	if !ok {
		return ac.DesignSystemResponse{}, nil, ErrUnavailable
	}
	evaluation, asset, err := reader.ReadDesignSystem(ctx, projectID, companyID, resolved.RepositoryID, resolved.Revision, resolved.Branch, resolved.Level, assetPath)
	if err != nil {
		if assetPath != "" && errors.Is(err, ErrNotFound) {
			return ac.DesignSystemResponse{}, nil, todoTeach("asset_path", "request only an asset registered by the selected Design System", ErrNotFound)
		}
		return ac.DesignSystemResponse{}, nil, err
	}
	if evaluation.Outcome != "go" || evaluation.DesignSystemValidation != "valid" || len(evaluation.Items) != 1 || evaluation.InventoryDigest == nil || resolved.Inventory != "" && *evaluation.InventoryDigest != resolved.Inventory {
		return ac.DesignSystemResponse{}, nil, todoTeach("design_system_source", "the exact selected Design System revision is unavailable or fails shared structural validation", ErrUnavailable)
	}
	if assetPath != "" {
		listed := false
		for _, item := range evaluation.Items {
			for _, asset := range item.Assets {
				if asset == assetPath {
					listed = true
				}
			}
		}
		if !listed {
			return ac.DesignSystemResponse{}, nil, todoTeach("asset_path", "request only an asset registered by the selected Design System", ErrNotFound)
		}
	}
	return evaluation, asset, nil
}

func writeDesignSystemAsset(w http.ResponseWriter, r *http.Request, asset designSystemAssetResponse) {
	w.Header().Set("Content-Type", asset.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(len(asset.Data)))
	w.Header().Set("ETag", asset.ETag)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("X-Design-System-Revision", asset.Revision)
	w.Header().Set("X-Design-System-Inventory-Digest", asset.InventoryDigest)
	if r.Header.Get("If-None-Match") == asset.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(asset.Data)
}
