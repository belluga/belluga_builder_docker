package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestMongoProjectPromotionModeHTTPAndCAS(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo replica-set integration fixture is unavailable")
		}
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	const companyID, projectID, sessionID = "company-a", "project-a", "mode-fixture-conversation"
	database := store.client.Database(store.tenantDatabase(companyID))
	t.Cleanup(func() {
		_ = database.Drop(ctx)
		_ = store.control.Drop(ctx)
		_ = store.Close(ctx)
	})
	for _, project := range []string{projectID, "project-b"} {
		if _, err := store.Register(ctx, Registration{CompanyID: companyID, CompanyName: "Company A", ProjectID: project, ProjectName: project}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindSession(ctx, SessionBindingRequest{Type: "codex", ExternalID: sessionID, CompanyID: companyID, ProjectID: projectID, OperationID: "bind-fixture-mode", OwnerReference: "owner-decision"}); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(store)
	invoke := func(method, project, session string, requestBody []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, "/internal/v1/companies/"+companyID+"/projects/"+project+"/promotion-mode", bytes.NewReader(requestBody))
		if session != "" {
			request.Header.Set(sessionLaneHeader, "conversation")
			request.Header.Set(sessionTypeHeader, "codex")
			request.Header.Set(sessionExternalIDHeader, session)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	read := invoke(http.MethodGet, projectID, sessionID, nil)
	var state ProjectPromotionMode
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || read.Code != http.StatusOK ||
		state.CompanyID != companyID || state.ProjectID != projectID || state.Source != "unconfigured" || state.EffectiveMode != "" || state.Revision != 0 || state.Decision != nil {
		t.Fatalf("unconfigured Project mode: status=%d body=%s err=%v", read.Code, read.Body, err)
	}

	for _, invalid := range []string{
		`{"project_id":"project-a","mode":"mvp","operation_id":"missing-revision","actor":"fixture","owner_reference":"owner"}`,
		`{"project_id":"project-a","mode":"unknown","expected_revision":0,"operation_id":"invalid-mode","actor":"fixture","owner_reference":"owner"}`,
		`{"project_id":"project-a","mode":"complete","expected_revision":0,"operation_id":"missing-owner","actor":"fixture"}`,
		`{"project_id":"project-a","mode":"complete","expected_revision":0,"operation_id":"unknown-key","actor":"fixture","owner_reference":"owner","unexpected":true}`,
		`{"project_id":"project-b","mode":"complete","expected_revision":0,"operation_id":"wrong-project","actor":"fixture","owner_reference":"owner"}`,
	} {
		response := invoke(http.MethodPut, projectID, sessionID, []byte(invalid))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "teach_detail") {
			t.Fatalf("invalid input entered the Project mutation: status=%d body=%s", response.Code, response.Body)
		}
	}
	if response := invoke(http.MethodGet, "unknown-project", "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown Project status=%d body=%s", response.Code, response.Body)
	}
	blocked := invoke(http.MethodPut, "project-b", sessionID, []byte(`{"project_id":"project-b","mode":"complete","expected_revision":0,"operation_id":"cross-project","actor":"fixture","owner_reference":"owner"}`))
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), "session_project_mismatch") {
		t.Fatalf("bound session could write a different Project: status=%d body=%s", blocked.Code, blocked.Body)
	}
	other, err := store.ReadProjectPromotionMode(ctx, companyID, "project-b")
	if err != nil || other.Revision != 0 || other.Source != "unconfigured" {
		t.Fatalf("cross-Project request changed state: %+v err=%v", other, err)
	}

	encode := func(input ProjectPromotionModeChange) []byte {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	zero := int64(0)
	first := ProjectPromotionModeChange{
		ProjectID: projectID, Mode: "complete", ExpectedRevision: &zero, OperationID: "set-complete",
		Actor: "fixture-operator", OwnerReference: "owner selected Complete",
	}
	second := first
	second.Mode, second.OperationID = "mvp", "set-mvp"
	type outcome struct {
		request  ProjectPromotionModeChange
		response *httptest.ResponseRecorder
	}
	start, results := make(chan struct{}), make(chan outcome, 2)
	for _, request := range []ProjectPromotionModeChange{first, second} {
		body := encode(request)
		go func(request ProjectPromotionModeChange) {
			<-start
			results <- outcome{request, invoke(http.MethodPut, projectID, sessionID, body)}
		}(request)
	}
	close(start)
	var winner ProjectPromotionModeChange
	applied, conflicts := 0, 0
	for range 2 {
		result := <-results
		switch result.response.Code {
		case http.StatusOK:
			winner = result.request
			applied++
		case http.StatusConflict:
			var failed errorResponse
			if err := json.Unmarshal(result.response.Body.Bytes(), &failed); err != nil || failed.TeachDetail == nil ||
				failed.TeachDetail.Revision == nil || *failed.TeachDetail.Revision != 1 || failed.TeachDetail.Code != "promotion_mode_revision_conflict" {
				t.Fatalf("losing CAS diagnostic: %s err=%v", result.response.Body, err)
			}
			conflicts++
		default:
			t.Fatalf("racing mode writes: status=%d body=%s", result.response.Code, result.response.Body)
		}
	}
	if applied != 1 || conflicts != 1 {
		t.Fatalf("compare-and-set outcomes applied=%d conflict=%d", applied, conflicts)
	}
	replay := invoke(http.MethodPut, projectID, sessionID, encode(winner))
	state = ProjectPromotionMode{}
	if err := json.Unmarshal(replay.Body.Bytes(), &state); err != nil || replay.Code != http.StatusOK || !state.NoOp ||
		state.Source != "project" || state.Revision != 1 || state.EffectiveMode != winner.Mode || state.Decision == nil ||
		state.Decision.OperationID != winner.OperationID || state.Decision.PermissionDecision != authorizeTODOAdministrativeOperation(ctx, companyID, projectID, "set-promotion-mode").Decision {
		t.Fatalf("replay did not return exact accepted owner state: status=%d body=%s err=%v", replay.Code, replay.Body, err)
	}
	changed := winner
	changed.OwnerReference = "different decision"
	if _, err := store.ChangeProjectPromotionMode(ctx, companyID, projectID, changed); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "operation_id") {
		t.Fatalf("changed request reused the winning operation ID: %v", err)
	}
	same := winner
	one := int64(1)
	same.ExpectedRevision, same.OperationID = &one, "same-mode"
	if noOp, err := store.ChangeProjectPromotionMode(ctx, companyID, projectID, same); err != nil || !noOp.NoOp || noOp.Revision != 1 {
		t.Fatalf("setting the configured mode changed its revision: %+v err=%v", noOp, err)
	}
	read = invoke(http.MethodGet, projectID, sessionID, nil)
	state = ProjectPromotionMode{}
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || read.Code != http.StatusOK ||
		state.Revision != 1 || state.Source != "project" || state.EffectiveMode != winner.Mode || state.Decision == nil ||
		state.Decision.OwnerReference != winner.OwnerReference || state.Decision.Actor != winner.Actor || state.Decision.ConfirmedAt.IsZero() {
		t.Fatalf("accepted mode readback: status=%d body=%s err=%v", read.Code, read.Body, err)
	}

	// The explicit value must still resolve when the active constitution is
	// invalid. The fixture inserts corrupt legacy state directly.
	const generation = "invalid-fallback"
	if _, err := database.Collection(canonicalConstitutions).InsertOne(ctx, canonicalStored{
		ProjectID: projectID, GenerationID: generation, Family: FamilyConstitution, ID: "invalid-mode",
		Revision: 1, ContentDigest: sourceHash([]byte("fixture")), Payload: bson.M{"promotion_mode": "unsupported"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(projectsCollection).UpdateOne(ctx, bson.M{"id": projectID}, bson.M{"$set": bson.M{"knowledge_generation_id": generation}}); err != nil {
		t.Fatal(err)
	}
	if explicit, err := store.ReadProjectPromotionMode(ctx, companyID, projectID); err != nil || explicit.Source != "project" ||
		explicit.EffectiveMode != winner.Mode || explicit.Revision != 1 {
		t.Fatalf("explicit mode attempted to use invalid Knowledge: %+v err=%v", explicit, err)
	}

	for _, fixture := range []struct {
		name    string
		payload bson.M
		invalid bool
	}{
		{"absent", bson.M{}, false},
		{"empty", bson.M{"promotion_mode": ""}, true},
		{"null", bson.M{"promotion_mode": nil}, true},
		{"wrong-type", bson.M{"promotion_mode": 1}, true},
	} {
		t.Run("fallback-mode-"+fixture.name, func(t *testing.T) {
			const fallbackProject = "project-b"
			generation := "fixture-fallback-" + fixture.name
			if _, err := database.Collection(canonicalConstitutions).InsertOne(ctx, canonicalStored{
				ProjectID: fallbackProject, GenerationID: generation, Family: FamilyConstitution, ID: "fallback-constitution",
				Revision: 1, ContentDigest: sourceHash([]byte(generation)), Payload: fixture.payload,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Collection(projectsCollection).UpdateOne(ctx, bson.M{"id": fallbackProject, "company_id": companyID},
				bson.M{"$set": bson.M{"knowledge_generation_id": generation}}); err != nil {
				t.Fatal(err)
			}
			state, err := store.ReadProjectPromotionMode(ctx, companyID, fallbackProject)
			if fixture.invalid {
				var validation *TODOValidationError
				if !errors.Is(err, ErrConflict) || !errors.As(err, &validation) || validation.Field != "constitution.promotion_mode" {
					t.Fatalf("present invalid promotion mode returned state=%+v err=%v", state, err)
				}
				return
			}
			if err != nil || state.Source != "unconfigured" || state.EffectiveMode != "" || state.Revision != 0 {
				t.Fatalf("absent fallback mode returned state=%+v err=%v", state, err)
			}
		})
	}

	// Simulate the direct MVP write seam after a transaction has already seen
	// its prior Project snapshot. Mongo must reject the stale write itself.
	next := "mvp"
	if winner.Mode == "mvp" {
		next = "complete"
	}
	session, err := database.Client().StartSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.EndSession(ctx)
	if err := session.StartTransaction(); err != nil {
		t.Fatal(err)
	}
	txCtx := mongo.NewSessionContext(ctx, session)
	projectObjectID, err := checkTODOProjectPromotionPolicy(txCtx, database, companyID, projectID, todoPromotionPolicyCheck{mode: winner.Mode})
	if err != nil {
		t.Fatal(err)
	}
	configureFixturePromotionMode(t, ctx, store, companyID, projectID, next, 1, "set-during-transaction")
	if err := advanceProjectPromotionPolicyFence(txCtx, database, companyID, projectID, projectObjectID); !mongoTransactionShouldRetry(err) {
		t.Fatalf("stage policy write did not contend with a committed mode change: %v", err)
	}
	if err := session.AbortTransaction(ctx); err != nil {
		t.Fatal(err)
	}
}
