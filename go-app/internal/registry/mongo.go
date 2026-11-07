package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	defaultControlDatabase     = "builder_control"
	companiesCollection        = "companies"
	lifecycleControlCollection = "lifecycle_control"
	preferencesCollection      = "registration_preferences"
	projectsCollection         = "projects"
	epicsCollection            = "roadmap_epics"
	releasesCollection         = "roadmap_releases"
	operationTimeout           = 15 * time.Second
	registrySchema             = "builder-project-registry-v1"
)

var mongoDatabaseName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)

type MongoStore struct {
	client                    *mongo.Client
	control                   *mongo.Database
	beforeProjectWrite        func() error
	beforeSnapshotWrite       func() error
	tenantPrefix              string
	proofReaderURL            string
	proofReaderToken          string
	proofHTTPClient           *http.Client
	localMatrix               *localProofMatrix
	designSystemVerifier      designSystemRemoteVerifier
	designSystemSourceMapRoot string
}

func (m *MongoStore) ConfigureDesignSystemSourceMapRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("%w: private Design System source-map root must be absolute", ErrInvalid)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: private Design System source-map root is unavailable", ErrInvalid)
	}
	m.designSystemSourceMapRoot = filepath.Clean(root)
	return nil
}

func (m *MongoStore) ConfigureProofReader(readerURL, token string) error {
	parsed, err := url.Parse(readerURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(token) < 32 {
		return fmt.Errorf("%w: private proof Reader URL/token is invalid", ErrInvalid)
	}
	m.proofReaderURL = strings.TrimRight(parsed.String(), "/")
	m.proofReaderToken = token
	m.proofHTTPClient = &http.Client{Timeout: 20 * time.Second}
	return nil
}

const maxSnapshotReferenceBytes = 128 << 10

type projectDocument struct {
	ObjectID                    bson.ObjectID               `bson:"_id,omitempty"`
	ID                          string                      `bson:"id"`
	Name                        string                      `bson:"name"`
	CompanyID                   string                      `bson:"company_id"`
	RoadmapRevision             int64                       `bson:"roadmap_revision,omitempty"`
	RoadmapLastOperation        *roadmapOperation           `bson:"roadmap_last_operation,omitempty"`
	Binding                     *Binding                    `bson:"binding,omitempty"`
	BindingRevision             int64                       `bson:"binding_revision,omitempty"`
	BindingOperation            string                      `bson:"binding_operation,omitempty"`
	BindingActor                string                      `bson:"binding_actor,omitempty"`
	BindingRequestDigest        string                      `bson:"binding_request_digest,omitempty"`
	PendingDesignSystemMutation *DesignSystemMutationIntent `bson:"pending_design_system_mutation,omitempty"`
	DesignSystemState           string                      `bson:"design_system_state,omitempty"`
	DesignSystemSHA             string                      `bson:"design_system_sha,omitempty"`
	DesignSystemInventoryDigest string                      `bson:"design_system_inventory_digest,omitempty"`
	DesignSystemLastOperation   *DesignSystemMutationResult `bson:"design_system_last_operation,omitempty"`
	OverviewRevision            int64                       `bson:"overview_revision,omitempty"`
	OverviewOperation           string                      `bson:"overview_operation,omitempty"`
	OverviewDigest              string                      `bson:"overview_digest,omitempty"`
	RegisteredAt                time.Time                   `bson:"registered_at"`
	PreparationState            string                      `bson:"preparation_state"`
	PreparationDiagnostic       string                      `bson:"preparation_diagnostic,omitempty"`
	SnapshotFingerprint         string                      `bson:"snapshot_fingerprint,omitempty"`
	KnowledgeGenerationID       string                      `bson:"knowledge_generation_id,omitempty"`
}

type preferenceDocument struct {
	ObjectID   bson.ObjectID `bson:"_id,omitempty"`
	CompanyID  string        `bson:"company_id"`
	ProjectID  string        `bson:"project_id"`
	Binding    Binding       `bson:"binding"`
	DeclinedAt time.Time     `bson:"declined_at"`
}

func (p preferenceDocument) toAPI() Preference {
	declinedAt := ""
	if !p.DeclinedAt.IsZero() {
		declinedAt = time.UnixMilli(p.DeclinedAt.UnixMilli()).UTC().Format(time.RFC3339Nano)
	}
	return Preference{CompanyID: p.CompanyID, ProjectID: p.ProjectID, Binding: p.Binding, DeclinedAt: declinedAt}
}

func preferencesToDocuments(preferences []Preference) ([]preferenceDocument, error) {
	result := make([]preferenceDocument, 0, len(preferences))
	for _, preference := range preferences {
		declinedAt, err := time.Parse(time.RFC3339Nano, preference.DeclinedAt)
		if err != nil || !validID(preference.CompanyID) || !validID(preference.ProjectID) || preference.Binding != (Binding{}) && !validBinding(preference.Binding) {
			return nil, ErrInvalid
		}
		result = append(result, preferenceDocument{ObjectID: bson.NewObjectID(), CompanyID: preference.CompanyID, ProjectID: preference.ProjectID, Binding: preference.Binding, DeclinedAt: time.UnixMilli(declinedAt.UnixMilli()).UTC()})
	}
	return result, nil
}

func projectToDocument(project Project, objectID bson.ObjectID) (projectDocument, error) {
	registeredAt, err := time.Parse(time.RFC3339Nano, project.RegisteredAt)
	if err != nil {
		return projectDocument{}, ErrInvalid
	}
	document := projectDocument{
		ObjectID: objectID, ID: project.ID, Name: project.Name, CompanyID: project.CompanyID,
		RoadmapRevision: project.RoadmapRevision, BindingRevision: project.BindingRevision,
		RegisteredAt: time.UnixMilli(registeredAt.UnixMilli()).UTC(), PreparationState: project.PreparationState,
		PreparationDiagnostic: project.PreparationDiagnostic, SnapshotFingerprint: project.SnapshotFingerprint,
		KnowledgeGenerationID: project.KnowledgeGenerationID,
	}
	if project.Binding != nil && !validBinding(*project.Binding) {
		return projectDocument{}, ErrInvalid
	}
	document.Binding = project.Binding
	encoded, err := bson.Marshal(document)
	if err != nil || len(encoded) > maxSnapshotReferenceBytes {
		return projectDocument{}, ErrInvalid
	}
	return document, nil
}

func (p projectDocument) toAPI() Project {
	return Project{
		ID: p.ID, Name: p.Name, CompanyID: p.CompanyID,
		RoadmapRevision:  p.RoadmapRevision,
		Binding:          p.Binding,
		BindingRevision:  p.BindingRevision,
		RegisteredAt:     time.UnixMilli(p.RegisteredAt.UnixMilli()).UTC().Format(time.RFC3339Nano),
		PreparationState: p.PreparationState, PreparationDiagnostic: p.PreparationDiagnostic,
		SnapshotFingerprint: p.SnapshotFingerprint, KnowledgeGenerationID: p.KnowledgeGenerationID,
	}
}

func canonicalStoredProject(project Project) (Project, error) {
	registeredAt, err := time.Parse(time.RFC3339Nano, project.RegisteredAt)
	if err != nil {
		return Project{}, ErrInvalid
	}
	project.RegisteredAt = time.UnixMilli(registeredAt.UnixMilli()).UTC().Format(time.RFC3339Nano)
	return project, nil
}

func validateProjectDocument(project Project) error {
	_, err := projectToDocument(project, bson.NilObjectID)
	return err
}

func (m *MongoStore) tenantDatabase(companyID string) string {
	if m.tenantPrefix == "" {
		return tenantDatabase(companyID)
	}
	return tenantDatabase(m.tenantPrefix + "-" + companyID)
}

func normalizeCompany(company Company) (Company, error) {
	if !validID(company.ID) || !validName(company.Name) {
		return Company{}, ErrInvalid
	}
	seen := make(map[string]bool)
	company.DomainKeys = nil
	company.AppKeys = nil
	for i, domain := range company.Domains {
		key, err := tenantKey("host", domain)
		if err != nil || seen[key] {
			return Company{}, ErrInvalid
		}
		seen[key] = true
		company.Domains[i] = strings.TrimPrefix(key, "host:")
		company.DomainKeys = append(company.DomainKeys, key)
	}
	for i, app := range company.AppIDs {
		key, err := tenantKey(app.Platform, app.Value)
		if err != nil || seen[key] {
			return Company{}, ErrInvalid
		}
		seen[key] = true
		company.AppIDs[i].Value = strings.TrimPrefix(key, app.Platform+":")
		company.AppKeys = append(company.AppKeys, key)
	}
	sort.Strings(company.DomainKeys)
	sort.Strings(company.AppKeys)
	return company, nil
}

func OpenMongo(ctx context.Context, uri, controlDatabase string) (*MongoStore, error) {
	return openMongo(ctx, uri, controlDatabase)
}

func openMongo(ctx context.Context, uri, controlDatabase string) (*MongoStore, error) {
	uri = strings.TrimSpace(uri)
	parsed, err := url.Parse(uri)
	if err != nil || (parsed.Scheme != "mongodb" && parsed.Scheme != "mongodb+srv") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: MongoDB URI is invalid", ErrInvalid)
	}
	if controlDatabase == "" {
		controlDatabase = defaultControlDatabase
	}
	if !mongoDatabaseName.MatchString(controlDatabase) {
		return nil, fmt.Errorf("%w: control database name is invalid", ErrInvalid)
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(operationTimeout))
	if err != nil {
		return nil, ErrUnavailable
	}
	pingCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, ErrUnavailable
	}
	store := &MongoStore{client: client, control: client.Database(controlDatabase), designSystemVerifier: newGitDesignSystemVerifier()}
	if err := store.ensureControlIndexes(pingCtx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, ErrUnavailable
	}
	return store, nil
}

