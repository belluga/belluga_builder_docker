package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var selectedTODOIDs = []string{
	"t-9936500c9582e07e", "t-cf5c0ffd07c4f50d", "t-b9e3ea9e3a616b01", "builder-company-tenant-mongo-registry",
}

func validSelectedTODORequest() SelectedTODOImportRequest {
	records := make([]TODORecord, 0, len(selectedTODOIDs))
	for _, id := range selectedTODOIDs {
		contract := selectedTODOSources[id]
		position := contract.Position
		qualifiers := []string{}
		if contract.Qualifier != "" {
			qualifiers = append(qualifiers, contract.Qualifier)
		}
		records = append(records, TODORecord{
			ID: id, ProjectID: contract.ProjectID, ReleaseID: contract.ReleaseID, Position: &position,
			Title: "Fixture title", Objective: "Executor mapped the objective heading without editing its source.",
			NextExactStep: "Executor mapped the next exact step.", ArtifactType: "tactical_execution_contract",
			Stage: contract.Stage, Qualifiers: qualifiers, WorkState: "review",
			Sections:      []TODOSection{{Key: "executor_notes", Title: "Executor Notes", BodyMarkdown: "Preserved authored narrative.", Position: 0}},
			AffectedFiles: nil,
			Source:        TODOSource{Foundation: contract.Foundation, Revision: contract.FoundationSHA, Path: contract.Path, Digest: contract.SourceDigest},
		})
	}
	return SelectedTODOImportRequest{SchemaVersion: todoSchemaVersion, TODOs: records}
}

func TestValidateSelectedTODOImportAcceptsOnlyCanonicalFourRecordSet(t *testing.T) {
	report, err := ValidateSelectedTODOImport(validSelectedTODORequest())
	if err != nil || report.Imported != 4 || report.Digest == "" {
		t.Fatalf("validated import = %+v, %v", report, err)
	}
}

