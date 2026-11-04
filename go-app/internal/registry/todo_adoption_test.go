package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	historicalPilotRevision     = "f1edcc2a07a17252491e91b3960a6075b6dee55c"
	historicalPilotPath         = "todos/completed/TODO-v1-tenant-admin-events-temporal-filter.md"
	historicalPilotDigest       = "sha256:8117a5f840286e2c8afff274c6ef03fcb47ce05855fdcad3877bf50f5be738cb"
	historicalPilotTODOID       = "boora-v1-tenant-admin-events-temporal-filter"
	historicalPilotGitRepo      = "belluga_now_foundation_documentation"
	historicalPilotFoundationID = "belluga-now-foundation-documentation"
)

func historicalPilotSource() (TODOSource, error) {
	data, err := os.ReadFile("testdata/historical-todo-v1-tenant-admin-events-temporal-filter.md")
	if err != nil {
		return TODOSource{}, err
	}
	digest := sha256.Sum256(data)
	actualDigest := "sha256:" + hex.EncodeToString(digest[:])
	if actualDigest != historicalPilotDigest || !strings.Contains(string(data), "**Current delivery stage:** `Local-Implemented`") {
		return TODOSource{}, fmt.Errorf("pinned Bóora pilot fixture digest or stage evidence mismatch: %s", actualDigest)
	}
	// The canonical TODO source ID is hyphenated; historicalPilotGitRepo retains
	// the exact repository slug from the pinned Bóora Foundation source.
	return TODOSource{Foundation: historicalPilotFoundationID, Revision: historicalPilotRevision, Path: historicalPilotPath, Digest: actualDigest}, nil
}

func TestHistoricalAdoptionValidatesPinnedBooraPilotSourceWithoutStageInference(t *testing.T) {
	source, err := historicalPilotSource()
	if err != nil {
		t.Fatal(err)
	}
	request := HistoricalTODOAdoptionRequest{ProjectID: "boora", OperationID: "pilot-adopt-001", Reason: "Accept the exact source-recorded Local-Implemented stage as historical state; this is not new CI or commit proof.", HistoricalAcceptance: true, Items: []HistoricalTODOAdoptionItem{{TODOID: historicalPilotTODOID, ExpectedRevision: 0, RequestedStage: "Local-Implemented", SourceReference: todoHistoricalSourceReference(source), SourceDigest: source.Digest}}}
	if err := validateHistoricalTODOAdoption(request); err != nil {
		t.Fatalf("pinned adoption request rejected: %v", err)
	}
	if want := source.Foundation + "@" + historicalPilotRevision + ":" + historicalPilotPath; request.Items[0].SourceReference != want {
		t.Fatalf("pinned source reference = %q; want canonical source identity %q (original Git repository: %s)", request.Items[0].SourceReference, want, historicalPilotGitRepo)
	}
	record := TODORecord{ID: historicalPilotTODOID, ProjectID: "boora", Source: source, Stage: "Planning", StageVerification: "unverified"}
	if err := validateHistoricalTODOAdoptionTarget(record, request.Items[0], request.Items[0].SourceReference, request.Items[0].SourceDigest); err == nil {
		t.Fatal("Foundation source was accepted as operational stage authority")
	}
	if request.Items[0].RequestedStage != "Local-Implemented" {
		t.Fatal("pilot stage must be explicitly taken from source evidence, never inferred from the `completed` folder")
	}
}

func TestHistoricalAdoptionRequestRejectsMongoBornAndVerifiedTargets(t *testing.T) {
	item := HistoricalTODOAdoptionItem{TODOID: "legacy-one", ExpectedRevision: 0, RequestedStage: "Local-Implemented", SourceReference: "foundation@" + historicalPilotRevision + ":" + historicalPilotPath, SourceDigest: historicalPilotDigest}
	request := HistoricalTODOAdoptionRequest{ProjectID: "boora", OperationID: "adopt-001", Reason: "Historical state acceptance", HistoricalAcceptance: true, Items: []HistoricalTODOAdoptionItem{item}}
	if err := validateHistoricalTODOAdoption(request); err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]TODORecord{
		"Mongo-born":       {ID: item.TODOID, ProjectID: "boora", Source: TODOSource{Kind: "mongo-content-revision", Ref: item.SourceReference, Digest: item.SourceDigest}, StageVerification: "unverified"},
		"already verified": {ID: item.TODOID, ProjectID: "boora", Source: TODOSource{Foundation: "foundation", Revision: historicalPilotRevision, Path: historicalPilotPath, Digest: item.SourceDigest}, StageVerification: "verified", VerifiedTransitionRef: &TODOProofReference{Kind: "old-proof", Source: "test", Reference: item.SourceReference, SourceDigest: item.SourceDigest, VerificationState: "verified"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateHistoricalTODOAdoptionTarget(record, item, item.SourceReference, item.SourceDigest); err == nil {
				t.Fatal("historical adoption accepted a Mongo-born or already verified TODO")
			}
		})
	}
}