func (m *MongoStore) Close(ctx context.Context) error { return m.client.Disconnect(ctx) }

func (m *MongoStore) Catalog(ctx context.Context) (Catalog, error) {
	companies, err := m.readCompanies(ctx)
	if err != nil {
		return Catalog{}, ErrUnavailable
	}
	publicCompanies := make([]Company, len(companies))
	for i, company := range companies {
		publicCompanies[i] = sanitizeCompany(company)
	}
	catalog := Catalog{SchemaVersion: "1", Companies: publicCompanies, Projects: []Project{}, Preferences: []Preference{}}
	for _, company := range companies {
		database := m.client.Database(company.DatabaseName)
		projects, err := database.Collection(projectsCollection).Find(ctx, bson.D{})
		if err != nil {
			return Catalog{}, ErrUnavailable
		}
		for projects.Next(ctx) {
			var record projectDocument
			if projects.Decode(&record) != nil || record.ObjectID.IsZero() || record.CompanyID != company.ID || record.Binding != nil && !validBinding(*record.Binding) {
				projects.Close(ctx)
				return Catalog{}, ErrUnavailable
			}
			catalog.Projects = append(catalog.Projects, record.toAPI())
		}
		if projects.Err() != nil {
			projects.Close(ctx)
			return Catalog{}, ErrUnavailable
		}
		projects.Close(ctx)
		preferences, err := database.Collection(preferencesCollection).Find(ctx, bson.D{})
		if err != nil {
			return Catalog{}, ErrUnavailable
		}
		for preferences.Next(ctx) {
			var record preferenceDocument
			if preferences.Decode(&record) != nil || record.ObjectID.IsZero() || record.CompanyID != company.ID {
				preferences.Close(ctx)
				return Catalog{}, ErrUnavailable
			}
			catalog.Preferences = append(catalog.Preferences, record.toAPI())
		}
		if preferences.Err() != nil {
			preferences.Close(ctx)
			return Catalog{}, ErrUnavailable
		}
		preferences.Close(ctx)
	}
	sort.Slice(catalog.Projects, func(i, j int) bool {
		if catalog.Projects[i].CompanyID != catalog.Projects[j].CompanyID {
			return catalog.Projects[i].CompanyID < catalog.Projects[j].CompanyID
		}
		return catalog.Projects[i].ID < catalog.Projects[j].ID
	})
	sort.Slice(catalog.Preferences, func(i, j int) bool {
		if catalog.Preferences[i].CompanyID != catalog.Preferences[j].CompanyID {
			return catalog.Preferences[i].CompanyID < catalog.Preferences[j].CompanyID
		}
		return catalog.Preferences[i].ProjectID < catalog.Preferences[j].ProjectID
	})
	registered := make(map[string]bool, len(catalog.Projects))
	for _, project := range catalog.Projects {
		registered[project.CompanyID+"/"+project.ID] = true
	}
	for _, preference := range catalog.Preferences {
		if registered[preference.CompanyID+"/"+preference.ProjectID] {
			return Catalog{}, ErrUnavailable
		}
	}
	return catalog, nil
}

