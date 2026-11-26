package registry

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoAdminOnboardingRetainsExactCreatorGrants(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo integration fixture unavailable")
		}
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prefix := fmt.Sprintf("onboarding_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, prefix)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = prefix
	const company, project, withImage = "company-a", "project-a", "company-image"
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = store.client.Database(store.tenantDatabase(company)).Drop(cleanup)
		_ = store.client.Database(store.tenantDatabase(withImage)).Drop(cleanup)
		_ = store.control.Drop(cleanup)
		_ = store.Close(cleanup)
	})
	owner := githubOperator{Issuer: githubAccessTestIssuer, Subject: githubAccessTestSubject}
	other := githubOperator{Issuer: owner.Issuer, Subject: "other"}
	req := adminRegistration{Name: "Company", Slug: company, Operation: "company-operation"}
	if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, Company{
		ID: company, Name: req.Name, DatabaseName: store.tenantDatabase(company), SchemaState: "initializing",
		CreatedBy: req.grant(owner, struct {
			Registration adminRegistration
			ImageDigest  string
		}{req, sourceHash(nil)}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createAdminCompany(ctx, other, req, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("another operator resumed company setup: %v", err)
	}
	catalog, err := store.Catalog(ctx)
	if err != nil || len(catalog.Companies) != 0 {
		t.Fatalf("initializing administrative Company blocked or entered the public catalog: %+v, %v", catalog, err)
	}
	if _, err := store.control.Collection(companiesCollection).UpdateOne(ctx, bson.M{"id": company}, bson.M{"$set": bson.M{"schema_state": "pending"}}); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Catalog(ctx)
	if err != nil || len(catalog.Companies) != 0 {
		t.Fatalf("pending administrative Company blocked or entered the public catalog: %+v, %v", catalog, err)
	}
	got, err := store.createAdminCompany(ctx, owner, req, nil)
	if err != nil || got.SchemaState != "ready" {
		t.Fatalf("same-intent company retry: %+v, %v", got, err)
	}
	catalog, err = store.Catalog(ctx)
	if err != nil || len(catalog.Companies) != 1 || catalog.Companies[0].ID != company {
		t.Fatalf("ready administrative Company missing from the public catalog: %+v, %v", catalog, err)
	}
	input := adminRegistration{Name: "Project", Slug: project, Operation: "project-operation"}
	if first, err := store.createAdminProject(ctx, company, owner, input); err != nil || first.ID != project {
		t.Fatalf("create project: %+v, %v", first, err)
	}
	if _, err := store.createAdminProject(ctx, company, owner, input); err != nil {
		t.Fatalf("project replay: %v", err)
	}
	if _, err := store.createAdminProject(ctx, company, other, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-operator project replay: %v", err)
	}
	changedProject := input
	changedProject.Slug = "changed-project"
	if _, err := store.createAdminProject(ctx, company, owner, changedProject); !errors.Is(err, ErrConflict) {
		t.Fatalf("an operation registered a different Project slug: %v", err)
	}
	sibling := adminRegistration{Name: "Sibling", Slug: "sibling", Operation: "sibling-operation"}
	if _, err := store.createAdminProject(ctx, company, other, sibling); err != nil {
		t.Fatal(err)
	}
	fresh := &githubAdminHTTP{store: store, access: &githubAccessVerifier{}}
	for _, check := range []struct {
		mode, company, project string
		operator               githubOperator
		want                   bool
	}{
		{"company", company, "", owner, true},
		{"company", company, "", other, false},
		{"github", company, project, owner, true},
		{"github", company, project, other, false},
		{"github", company, project, githubOperator{"https://different.test", owner.Subject}, false},
		{"github", company, sibling.Slug, owner, false},
		{"github", company, sibling.Slug, other, true},
		{"github", "missing-company", project, owner, false},
	} {
		allowed, err := fresh.onboardingAccess(ctx, check.mode, githubAdminTarget{company: check.company, project: check.project, operator: check.operator})
		if err != nil || allowed != check.want {
			t.Errorf("%s %s/%s: allow=%v, want %v, error %v", check.mode, check.company, check.project, allowed, check.want, err)
		}
	}
	var stored bson.M
	if err := store.control.Collection(companiesCollection).FindOne(ctx, bson.M{"id": company}).Decode(&stored); err != nil || stored["created_by"] == nil {
		t.Fatalf("creator grant was lost during schema convergence: %v", err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jX8sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	imageReq := adminRegistration{Name: "Company with image", Slug: withImage, Operation: "image-operation"}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	access, err := newGitHubAccessVerifier(githubAccessTestIssuer, githubAccessTestAudience, `{}`, &http.Client{Transport: githubAppTestTransport(func(*http.Request) (*http.Response, error) {
		return githubAppTestResponse(http.StatusOK, githubAccessTestKeys(t, &key.PublicKey)), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	handler := &githubAdminHTTP{store: store, access: access, origin: githubAccessTestOrigin, companyCreators: map[string]bool{owner.Subject: true}}
	mux := http.NewServeMux()
	handler.registerOnboarding(mux)
	claims := githubAccessTestClaims(time.Now())
	ownerJWT := githubAccessTestAssertion(t, key, "key", "RS256", claims)
	claims["sub"] = other.Subject
	otherJWT := githubAccessTestAssertion(t, key, "key", "RS256", claims)
	const endpoint = "/internal/v1/admin/companies"
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	for field, value := range map[string]string{"name": imageReq.Name, "slug": imageReq.Slug, "operation_id": imageReq.Operation} {
		if err := form.WriteField(field, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := form.CreateFormFile("image", "company.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		assertion string
		status    int
	}{{otherJWT, 403}, {ownerJWT, 200}} {
		req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(upload.Bytes()))
		req.Header.Set(githubAccessAssertionHeader, attempt.assertion)
		req.Header.Set("Origin", githubAccessTestOrigin)
		req.Header.Set("Content-Type", form.FormDataContentType())
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != attempt.status {
			t.Fatalf("protected upload expected %d, got %d: %s", attempt.status, res.Code, res.Body.String())
		}
	}
	for _, attempt := range []struct {
		assertion string
		status    int
	}{{otherJWT, 403}, {ownerJWT, 200}} {
		req := httptest.NewRequest(http.MethodGet, endpoint+"/"+withImage+"/image", nil)
		req.Header.Set(githubAccessAssertionHeader, attempt.assertion)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != attempt.status || res.Code == 200 && (res.Header().Get("Content-Type") != "image/png" || res.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(res.Body.Bytes(), png)) {
			t.Fatalf("protected image readback: HTTP %d, expected %d", res.Code, attempt.status)
		}
	}
	for _, attempt := range []struct {
		name, assertion, query string
		rows, projects         int
	}{
		{"unfiltered", ownerJWT, "", 2, 0},
		{"exact", ownerJWT, "?company_id=" + company, 1, 2},
		{"unauthorized", otherJWT, "?company_id=" + company, 0, 0},
		{"missing", ownerJWT, "?company_id=missing-company", 0, 0},
	} {
		req := httptest.NewRequest(http.MethodGet, endpoint+attempt.query, nil)
		req.Header.Set(githubAccessAssertionHeader, attempt.assertion)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		var result struct {
			Companies []struct {
				ID       string `json:"id"`
				Projects []struct {
					ID string `json:"id"`
				} `json:"projects"`
			} `json:"companies"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil || res.Code != 200 || len(result.Companies) != attempt.rows {
			t.Fatalf("%s: directory status %d, rows %d, error %v", attempt.name, res.Code, len(result.Companies), err)
		}
		projects := 0
		for _, row := range result.Companies {
			projects += len(row.Projects)
		}
		if projects != attempt.projects || (attempt.name == "exact" && result.Companies[0].ID != company) {
			t.Fatalf("%s: invalid directory selection %+v", attempt.name, result.Companies)
		}
	}
	changedCompany := imageReq
	changedCompany.Slug = "changed-company"
	if _, err := store.createAdminCompany(ctx, owner, changedCompany, png); !errors.Is(err, ErrConflict) {
		t.Fatalf("an operation registered a different Company slug: %v", err)
	}
	img, err := store.readAdminCompanyImage(ctx, withImage)
	if err != nil || img.MIME != "image/png" || !bytes.Equal(img.Bytes, png) {
		t.Fatalf("image readback: %+v, %v", img, err)
	}
	compacted, err := store.rawCompany(ctx, withImage)
	if err != nil || !compacted.HasAdminImage || compacted.AdminImage != nil {
		t.Fatalf("company metadata included image bytes: %+v, %v", compacted, err)
	}
	if _, err := store.createAdminCompany(ctx, owner, imageReq, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("image change replayed the original registration: %v", err)
	}

}