func TestHistoricalAdoptionRequestRequiresExplicitBoundedPerTODODecisions(t *testing.T) {
	validItem := HistoricalTODOAdoptionItem{TODOID: "legacy-one", ExpectedRevision: 0, RequestedStage: "Local-Implemented", SourceReference: "foundation@" + historicalPilotRevision + ":" + historicalPilotPath, SourceDigest: historicalPilotDigest}
	valid := HistoricalTODOAdoptionRequest{ProjectID: "boora", OperationID: "adopt-001", Reason: "Accept the source-recorded historical stage", HistoricalAcceptance: true, Items: []HistoricalTODOAdoptionItem{validItem}}
	cases := map[string]func(*HistoricalTODOAdoptionRequest){
		"missing acknowledgement": func(request *HistoricalTODOAdoptionRequest) { request.HistoricalAcceptance = false },
		"unknown stage": func(request *HistoricalTODOAdoptionRequest) {
			request.Items[0].RequestedStage = "Completed from folder"
		},
		"missing source digest": func(request *HistoricalTODOAdoptionRequest) { request.Items[0].SourceDigest = "" },
		"duplicate identity":    func(request *HistoricalTODOAdoptionRequest) { request.Items = append(request.Items, request.Items[0]) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := valid
			request.Items = append([]HistoricalTODOAdoptionItem(nil), valid.Items...)
			mutate(&request)
			if err := validateHistoricalTODOAdoption(request); err == nil {
				t.Fatal("invalid historical adoption request was accepted")
			}
		})
	}
}

type testHistoricalTODOAdopter struct {
	*testService
	request HistoricalTODOAdoptionRequest
}

func (s *testHistoricalTODOAdopter) AdoptHistoricalTODOStages(_ context.Context, companyID string, request HistoricalTODOAdoptionRequest) (HistoricalTODOAdoptionResponse, error) {
	s.request = request
	return HistoricalTODOAdoptionResponse{CompanyID: companyID, ProjectID: request.ProjectID}, nil
}