func (m *MongoStore) Project(ctx context.Context, companyID, id string) (Project, error) {
	if !validID(companyID) || !validID(id) {
		return Project{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return Project{}, err
	}
	project, err := m.readProject(ctx, company, id)
	if err != nil {
		return Project{}, err
	}
	return project, nil
}

func (m *MongoStore) Register(ctx context.Context, input Registration) (Project, error) {
	if !validID(input.CompanyID) || !validID(input.ProjectID) || !validName(input.CompanyName) || !validName(input.ProjectName) {
		return Project{}, ErrInvalid
	}
	if input.Binding != (Binding{}) {
		return Project{}, todoTeach("binding", "register Project identity without repository binding; configure it through the compare-and-set repository binding API", ErrInvalid)
	}
	company := Company{ID: input.CompanyID, Name: input.CompanyName, DatabaseName: m.tenantDatabase(input.CompanyID)}
	company, err := normalizeCompany(company)
	if err != nil {
		return Project{}, err
	}
	project := Project{ID: input.ProjectID, Name: input.ProjectName, CompanyID: input.CompanyID, RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano), PreparationState: "not_ready"}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := m.ensureTenantIndexes(ctx, company.DatabaseName); err != nil {
		return Project{}, ErrUnavailable
	}
	companies := m.control.Collection(companiesCollection)
	var existing Company
	companyErr := companies.FindOne(ctx, bson.D{{Key: "id", Value: company.ID}}).Decode(&existing)
	if companyErr == nil {
		if existing.Name != company.Name || existing.DatabaseName != company.DatabaseName {
			return Project{}, ErrConflict
		}
	} else if !errors.Is(companyErr, mongo.ErrNoDocuments) {
		return Project{}, ErrUnavailable
	} else if _, err := companies.InsertOne(ctx, company); err != nil {
		if !errors.Is(normalizeMongoError(err), ErrConflict) {
			return Project{}, normalizeMongoError(err)
		}
		current, readErr := m.company(ctx, company.ID)
		if readErr != nil || current.Name != company.Name {
			return Project{}, ErrConflict
		}
	}
	prior, readErr := m.readProject(ctx, company, project.ID)
	if readErr == nil {
		if prior.Name != project.Name {
			return Project{}, ErrConflict
		}
		if err := m.clearPreference(ctx, company, project.ID); err != nil {
			return Project{}, err
		}
		return prior, nil
	}
	if !errors.Is(readErr, ErrNotFound) {
		return Project{}, readErr
	}
	if err := m.writeProject(ctx, company, project); err != nil {
		if !errors.Is(normalizeMongoError(err), ErrConflict) {
			return Project{}, normalizeMongoError(err)
		}
		current, readErr := m.readProject(ctx, company, project.ID)
		if readErr != nil || current.Name != project.Name {
			return Project{}, ErrConflict
		}
		if err := m.clearPreference(ctx, company, project.ID); err != nil {
			return Project{}, err
		}
		return current, nil
	}
	if err := m.clearPreference(ctx, company, project.ID); err != nil {
		return Project{}, err
	}
	return m.readProject(ctx, company, project.ID)
}

