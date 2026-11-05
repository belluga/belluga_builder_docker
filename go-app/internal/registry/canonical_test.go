package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func canonicalTestRecord(family CanonicalFamily, id string, payload string) CanonicalImportRecord {
	return CanonicalImportRecord{Family: family, Record: CanonicalRecord{ID: id, ProjectID: "builder", GenerationID: "g1", Revision: 1, Payload: json.RawMessage(payload)}}
}

func TestCanonicalTemplateDescribesCompleteTypedPayloadsAndDraftLimit(t *testing.T) {
	template := CanonicalTemplate()
	types := template["payload_types"].(map[string]any)
	fields := template["type_fields"].(map[string]map[string]string)
	module := fields[types[string(FamilyModule)].(string)]
	for _, name := range []string{"purpose", "decisions", "interfaces", "workflows", "scope", "exclusions", "dependencies", "routes", "schemas", "invariants", "local_operations", "events", "jobs", "observability", "verification"} {
		if module[name] == "" {
			t.Fatalf("module field %s absent from public template", name)
		}
	}
	if fields["CanonicalField"]["fields"] == "" || fields["CanonicalDecision"]["evidence"] == "" {
		t.Fatal("nested typed contracts absent from public template")
	}
	if fields["DomainCatalogPayload"]["qa_validation_outline"] == "" || fields["MandatePayload"]["business_thesis"] == "" || fields["StrategicRoadmapPayload"]["epic_candidates"] == "" {
		t.Fatal("source corpus has no complete typed field map")
	}
	guidance := strings.Join(template["migration_guidance"].([]string), " ")
	if !strings.Contains(guidance, "does not prove source-content completeness") || !strings.Contains(guidance, "before activation") {
		t.Fatal("public template must distinguish draft admission from complete migration")
	}
	if template["migration_id_policy"].(map[string]string)["output"] == "" {
		t.Fatal("missing source-ID migration policy")
	}
	guides := template["object_guides"].(map[string]any)
	moduleGuide := guides[string(FamilyModule)].(map[string]any)
	if moduleGuide["purpose"] == "" || len(moduleGuide["required_fields"].([]string)) == 0 || moduleGuide["accepted_fields"].(map[string]string)["decisions"] == "" {
		t.Fatalf("public template lacks positive module guidance: %+v", moduleGuide)
	}
}