func TestValidateSelectedTODOImportRejectsIdentitySourceAndFieldDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SelectedTODOImportRequest)
	}{
		{"unknown identity", func(request *SelectedTODOImportRequest) { request.TODOs[0].ID = "unselected-todo" }},
		{"duplicate identity", func(request *SelectedTODOImportRequest) { request.TODOs[1].ID = request.TODOs[0].ID }},
		{"project mismatch", func(request *SelectedTODOImportRequest) { request.TODOs[0].ProjectID = "builder" }},
		{"release mismatch", func(request *SelectedTODOImportRequest) { request.TODOs[0].ReleaseID = "r-08" }},
		{"position mismatch", func(request *SelectedTODOImportRequest) { *request.TODOs[0].Position++ }},
		{"stage mismatch", func(request *SelectedTODOImportRequest) { request.TODOs[0].Stage = "invented" }},
		{"source revision mismatch", func(request *SelectedTODOImportRequest) { request.TODOs[0].Source.Revision = "deadbeef" }},
		{"source digest mismatch", func(request *SelectedTODOImportRequest) {
			request.TODOs[0].Source.Digest = "sha256:" + strings.Repeat("0", 64)
		}},
		{"duplicate qualifier", func(request *SelectedTODOImportRequest) {
			request.TODOs[0].Qualifiers = []string{"Provisional", "Provisional"}
		}},
		{"verified claim copied at import", func(request *SelectedTODOImportRequest) {
			request.TODOs[1].ProofReferences = []TODOProofReference{{Kind: "test", Source: "source", Reference: "ref", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "verified"}}
		}},
		{"oversized section", func(request *SelectedTODOImportRequest) {
			request.TODOs[0].Sections[0].BodyMarkdown = strings.Repeat("x", 65<<10)
		}},
		{"objective exceeds exact-read limit", func(request *SelectedTODOImportRequest) {
			request.TODOs[0].Objective = strings.Repeat("x", 1025)
		}},
		{"next exact step exceeds exact-read limit", func(request *SelectedTODOImportRequest) {
			request.TODOs[0].NextExactStep = strings.Repeat("x", 513)
		}},
		{"agent supplied derived files", func(request *SelectedTODOImportRequest) { request.TODOs[1].AffectedFiles = []string{"go-app/file.go"} }},
		{"agent supplied stage verification", func(request *SelectedTODOImportRequest) { request.TODOs[1].StageVerification = "verified" }},
		{"missing record", func(request *SelectedTODOImportRequest) { request.TODOs = request.TODOs[:3] }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := validSelectedTODORequest()
			test.mutate(&request)
			if _, err := ValidateSelectedTODOImport(request); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestGovernanceHeadingsRemainNarrativeAndDoNotOverrideTypedFields(t *testing.T) {
	request := validSelectedTODORequest()
	record := &request.TODOs[0]
	record.Sections = []TODOSection{{
		Key: "delivery_status_canon", Title: "Delivery Status Canon",
		BodyMarkdown: "- **Current delivery stage:** Completed\n- **Qualifiers:** Blocked",
		Position:     0,
	}}
	stage, qualifierCount := record.Stage, len(record.Qualifiers)
	if _, err := ValidateSelectedTODOImport(request); err != nil {
		t.Fatalf("governance heading preserved as source narrative: %v", err)
	}
	if record.Stage != stage || len(record.Qualifiers) != qualifierCount {
		t.Fatalf("narrative changed typed authority: stage=%q qualifiers=%v", record.Stage, record.Qualifiers)
	}
}

func TestSelectedTODOExecutorMappingRequiresReviewAgainstPinnedRealSources(t *testing.T) {
	root := os.Getenv("BELLUGA_ECOSYSTEM_ROOT")
	if root == "" {
		t.Skip("set BELLUGA_ECOSYSTEM_ROOT to validate the pinned Foundation source files")
	}
	type preparedMapping struct {
		title, objective, nextStep, stage, qualifier, workState string
		sectionTitle, sectionBody                               string
	}
	prepared := map[string]preparedMapping{
		"t-9936500c9582e07e": {
			title: "TODO v0.7.1: Participant Media and Stage Domain Release Package", objective: "Coordinate two independent children:",
			nextStep: "Confirm Builder release assignment for the new stage-domain Flutter child; the participant-media child proceeds on its existing Laravel PR, while the Flutter child awaits its own approval.",
			stage:    "Planning", qualifier: "Provisional", workState: "review", sectionTitle: "Exclusions",
			sectionBody: "- `todos/active/v0.7.0/TODO-v0.7.0-docker-php-8-4-critical-security-update.md` remains in `v0.7.0` by the user's explicit correction on 2026-10-24.\n- `v0.8.0` Account Workspace and `v0.8.1` platform follow-up retain their separate scopes and order.",
		},
		"t-cf5c0ffd07c4f50d": {
			title:     "TODO v0.7.1: Expose public avatar URLs in event participant tabs",
			objective: "A publicly permitted participant avatar stored as a canonical hostless path is returned by the event members API as an absolute URL on the current request host, preserving its version query; Flutter can render it without a parser change.",
			nextStep:  "inspect CI on Laravel PR #344 from `v0.7.1-rc` into `dev`; if a job fails, reproduce its owning file locally, fix and push; if green, finish the Builder local checkpoint and promotion disposition.",
			stage:     "In-Progress", qualifier: "none", workState: "review", sectionTitle: "Target Outcome and Scope",
			sectionBody: "- A publicly permitted participant avatar stored as a canonical hostless path is returned by the event members API as an absolute URL on the current request host, preserving its version query; Flutter can render it without a parser change.\n- Apply the same URL contract to a publicly permitted cover, while preserving `null` and already absolute external HTTP(S) URLs.\n- Preserve public profile/account eligibility, pagination, ordering, cursor, search and `public_detail_path`. This is a response serialization fix, not a participant relationship change.\n- Keep image storage hostless. Reuse the existing account-profile public-media normalizer; add no Flutter fallback, new service, request per participant, or endpoint-specific URL normalization rule.",
		},
		"t-b9e3ea9e3a616b01": {
			title:     "TODO v0.7.1: Point the Flutter stage build at bellugademo.site",
			objective: "Change the Flutter stage landlord define and set the approved Flutter version to `0.7.1+30`, then push `v0.7.1-rc` and open a PR to `dev`.",
			nextStep:  "refresh Project API status for this exact TODO, then request the governed `Completed` transition if production-publication proof and completion guards pass; do not edit the delivery-stage field directly.",
			stage:     "Completed", qualifier: "none", workState: "review", sectionTitle: "Implementation Intent",
			sectionBody: "- **Current delivery:** Change the Flutter stage landlord define and set the approved Flutter version to `0.7.1+30`, then push `v0.7.1-rc` and open a PR to `dev`. The workflow gate checks semantic version `0.7.1` against the RC branch; `+30` is the user-approved sequential build number, not a gate predicate.\n- **Planned next steps:** none.\n- **Anticipatory implementation authorized now:** none.\n- **Rationale:** Keep this delivery to the approved source diff and PR boundary. Do not merge or promote it, publish a stage artifact, resubmit Docker, or claim runtime validation here.",
		},
		"builder-company-tenant-mongo-registry": {
			title:     "TODO: Company tenants and Project registry in Builder MongoDB",
			objective: "This TODO establishes Company identity, Tenant resolution, per-Company database selection, Project registration/query authority, and Mongo-backed Roadmap reads for every registered Project.",
			nextStep:  "Prepare the separate TODO migration contract to include this Builder TODO by stable ID after Builder's Epic/Release relationship exists in Mongo; then retry its governed `Local-Implemented` transition. The Mongo registry cut is validated locally, but this document does not claim that stage now.",
			stage:     "In-Progress", qualifier: "none", workState: "implementation", sectionTitle: "Contract Boundary",
			sectionBody: "This TODO establishes Company identity, Tenant resolution, per-Company database selection, Project registration/query authority, and Mongo-backed Roadmap reads for every registered Project. Only Bóora receives Epic/Release records in this cut: its existing Epics and the `v0.7.1` Release with an empty TODO list. The original file records and Foundation Roadmaps are retained unchanged but are no longer Roadmap API read authorities. This is temporary physical duplication, **not dual write or a merge of two Roadmaps**. It does not migrate TODOs or give the public panel write permissions. MongoDB credentials and database names remain server-side. A domain or app-domain identifies a Tenant but does not authenticate or authorize a person.",
		},
	}
	request := validSelectedTODORequest()
	for i := range request.TODOs {
		id := request.TODOs[i].ID
		mapping := prepared[id]
		request.TODOs[i].Title = mapping.title
		request.TODOs[i].Objective = mapping.objective
		request.TODOs[i].NextExactStep = mapping.nextStep
		request.TODOs[i].Stage = mapping.stage
		request.TODOs[i].WorkState = mapping.workState
		request.TODOs[i].Sections = []TODOSection{{Key: "preserved_" + id, Title: mapping.sectionTitle, BodyMarkdown: mapping.sectionBody, Position: 0}}
		request.TODOs[i].Qualifiers = []string{}
		if mapping.qualifier != "none" {
			request.TODOs[i].Qualifiers = []string{mapping.qualifier}
		}
	}
	for _, id := range selectedTODOIDs {
		contract := selectedTODOSources[id]
		mapping := prepared[id]
		var sourceRoot string
		if contract.Foundation == "boora" {
			sourceRoot = filepath.Join(root, "belluga_now_docker/foundation_documentation")
		} else {
			sourceRoot = filepath.Join(root, "belluga_builder_docker/belluga_builder_foundation_documentation")
		}
		content, err := os.ReadFile(filepath.Join(sourceRoot, filepath.FromSlash(contract.Path)))
		if err != nil {
			t.Fatalf("read pinned source for %s: %v", id, err)
		}
		digest := sha256.Sum256(content)
		if "sha256:"+hex.EncodeToString(digest[:]) != contract.SourceDigest {
			t.Fatalf("source digest changed for %s", id)
		}
		body := string(content)
		var mapped *TODORecord
		for i := range request.TODOs {
			if request.TODOs[i].ID == id {
				mapped = &request.TODOs[i]
				break
			}
		}
		if mapped == nil || !strings.Contains(body, "# "+mapping.title) || !strings.Contains(body, mapping.objective) || !strings.Contains(body, "**Artifact type:** `tactical_execution_contract`") || !strings.Contains(body, "**Current delivery stage:** "+mapping.stage) || !strings.Contains(body, "**Qualifiers:** "+mapping.qualifier) || !strings.Contains(body, "**Next exact step:** "+mapping.nextStep) || !strings.Contains(body, "**Work state:** "+mapping.workState) {
			t.Fatalf("prepared canonical field mapping no longer matches exact pinned source values for %s; mapping review required", id)
		}
		if len(mapped.Sections) != 1 || mapped.Sections[0].Title != mapping.sectionTitle || fixtureMarkdownSection(body, mapping.sectionTitle) != mapped.Sections[0].BodyMarkdown {
			t.Fatalf("prepared narrative section differs from exact pinned source section for %s; mapping review required", id)
		}
	}
	if _, err := ValidateSelectedTODOImport(request); err != nil {
		t.Fatalf("prepared canonical JSON rejected after source mapping review: %v", err)
	}
}

// fixtureMarkdownSection is test-only: production Go accepts canonical typed
// JSON from the executor and never interprets Markdown.
func fixtureMarkdownSection(document, title string) string {
	marker := "## " + title + "\n"
	sectionStart := strings.Index(document, marker)
	if sectionStart < 0 {
		return ""
	}
	sectionStart += len(marker)
	end := strings.Index(document[sectionStart:], "\n## ")
	if end >= 0 {
		return strings.TrimSpace(document[sectionStart : sectionStart+end])
	}
	return strings.TrimSpace(document[sectionStart:])
}

func TestTODOCollectionOptionsRequireStrictErrorValidation(t *testing.T) {
	options := TODOCollectionOptions()
	if valueAt(options, "validationLevel") != "strict" || valueAt(options, "validationAction") != "error" {
		t.Fatalf("unexpected collection validation policy: %#v", options)
	}
	validator, ok := valueAt(options, "validator").(bson.D)
	if !ok {
		t.Fatalf("validator has unexpected type %T", valueAt(options, "validator"))
	}
	schema, ok := valueAt(validator, "$jsonSchema").(bson.D)
	if !ok || valueAt(schema, "additionalProperties") != false {
		t.Fatalf("schema is not closed: %#v", validator)
	}
	properties, ok := valueAt(schema, "properties").(bson.D)
	if !ok {
		t.Fatalf("properties has unexpected type %T", valueAt(schema, "properties"))
	}
	stage, _ := valueAt(properties, "stage").(bson.D)
	stageVerification, _ := valueAt(properties, "stage_verification").(bson.D)
	qualifiers, _ := valueAt(properties, "qualifiers").(bson.D)
	if valueAt(stage, "enum") == nil || valueAt(stageVerification, "enum") == nil || valueAt(qualifiers, "uniqueItems") != true {
		t.Fatal("stage/stage verification enum or unique qualifier constraint is missing")
	}
	affectedFiles, _ := valueAt(properties, "affected_files").(bson.D)
	if got := valueAt(affectedFiles, "bsonType"); !bsonEqual(got, bson.A{"array", "null"}) {
		t.Fatalf("affected_files must preserve null versus array: %#v", got)
	}
}

func TestMongoSelectedTODOImportIsAtomicIdempotentAndNoOverwrite(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("todo_import_%d", time.Now().UnixNano())
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
	company, err := normalizeCompany(Company{ID: "belluga-solutions", Name: "Belluga Solutions", DatabaseName: databaseName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
		t.Fatal(err)
	}
	database := store.client.Database(databaseName)
	for _, id := range []string{"boora", "builder"} {
		project := Project{ID: id, CompanyID: "belluga-solutions", Name: id, Binding: ptrTestBinding(validTestBinding()), RegisteredAt: "2026-10-31T00:00:00Z", PreparationState: "not_ready"}
		document, err := projectToDocument(project, bson.NewObjectID())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Collection(projectsCollection).InsertOne(ctx, document); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ProvisionTODOCollection(ctx, "belluga-solutions"); err != nil {
		t.Fatalf("provision TODO collection: %v", err)
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		t.Fatalf("validator readback: %v", err)
	}
	if err := verifyTODOIndexes(ctx, database.Collection(todosCollection)); err != nil {
		t.Fatalf("index readback: %v", err)
	}
	invalid := bson.M{"_id": bson.NewObjectID(), "id": "invalid", "project_id": "boora", "stage": "Invented"}
	if _, err := database.Collection(todosCollection).InsertOne(ctx, invalid); err == nil {
		t.Fatal("strict validator accepted incomplete document with invented stage")
	}

	request := validSelectedTODORequest()
	if _, err := store.ImportSelectedTODOs(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("import without Bóora Release = %v, want conflict", err)
	}
	for _, collectionName := range []string{epicsCollection, releasesCollection} {
		count, err := database.Collection(collectionName).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}})
		if err != nil || count != 0 {
			t.Fatalf("failed transaction left Builder hierarchy in %s: count=%d err=%v", collectionName, count, err)
		}
	}
	booraEpic := roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "boora", ID: "epic-pilot", Name: "APP Público Piloto (Guarappari)", Position: 1}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, booraEpic); err != nil {
		t.Fatal(err)
	}
	booraRelease := roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "boora", ID: "release-v0-7-1-participant-media", EpicID: "epic-pilot", Name: "v0.7.1 — participant media", Position: 0}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, booraRelease); err != nil {
		t.Fatal(err)
	}
	report, err := store.ImportSelectedTODOs(ctx, request)
	if err != nil || report.NoOp || report.Imported != 4 {
		t.Fatalf("first selected import = %+v, %v", report, err)
	}
	report, err = store.ImportSelectedTODOs(ctx, request)
	if err != nil || !report.NoOp || report.Imported != 4 {
		t.Fatalf("repeat selected import = %+v, %v", report, err)
	}
	count, err := database.Collection(todosCollection).CountDocuments(ctx, bson.D{})
	if err != nil || count != 4 {
		t.Fatalf("selected TODO count = %d, %v; want 4", count, err)
	}
	initialRevision := int64(0)
	read, err := store.ReadTODO(ctx, "belluga-solutions", "builder", "builder-company-tenant-mongo-registry", &initialRevision)
	if err != nil || read.TODORevision != 0 || read.TODO.StageVerification != "unverified" {
		t.Fatalf("exact imported TODO read = %+v, %v", read, err)
	}
	staleRevision := int64(9)
	if _, err := store.ReadTODO(ctx, "belluga-solutions", "builder", "builder-company-tenant-mongo-registry", &staleRevision); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale exact TODO read = %v, want revision conflict", err)
	} else if got, ok := TODOConflictRevision(err); !ok || got != 0 {
		t.Fatalf("stale exact TODO revision = %d, %t; want fresh revision 0", got, ok)
	}
	var builderRoadmap struct {
		Epics []struct {
			Releases []struct {
				ID    string            `json:"id"`
				TODOs []roadmapTODOView `json:"todos"`
			} `json:"releases"`
		} `json:"epics"`
	}
	roadmap, err := store.Roadmap(ctx, "builder", "belluga-solutions")
	if err != nil || json.Unmarshal(roadmap, &builderRoadmap) != nil || len(builderRoadmap.Epics) != 1 || len(builderRoadmap.Epics[0].Releases) != 1 || len(builderRoadmap.Epics[0].Releases[0].TODOs) != 1 {
		t.Fatalf("Builder Project Roadmap did not include the migrated TODO: %s, %v", roadmap, err)
	}
	listed := builderRoadmap.Epics[0].Releases[0].TODOs[0]
	if listed.ID != "builder-company-tenant-mongo-registry" || listed.TODORevision != 0 || listed.AuthoredDeliveryStage != "In-Progress" || listed.DeliveryStage != "" || listed.DeliveryStageState != "unverified" || !strings.HasPrefix(listed.DeliveryStageTeach, "TEACH:") {
		t.Fatalf("unverified Builder Roadmap summary = %+v", listed)
	}
	var booraRoadmap struct {
		Epics []struct {
			Releases []struct {
				TODOs []roadmapTODOView `json:"todos"`
			} `json:"releases"`
		} `json:"epics"`
	}
	roadmap, err = store.Roadmap(ctx, "boora", "belluga-solutions")
	if err != nil || json.Unmarshal(roadmap, &booraRoadmap) != nil || len(booraRoadmap.Epics) != 1 || len(booraRoadmap.Epics[0].Releases) != 1 || len(booraRoadmap.Epics[0].Releases[0].TODOs) != 3 {
		t.Fatalf("Bóora Project Roadmap did not include its three migrated TODOs: %s, %v", roadmap, err)
	}
	completed := booraRoadmap.Epics[0].Releases[0].TODOs[2]
	if completed.AuthoredDeliveryStage != "Completed" || completed.DeliveryStage != "" || completed.DeliveryStageState != "unverified" || !strings.HasPrefix(completed.DeliveryStageTeach, "TEACH:") {
		t.Fatalf("unverified authored Completed stage became effective: %+v", completed)
	}
	zeroRevision := int64(0)
	transition := TODOActionRequest{ProjectID: "builder", TODOID: "builder-company-tenant-mongo-registry", ExpectedRevision: &zeroRevision, OperationID: "transition-001", RequestedStage: "Local-Implemented", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	if _, err := store.TransitionTODO(ctx, "belluga-solutions", transition); err == nil || !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "proof guard defined and approved in a later TODO") || !strings.Contains(err.Error(), "no write occurred") {
		t.Fatalf("unguarded stage transition = %v, want actionable fail-closed TEACH", err)
	}
	staleTransition := transition
	staleTransition.OperationID = "transition-stale"
	staleRevision = int64(1)
	staleTransition.ExpectedRevision = &staleRevision
	if _, err := store.TransitionTODO(ctx, "belluga-solutions", staleTransition); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale stage transition = %v, want conflict", err)
	} else if got, ok := TODOConflictRevision(err); !ok || got != 0 {
		t.Fatalf("stale transition revision = %d, %t; want fresh revision 0", got, ok)
	}
	invalidJump := transition
	invalidJump.OperationID = "transition-jump"
	invalidJump.RequestedStage = "Completed"
	if _, err := store.TransitionTODO(ctx, "belluga-solutions", invalidJump); err == nil || !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "proof guard defined and approved in a later TODO") || !strings.Contains(err.Error(), "no write occurred") {
		t.Fatalf("guardless nonadjacent stage transition = %v, want fail-closed TEACH", err)
	}
	verify := VerifyCurrentTODOStageRequest{ProjectID: transition.ProjectID, TODOID: transition.TODOID, ExpectedRevision: &zeroRevision, OperationID: "verify-001"}
	if _, err := store.VerifyCurrentTODOStage(ctx, "belluga-solutions", verify); err == nil || !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "proof guard defined and approved in a later TODO") || !strings.Contains(err.Error(), "no write occurred") {
		t.Fatalf("untrusted direct current-stage verification = %v, want actionable fail-closed TEACH", err)
	}
	read, err = store.ReadTODO(ctx, "belluga-solutions", "builder", transition.TODOID, &zeroRevision)
	if err != nil || read.TODO.ID != transition.TODOID || read.TODO.Stage != "In-Progress" || read.TODO.StageVerification != "unverified" || read.TODORevision != 0 {
		t.Fatalf("direct verify attempt changed TODO authority: %+v, %v", read, err)
	}
	var before TODORecord
	if err := database.Collection(todosCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}}).Decode(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "_id", Value: before.ObjectID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "objective", Value: "newer Mongo revision"}, {Key: "revision", Value: int64(2)}}}}); err != nil {
		t.Fatal(err)
	}
	report, err = store.ImportSelectedTODOs(ctx, request)
	if err != nil || !report.NoOp {
		t.Fatalf("repeat after post-cutover revision = %+v, %v", report, err)
	}
	var after TODORecord
	if err := database.Collection(todosCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}}).Decode(&after); err != nil {
		t.Fatal(err)
	}
	if after.Objective != "newer Mongo revision" || after.Revision != 2 {
		t.Fatalf("repeat import overwrote newer Mongo state: %+v", after)
	}
	changed := validSelectedTODORequest()
	changed.TODOs[3].Objective = "changed mapping under old source digest"
	if _, err := store.ImportSelectedTODOs(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed typed mapping with pinned source accepted: %v", err)
	}
}

func valueAt(document bson.D, key string) any {
	for _, element := range document {
		if element.Key == key {
			return element.Value
		}
	}
	return nil
}

func bsonEqual(left, right any) bool {
	leftBytes, leftErr := bson.Marshal(bson.D{{Key: "value", Value: left}})
	rightBytes, rightErr := bson.Marshal(bson.D{{Key: "value", Value: right}})
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}