func sameOptionalBinding(a, b *Binding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// BindFirst changes only a newly registered Genesis Project. A retry of the
// exact operation is safe; binding correction has a separate authority.
func (m *MongoStore) BindFirst(ctx context.Context, companyID, id string, input FirstBindingRequest) (Project, error) {
	if !validID(companyID) || !validID(id) || input.ProjectID != id || input.ExpectedRevision != 0 ||
		!validID(input.OperationID) || !validBinding(input.Binding) {
		return Project{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return Project{}, err
	}
	var prior projectDocument
	collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
	if err := collection.FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&prior); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Project{}, ErrNotFound
		}
		return Project{}, ErrUnavailable
	}
	if prior.CompanyID != companyID || prior.ObjectID.IsZero() || prior.Binding != nil && !validBinding(*prior.Binding) {
		return Project{}, ErrUnavailable
	}
	if prior.Binding != nil {
		if prior.BindingOperation == input.OperationID && prior.BindingRevision == 1 && *prior.Binding == input.Binding {
			return prior.toAPI(), nil
		}
		return Project{}, ErrConflict
	}
	filter := bson.D{{Key: "_id", Value: prior.ObjectID}, {Key: "company_id", Value: companyID},
		{Key: "binding", Value: bson.M{"$eq": nil}},
		{Key: "binding_revision", Value: bson.M{"$in": []any{nil, int64(0)}}}}
	update := bson.M{"$set": bson.M{"binding": input.Binding, "binding_revision": int64(1),
		"binding_operation": input.OperationID, "preparation_state": "not_ready", "snapshot_fingerprint": ""}}
	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return Project{}, normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		current, readErr := m.readProject(ctx, company, id)
		if readErr == nil && current.Binding != nil && current.BindingRevision == 1 && *current.Binding == input.Binding {
			var stored projectDocument
			if collection.FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&stored) == nil && stored.BindingOperation == input.OperationID {
				return current, nil
			}
		}
		return Project{}, ErrConflict
	}
	return m.readProject(ctx, company, id)
}

func repositoryBindingFromProject(project Project) RepositoryBinding {
	if project.Binding == nil || project.Binding.RepositoryID == "" || project.Binding.PublicationBranch == "" {
		return RepositoryBinding{State: "absent", Revision: project.BindingRevision}
	}
	return RepositoryBinding{RepositoryID: project.Binding.RepositoryID, PublicationBranch: project.Binding.PublicationBranch, State: "configured", Revision: project.BindingRevision}
}

func (m *MongoStore) ReadRepositoryBinding(ctx context.Context, companyID, id string) (RepositoryBinding, error) {
	project, err := m.Project(ctx, companyID, id)
	if err != nil {
		return RepositoryBinding{}, err
	}
	return repositoryBindingFromProject(project), nil
}

