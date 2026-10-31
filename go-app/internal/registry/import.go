package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type LegacyImport struct {
	SchemaVersion      string                `json:"schema_version"`
	Companies          map[string]Company    `json:"companies"`
	Projects           map[string]Project    `json:"projects"`
	Preferences        map[string]Preference `json:"preferences"`
	LifecycleMigration *LifecycleMigration   `json:"lifecycle_migration,omitempty"`
	Roadmap            LegacyRoadmap         `json:"roadmap"`
}

type LifecycleMigration struct {
	State       string                         `json:"state" bson:"state"`
	ProjectIDs  []string                       `json:"project_ids" bson:"project_ids"`
	Excluded    []LifecycleMigrationDiagnostic `json:"excluded,omitempty" bson:"excluded,omitempty"`
	StartedAt   string                         `json:"started_at" bson:"started_at"`
	CompletedAt string                         `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
}
type LifecycleMigrationDiagnostic struct {
	ProjectID string `json:"project_id" bson:"project_id"`
	TODOID    string `json:"todo_id,omitempty" bson:"todo_id,omitempty"`
	Path      string `json:"path,omitempty" bson:"path,omitempty"`
	Message   string `json:"message" bson:"message"`
}
type LegacyRoadmap struct {
	SchemaVersion string       `json:"schema_version"`
	ProjectID     string       `json:"project_id"`
	Epics         []LegacyEpic `json:"epics"`
}
type LegacyEpic struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Releases    []LegacyRelease `json:"releases"`
}
type LegacyRelease struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	TODOs       []json.RawMessage `json:"todos"`
}
type ImportReport struct {
	Companies      int               `json:"companies"`
	Projects       int               `json:"projects"`
	Epics          int               `json:"epics"`
	Releases       int               `json:"releases"`
	NoOp           bool              `json:"no_op"`
	Digest         string            `json:"source_digest"`
	ProjectIDs     []string          `json:"project_ids"`
	BindingDigests map[string]string `json:"binding_digests"`
}

var expectedCompanies = map[string]string{"belluga-solutions": "Belluga Solutions", "unifast": "Unifast"}
var expectedProjects = map[string]string{"builder": "belluga-solutions", "boora": "belluga-solutions", "belluga-pay": "unifast"}
var expectedProjectNames = map[string]string{"builder": "Builder", "boora": "Bóora!", "belluga-pay": "Belluga Pay"}
var expectedEpicNames = map[string]string{
	"epic-mvp":          "MVP",
	"epic-pilot":        "APP Público Piloto (Guarappari)",
	"epic-plans":        "Planos Pagos + Gestão de Conta",
	"epic-monetization": "Funcionalidades de Monetização",
}

func ValidateImport(input LegacyImport) (ImportReport, []roadmapEpic, []roadmapRelease, error) {
	if input.SchemaVersion != "builder-project-registry-v1" || len(input.Companies) != len(expectedCompanies) || len(input.Projects) != len(expectedProjects) || input.Preferences == nil {
		return ImportReport{}, nil, nil, ErrInvalid
	}
	for id, name := range expectedCompanies {
		company, ok := input.Companies[id]
		if !ok || company.ID != id || company.Name != name || company.DatabaseName != "" && company.DatabaseName != tenantDatabase(id) {
			return ImportReport{}, nil, nil, ErrConflict
		}
	}
	projectIDs := make([]string, 0, len(expectedProjects))
	bindingDigests := make(map[string]string, len(expectedProjects))
	for id, companyID := range expectedProjects {
		project, ok := input.Projects[id]
		if !ok || project.ID != id || project.CompanyID != companyID || project.Name != expectedProjectNames[id] || project.Binding == nil || !validBinding(*project.Binding) {
			return ImportReport{}, nil, nil, ErrConflict
		}
		projectIDs = append(projectIDs, id)
		bindingDigests[id] = bindingDigest(*project.Binding)
	}
	sort.Strings(projectIDs)
	if input.Roadmap.SchemaVersion != "builder-roadmap-v1" || input.Roadmap.ProjectID != "boora" || len(input.Roadmap.Epics) != len(expectedEpicNames) {
		return ImportReport{}, nil, nil, ErrConflict
	}
	seenEpics := make(map[string]bool, len(expectedEpicNames))
	epics := make([]roadmapEpic, 0, len(expectedEpicNames))
	var releases []roadmapRelease
	for epicPosition, epic := range input.Roadmap.Epics {
		expectedName, exists := expectedEpicNames[epic.ID]
		if !exists || seenEpics[epic.ID] || epic.Name != expectedName {
			return ImportReport{}, nil, nil, ErrConflict
		}
		seenEpics[epic.ID] = true
		epics = append(epics, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "boora", ID: epic.ID, Name: epic.Name, Description: epic.Description, Position: epicPosition})
		for releasePosition, release := range epic.Releases {
			if release.ID != "release-v0-7-1-participant-media" {
				continue
			}
			if epic.ID != "epic-pilot" || release.Name != "v0.7.1 — participant media" || len(releases) != 0 {
				return ImportReport{}, nil, nil, ErrConflict
			}
			releases = append(releases, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "boora", ID: release.ID, EpicID: epic.ID, Name: release.Name, Description: release.Description, Position: releasePosition})
		}
	}
	if len(seenEpics) != len(expectedEpicNames) || len(releases) != 1 {
		return ImportReport{}, nil, nil, ErrConflict
	}
	canonical, err := json.Marshal(input)
	if err != nil {
		return ImportReport{}, nil, nil, ErrInvalid
	}
	digest := sha256.Sum256(canonical)
	return ImportReport{Companies: len(expectedCompanies), Projects: len(expectedProjects), Epics: len(epics), Releases: len(releases), Digest: "sha256:" + hex.EncodeToString(digest[:]), ProjectIDs: projectIDs, BindingDigests: bindingDigests}, epics, releases, nil
}

func (m *MongoStore) Import(ctx context.Context, input LegacyImport) (ImportReport, error) {
	report, epics, releases, err := ValidateImport(input)
	if err != nil {
		return ImportReport{}, err
	}
	preferences := make([]Preference, 0, len(input.Preferences))
	for id, preference := range input.Preferences {
		if id != preference.ProjectID || preference.CompanyID == "" {
			return ImportReport{}, ErrInvalid
		}
		if project, registered := input.Projects[id]; registered && project.CompanyID == preference.CompanyID {
			return ImportReport{}, ErrConflict
		}
		preferences = append(preferences, preference)
	}
	prefs, err := preferencesToDocuments(preferences)
	if err != nil {
		return ImportReport{}, err
	}
	report.NoOp = true
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	currentLifecycle, err := m.LifecycleControl(ctx)
	if err != nil {
		return ImportReport{}, err
	}
	if input.LifecycleMigration == nil && currentLifecycle.Revision != 0 {
		return ImportReport{}, ErrConflict
	}
	if input.LifecycleMigration != nil && currentLifecycle.Revision != 0 {
		if !sameLifecycleMigration(currentLifecycle.Value, input.LifecycleMigration) {
			return ImportReport{}, ErrConflict
		}
	}
	for _, companyID := range []string{"belluga-solutions", "unifast"} {
		if err := m.ensureTenantIndexes(ctx, m.tenantDatabase(companyID)); err != nil {
			return ImportReport{}, ErrUnavailable
		}
	}
	// An import is repeatable by verifying domain records. No digest marker or
	// cross-database transaction is needed for a fixed, bounded source set.
	for _, id := range sortedCompanyIDs(input.Companies) {
		company := input.Companies[id]
		company.DatabaseName = m.tenantDatabase(id)
		company, err = normalizeCompany(company)
		if err != nil {
			return ImportReport{}, err
		}
		var prior Company
		readErr := m.control.Collection(companiesCollection).FindOne(ctx, bson.D{{Key: "id", Value: id}}).Decode(&prior)
		if readErr == nil {
			if prior.ObjectID.IsZero() || prior.Name != company.Name || prior.DatabaseName != company.DatabaseName || !slices.Equal(prior.DomainKeys, company.DomainKeys) || !slices.Equal(prior.AppKeys, company.AppKeys) {
				return ImportReport{}, ErrConflict
			}
		} else if !errors.Is(readErr, mongo.ErrNoDocuments) {
			return ImportReport{}, ErrUnavailable
		} else if _, err := m.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
			return ImportReport{}, normalizeMongoError(err)
		} else {
			report.NoOp = false
		}
	}
	for _, id := range report.ProjectIDs {
		project := input.Projects[id]
		expected, err := canonicalStoredProject(project)
		if err != nil {
			return ImportReport{}, err
		}
		company := Company{ID: project.CompanyID, DatabaseName: m.tenantDatabase(project.CompanyID)}
		prior, readErr := m.readProject(ctx, company, id)
		if readErr == nil {
			if prior.CompanyID != project.CompanyID || prior.Name != project.Name || prior.Binding == nil || *prior.Binding != *project.Binding || prior.RegisteredAt != expected.RegisteredAt || prior.PreparationState != project.PreparationState || prior.PreparationDiagnostic != project.PreparationDiagnostic || prior.SnapshotFingerprint != project.SnapshotFingerprint {
				return ImportReport{}, ErrConflict
			}
		} else if !errors.Is(readErr, ErrNotFound) {
			return ImportReport{}, readErr
		} else if err := m.writeProject(ctx, company, project); err != nil {
			return ImportReport{}, normalizeMongoError(err)
		} else {
			report.NoOp = false
		}
	}
	for _, preference := range prefs {
		collection := m.client.Database(m.tenantDatabase(preference.CompanyID)).Collection(preferencesCollection)
		var prior preferenceDocument
		err := collection.FindOne(ctx, bson.D{{Key: "project_id", Value: preference.ProjectID}}).Decode(&prior)
		if err == nil {
			if prior.CompanyID != preference.CompanyID || prior.Binding != preference.Binding || !prior.DeclinedAt.Equal(preference.DeclinedAt) {
				return ImportReport{}, ErrConflict
			}
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return ImportReport{}, ErrUnavailable
		} else if _, err := collection.InsertOne(ctx, preference); err != nil {
			return ImportReport{}, normalizeMongoError(err)
		} else {
			report.NoOp = false
		}
	}
	roadmapDatabase := m.client.Database(m.tenantDatabase("belluga-solutions"))
	for _, epic := range epics {
		collection := roadmapDatabase.Collection(epicsCollection)
		var prior roadmapEpic
		err := collection.FindOne(ctx, bson.D{{Key: "project_id", Value: epic.ProjectID}, {Key: "id", Value: epic.ID}}).Decode(&prior)
		if err == nil {
			if prior.ProjectID != epic.ProjectID || prior.ID != epic.ID || prior.Name != epic.Name || prior.Description != epic.Description || prior.Position != epic.Position {
				return ImportReport{}, ErrConflict
			}
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return ImportReport{}, ErrUnavailable
		} else if _, err := collection.InsertOne(ctx, epic); err != nil {
			return ImportReport{}, normalizeMongoError(err)
		} else {
			report.NoOp = false
		}
	}
	for _, release := range releases {
		collection := roadmapDatabase.Collection(releasesCollection)
		var prior roadmapRelease
		err := collection.FindOne(ctx, bson.D{{Key: "project_id", Value: release.ProjectID}, {Key: "id", Value: release.ID}}).Decode(&prior)
		if err == nil {
			if prior.ProjectID != release.ProjectID || prior.ID != release.ID || prior.EpicID != release.EpicID || prior.Name != release.Name || prior.Description != release.Description || prior.Position != release.Position {
				return ImportReport{}, ErrConflict
			}
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			return ImportReport{}, ErrUnavailable
		} else if _, err := collection.InsertOne(ctx, release); err != nil {
			return ImportReport{}, normalizeMongoError(err)
		} else {
			report.NoOp = false
		}
	}
	if input.LifecycleMigration == nil {
		current, err := m.LifecycleControl(ctx)
		if err != nil {
			return ImportReport{}, err
		}
		if current.Revision != 0 {
			return ImportReport{}, ErrConflict
		}
	} else {
		current, err := m.LifecycleControl(ctx)
		if err != nil {
			return ImportReport{}, err
		}
		if current.Revision == 0 {
			value, err := json.Marshal(input.LifecycleMigration)
			if err != nil {
				return ImportReport{}, ErrInvalid
			}
			if _, err := m.UpdateLifecycleControl(ctx, 0, value); err != nil {
				return ImportReport{}, err
			}
			report.NoOp = false
		} else if !sameLifecycleMigration(current.Value, input.LifecycleMigration) {
			return ImportReport{}, ErrConflict
		}
	}
	return report, nil
}

func sameLifecycleMigration(current json.RawMessage, expected *LifecycleMigration) bool {
	if expected == nil {
		return false
	}
	var actual LifecycleMigration
	if err := json.Unmarshal(current, &actual); err != nil {
		return false
	}
	return actual.State == expected.State &&
		slices.Equal(actual.ProjectIDs, expected.ProjectIDs) &&
		slices.Equal(actual.Excluded, expected.Excluded) &&
		actual.StartedAt == expected.StartedAt &&
		actual.CompletedAt == expected.CompletedAt
}

func sortedCompanyIDs(companies map[string]Company) []string {
	ids := make([]string, 0, len(companies))
	for id := range companies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func ValidateLegacyRoadmap(data []byte) (LegacyRoadmap, error) {
	if len(data) == 0 || len(data) > 2<<20 || !json.Valid(data) {
		return LegacyRoadmap{}, ErrInvalid
	}
	var roadmap LegacyRoadmap
	if json.Unmarshal(data, &roadmap) != nil {
		return LegacyRoadmap{}, ErrInvalid
	}
	return roadmap, nil
}