func TestHistoricalAdoptionRouteIsPrivateProjectScoped(t *testing.T) {
	service := &testHistoricalTODOAdopter{testService: &testService{}}
	body := `{"project_id":"boora","operation_id":"pilot-adopt-001","reason":"Historical source acceptance","historical_acceptance":true,"items":[{"todo_id":"boora-v1-tenant-admin-events-temporal-filter","expected_revision":0,"requested_stage":"Local-Implemented","source_reference":"belluga-now-foundation-documentation@f1edcc2a07a17252491e91b3960a6075b6dee55c:todos/completed/TODO-v1-tenant-admin-events-temporal-filter.md","source_digest":"sha256:8117a5f840286e2c8afff274c6ef03fcb47ce05855fdcad3877bf50f5be738cb"}]}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/boora/todos/historical-adoption", strings.NewReader(body))
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.request.ProjectID != "boora" || service.request.Items[0].TODOID != historicalPilotTODOID {
		t.Fatalf("historical adoption route = %d %s, request=%+v", response.Code, response.Body.String(), service.request)
	}
	wrongProject := strings.Replace(body, `"project_id":"boora"`, `"project_id":"builder"`, 1)
	request = httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/boora/todos/historical-adoption", strings.NewReader(wrongProject))
	response = httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("cross-Project adoption accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestTODOAuthoredContentV1BSONMatchesStrictValidator(t *testing.T) {
	record := genericTODOAdmissionFixture().TODO
	content := contentFromTODO(record)
	if content.SchemaVersion != 0 {
		t.Fatalf("legacy content schema version = %d; want absent v1 discriminator", content.SchemaVersion)
	}
	encoded, err := bson.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	raw := bson.Raw(encoded)
	if raw.Lookup("schema_version").Type != 0 || raw.Lookup("scope").Type != 0 || raw.Lookup("sections").Type != bson.TypeArray {
		t.Fatalf("v1 content BSON has schema_version=%v scope=%v sections=%v", raw.Lookup("schema_version").Type, raw.Lookup("scope").Type, raw.Lookup("sections").Type)
	}
	validator := TODOAuthoredContentV1Validator()
	if allowed, ok := bsonDValue(validator, "additionalProperties").(bool); !ok || allowed {
		t.Fatal("v1 strict validator unexpectedly permits unknown top-level fields")
	}
	for _, element := range validatorProperties(validator) {
		if element.Key == "schema_version" {
			t.Fatal("v1 validator should omit the v2 discriminator when v1 BSON omits it")
		}
	}
}

func bsonDValue(document bson.D, key string) any {
	for _, element := range document {
		if element.Key == key {
			return element.Value
		}
	}
	return nil
}

func TestHistoricalAdoptionValidatorsRecognizeExactPredecessors(t *testing.T) {
	for name, validators := range map[string]struct{ current, previous bson.D }{
		"todo collection":                                    {TODOCollectionValidator(), TODOCollectionValidatorBeforeHistoricalAdoption()},
		"repository identity":                                {TODOCollectionValidator(), TODOCollectionValidatorBeforeRepositoryIdentity()},
		"history planning and repository identity":           {TODOContentVersionsValidator(), contentVersionsValidatorBeforePlanningAndRepositoryIdentity()},
		"history events":                                     {TODOEventsValidator(), historyValidatorBeforeHistoricalAdoption(TODOEventsValidator())},
		"migration lineage":                                  {TODOEventsValidator(), historyValidatorBeforeContentMigration(TODOEventsValidator())},
		"combined pre-adoption/pre-migration history events": {TODOEventsValidator(), historyValidatorBeforeContentMigration(historyValidatorBeforeHistoricalAdoption(TODOEventsValidator()))},
	} {
		t.Run(name, func(t *testing.T) {
			currentBytes, err := bson.Marshal(validators.current)
			if err != nil {
				t.Fatal(err)
			}
			previousBytes, err := bson.Marshal(validators.previous)
			if err != nil {
				t.Fatal(err)
			}
			if bsonValidatorEqual(bson.Raw(currentBytes), validators.previous) || bsonValidatorEqual(bson.Raw(previousBytes), validators.current) {
				t.Fatal("predecessor validator was not distinguished from the current adoption validator")
			}
		})
	}
}

func TestMongoProvisionTODOHistoryUpgradesCombinedEventValidatorPredecessor(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	databaseName := store.tenantDatabase("company-a")
	database := store.client.Database(databaseName)
	t.Cleanup(func() {
		if err := database.Drop(context.Background()); err != nil {
			t.Errorf("drop isolated tenant database %q: %v", databaseName, err)
		}
		if err := store.control.Drop(context.Background()); err != nil {
			t.Errorf("drop isolated control database %q: %v", stamp, err)
		}
		_ = store.Close(context.Background())
	})
	company, err := normalizeCompany(Company{ID: "company-a", Name: "Company A", DatabaseName: databaseName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, company.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, company.ID); err != nil {
		t.Fatal(err)
	}
	combinedPredecessor := historyValidatorBeforeContentMigration(historyValidatorBeforeHistoricalAdoption(TODOEventsValidator()))
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: combinedPredecessor}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("install exact combined predecessor: %v", err)
	}
	if err := store.ProvisionTODOHistory(ctx, company.ID); err != nil {
		t.Fatalf("upgrade exact combined event validator predecessor: %v", err)
	}
	if err := verifyTODOHistoryCollections(ctx, database); err != nil {
		t.Fatalf("verify upgraded history validators/indexes: %v", err)
	}
}

func validatorProperties(validator bson.D) bson.D {
	schema := validator
	for _, field := range validator {
		if field.Key == "$jsonSchema" {
			schema, _ = field.Value.(bson.D)
		}
	}
	for _, field := range schema {
		if field.Key == "properties" {
			properties, _ := field.Value.(bson.D)
			return properties
		}
	}
	return nil
}

func TestMongoHistoricalAdoptionPilotIsAtomicAndIdempotent(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("todo_adoption_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	databaseName := store.tenantDatabase("belluga-solutions")
	t.Cleanup(func() {
		_ = store.client.Database(databaseName).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := store.Register(ctx, Registration{CompanyID: "belluga-solutions", CompanyName: "Belluga Solutions", ProjectID: "boora", ProjectName: "Bóora", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	database := store.client.Database(databaseName)
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "boora", ID: "e-01", Name: "Historical adoption pilot", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "boora", ID: "r-01", EpicID: "e-01", Name: "Imported TODOs", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "belluga-solutions"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "belluga-solutions"); err != nil {
		t.Fatalf("provision history collections before legacy import: %v", err)
	}
	source, err := historicalPilotSource()
	if err != nil {
		t.Fatal(err)
	}
	record := genericTODOAdmissionFixture().TODO
	record.ID, record.ProjectID = historicalPilotTODOID, "boora"
	record.ReleaseID, record.Position = "r-01", intPointer(0)
	record.Title = "TODO (V1): Tenant Admin Events Temporal Filter"
	record.Objective = "Add the temporal tenant-admin events filter."
	record.NextExactStep = "Run the final combined tenant-admin filter smoke."
	record.Stage = "Planning"
	fixtureBody, err := os.ReadFile("testdata/historical-todo-v1-tenant-admin-events-temporal-filter.md")
	if err != nil {
		t.Fatal(err)
	}
	record.Sections = []TODOSection{{Key: "legacy_source", Title: "Pinned historical source", BodyMarkdown: string(fixtureBody), Position: 0, Ownership: "contract"}}
	record.Source = source
	record.StageVerification = ""
	if _, err := store.AdmitTODO(ctx, "belluga-solutions", "boora", TODOAdmissionRequest{TODO: record}); err != nil {
		t.Fatalf("admit isolated pinned historical fixture: %v", err)
	}
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: TODOCollectionValidatorBeforeHistoricalAdoption()}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("prepare TODO validator predecessor: %v", err)
	}
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: historyValidatorBeforeHistoricalAdoption(TODOEventsValidator())}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("prepare history validator predecessor: %v", err)
	}
	var admitted TODORecord
	if err := database.Collection(todosCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "id", Value: record.ID}}).Decode(&admitted); err != nil {
		t.Fatal(err)
	}
	if err := validateStoredTODO(admitted); err != nil {
		t.Fatalf("fixture is not a valid stored imported TODO: %v (%+v)", err, admitted)
	}
	request := HistoricalTODOAdoptionRequest{ProjectID: "boora", OperationID: "pilot-adopt-001", Reason: "Accept the exact source-recorded Local-Implemented stage as historical state; this is not fresh proof.", HistoricalAcceptance: true, Items: []HistoricalTODOAdoptionItem{{TODOID: record.ID, ExpectedRevision: 0, RequestedStage: "Local-Implemented", SourceReference: todoHistoricalSourceReference(source), SourceDigest: source.Digest}}}
	if _, err := store.AdoptHistoricalTODOStages(ctx, "belluga-solutions", request); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("v1 Foundation subject was accepted for historical stage adoption: %v", err)
	}
	read, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || read.TODO.Stage != "Planning" || read.TODO.Revision != 0 || read.TODO.StageVerification != "unverified" || read.TODO.VerifiedTransitionRef != nil {
		t.Fatalf("refused v1 historical adoption changed operational state: %+v, %v", read.TODO, err)
	}
	if count, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "historical-adoption"}}); err != nil || count != 0 {
		t.Fatalf("refused v1 adoption wrote %d historical events: %v", count, err)
	}
	// Ordinary content edits cannot convert a v1 record; the governed migration
	// preserves the reviewed operational stage and immutable source bytes.
	oldVersion, err := store.ReadTODOAsOf(ctx, "belluga-solutions", "boora", record.ID, 0)
	if err != nil || oldVersion.Content.SchemaVersion != 0 || len(oldVersion.Content.Sections) != 1 || oldVersion.Content.Sections[0].BodyMarkdown != string(fixtureBody) {
		t.Fatalf("exact v1 content before conversion = %+v, %v", oldVersion.Content, err)
	}
	converted := TODOAuthoredContent{
		TODOContent: TODOContent{
			SchemaVersion:       todoContentSchemaVersion,
			Context:             "The historical TODO implements a temporal tenant-admin events filter.",
			Scope:               &TODOContentScope{Included: []string{"Apply the tenant-admin event filter to the selected time interval."}, Excluded: []string{}},
			Criteria:            []TODOContentItem{{ID: "filter-window", Text: "The event query applies the approved temporal window."}},
			ValidationScenarios: []TODOValidationScenario{{ID: "tenant-filter", CriterionIDs: []string{"filter-window"}, Method: "Run the existing tenant-admin event filter tests.", Expected: "Only events in the requested tenant and time interval are returned."}},
			ImplementationPlan:  []TODOContentItem{{ID: "verify-filter", Text: "Run the final combined tenant-admin filter smoke."}},
			Decisions:           []TODOContentItem{},
			Risks:               []TODOContentRisk{},
			References:          []TODOContentReference{},
		},
		Title:           oldVersion.Content.Title,
		Objective:       oldVersion.Content.Objective,
		DiffExpectation: oldVersion.Content.DiffExpectation,
		AffectedFiles:   oldVersion.Content.AffectedFiles,
	}
	editRevision := int64(0)
	if _, err := store.EditTODOContent(ctx, "belluga-solutions", TODOContentEditRequest{ProjectID: "boora", TODOID: record.ID, ExpectedRevision: &editRevision, OperationID: "pilot-content-edit-001", Content: converted, Reason: "Test that ordinary edits cannot convert an imported v1 contract"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ordinary v1-to-v2 content edit was not rejected: %v", err)
	}
	unchanged, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || unchanged.TODO.Revision != 0 || unchanged.TODO.Stage != "Planning" || unchanged.TODO.StageVerification != "unverified" || unchanged.TODO.VerifiedTransitionRef != nil {
		t.Fatalf("rejected ordinary edit changed aggregate or proof: %+v, %v", unchanged, err)
	}
	oldVersion, err = store.ReadTODOAsOf(ctx, "belluga-solutions", "boora", record.ID, 0)
	if err != nil || oldVersion.Content.SchemaVersion != 0 || oldVersion.Content.Sections[0].BodyMarkdown != string(fixtureBody) {
		t.Fatalf("rejected ordinary edit changed immutable v1 history = %+v, %v", oldVersion.Content, err)
	}
	var sourceVersion TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: int64(0)}}).Decode(&sourceVersion); err != nil {
		t.Fatalf("read immutable v1 migration source: %v", err)
	}
	migrationRequest := TODOContentMigrationRequest{ProjectID: "boora", TODOID: record.ID, ExpectedRevision: &editRevision, ExpectedContentRevision: 0, ExpectedContentDigest: sourceVersion.Digest, ExpectedSourceReference: todoHistoricalSourceReference(source), ExpectedSourceDigest: source.Digest, OperationID: "pilot-content-migration-001", Reason: "Convert the owner-reviewed typed contract while preserving the historical state and source", ReviewedAcceptance: todoContentMigrationAcceptance, Content: converted}
	staleRequest := migrationRequest
	staleRevision := int64(1)
	staleRequest.ExpectedRevision = &staleRevision
	staleRequest.OperationID = "pilot-content-migration-stale-revision"
	if _, err := store.MigrateTODOContent(ctx, "belluga-solutions", staleRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale aggregate revision was accepted for migration: %v", err)
	}
	wrongSource := migrationRequest
	wrongSource.OperationID = "pilot-content-migration-wrong-source"
	wrongSource.ExpectedSourceDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := store.MigrateTODOContent(ctx, "belluga-solutions", wrongSource); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong source digest was accepted for migration: %v", err)
	}
	baselineFilter := bson.D{{Key: "project_id", Value: "boora"}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "baseline"}}
	var baseline TODOHistoryEvent
	if err := database.Collection(todoEventsCollection).FindOne(ctx, baselineFilter).Decode(&baseline); err != nil {
		t.Fatalf("read isolated baseline for negative migration case: %v", err)
	}
	if _, err := database.Collection(todoEventsCollection).DeleteOne(ctx, baselineFilter); err != nil {
		t.Fatalf("remove isolated baseline for negative migration case: %v", err)
	}
	missingBaseline := migrationRequest
	missingBaseline.OperationID = "pilot-content-migration-missing-baseline"
	if _, err := store.MigrateTODOContent(ctx, "belluga-solutions", missingBaseline); err == nil {
		t.Fatalf("migration without baseline was accepted: %v", err)
	}
	if _, err := database.Collection(todoEventsCollection).InsertOne(ctx, baseline); err != nil {
		t.Fatalf("restore isolated baseline after negative case: %v", err)
	}
	unchangedAfterRejectedMigrations, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || unchangedAfterRejectedMigrations.TODO.Revision != 0 || unchangedAfterRejectedMigrations.TODO.Stage != "Planning" {
		t.Fatalf("rejected migrations changed aggregate state: %+v, %v", unchangedAfterRejectedMigrations.TODO, err)
	}
	migrationCount, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: todoContentMigrationEventKind}})
	if err != nil || migrationCount != 0 {
		t.Fatalf("rejected migrations left %d migration events: %v", migrationCount, err)
	}
	if _, err := store.MigrateTODOContent(ctx, "belluga-solutions", migrationRequest); err != nil {
		t.Fatalf("governed content migration: %v", err)
	}
	convertedRead, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || convertedRead.TODO.Revision != 1 || convertedRead.TODO.Stage != "Planning" || convertedRead.TODO.StageVerification != "unverified" || convertedRead.TODO.VerifiedTransitionRef != nil || convertedRead.TODO.Content == nil || convertedRead.TODO.Content.SchemaVersion != todoContentSchemaVersion || convertedRead.TODO.Source.Kind != "mongo-content-revision" {
		t.Fatalf("governed migration did not expose v2 without authorizing a stage: %+v, %v", convertedRead, err)
	}
	fresh := mongoBornTODOFixture()
	fresh.ID, fresh.ProjectID, fresh.ReleaseID, fresh.Position = "mongo-born-adoption-negative", "boora", "r-01", intPointer(1)
	if _, err := store.AdmitTODO(ctx, "belluga-solutions", "boora", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, TODO: fresh}); err != nil {
		t.Fatalf("admit current Mongo-born TODO: %v", err)
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx,
		bson.D{{Key: "project_id", Value: "boora"}, {Key: "id", Value: fresh.ID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "stage_verification", Value: "unverified"}}}, {Key: "$unset", Value: bson.D{{Key: "verified_transition_ref", Value: ""}}}}); err != nil {
		t.Fatalf("prepare unverified Mongo-born adoption negative: %v", err)
	}
	freshRead, err := store.ReadTODO(ctx, "belluga-solutions", "boora", fresh.ID, nil)
	if err != nil || freshRead.TODO.StageVerification != "unverified" {
		t.Fatalf("read unverified Mongo-born negative: %+v, %v", freshRead.TODO, err)
	}
	freshAdoption := HistoricalTODOAdoptionRequest{ProjectID: "boora", OperationID: "reject-mongo-born-adoption", Reason: "A newly authored TODO must not bypass ordinary verification", HistoricalAcceptance: true, Items: []HistoricalTODOAdoptionItem{{TODOID: fresh.ID, ExpectedRevision: freshRead.TODORevision, RequestedStage: "Local-Implemented", SourceReference: freshRead.TODO.Source.Ref, SourceDigest: freshRead.ContentDigest}}}
	if _, err := store.AdoptHistoricalTODOStages(ctx, "belluga-solutions", freshAdoption); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "content-migration event") {
		t.Fatalf("Mongo-born unverified TODO entered historical adoption: %v", err)
	}
	v2Adoption := request
	v2Adoption.OperationID = "pilot-adopt-v2-subject"
	v2Adoption.Items = append([]HistoricalTODOAdoptionItem(nil), request.Items...)
	v2Adoption.Items[0].ExpectedRevision = convertedRead.TODO.Revision
	v2Adoption.Items[0].SourceReference = convertedRead.TODO.Source.Ref
	v2Adoption.Items[0].SourceDigest = convertedRead.ContentDigest
	first, err := store.AdoptHistoricalTODOStages(ctx, "belluga-solutions", v2Adoption)
	if err != nil || len(first.Results) != 1 || first.Results[0].Revision != 2 || first.Results[0].Stage != "Local-Implemented" {
		t.Fatalf("v2-subject historical adoption = %+v, %v", first, err)
	}
	second, err := store.AdoptHistoricalTODOStages(ctx, "belluga-solutions", v2Adoption)
	if err != nil || !second.NoOp || len(second.Results) != 1 || !second.Results[0].NoOp {
		t.Fatalf("v2-subject historical adoption retry = %+v, %v", second, err)
	}
	convertedRead, err = store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || convertedRead.TODO.Revision != 2 || convertedRead.TODO.Stage != "Local-Implemented" || convertedRead.TODO.StageVerification != "verified" || convertedRead.TODO.VerifiedTransitionRef == nil || convertedRead.TODO.VerifiedTransitionRef.Kind != "historical-adoption" || convertedRead.TODO.VerifiedTransitionRef.SourceDigest != convertedRead.ContentDigest {
		t.Fatalf("v2-subject adoption readback = %+v, %v", convertedRead, err)
	}
	var adoptionEvent TODOHistoryEvent
	if err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "historical-adoption"}}).Decode(&adoptionEvent); err != nil || adoptionEvent.ProofReference == nil || adoptionEvent.ProofReference.SourceDigest != convertedRead.ContentDigest || adoptionEvent.ContentRevision == nil || *adoptionEvent.ContentRevision != 1 || adoptionEvent.Reason != v2Adoption.Reason || adoptionEvent.PermissionDecision != "pre-iam-private-api-allow-all" || adoptionEvent.OccurredAt.IsZero() {
		t.Fatalf("v2 historical adoption event = %+v, %v", adoptionEvent, err)
	}
	var migrationEvent TODOHistoryEvent
	if err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: todoContentMigrationEventKind}}).Decode(&migrationEvent); err != nil || migrationEvent.FromContentRevision == nil || *migrationEvent.FromContentRevision != 0 || migrationEvent.FromContentDigest != sourceVersion.Digest || migrationEvent.ToContentDigest != convertedRead.ContentDigest || migrationEvent.ReviewedAcceptance != todoContentMigrationAcceptance || migrationEvent.PermissionDecision != "pre-iam-private-api-allow-all" || migrationEvent.OccurredAt.IsZero() {
		t.Fatalf("governed migration lineage event = %+v, %v", migrationEvent, err)
	}
	// A migration event must not carry v1 approval authority into v2 delivery.
	activeProof := TODOProofReference{Kind: "work-started", Source: "mongo-todo-api", Reference: fmt.Sprintf("boora/%s/content/0/revision/1", record.ID), SourceDigest: sourceVersion.Digest, VerificationState: "verified"}
	activeRecord := convertedRead.TODO
	activeRecord.Stage, activeRecord.StageVerification, activeRecord.VerifiedTransitionRef = "In-Progress", "verified", &activeProof
	if err := requireApprovedCurrentContent(activeRecord, convertedRead.ContentDigest); err == nil {
		t.Fatal("migration-carried v1 approval authorized a new v2 stage")
	}
	// The v2 delivery edge must reject the former v1 approval digest.
	activeMongoSource := TODOSource{Kind: "mongo-content-revision", Ref: fmt.Sprintf("boora/%s/content/0", record.ID), Digest: sourceVersion.Digest}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "id", Value: record.ID}}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "source", Value: activeMongoSource}, {Key: "stage", Value: "In-Progress"}, {Key: "stage_verification", Value: "verified"}, {Key: "verified_transition_ref", Value: activeProof},
	}}}); err != nil {
		t.Fatalf("prepare isolated migrated In-Progress aggregate: %v", err)
	}
	closeout, closeoutErr := store.TransitionTODO(ctx, "belluga-solutions", TODOActionRequest{ProjectID: "boora", TODOID: record.ID, ExpectedRevision: int64Pointer(activeRecord.Revision), OperationID: "pilot-reviewed-conversion-closeout", RequestedStage: "Local-Implemented"})
	if closeoutErr == nil || !strings.Contains(closeoutErr.Error(), "approval/content_revision") {
		t.Fatalf("v2 delivery accepted the prior v1 approval: %+v, %v", closeout, closeoutErr)
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "id", Value: record.ID}}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "source", Value: convertedRead.TODO.Source}, {Key: "stage", Value: convertedRead.TODO.Stage}, {Key: "stage_verification", Value: convertedRead.TODO.StageVerification}, {Key: "verified_transition_ref", Value: convertedRead.TODO.VerifiedTransitionRef},
	}}}); err != nil {
		t.Fatalf("restore historical adoption pilot after isolated transition guard: %v", err)
	}
	// Exercise both lifecycle edges with approval of the current v2 digest.
	approval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: convertedRead.ContentDigest, VerificationState: "verified"}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "id", Value: record.ID}}, bson.D{{Key: "$set", Value: bson.D{
		{Key: "source", Value: activeMongoSource}, {Key: "stage", Value: "Implementation-Approval-Pending"}, {Key: "stage_verification", Value: "verified"}, {Key: "verified_transition_ref", Value: approval},
	}}}); err != nil {
		t.Fatalf("prepare isolated approval-pending migration lineage: %v", err)
	}
	ready, err := store.TransitionTODO(ctx, "belluga-solutions", TODOActionRequest{ProjectID: "boora", TODOID: record.ID, ExpectedRevision: int64Pointer(convertedRead.TODO.Revision), OperationID: "pilot-migration-approval-ready", RequestedStage: "Ready-for-Implementation"})
	if err != nil || ready.Stage != "Ready-for-Implementation" || ready.TODORevision != 3 {
		t.Fatalf("reviewed migration did not preserve frozen approval on Ready edge: %+v, %v", ready, err)
	}
	readyRecord, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || readyRecord.TODO.VerifiedTransitionRef == nil || *readyRecord.TODO.VerifiedTransitionRef != approval {
		t.Fatalf("Ready edge changed the frozen owner approval: %+v, %v", readyRecord.TODO.VerifiedTransitionRef, err)
	}
	started, err := store.TransitionTODO(ctx, "belluga-solutions", TODOActionRequest{ProjectID: "boora", TODOID: record.ID, ExpectedRevision: int64Pointer(ready.TODORevision), OperationID: "pilot-migration-work-start", RequestedStage: "In-Progress"})
	if err != nil || started.Stage != "In-Progress" || started.TODORevision != 4 {
		t.Fatalf("reviewed migration did not permit adjacent work-start edge: %+v, %v", started, err)
	}
	startedRecord, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || startedRecord.TODO.VerifiedTransitionRef == nil || startedRecord.TODO.VerifiedTransitionRef.Kind != "work-started" || startedRecord.TODO.VerifiedTransitionRef.SourceDigest != convertedRead.ContentDigest {
		t.Fatalf("work-start edge did not bind current migrated content: %+v, %v", startedRecord.TODO.VerifiedTransitionRef, err)
	}
	oldVersion, err = store.ReadTODOAsOf(ctx, "belluga-solutions", "boora", record.ID, 0)
	if err != nil || oldVersion.Content.SchemaVersion != 0 || oldVersion.Content.Sections[0].BodyMarkdown != string(fixtureBody) {
		t.Fatalf("governed migration changed immutable v1 history = %+v, %v", oldVersion.Content, err)
	}
	changedContent := converted
	changedContent.Objective += " Later reviewed edit."
	editAfterMigrationEdges := int64(4)
	if _, err := store.EditTODOContent(ctx, "belluga-solutions", TODOContentEditRequest{ProjectID: "boora", TODOID: record.ID, ExpectedRevision: &editAfterMigrationEdges, OperationID: "pilot-later-content-edit", Content: changedContent, Reason: "Exercise adoption retry after a subsequent TODO mutation"}); err != nil {
		t.Fatalf("later content edit: %v", err)
	}
	postEdit, err := store.ReadTODO(ctx, "belluga-solutions", "boora", record.ID, nil)
	if err != nil || postEdit.ContentDigest == convertedRead.ContentDigest || requireApprovedCurrentContentOrMigration(ctx, database, activeRecord, postEdit.ContentDigest) == nil {
		t.Fatalf("ordinary post-migration content edit incorrectly inherited reviewed approval lineage: %+v, %v", postEdit, err)
	}
	migrationRetry, err := store.MigrateTODOContent(ctx, "belluga-solutions", migrationRequest)
	if err != nil || !migrationRetry.NoOp {
		t.Fatalf("exact migration retry after later content edit = %+v, %v", migrationRetry, err)
	}
	changedMigrationRetry := migrationRequest
	changedMigrationRetry.Reason = "Changed request after migration"
	if _, err := store.MigrateTODOContent(ctx, "belluga-solutions", changedMigrationRetry); !errors.Is(err, ErrConflict) {
		t.Fatalf("migration operation ID accepted changed reviewed acceptance: %v", err)
	}
	third, err := store.AdoptHistoricalTODOStages(ctx, "belluga-solutions", v2Adoption)
	if err != nil || !third.NoOp || len(third.Results) != 1 || !third.Results[0].NoOp {
		t.Fatalf("historical adoption retry after later mutation = %+v, %v", third, err)
	}
	bad := request
	bad.OperationID = "pilot-adopt-different"
	bad.Items = append([]HistoricalTODOAdoptionItem(nil), request.Items...)
	bad.Items[0].SourceDigest = "sha256:" + strings.Repeat("a", 64)
	if _, err := store.AdoptHistoricalTODOStages(ctx, "belluga-solutions", bad); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched pinned source accepted: %v", err)
	}
}