func (m *MongoStore) ChangeRepositoryBinding(ctx context.Context, companyID, id string, input RepositoryBindingRequest) (RepositoryBinding, error) {
	if !validID(companyID) || !validID(id) || input.ProjectID != id || input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 || !validID(input.OperationID) || !validName(input.Actor) {
		return RepositoryBinding{}, todoTeach("identity/action", "provide the exact Project identity, attributable actor, stable operation ID and current nonnegative binding revision", ErrInvalid)
	}
	if input.Action != "bind" && input.Action != "replace" && input.Action != "unbind" {
		return RepositoryBinding{}, todoTeach("action", "use bind, replace or unbind", ErrInvalid)
	}
	if input.Action == "unbind" {
		if input.RepositoryID != "" || input.Branch != "" {
			return RepositoryBinding{}, todoTeach("repository_id/publication_branch", "omit repository fields when unbinding", ErrInvalid)
		}
	} else if !validGitRepositoryIdentity(input.RepositoryID) || !validGitBranch(input.Branch) {
		return RepositoryBinding{}, todoTeach("repository_id/publication_branch", "provide a valid owner/repository and Git-ref-safe publication branch", ErrInvalid)
	}
	requestBytes, _ := json.Marshal(input)
	digestBytes := sha256.Sum256(requestBytes)
	requestDigest := hex.EncodeToString(digestBytes[:])
	company, err := m.company(ctx, companyID)
	if err != nil {
		return RepositoryBinding{}, err
	}
	collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
	var stored projectDocument
	if err := collection.FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return RepositoryBinding{}, ErrNotFound
		}
		return RepositoryBinding{}, ErrUnavailable
	}
	if stored.CompanyID != companyID || stored.ObjectID.IsZero() || stored.Binding != nil && !validBinding(*stored.Binding) {
		return RepositoryBinding{}, ErrUnavailable
	}
	if stored.BindingOperation == input.OperationID {
		if stored.BindingRequestDigest == requestDigest {
			return repositoryBindingFromProject(stored.toAPI()), nil
		}
		return RepositoryBinding{}, todoTeach("operation_id", "choose a new ID or retry the exact same repository binding request", ErrConflict)
	}
	if stored.PendingDesignSystemMutation != nil {
		return RepositoryBinding{}, todoTeach("pending_operation", "resolve the pending Project Design System Git operation before changing the shared repository binding", ErrConflict)
	}
	if stored.BindingRevision != input.ExpectedRevision {
		return RepositoryBinding{}, todoTeach("expected_revision", "reload the current repository binding and retry with its revision", ErrConflict)
	}
	currentlyBound := stored.Binding != nil && stored.Binding.RepositoryID != "" && stored.Binding.PublicationBranch != ""
	if input.Action == "bind" && currentlyBound || input.Action == "replace" && !currentlyBound || input.Action == "unbind" && !currentlyBound {
		field, expected := "action", "use bind only when absent, replace only when configured, or unbind only when configured"
		if input.Action == "bind" {
			field, expected = "repository_id/publication_branch", "use replace for an already configured Project repository"
		}
		if input.Action == "replace" || input.Action == "unbind" {
			field, expected = "repository_binding", "reload state; replace and unbind require a configured repository"
		}
		return RepositoryBinding{}, todoTeach(field, expected, ErrConflict)
	}
	updated := Binding{}
	if stored.Binding != nil {
		updated = *stored.Binding
		updated.LegacyFoundationSourceDisabled = updated.LegacyFoundationSourceDisabled || updated.FoundationRoot != ""
	}
	if input.Action == "unbind" {
		updated.RepositoryID = ""
		updated.PublicationBranch = ""
	} else {
		updated.RepositoryID = input.RepositoryID
		updated.PublicationBranch = input.Branch
	}
	var nextBinding *Binding
	if updated != (Binding{}) {
		nextBinding = &updated
	}
	nextRevision := stored.BindingRevision + 1
	filter := bson.D{{Key: "_id", Value: stored.ObjectID}, {Key: "company_id", Value: companyID}}
	if stored.BindingRevision == 0 {
		filter = append(filter, bson.E{Key: "binding_revision", Value: bson.M{"$in": []any{nil, int64(0)}}})
	} else {
		filter = append(filter, bson.E{Key: "binding_revision", Value: stored.BindingRevision})
	}
	if stored.Binding == nil {
		filter = append(filter, bson.E{Key: "binding", Value: bson.M{"$in": []any{nil, bson.M{}}}})
	} else {
		filter = append(filter, bson.E{Key: "binding", Value: *stored.Binding})
	}
	filter = append(filter, bson.E{Key: "pending_design_system_mutation", Value: bson.M{"$exists": false}})
	set := bson.M{"binding_revision": nextRevision, "binding_operation": input.OperationID, "binding_actor": input.Actor, "binding_request_digest": requestDigest, "preparation_state": "not_ready", "preparation_diagnostic": "", "snapshot_fingerprint": ""}
	if input.Action == "unbind" {
		set["design_system_state"] = "unbound"
	} else {
		set["design_system_state"] = "unselected"
	}
	set["design_system_sha"] = ""
	set["design_system_inventory_digest"] = ""
	update := bson.M{"$set": set}
	if nextBinding != nil {
		set["binding"] = *nextBinding
	} else {
		update["$unset"] = bson.M{"binding": ""}
	}
	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return RepositoryBinding{}, normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		var latest projectDocument
		if readErr := collection.FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&latest); readErr == nil {
			if latest.CompanyID == companyID && latest.BindingOperation == input.OperationID && latest.BindingRequestDigest == requestDigest {
				return repositoryBindingFromProject(latest.toAPI()), nil
			}
			if latest.CompanyID == companyID && latest.PendingDesignSystemMutation != nil {
				return RepositoryBinding{}, todoTeach("pending_operation", "resolve the pending Project Design System Git operation before changing the shared repository binding", ErrConflict)
			}
		} else if !errors.Is(readErr, mongo.ErrNoDocuments) {
			return RepositoryBinding{}, normalizeMongoError(readErr)
		}
		return RepositoryBinding{}, todoTeach("expected_revision", "reload the current repository binding after a concurrent change and retry", ErrConflict)
	}
	return RepositoryBinding{RepositoryID: updated.RepositoryID, PublicationBranch: updated.PublicationBranch, State: repositoryBindingState(updated), Revision: nextRevision}, nil
}