func TestCanonicalFamilyPayloadValidationIsTypedAndMarkdownFree(t *testing.T) {
	cases := []struct {
		family  CanonicalFamily
		payload string
		valid   bool
	}{
		{FamilyMandate, `{"purpose":"Build","scope":["web"]}`, true},
		{FamilyDomain, `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`, true},
		{FamilyDomain, `{"entities":[{"id":"Project","name":"Project","description":""},{"id":"Project","name":"Duplicate","description":""}],"relations":[],"distinctions":[]}`, false},
		{FamilyDomain, `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"},{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`, false},
		{FamilyDomain, `{"entities":[{"id":"Project!","name":"Project","description":""}],"relations":[],"distinctions":[]}`, false},
		{FamilyConstitution, `{"promotion_mode":"mvp","capability_namespaces":["go"],"rules":[{"id":"tenant-access","status":"current","statement":"Scope reads"}]}`, true},
		{FamilyRoadmap, `{"stages":[{"id":"launch","name":"Launch","status":"planned"}]}`, true},
		{FamilyModule, `{"purpose":"Owns project work","decisions":[{"id":"ownership","status":"current","statement":"Module owns its routes"}]}`, true},
		{FamilyEndpoint, `{"method":"GET","route":"/projects","host_scope":"api","module_id":"projects","request":[],"response":[]}`, true},
		{FamilyEndpoint, `{"status":"draft","method":"GET","route":"/projects","host_scope":"api","module_id":"projects","request":[],"response":[],"behavior_rules":[{"id":"fallback","statement":"Redirect with a deterministic fallback"}]}`, true},
		{FamilyPolicy, `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"ownership"}]}`, true},
		{FamilyMandate, `{"purpose":"x","scope":[],"sections":[]}`, false},
		{FamilyMandate, `# Markdown is deliberately not parsed`, false},
		{FamilyPolicy, `{"rules":[{"id":"x","status":"open","statement":"Unknown status"}],"ownership":[]}`, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.family)+fmt.Sprint(tc.valid), func(t *testing.T) {
			record := CanonicalRecord{ID: "stable", ProjectID: "builder", GenerationID: "g1", Revision: 1, Payload: json.RawMessage(tc.payload)}
			err := validateCanonicalRecord("builder", "g1", tc.family, record)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	record := CanonicalRecord{ID: "hidden", ProjectID: "builder", GenerationID: "g1", Revision: 1, Deleted: true, Payload: json.RawMessage(`{"purpose":"Hidden","scope":["web"]}`)}
	if err := validateCanonicalRecord("builder", "g1", FamilyMandate, record); !errors.Is(err, ErrInvalid) {
		t.Fatalf("content write accepted tombstone: %v", err)
	}
}

func TestCanonicalDigestNormalizesJSONObjectOrder(t *testing.T) {
	if canonicalDigest(json.RawMessage(`{"a":1,"b":{"c":2}}`)) != canonicalDigest(json.RawMessage(`{"b":{"c":2},"a":1}`)) {
		t.Fatal("semantically equal payloads produced different digests")
	}
}

func TestCanonicalHistoryCursorRoundTripsTimestampAndStableID(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 30, 0, 123000000, time.UTC)
	id := bson.ObjectID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	cursor := encodeCanonicalHistoryCursor("company-a", "project-a", at, id)
	gotTime, gotID, err := decodeCanonicalHistoryCursor(cursor, "company-a", "project-a")
	if err != nil || !gotTime.Equal(at) || gotID != id {
		t.Fatalf("cursor %q decoded as %s/%s, %v", cursor, gotTime, gotID.Hex(), err)
	}
	for _, invalid := range []string{"not-base64!", "", "eHk", encodeCanonicalHistoryCursor("company-a", "project-a", at, id) + "="} {
		if _, _, err := decodeCanonicalHistoryCursor(invalid, "company-a", "project-a"); err == nil {
			t.Fatalf("invalid cursor %q was accepted", invalid)
		}
	}
	if _, _, err := decodeCanonicalHistoryCursor(cursor, "company-a", "project-b"); err == nil {
		t.Fatal("cursor from another Project was accepted")
	}
	if _, _, err := decodeCanonicalHistoryCursor(cursor, "company-b", "project-a"); err == nil {
		t.Fatal("cursor from another Company was accepted")
	}
}

func TestCanonicalTEACHReturnsFieldRecordAcceptedValuesAndNextAction(t *testing.T) {
	response := httptest.NewRecorder()
	handler := &Handler{}
	handler.writeError(response, teachCanonical("module-a", "payload.decisions.status", "use an accepted decision status", ErrInvalid))
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 400 || body.TeachDetail == nil || body.TeachDetail.Code != "canonical_validation" || body.TeachDetail.FieldPath != "payload.decisions.status" || body.TeachDetail.RecordID != "module-a" || len(body.TeachDetail.AcceptedValues) != 5 || body.TeachDetail.NextAction == "" {
		t.Fatalf("TEACH response=%+v status=%d", body, response.Code)
	}
}

func TestCanonicalUnknownFieldTEACHReportsAcceptedAndNotPersistedFields(t *testing.T) {
	var module ModulePayload
	err := decodeCanonicalPayload("module-a", json.RawMessage(`{"purpose":"Owner","screens":[]}`), &module)
	var teach *canonicalTeachError
	if !errors.As(err, &teach) || teach.Diagnostic.ObjectPurpose == "" || len(teach.Diagnostic.AcceptedFields) == 0 || len(teach.Diagnostic.NotPersisted) != 1 || teach.Diagnostic.NotPersisted[0] != "screens" {
		t.Fatalf("unknown field guidance incomplete: %v", err)
	}
	var endpoint CanonicalEndpointPayload
	err = decodeCanonicalPayload("endpoint-a", json.RawMessage(`{"method":"GET","route":"/x","host_scope":"api","module_id":"projects","request":[{"name":"id","type":"string","invented":"x"}],"response":[]}`), &endpoint)
	if !errors.As(err, &teach) || teach.Diagnostic.FieldPath != "payload.request[0].invented" || teach.Diagnostic.ObjectPurpose != "Typed canonical knowledge object" || len(teach.Diagnostic.NotPersisted) != 1 || teach.Diagnostic.NotPersisted[0] != "invented" {
		t.Fatalf("nested unknown field did not identify its owning object: %v", err)
	}
}

func TestCanonicalImportEnvelopeTEACHNamesInvalidField(t *testing.T) {
	base := CanonicalImport{ProjectID: "builder", GenerationID: "g1", OperationID: "import-1", SourceRevision: "6dd3f957b9bbcdb8e3d6131c3e6f22ea5d6865b7", Actor: "migration-agent", Records: []CanonicalImportRecord{{}}}
	tests := []struct {
		name  string
		field string
		edit  func(*CanonicalImport)
	}{
		{"records", "records", func(v *CanonicalImport) { v.Records = nil }},
		{"generation", "generation_id", func(v *CanonicalImport) { v.GenerationID = "" }},
		{"operation", "operation_id", func(v *CanonicalImport) { v.OperationID = "" }},
		{"source", "source_revision", func(v *CanonicalImport) { v.SourceRevision = "" }},
		{"actor", "actor", func(v *CanonicalImport) { v.Actor = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.edit(&input)
			_, err := (*MongoStore)(nil).ImportCanonicalDraft(context.Background(), "company-canonical", input)
			var teach *canonicalTeachError
			if !errors.As(err, &teach) || teach.Diagnostic.FieldPath != tc.field || teach.Diagnostic.NextAction == "" {
				t.Fatalf("expected %s TEACH, got %v", tc.field, err)
			}
		})
	}
}

func TestCanonicalImportRequestDecodeTEACHNamesUnknownField(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"project_id":"boora","unexpected":"value"}`))
	var input CanonicalImport
	if (&Handler{}).decodeCanonicalRequest(response, request, &input) {
		t.Fatal("unknown request field was accepted")
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 400 || body.TeachDetail == nil || body.TeachDetail.FieldPath != "request.unexpected" {
		t.Fatalf("unexpected TEACH: %+v", body)
	}
}

func TestCanonicalEndpointRouteTEACHNamesRecord(t *testing.T) {
	_, err := normalizeCanonicalPayload(FamilyEndpoint, "endpoint-a", json.RawMessage(`{"method":"GET","route":"/events?after=1","host_scope":"api","module_id":"events"}`))
	var teach *canonicalTeachError
	if !errors.As(err, &teach) || teach.Diagnostic.RecordID != "endpoint-a" || teach.Diagnostic.FieldPath != "payload.route" {
		t.Fatalf("expected exact endpoint route TEACH, got %v", err)
	}
}

func TestCanonicalImportRecordTEACHNamesMissingIdentityAndRevision(t *testing.T) {
	record := CanonicalRecord{ID: "boora", ProjectID: "boora", Payload: json.RawMessage(`{"purpose":"Project","scope":["web"]}`)}
	err := validateCanonicalRecord("boora", "draft-1", FamilyMandate, record)
	var teach *canonicalTeachError
	if !errors.As(err, &teach) || teach.Diagnostic.FieldPath != "record.generation_id" {
		t.Fatalf("expected generation TEACH, got %v", err)
	}
	record.GenerationID = "draft-1"
	err = validateCanonicalRecord("boora", "draft-1", FamilyMandate, record)
	if !errors.As(err, &teach) || teach.Diagnostic.FieldPath != "record.revision" {
		t.Fatalf("expected revision TEACH, got %v", err)
	}
}

func TestMongoCanonicalKnowledgeDraftActivationHistoryAndCAS(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("canonical_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-canonical")).Drop(context.Background())
		_ = store.client.Database(store.tenantDatabase("company-other")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := store.Register(ctx, Registration{CompanyID: "company-canonical", CompanyName: "Canonical", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.CanonicalManifest(ctx, "company-canonical", "builder", "")
	if err != nil || pending.State != "pending" || pending.Active || pending.TeachDetail == nil || pending.TeachDetail.Code != "canonical_pending" || len(pending.Documents) != 0 {
		t.Fatalf("pending manifest=%+v, %v", pending, err)
	}
	handler := NewHandler(store)
	base := "/internal/v1/companies/company-canonical/projects/builder/knowledge/"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", base+"project_mandate/builder", nil))
	var pendingBody errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &pendingBody); err != nil || response.Code != 404 || pendingBody.State != "pending" || pendingBody.TeachDetail == nil || pendingBody.TeachDetail.Code != "canonical_pending" {
		t.Fatalf("pending read=%s status=%d error=%v", response.Body, response.Code, err)
	}
	input := CanonicalImport{ProjectID: "builder", GenerationID: "g1", SourceRevision: "6dd3f957b9bbcdb8e3d6131c3e6f22ea5d6865b7", OperationID: "import-1", Actor: "migration-agent", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyMandate, "builder", `{"purpose":"Build","scope":["web"]}`),
		canonicalTestRecord(FamilyDomain, "builder", `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`),
		canonicalTestRecord(FamilyConstitution, "builder", `{"promotion_mode":"mvp","capability_namespaces":["go"],"rules":[{"id":"tenant-access","status":"current","statement":"Scope reads"}]}`),
		canonicalTestRecord(FamilyRoadmap, "builder", `{"stages":[{"id":"launch","name":"Launch","status":"planned"}]}`),
		canonicalTestRecord(FamilyModule, "projects", `{"purpose":"Owns project work","decisions":[{"id":"DEC-ART-01","status":"current","statement":"Module owns routes"}],"schemas":[{"id":"project","name":"projects","kind":"collection","fields":[{"name":"name","type":"string"}]}]}`),
		canonicalTestRecord(FamilyPolicy, "access-policy", `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`),
	}}
	for i := range input.Records {
		input.Records[i].Record.ProjectID = "builder"
		input.Records[i].Record.GenerationID = "g1"
	}
	draft, err := store.ImportCanonicalDraft(ctx, "company-canonical", input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Active || len(draft.Documents) != 6 {
		t.Fatalf("draft manifest=%+v", draft)
	}
	if repeated, e := store.ImportCanonicalDraft(ctx, "company-canonical", input); e != nil || len(repeated.Documents) != 6 {
		t.Fatalf("idempotent draft import=%+v, %v", repeated, e)
	}
	duplicate := CanonicalImport{ProjectID: "builder", GenerationID: "g1", SourceRevision: input.SourceRevision, OperationID: "import-2", Actor: "migration-agent", Records: []CanonicalImportRecord{canonicalTestRecord(FamilyMandate, "builder", `{"purpose":"Changed after import","scope":["web"]}`)}}
	duplicate.Records[0].Record.ProjectID = "builder"
	duplicate.Records[0].Record.GenerationID = "g1"
	if _, err := store.ImportCanonicalDraft(ctx, "company-canonical", duplicate); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already exists in the draft") {
		t.Fatalf("duplicate family/id should require a new draft generation: %v", err)
	}
	draftRead, err := store.ReadCanonicalDraft(ctx, "company-canonical", "builder", "g1", FamilyModule, "projects")
	if err != nil || draftRead.GenerationID != "g1" || !strings.Contains(string(draftRead.Payload), "DEC-ART-01") {
		t.Fatalf("draft readback lost typed content: %+v, %v", draftRead, err)
	}
	draftHTTP := httptest.NewRecorder()
	handler.ServeHTTP(draftHTTP, httptest.NewRequest("GET", base+"module/projects?generation_id=g1", nil))
	if draftHTTP.Code != 200 || !strings.Contains(draftHTTP.Body.String(), "DEC-ART-01") {
		t.Fatalf("private draft readback=%d %s", draftHTTP.Code, draftHTTP.Body.String())
	}
	wrongDraft := httptest.NewRecorder()
	handler.ServeHTTP(wrongDraft, httptest.NewRequest("GET", base+"module/projects?generation_id=other", nil))
	if wrongDraft.Code != 404 || !strings.Contains(wrongDraft.Body.String(), "import this record") {
		t.Fatalf("wrong draft readback=%d %s", wrongDraft.Code, wrongDraft.Body.String())
	}
	if _, err := store.ReadCanonical(ctx, "company-canonical", "builder", FamilyMandate, "builder"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft leaked into active reads: %v", err)
	}
	incomplete := CanonicalImport{ProjectID: "builder", GenerationID: "incomplete", SourceRevision: "6dd3f957b9bbcdb8e3d6131c3e6f22ea5d6865b7", OperationID: "import-incomplete", Actor: "migration-agent", Records: []CanonicalImportRecord{canonicalTestRecord(FamilyMandate, "builder", `{"purpose":"Incomplete","scope":["web"]}`)}}
	incomplete.Records[0].Record.GenerationID = incomplete.GenerationID
	if _, err := store.ImportCanonicalDraft(ctx, "company-canonical", incomplete); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateCanonical(ctx, "company-canonical", "builder", "incomplete", "activate-incomplete", "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("activation accepted a generation missing required singleton families: %v", err)
	}
	colliding := CanonicalImport{ProjectID: "builder", GenerationID: "colliding", SourceRevision: "6dd3f957b9bbcdb8e3d6131c3e6f22ea5d6865b7", OperationID: "import-colliding", Actor: "migration-agent", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyEndpoint, "route-a", `{"method":"get","route":"//projects/","host_scope":"API","module_id":"projects","request":[],"response":[]}`),
		canonicalTestRecord(FamilyEndpoint, "route-b", `{"method":"GET","route":"/projects","host_scope":"api","module_id":"projects","request":[],"response":[]}`),
	}}
	for i := range colliding.Records {
		colliding.Records[i].Record.GenerationID = colliding.GenerationID
	}
	if _, err := store.ImportCanonicalDraft(ctx, "company-canonical", colliding); !errors.Is(err, ErrConflict) {
		t.Fatalf("normalized endpoint method/route/host collision was accepted: %v", err)
	}
	active, err := store.ActivateCanonical(ctx, "company-canonical", "builder", "g1", "activate-1", "owner")
	if err != nil || !active.Active || active.State != "active" {
		t.Fatalf("activation=%+v, %v", active, err)
	}
	if repeated, e := store.ActivateCanonical(ctx, "company-canonical", "builder", "g1", "activate-1", "owner"); e != nil || !repeated.Active {
		t.Fatalf("idempotent activation=%+v, %v", repeated, e)
	}
	index, err := store.CanonicalIndex(ctx, "company-canonical", "builder", CanonicalIndexQuery{Limit: 2})
	if err != nil || len(index.Items) != 2 || index.NextCursor == "" {
		t.Fatalf("bounded canonical index=%+v, %v", index, err)
	}
	second, err := store.CanonicalIndex(ctx, "company-canonical", "builder", CanonicalIndexQuery{Limit: 2, Cursor: index.NextCursor})
	if err != nil || len(second.Items) != 2 || second.Items[0].ID == index.Items[0].ID {
		t.Fatalf("canonical index cursor=%+v, %v", second, err)
	}
	decisions, err := store.CanonicalIndex(ctx, "company-canonical", "builder", CanonicalIndexQuery{DecisionsOnly: true, Q: "DEC-ART-01"})
	if err != nil || len(decisions.Items) != 1 || decisions.Items[0].ID != "projects" || len(decisions.Items[0].Decisions) != 1 {
		t.Fatalf("decision owner index=%+v, %v", decisions, err)
	}
	if _, err := store.CanonicalIndex(ctx, "company-canonical", "builder", CanonicalIndexQuery{Cursor: index.NextCursor, Family: FamilyModule}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor must reject changed filters: %v", err)
	}
	if _, err := store.ActivateCanonical(ctx, "company-canonical", "builder", "incomplete", "replace-generation", "owner"); !errors.Is(err, ErrConflict) {
		t.Fatalf("active generation replacement must fail: %v", err)
	}
	late := CanonicalImport{ProjectID: "builder", GenerationID: "g1", SourceRevision: input.SourceRevision, OperationID: "import-after-activation", Actor: "migration-agent", Records: []CanonicalImportRecord{canonicalTestRecord(FamilyModule, "late-module", `{"purpose":"Late import"}`)}}
	if _, err := store.ImportCanonicalDraft(ctx, "company-canonical", late); !errors.Is(err, ErrConflict) {
		t.Fatalf("active generation accepted a draft import: %v", err)
	}
	stillActive, err := store.CanonicalManifest(ctx, "company-canonical", "builder", "")
	if err != nil || stillActive.GenerationID != "g1" || stillActive.State != "active" {
		t.Fatalf("rejected activation changed active pointer: %+v, %v", stillActive, err)
	}
	constitution, err := store.ReadCanonical(ctx, "company-canonical", "builder", FamilyConstitution, "builder")
	if err != nil {
		t.Fatal(err)
	}
	var constitutionPayload ConstitutionPayload
	if err := json.Unmarshal(constitution.Payload, &constitutionPayload); err != nil || constitutionPayload.PromotionMode != "mvp" {
		t.Fatalf("active API lost Builder promotion mode: %s, %v", constitution.Payload, err)
	}
	module, err := store.ReadCanonical(ctx, "company-canonical", "builder", FamilyModule, "projects")
	if err != nil {
		t.Fatal(err)
	}
	var modulePayload ModulePayload
	if err := json.Unmarshal(module.Payload, &modulePayload); err != nil || len(modulePayload.Schemas) != 1 || modulePayload.Decisions[0].ID != "DEC-ART-01" {
		t.Fatalf("active API lost typed module contract: %s, %v", module.Payload, err)
	}
	read, err := store.ReadCanonical(ctx, "company-canonical", "builder", FamilyMandate, "builder")
	if err != nil || read.Revision != 1 {
		t.Fatalf("active read=%+v, %v", read, err)
	}
	read.Payload = json.RawMessage(`{"purpose":"Build safely","scope":["web"]}`)
	updated, err := store.EditCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyMandate, Record: read, ExpectedRevision: 1, OperationID: "edit-1", Actor: "owner"})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("edit=%+v, %v", updated, err)
	}
	if repeated, e := store.EditCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyMandate, Record: read, ExpectedRevision: 1, OperationID: "edit-1", Actor: "owner"}); e != nil || repeated.Revision != 2 {
		t.Fatalf("idempotent edit=%+v, %v", repeated, e)
	}
	if _, err := store.EditCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyMandate, Record: read, ExpectedRevision: 1, OperationID: "edit-stale", Actor: "owner"}); err != ErrConflict {
		t.Fatalf("stale write error=%v", err)
	}
	created, err := store.CreateCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyPolicy, Record: CanonicalRecord{ID: "new-policy", Payload: json.RawMessage(`{"rules":[{"id":"new-rule","status":"planned","statement":"New policy"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`)}, OperationID: "create-1", Actor: "owner"})
	if err != nil || created.Revision != 1 {
		t.Fatalf("create=%+v, %v", created, err)
	}
	if repeated, e := store.CreateCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyPolicy, Record: CanonicalRecord{ID: "new-policy", Payload: json.RawMessage(`{"rules":[{"id":"new-rule","status":"planned","statement":"New policy"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`)}, OperationID: "create-1", Actor: "owner"}); e != nil || repeated.Revision != 1 {
		t.Fatalf("idempotent create=%+v, %v", repeated, e)
	}
	badModule := module
	badModule.Payload = json.RawMessage(`{"purpose":"Owns project work","decisions":[]}`)
	if _, err := store.EditCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyModule, Record: badModule, ExpectedRevision: 1, OperationID: "edit-remove-referenced-decision", Actor: "owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("active edit removed a referenced decision: %v", err)
	}
	if _, err := store.DeleteCanonical(ctx, "company-canonical", CanonicalDeleteRequest{ProjectID: "builder", Family: FamilyModule, RecordID: "projects", ExpectedRevision: 1, OperationID: "delete-referenced-module", Actor: "owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete removed referenced module: %v", err)
	}
	if _, err := store.CreateCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyEndpoint, Record: CanonicalRecord{ID: "bad-reference", Payload: json.RawMessage(`{"method":"GET","route":"/missing","host_scope":"api","module_id":"projects","request":[],"response":[],"policy_ids":["missing-policy"]}`)}, OperationID: "create-bad-reference", Actor: "owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("endpoint accepted unknown policy reference: %v", err)
	}
	if _, err := store.CreateCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyEndpoint, Record: CanonicalRecord{ID: "bad-entity", Payload: json.RawMessage(`{"method":"GET","route":"/missing-entity","host_scope":"api","module_id":"projects","request":[],"response":[],"entity_ids":["absent"]}`)}, OperationID: "create-bad-entity", Actor: "owner"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("endpoint accepted unknown entity reference: %v", err)
	}
	deleted, err := store.DeleteCanonical(ctx, "company-canonical", CanonicalDeleteRequest{ProjectID: "builder", Family: FamilyPolicy, RecordID: "new-policy", ExpectedRevision: 1, OperationID: "delete-new-policy", Actor: "owner"})
	if err != nil || !deleted.Deleted || deleted.Revision != 2 {
		t.Fatalf("delete=%+v, %v", deleted, err)
	}
	if _, err := store.ReadCanonical(ctx, "company-canonical", "builder", FamilyPolicy, "new-policy"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted record remained readable: %v", err)
	}
	if repeated, err := store.CreateCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyPolicy, Record: CanonicalRecord{ID: "new-policy", Payload: json.RawMessage(`{"rules":[{"id":"new-rule","status":"planned","statement":"New policy"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`)}, OperationID: "create-1", Actor: "owner"}); err != nil || repeated.Revision != 1 || repeated.Deleted {
		t.Fatalf("create retry after deletion lost its immutable response: %+v, %v", repeated, err)
	}
	if historical, err := store.ReadCanonicalAsOf(ctx, "company-canonical", "builder", FamilyPolicy, "new-policy", 1); err != nil || historical.Deleted {
		t.Fatalf("deleted record history lost: %+v, %v", historical, err)
	}
	restored, err := store.RestoreCanonical(ctx, "company-canonical", CanonicalRestoreRequest{ProjectID: "builder", Family: FamilyMandate, RecordID: "builder", Revision: 1, ExpectedRevision: 2, OperationID: "restore-1", Actor: "owner"})
	if err != nil || restored.Revision != 3 || canonicalDigest(restored.Payload) != read.ContentDigest {
		t.Fatalf("restore=%+v, %v", restored, err)
	}
	history, err := store.CanonicalHistory(ctx, "company-canonical", "builder", "", 100)
	if err != nil || len(history.Events) != 12 {
		t.Fatalf("history events=%d, %v", len(history.Events), err)
	}
	indexCursor, err := store.client.Database(store.tenantDatabase("company-canonical")).Collection(canonicalEvents).Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projectTimelineFound := false
	for indexCursor.Next(ctx) {
		var index struct {
			Name string `bson:"name"`
			Key  bson.D `bson:"key"`
		}
		if indexCursor.Decode(&index) != nil {
			_ = indexCursor.Close(ctx)
			t.Fatal("decode canonical event index")
		}
		if index.Name == "canonical_events_project_timeline" && reflect.DeepEqual(index.Key, bson.D{{Key: "project_id", Value: int32(1)}, {Key: "occurred_at", Value: int32(1)}, {Key: "_id", Value: int32(1)}}) {
			projectTimelineFound = true
		}
	}
	if indexCursor.Err() != nil || indexCursor.Close(ctx) != nil || !projectTimelineFound {
		t.Fatalf("project-wide canonical timeline index missing or incorrect: found=%v", projectTimelineFound)
	}
	tieTime := time.Now().UTC().Truncate(time.Millisecond).Add(24 * time.Hour)
	tiedIDs := []bson.ObjectID{}
	for _, hexID := range []string{"0102030405060708090a0b01", "0102030405060708090a0b02", "0102030405060708090a0b03", "0102030405060708090a0b04"} {
		id, parseErr := bson.ObjectIDFromHex(hexID)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		tiedIDs = append(tiedIDs, id)
		if _, err := store.client.Database(store.tenantDatabase("company-canonical")).Collection(canonicalEvents).InsertOne(ctx, CanonicalEvent{ObjectID: id, ProjectID: "builder", GenerationID: "other-generation", Family: "project", RecordID: "builder", Kind: "fixed-timestamp-test", Revision: 1, OperationID: "tied-" + id.Hex(), Actor: "test", OccurredAt: tieTime}); err != nil {
			t.Fatal(err)
		}
	}
	seenTied := map[bson.ObjectID]int{}
	continuation := ""
	terminalSeen := false
	for pageNumber := 0; pageNumber < 16; pageNumber++ {
		page, pageErr := store.CanonicalHistory(ctx, "company-canonical", "builder", continuation, 2)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		for _, event := range page.Events {
			if event.Kind == "fixed-timestamp-test" {
				seenTied[event.ObjectID]++
			}
		}
		continuation = page.NextCursor
		if continuation == "" {
			terminalSeen = true
			break
		}
	}
	if !terminalSeen || len(seenTied) != len(tiedIDs) {
		t.Fatalf("tied fixed event set yielded terminal=%v, %d IDs, want %d: %v", terminalSeen, len(seenTied), len(tiedIDs), seenTied)
	}
	for _, id := range tiedIDs {
		if seenTied[id] != 1 {
			t.Fatalf("tied event %s appeared %d times across cursor pages", id.Hex(), seenTied[id])
		}
	}
	entity, err := store.ReadCanonicalEntity(ctx, "company-canonical", "builder", "Project")
	if err != nil || entity.Entity == nil || entity.Entity.Name != "Project" || entity.RecordID != "builder" || entity.Revision != 1 {
		t.Fatalf("entity projection=%+v, %v", entity, err)
	}
	decision, err := store.ReadCanonicalDecision(ctx, "company-canonical", "builder", FamilyModule, "projects", "DEC-ART-01")
	if err != nil || decision.Decision == nil || decision.Decision.Statement != "Module owns routes" || decision.RecordID != "projects" {
		t.Fatalf("decision projection=%+v, %v", decision, err)
	}
	if _, err := store.ReadCanonicalDecision(ctx, "company-canonical", "builder", FamilyPolicy, "access-policy", "DEC-ART-01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decision lookup escaped owner: %v", err)
	}
	if _, err := store.ReadCanonicalDecision(ctx, "company-canonical", "builder", FamilyModule, "projects", "dec-art-01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("decision identity lost case: %v", err)
	}
	before, err := store.ReadCanonicalAsOf(ctx, "company-canonical", "builder", FamilyMandate, "builder", 1)
	if err != nil || before.ContentDigest != read.ContentDigest || before.Revision != 1 {
		t.Fatalf("as-of=%+v, %v", before, err)
	}
	comparison, err := store.CompareCanonicalHistory(ctx, "company-canonical", "builder", FamilyMandate, "builder", 1, 2)
	if err != nil || !comparison.Changed || comparison.From.Revision != 1 || comparison.To.Revision != 2 || !strings.Contains(string(comparison.To.Payload), "Build safely") {
		t.Fatalf("comparison=%+v, %v", comparison, err)
	}
	comparison, err = store.CompareCanonicalHistory(ctx, "company-canonical", "builder", FamilyMandate, "builder", 1, 3)
	if err != nil || comparison.Changed {
		t.Fatalf("restored comparison=%+v, %v", comparison, err)
	}
	if _, err := store.ReadCanonicalAsOf(ctx, "company-canonical", "builder", FamilyMandate, "builder", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing version=%v", err)
	}
	endpoint, err := store.CreateCanonical(ctx, "company-canonical", CanonicalMutation{ProjectID: "builder", Family: FamilyEndpoint, Record: CanonicalRecord{ID: "list-projects", Payload: json.RawMessage(`{"method":"GET","route":"/projects","host_scope":"api","module_id":"projects","request":[],"response":[]}`)}, OperationID: "create-endpoint", Actor: "owner"})
	if err != nil || endpoint.Revision != 1 {
		t.Fatalf("endpoint=%+v, %v", endpoint, err)
	}
	for _, path := range []string{"entities/Project", "endpoints/list-projects", "module/projects/decisions/DEC-ART-01", "project_mandate/builder/history/1", "project_mandate/builder/history/compare?from=1&to=2"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", base+path, nil))
		if response.Code != 200 || !json.Valid(response.Body.Bytes()) || !strings.Contains(response.Body.String(), `"revision":1`) {
			t.Fatalf("HTTP projection %s status=%d body=%s", path, response.Code, response.Body)
		}
	}
	for _, path := range []string{"entities/missing", "endpoints/missing", "module/projects/decisions/missing", "project_mandate/builder/history/99"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", base+path, nil))
		if response.Code != 404 || strings.Contains(response.Body.String(), "canonical_pending") {
			t.Fatalf("missing ID must differ from pending: %s status=%d body=%s", path, response.Code, response.Body)
		}
	}
	for _, path := range []string{"module/projects/history/0", "module/projects/history/compare?from=1&to=2&to=3", "module/projects/history/compare?from=1"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", base+path, nil))
		if response.Code != 400 || !strings.Contains(response.Body.String(), "teach_detail") {
			t.Fatalf("invalid version query %s status=%d body=%s", path, response.Code, response.Body)
		}
	}
	// One new endpoint operation above, but read/compare/projection operations
	// must not create events or mutate the owning record's revision.
	historyAfter, err := store.CanonicalHistory(ctx, "company-canonical", "builder", "", 100)
	if err != nil || len(historyAfter.Events) != len(history.Events)+5 {
		t.Fatalf("reads changed history: events=%d, %v", len(historyAfter.Events), err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-other", CompanyName: "Other", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCanonical(ctx, "company-other", "builder", FamilyMandate, "builder"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("canonical read crossed Company tenant boundary: %v", err)
	}
	if _, err := store.ReadCanonicalAsOf(ctx, "company-other", "builder", FamilyMandate, "builder", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("historical read crossed Company tenant boundary: %v", err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-canonical", CompanyName: "Canonical", ProjectID: "parallel", ProjectName: "Parallel", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	crossProjectCursor := encodeCanonicalHistoryCursor("company-canonical", "builder", tieTime, tiedIDs[0])
	if _, err := store.CanonicalHistory(ctx, "company-canonical", "parallel", crossProjectCursor, 2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("canonical history accepted a cursor from another Project: %v", err)
	}
	parallel := input
	parallel.ProjectID, parallel.GenerationID, parallel.OperationID = "parallel", "race", "import-race-base"
	parallel.Records = append([]CanonicalImportRecord(nil), input.Records...)
	for i := range parallel.Records {
		parallel.Records[i].Record.ProjectID, parallel.Records[i].Record.GenerationID = "parallel", "race"
	}
	if _, err := store.ImportCanonicalDraft(ctx, "company-canonical", parallel); err != nil {
		t.Fatal(err)
	}
	additional := CanonicalImport{ProjectID: "parallel", GenerationID: "race", SourceRevision: input.SourceRevision, OperationID: "import-race-extra", Actor: "migration-agent", Records: []CanonicalImportRecord{canonicalTestRecord(FamilyModule, "extra", `{"purpose":"Extra module"}`)}}
	additional.Records[0].Record.ProjectID, additional.Records[0].Record.GenerationID = "parallel", "race"
	var activateErr, importErr error
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		_, activateErr = store.ActivateCanonical(ctx, "company-canonical", "parallel", "race", "activate-race", "owner")
	}()
	go func() {
		defer group.Done()
		_, importErr = store.ImportCanonicalDraft(ctx, "company-canonical", additional)
	}()
	group.Wait()
	if activateErr != nil || importErr != nil && !errors.Is(importErr, ErrConflict) {
		t.Fatalf("concurrent activation/import: activation=%v import=%v", activateErr, importErr)
	}
	parallelManifest, err := store.CanonicalManifest(ctx, "company-canonical", "parallel", "")
	if err != nil || !parallelManifest.Active {
		t.Fatalf("parallel activation=%+v, %v", parallelManifest, err)
	}
	foundExtra := false
	for _, entry := range parallelManifest.Documents {
		if entry.Family == FamilyModule && entry.ID == "extra" {
			foundExtra = true
		}
	}
	if foundExtra != (importErr == nil) {
		t.Fatalf("import/activation race lost a committed record: import=%v manifest=%+v", importErr, parallelManifest)
	}
}

func TestCanonicalDuplicateJSONKeysFailBeforeTypedDecoding(t *testing.T) {
	for _, raw := range []string{
		`{"purpose":"first","purpose":"last","scope":["web"]}`,
		`{"purpose":"Build","decisions":[{"id":"D-1","id":"D-2","status":"current","statement":"Own"}]}`,
	} {
		err := rejectCanonicalDuplicateKeys("owner", []byte(raw), "payload")
		var teach *canonicalTeachError
		if !errors.As(err, &teach) || teach.Diagnostic.RecordID != "owner" || !strings.Contains(teach.Diagnostic.NextAction, "duplicate JSON key") {
			t.Fatalf("duplicate accepted: %s error=%v", raw, err)
		}
	}
	var mandate MandatePayload
	if err := decodeCanonicalPayload("owner", json.RawMessage(`{"purpose":"first","purpose":"last","scope":["web"]}`), &mandate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed decode accepted duplicate: %v", err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"project_id":"first","project_id":"last"}`))
	var input CanonicalImport
	if (&Handler{}).decodeCanonicalRequest(response, request, &input) || response.Code != 400 || !strings.Contains(response.Body.String(), "request.project_id") {
		t.Fatalf("duplicate envelope accepted: %s", response.Body)
	}
	// Same field names in distinct objects are valid.
	if err := rejectCanonicalDuplicateKeys("", []byte(`{"items":[{"id":"a"},{"id":"b"}]}`), "payload"); err != nil {
		t.Fatal(err)
	}
}
