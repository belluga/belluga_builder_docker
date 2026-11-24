package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoDiscoveryIndexedAndScoped(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stamp := fmt.Sprintf("test_discovery_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		for _, id := range []string{"company-a", "company-b", "company-c"} {
			_ = store.client.Database(store.tenantDatabase(id)).Drop(cleanup)
		}
		_ = store.control.Drop(cleanup)
		_ = store.Close(cleanup)
	})
	companies := store.control.Collection(companiesCollection)
	personas := []struct{ id, name string }{
		{"company-a", "Féstou"}, {"company-b", "FÉSTOU"}, {"company-c", "Festou"},
	}
	for _, company := range personas {
		input := Registration{CompanyID: company.id, CompanyName: company.name, ProjectID: "project-a", ProjectName: company.name}
		if _, err := store.Register(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	companyA := store.client.Database(store.tenantDatabase("company-a")).Collection(projectsCollection)
	companyB := store.client.Database(store.tenantDatabase("company-b")).Collection(projectsCollection)
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Féstou", ProjectID: "project-b", ProjectName: "Fe\u0301stou"}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store)
	get := func(path string, expectedStatus int, externalID ...string) (discoveryPage, string) {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/internal/v1"+path, nil)
		if len(externalID) == 1 {
			request.Header.Set(sessionLaneHeader, "conversation")
			request.Header.Set(sessionTypeHeader, "codex")
			request.Header.Set(sessionExternalIDHeader, externalID[0])
		}
		handler.ServeHTTP(response, request)
		output := response.Body.String()
		if response.Code != expectedStatus {
			t.Fatalf("%s: status %d, want %d: %s", path, response.Code, expectedStatus, output)
		}
		var page discoveryPage
		if expectedStatus == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
		}
		return page, output
	}

	companyPath := "/companies/search"
	projectPath := "/companies/company-a/projects"
	accentFilter := "name=" + url.QueryEscape("  féstou  ")
	_, noIndex := get(companyPath+"?"+accentFilter, http.StatusServiceUnavailable)
	if !strings.Contains(noIndex, "discovery-indexes") {
		t.Fatalf("expected preparation diagnostic: %s", noIndex)
	}
	get(projectPath+"?"+accentFilter, http.StatusServiceUnavailable)
	if _, err := store.PrepareDiscoveryIndexes(ctx, []string{"company-a", "company-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareDiscoveryIndexes(ctx, []string{"company-a", "company-b"}); err != nil {
		t.Fatalf("idempotent index preparation: %v", err)
	}
	get("/companies/company-c/projects?name=Festou", http.StatusServiceUnavailable)
	if _, err := store.PrepareDiscoveryIndexes(ctx, []string{"company-c"}); err != nil {
		t.Fatal(err)
	}
	get("/companies/company-c/projects?name=Festou", http.StatusOK)

	q := companyPath + "?" + accentFilter + "&limit=1&company_ids=company-a,company-b"
	first, raw := get(q, http.StatusOK)
	if first.MatchState != "ambiguous" || len(first.Items) != 1 || first.Items[0].ID != "company-a" || first.NextCursor == "" || strings.Contains(raw, "database_name") || strings.Contains(raw, "company_id") {
		t.Fatalf("Company first page: %s", raw)
	}
	last, _ := get(q+"&cursor="+first.NextCursor, http.StatusOK)
	if last.MatchState != "ambiguous" || len(last.Items) != 1 || last.Items[0].ID != "company-b" || last.NextCursor != "" {
		t.Fatalf("Company last page: %+v", last)
	}
	get(companyPath+"?"+accentFilter+"&company_ids=company-a&cursor="+first.NextCursor, http.StatusBadRequest)

	exact, _ := get(companyPath+"?name=Festou&limit=50", http.StatusOK)
	if exact.MatchState != "unique" || len(exact.Items) != 1 || exact.Items[0].ID != "company-c" {
		t.Fatalf("accent significance: %+v", exact)
	}
	none, raw := get(companyPath+"?name=festou!", http.StatusOK)
	if none.MatchState != "none" || !strings.Contains(raw, `"items":[]`) {
		t.Fatalf("punctuation significance: %s", raw)
	}
	projectFirst, raw := get(projectPath+"?"+accentFilter+"&limit=1", http.StatusOK)
	if projectFirst.MatchState != "ambiguous" || len(projectFirst.Items) != 1 || projectFirst.Items[0].CompanyID != "company-a" || strings.Contains(raw, "preparation") {
		t.Fatalf("Project minimum DTO: %s", raw)
	}
	projectLast, _ := get(projectPath+"?"+accentFilter+"&cursor="+projectFirst.NextCursor, http.StatusOK)
	if projectLast.MatchState != "ambiguous" || len(projectLast.Items) != 1 || projectLast.Items[0].ID != "project-b" {
		t.Fatalf("Project normalized last page: %+v", projectLast)
	}
	get("/companies/company-b/projects?"+accentFilter+"&cursor="+projectFirst.NextCursor, http.StatusBadRequest)
	isolated, _ := get("/companies/company-b/projects?id=project-a", http.StatusOK)
	if isolated.MatchState != "unique" || len(isolated.Items) != 1 || isolated.Items[0].CompanyID != "company-b" {
		t.Fatalf("Project ID after tenant lookup: %+v", isolated)
	}
	idScoped, _ := get(companyPath+"?id=company-a&company_ids=company-a,company-b", http.StatusOK)
	if idScoped.MatchState != "unique" || len(idScoped.Items) != 1 || idScoped.Items[0].ID != "company-a" {
		t.Fatalf("Company ID scope: %+v", idScoped)
	}
	noID, _ := get(companyPath+"?id=company-a&company_ids=company-b", http.StatusOK)
	if noID.MatchState != "none" || len(noID.Items) != 0 {
		t.Fatalf("Company outside scope: %+v", noID)
	}

	const boundSession = "owner-conversation"
	if _, err := store.BindSession(ctx, SessionBindingRequest{Type: "codex", ExternalID: boundSession, CompanyID: "company-a", ProjectID: "project-b", OperationID: "bind-owner", OwnerReference: "fixture owner confirmation"}); err != nil {
		t.Fatal(err)
	}
	bound, _ := get(projectPath+"?"+accentFilter, http.StatusOK, boundSession)
	if bound.MatchState != "unique" || len(bound.Items) != 1 || bound.Items[0].ID != "project-b" {
		t.Fatalf("Project search widened bound scope: %+v", bound)
	}
	get(projectPath+"?"+accentFilter+"&cursor="+projectFirst.NextCursor, http.StatusBadRequest, boundSession)
	get("/companies/company-b/projects?name=Festou", http.StatusForbidden, boundSession)
	get(companyPath+"?"+accentFilter+"&company_ids=company-b", http.StatusForbidden, boundSession)
	bound, _ = get(companyPath+"?"+accentFilter+"&company_ids=company-a,company-b", http.StatusOK, boundSession)
	if bound.MatchState != "unique" || len(bound.Items) != 1 || bound.Items[0].ID != "company-a" {
		t.Fatalf("Company search widened bound scope: %+v", bound)
	}
	unbound, _ := get(projectPath+"?"+accentFilter, http.StatusOK, "new-fork-conversation")
	if unbound.MatchState != "ambiguous" || len(unbound.Items) != 2 {
		t.Fatalf("unbound discovery did not run: %+v", unbound)
	}

	// Seed large collections and selective duplicate ranges without changing
	// any collection validator. Full owner writes above prove real DTO reads.
	for _, fixture := range []struct {
		collection *mongo.Collection
		company    string
	}{
		{companies, ""}, {companyA, "company-a"}, {companyB, "company-b"},
	} {
		docs := make([]any, 0, 10003)
		for i := 0; i < cap(docs); i++ {
			id := fmt.Sprintf("filler-%05d", i)
			name := fmt.Sprintf("Unrelated %05d", i)
			if i < 3 {
				name = "Fixture overflow"
			}
			doc := bson.M{"id": id, "name": name}
			if fixture.company != "" {
				doc["company_id"] = fixture.company
			}
			docs = append(docs, doc)
		}
		if _, err := fixture.collection.InsertMany(ctx, docs, options.InsertMany().SetBypassDocumentValidation(true)); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []struct {
		collection *mongo.Collection
		q          discoveryQuery
		nameIndex  string
		idIndex    string
	}{
		{companies, discoveryQuery{Field: "name", Value: "Fixture overflow", Scope: []string{"filler-00000", "filler-00001", "filler-00002"}, After: "filler-00000"}, companyDiscoveryIndex, "companies_id_unique"},
		{companyA, discoveryQuery{Company: "company-a", Field: "name", Value: "Fixture overflow", After: "filler-00000"}, projectDiscoveryIndex, "projects_id_unique"},
		{companies, discoveryQuery{Field: "id", Value: "company-a", Scope: []string{"company-a", "company-b"}}, companyDiscoveryIndex, "companies_id_unique"},
		{companyA, discoveryQuery{Company: "company-a", Field: "id", Value: "project-a"}, projectDiscoveryIndex, "projects_id_unique"},
	} {
		for _, pagination := range []bool{false, true} {
			verifyDiscoveryExplain(t, ctx, check.collection, check.q, check.nameIndex, check.idIndex, pagination)
		}
	}
	filter, find := discoveryCompanyQuery("company-a")
	var target options.FindOneOptions
	for _, apply := range find.List() {
		if err := apply(&target); err != nil {
			t.Fatal(err)
		}
	}
	tenantLookup := bson.D{
		{Key: "find", Value: companies.Name()}, {Key: "filter", Value: filter}, {Key: "projection", Value: target.Projection},
		{Key: "hint", Value: target.Hint}, {Key: "limit", Value: 1},
	}
	runDiscoveryExplain(t, ctx, companies, tenantLookup, companyDiscoveryIDIndex, "tenant resolution")
	if err := companies.Indexes().DropOne(ctx, "companies_id_unique"); err != nil {
		t.Fatal(err)
	}
	_, result := get(projectPath+"?id=project-a", http.StatusServiceUnavailable)
	if !strings.Contains(result, "discovery-indexes") {
		t.Fatalf("missing Company index did not fail discovery closed: %s", result)
	}
}

func verifyDiscoveryExplain(t *testing.T, ctx context.Context, collection *mongo.Collection, query discoveryQuery, nameIndex, idIndex string, paginated bool) {
	t.Helper()
	filter, find := query.mongoQuery(nameIndex, idIndex, paginated, 2)
	var opts options.FindOptions
	for _, apply := range find.List() {
		if err := apply(&opts); err != nil {
			t.Fatal(err)
		}
	}
	command := bson.D{{Key: "find", Value: collection.Name()}, {Key: "filter", Value: filter}, {Key: "sort", Value: opts.Sort}, {Key: "projection", Value: opts.Projection}, {Key: "limit", Value: opts.Limit}, {Key: "hint", Value: opts.Hint}}
	if opts.Collation != nil {
		command = append(command, bson.E{Key: "collation", Value: opts.Collation})
	}
	runDiscoveryExplain(t, ctx, collection, command, fmt.Sprint(opts.Hint), fmt.Sprintf("page=%t", paginated))
}

func runDiscoveryExplain(t *testing.T, ctx context.Context, collection *mongo.Collection, command bson.D, index, phase string) {
	t.Helper()
	var result struct {
		QueryPlanner struct {
			WinningPlan bson.M `bson:"winningPlan"`
		} `bson:"queryPlanner"`
		ExecutionStats struct {
			Returned int `bson:"nReturned"`
			Examined int `bson:"totalDocsExamined"`
			Keys     int `bson:"totalKeysExamined"`
		} `bson:"executionStats"`
	}
	if err := collection.Database().RunCommand(ctx, bson.D{{Key: "explain", Value: command}, {Key: "verbosity", Value: "executionStats"}}).Decode(&result); err != nil {
		t.Fatal(err)
	}
	plan, err := bson.MarshalExtJSON(result.QueryPlanner.WinningPlan, false, false)
	if err != nil {
		t.Fatal(err)
	}
	stats := result.ExecutionStats
	if !strings.Contains(string(plan), index) || strings.Contains(string(plan), `"stage":"COLLSCAN"`) || strings.Contains(string(plan), `"stage":"SORT"`) || stats.Returned < 1 || stats.Returned > 2 || stats.Examined > 2 || stats.Keys > 5 {
		t.Fatalf("unbounded or unexpected explain for %s: %+v %s", index, stats, plan)
	}
	t.Logf("explain %s %s nReturned=%d totalDocsExamined=%d totalKeysExamined=%d", index, phase, stats.Returned, stats.Examined, stats.Keys)
}