func repositoryBindingState(binding Binding) string {
	if binding.RepositoryID != "" && binding.PublicationBranch != "" {
		return "configured"
	}
	return "absent"
}

func validGitRepositoryIdentity(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".git") {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
				return false
			}
		}
	}
	return true
}

func validGitBranch(value string) bool {
	if value == "" || len(value) > 255 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return branchPattern.MatchString(value)
}

func (m *MongoStore) clearPreference(ctx context.Context, company Company, projectID string) error {
	if _, err := m.client.Database(company.DatabaseName).Collection(preferencesCollection).DeleteOne(ctx, bson.D{{Key: "project_id", Value: projectID}}); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (m *MongoStore) Decline(ctx context.Context, companyID, id string, binding Binding) error {
	if !validID(companyID) || !validID(id) || binding != (Binding{}) && !validBinding(binding) {
		return ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return err
	}
	_, err = m.readProject(ctx, company, id)
	if err == nil {
		return ErrConflict
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	_, err = m.client.Database(company.DatabaseName).Collection(preferencesCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: id}}, bson.M{
		"$set":         bson.M{"binding": binding, "declined_at": time.Now().UTC()},
		"$setOnInsert": bson.M{"_id": bson.NewObjectID(), "company_id": companyID, "project_id": id},
	}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return normalizeMongoError(err)
	}
	if _, err := m.readProject(ctx, company, id); err == nil {
		if clearErr := m.clearPreference(ctx, company, id); clearErr != nil {
			return clearErr
		}
		return ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (m *MongoStore) Preference(ctx context.Context, companyID, id string) (Preference, error) {
	if !validID(companyID) || !validID(id) {
		return Preference{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return Preference{}, err
	}
	var stored preferenceDocument
	if err := m.client.Database(company.DatabaseName).Collection(preferencesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: id}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Preference{}, ErrNotFound
		}
		return Preference{}, ErrUnavailable
	}
	if stored.CompanyID != companyID || stored.ProjectID != id || stored.ObjectID.IsZero() || stored.Binding != (Binding{}) && !validBinding(stored.Binding) {
		return Preference{}, ErrUnavailable
	}
	return stored.toAPI(), nil
}

func (m *MongoStore) UpdateSnapshotReference(ctx context.Context, companyID, id string, input SnapshotReference) (Project, error) {
	if !validID(id) || !validID(companyID) || companyID != input.CompanyID || !validSnapshotFingerprint(input.Fingerprint) || !validFingerprint(input.BindingDigest) || !validSnapshotState(input.PreparationState) {
		return Project{}, ErrInvalid
	}
	project, err := m.Project(ctx, companyID, id)
	if err != nil {
		return Project{}, err
	}
	if project.CompanyID != input.CompanyID || project.Binding == nil || project.BindingRevision != input.BindingRevision || bindingDigest(*project.Binding) != input.BindingDigest || project.SnapshotFingerprint != input.ExpectedFingerprint {
		return Project{}, ErrConflict
	}
	company, err := m.company(ctx, project.CompanyID)
	if err != nil {
		return Project{}, err
	}
	filter := bson.D{{Key: "id", Value: id}, {Key: "company_id", Value: input.CompanyID}, {Key: "binding", Value: *project.Binding}}
	if input.BindingRevision == 0 {
		filter = append(filter, bson.E{Key: "binding_revision", Value: bson.M{"$in": []any{nil, int64(0)}}})
	} else {
		filter = append(filter, bson.E{Key: "binding_revision", Value: input.BindingRevision})
	}
	if input.ExpectedFingerprint == "" {
		filter = append(filter, bson.E{Key: "snapshot_fingerprint", Value: bson.M{"$in": []any{"", nil}}})
	} else {
		filter = append(filter, bson.E{Key: "snapshot_fingerprint", Value: input.ExpectedFingerprint})
	}
	update := bson.M{"$set": bson.M{"snapshot_fingerprint": input.Fingerprint, "preparation_state": input.PreparationState}}
	if m.beforeSnapshotWrite != nil {
		if err := m.beforeSnapshotWrite(); err != nil {
			return Project{}, err
		}
	}
	result, err := m.client.Database(company.DatabaseName).Collection(projectsCollection).UpdateOne(ctx, filter, update)
	if err != nil {
		return Project{}, normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		return Project{}, ErrConflict
	}
	return m.readProject(ctx, company, id)
}

func (m *MongoStore) LifecycleControl(ctx context.Context) (LifecycleControl, error) {
	var record struct {
		Revision int64 `bson:"revision"`
		Value    any   `bson:"lifecycle_migration"`
	}
	err := m.control.Collection(lifecycleControlCollection).FindOne(ctx, bson.D{{Key: "key", Value: "primary"}}).Decode(&record)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return LifecycleControl{Revision: 0, Value: json.RawMessage(`null`)}, nil
	}
	if err != nil {
		return LifecycleControl{}, ErrUnavailable
	}
	data, err := json.Marshal(record.Value)
	if err != nil {
		return LifecycleControl{}, ErrUnavailable
	}
	return LifecycleControl{Revision: record.Revision, Value: data}, nil
}

func (m *MongoStore) UpdateLifecycleControl(ctx context.Context, revision int64, value json.RawMessage) (LifecycleControl, error) {
	if revision < 0 || len(value) == 0 || !json.Valid(value) {
		return LifecycleControl{}, ErrInvalid
	}
	var document any
	if json.Unmarshal(value, &document) != nil {
		return LifecycleControl{}, ErrInvalid
	}
	filter := bson.D{{Key: "key", Value: "primary"}, {Key: "revision", Value: revision}}
	if revision == 0 {
		filter = bson.D{{Key: "key", Value: "primary"}, {Key: "revision", Value: bson.D{{Key: "$exists", Value: false}}}}
	}
	update := bson.M{"$set": bson.M{"lifecycle_migration": document}, "$inc": bson.M{"revision": 1}}
	update["$setOnInsert"] = bson.M{"_id": bson.NewObjectID(), "key": "primary"}
	result, err := m.control.Collection(lifecycleControlCollection).UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(revision == 0))
	if err != nil {
		return LifecycleControl{}, normalizeMongoError(err)
	}
	if result.MatchedCount == 0 && result.UpsertedCount == 0 {
		return LifecycleControl{}, ErrConflict
	}
	return LifecycleControl{Revision: revision + 1, Value: value}, nil
}

func (m *MongoStore) Roadmap(ctx context.Context, id, companyID string) (json.RawMessage, error) {
	return m.readRoadmap(ctx, id, companyID, true)
}

func (m *MongoStore) readRoadmap(ctx context.Context, id, companyID string, retry bool) (json.RawMessage, error) {
	if !validID(id) || !validID(companyID) {
		return nil, ErrInvalid
	}
	project, err := m.Project(ctx, companyID, id)
	if err != nil {
		return nil, err
	}
	if project.CompanyID != companyID {
		return nil, ErrConflict
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	epicCursor, err := m.client.Database(company.DatabaseName).Collection(epicsCollection).Find(ctx, bson.D{{Key: "project_id", Value: id}}, options.Find().SetSort(bson.D{{Key: "position", Value: 1}, {Key: "id", Value: 1}}))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer epicCursor.Close(ctx)
	epics := make([]roadmapEpic, 0)
	for epicCursor.Next(ctx) {
		var item roadmapEpic
		if epicCursor.Decode(&item) != nil {
			return nil, ErrUnavailable
		}
		epics = append(epics, item)
	}
	if epicCursor.Err() != nil {
		return nil, ErrUnavailable
	}
	releaseCursor, err := m.client.Database(company.DatabaseName).Collection(releasesCollection).Find(ctx, bson.D{{Key: "project_id", Value: id}}, options.Find().SetSort(bson.D{{Key: "position", Value: 1}, {Key: "id", Value: 1}}).SetProjection(bson.D{{Key: "investigation.introduction", Value: 0}, {Key: "investigation.materials", Value: 0}}))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer releaseCursor.Close(ctx)
	releases := make([]roadmapRelease, 0)
	for releaseCursor.Next(ctx) {
		var item roadmapRelease
		if releaseCursor.Decode(&item) != nil {
			return nil, ErrUnavailable
		}
		releases = append(releases, item)
	}
	if releaseCursor.Err() != nil {
		return nil, ErrUnavailable
	}
	todos, err := m.roadmapTODOs(ctx, m.client.Database(company.DatabaseName).Collection(todosCollection), id, releases)
	if err != nil {
		return nil, err
	}
	latest, err := m.Project(ctx, companyID, id)
	if err != nil {
		return nil, err
	}
	if latest.RoadmapRevision != project.RoadmapRevision {
		if retry {
			return m.readRoadmap(ctx, id, companyID, false)
		}
		return nil, ErrConflict
	}
	return projectRoadmap(id, project.RoadmapRevision, epics, releases, todos)
}

func (m *MongoStore) ResolveTenant(ctx context.Context, request TenantRequest) (TenantResolution, error) {
	key, err := tenantKey(request.Type, request.Identifier)
	if err != nil {
		return TenantResolution{}, ErrInvalid
	}
	field := "domain_keys"
	if request.Type != "host" {
		field = "app_keys"
	}
	var company Company
	if err := m.control.Collection(companiesCollection).FindOne(ctx, bson.D{{Key: field, Value: key}}).Decode(&company); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return TenantResolution{}, ErrNotFound
		}
		return TenantResolution{}, ErrUnavailable
	}
	if request.CompanyID != "" && request.CompanyID != company.ID {
		return TenantResolution{}, ErrConflict
	}
	if company.DatabaseName != m.tenantDatabase(company.ID) {
		return TenantResolution{}, ErrUnavailable
	}
	return TenantResolution{CompanyID: company.ID, Company: sanitizeCompany(company)}, nil
}

func (m *MongoStore) readCompanies(ctx context.Context) ([]Company, error) {
	cursor, err := m.control.Collection(companiesCollection).Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	items := make([]Company, 0)
	for cursor.Next(ctx) {
		var item Company
		if cursor.Decode(&item) != nil || item.ObjectID.IsZero() || !validID(item.ID) || item.DatabaseName != m.tenantDatabase(item.ID) {
			return nil, ErrUnavailable
		}
		items = append(items, item)
	}
	if cursor.Err() != nil {
		return nil, cursor.Err()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (m *MongoStore) company(ctx context.Context, id string) (Company, error) {
	var item Company
	if err := m.control.Collection(companiesCollection).FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&item); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Company{}, ErrNotFound
		}
		return Company{}, ErrUnavailable
	}
	if item.ObjectID.IsZero() || item.DatabaseName != m.tenantDatabase(id) {
		return Company{}, ErrUnavailable
	}
	return item, nil
}

func (m *MongoStore) readProject(ctx context.Context, company Company, id string) (Project, error) {
	var stored projectDocument
	if err := m.client.Database(company.DatabaseName).Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Project{}, ErrNotFound
		}
		return Project{}, ErrUnavailable
	}
	if stored.ID != id || stored.CompanyID != company.ID || stored.ObjectID.IsZero() || stored.Binding != nil && !validBinding(*stored.Binding) {
		return Project{}, ErrUnavailable
	}
	return stored.toAPI(), nil
}

func (m *MongoStore) writeProject(ctx context.Context, company Company, project Project) error {
	if m.beforeProjectWrite != nil {
		if err := m.beforeProjectWrite(); err != nil {
			return err
		}
	}
	document, err := projectToDocument(project, bson.NewObjectID())
	if err != nil {
		return err
	}
	_, err = m.client.Database(company.DatabaseName).Collection(projectsCollection).InsertOne(ctx, document)
	return err
}

func (m *MongoStore) ensureControlIndexes(ctx context.Context) error {
	indexes := []struct {
		collection string
		keys       bson.D
		name       string
		partial    bson.M
	}{
		{companiesCollection, bson.D{{Key: "id", Value: 1}}, "companies_id_unique", bson.M{"id": bson.M{"$type": "string"}}},
		{companiesCollection, bson.D{{Key: "domain_keys", Value: 1}}, "companies_domain_unique", bson.M{"domain_keys": bson.M{"$exists": true}}},
		{companiesCollection, bson.D{{Key: "app_keys", Value: 1}}, "companies_app_unique", bson.M{"app_keys": bson.M{"$exists": true}}},
		{lifecycleControlCollection, bson.D{{Key: "key", Value: 1}}, "lifecycle_control_key_unique", bson.M{"key": bson.M{"$type": "string"}}},
	}
	for _, item := range indexes {
		model := mongo.IndexModel{Keys: item.keys, Options: options.Index().SetName(item.name).SetUnique(true).SetPartialFilterExpression(item.partial)}
		if _, err := m.control.Collection(item.collection).Indexes().CreateOne(ctx, model); err != nil {
			return err
		}
	}
	return nil
}

func (m *MongoStore) ensureTenantIndexes(ctx context.Context, database string) error {
	indexes := []struct {
		collection string
		keys       bson.D
		name       string
		partial    bson.M
	}{
		{projectsCollection, bson.D{{Key: "id", Value: 1}}, "projects_id_unique", bson.M{"id": bson.M{"$type": "string"}}},
		{preferencesCollection, bson.D{{Key: "project_id", Value: 1}}, "registration_preferences_project_unique", bson.M{"project_id": bson.M{"$type": "string"}}},
		{epicsCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "id", Value: 1}}, "roadmap_epic_identity_unique", bson.M{"id": bson.M{"$type": "string"}}},
		{releasesCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "id", Value: 1}}, "roadmap_release_identity_unique", bson.M{"id": bson.M{"$type": "string"}}},
		{overviewVersionsCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "revision", Value: 1}}, "overview_project_revision_unique", nil},
		{overviewVersionsCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}}, "overview_project_operation_unique", nil},
		{overviewMediaCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "slot", Value: 1}}, "overview_project_operation_slot_unique", nil},
	}
	for _, item := range indexes {
		model := mongo.IndexModel{Keys: item.keys, Options: options.Index().SetName(item.name).SetUnique(true).SetPartialFilterExpression(item.partial)}
		if _, err := m.client.Database(database).Collection(item.collection).Indexes().CreateOne(ctx, model); err != nil {
			return err
		}
	}
	return m.ensureCanonicalCollections(ctx, database)
}

func sanitizeProject(project Project) Project { project.Binding = nil; return project }
func sanitizeCompany(company Company) Company { company.DatabaseName = ""; return company }
func normalizeMongoError(err error) error {
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return err
	}
	var write mongo.WriteException
	if errors.As(err, &write) {
		for _, item := range write.WriteErrors {
			if item.Code == 11000 {
				return ErrConflict
			}
		}
	}
	return ErrUnavailable
}
func bindingDigest(binding Binding) string {
	data, _ := json.Marshal(binding)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
