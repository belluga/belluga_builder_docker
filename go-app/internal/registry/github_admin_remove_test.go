package registry

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMongoGitHubAdminRemovesOnlyTheRequestedRole(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo integration fixture unavailable")
		}
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("github_roles_remove_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	const company, project, repository = "company-a", "project-a", "team/shared"
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = store.client.Database(store.tenantDatabase(company)).Drop(cleanup)
		_ = store.control.Drop(cleanup)
		_ = store.Close(cleanup)
	})
	if _, err := store.Register(ctx, Registration{CompanyID: company, CompanyName: "Company", ProjectID: project, ProjectName: "Project"}); err != nil {
		t.Fatal(err)
	}
	db := store.client.Database(store.tenantDatabase(company))
	if _, err := store.ChangeRepositoryBinding(ctx, company, project, RepositoryBindingRequest{
		ProjectID: project, Action: "bind", RepositoryID: repository, Branch: "main", Actor: "operator", OperationID: "bind-visual",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, company, project, ImplementationRepositoryChange{
		ProjectID: project, Action: "confirm", RepositoryID: repository, Actor: "operator", OperationID: "confirm-code", OwnerReference: "fixture",
	}); err != nil {
		t.Fatal(err)
	}

	admin := newGitHubConnectionAdmin(store, nil)
	handler := githubAdminHTTP{store: store, admin: admin}
	scope := githubAdminTarget{company: company, project: project, operator: githubOperator{Issuer: "test", Subject: "operator"}}
	remove := func(role, operation string, revision int) (githubConnectionRoles, error) {
		payload := fmt.Sprintf(`{"role":%q,"repository_id":%q,"expected_revision":%d,"operation_id":%q}`, role, repository, revision, operation)
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload)).WithContext(ctx)
		out, err := handler.removeRole(request, scope)
		if err != nil {
			return githubConnectionRoles{}, err
		}
		return out.(githubConnectionRoles), nil
	}
	if _, err := remove("visual", "stale", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale removal must conflict: %v", err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	handler.origin = githubAccessTestOrigin
	handler.access, err = newGitHubAccessVerifier(githubAccessTestIssuer, githubAccessTestAudience,
		`{"operator-subject":["company-a"]}`, &http.Client{Transport: githubAppTestTransport(func(*http.Request) (*http.Response, error) {
			return githubAppTestResponse(http.StatusOK, githubAccessTestKeys(t, &key.PublicKey)), nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.register(mux)
	scope.operator = githubOperator{Issuer: githubAccessTestIssuer, Subject: githubAccessTestSubject}
	visualRemoval := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/"+company+"/projects/"+project+"/github-connections/remove",
		strings.NewReader(`{"role":"visual","repository_id":"team/shared","expected_revision":1,"operation_id":"remove-visual"}`)).WithContext(ctx)
	visualRemoval.Header.Set(githubAccessAssertionHeader, githubAccessTestAssertion(t, key, "key", "RS256", githubAccessTestClaims(time.Now())))
	visualRemoval.Header.Set("Origin", githubAccessTestOrigin)
	visualRemoval.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, visualRemoval)
	var state githubConnectionRoles
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &state) != nil ||
		state.Visual.RepositoryID != "" || state.Visual.Revision != 2 ||
		len(state.Implementation.Repositories) != 1 || state.Implementation.Repositories[0].RepositoryID != repository {
		t.Fatalf("unexpected Visual removal readback: HTTP %d %s", response.Code, response.Body.String())
	}
	if _, err := remove("visual", "remove-visual", 1); err != nil {
		t.Fatalf("exact retry must remain idempotent: %v", err)
	}
	if _, err := store.ChangeRepositoryBinding(ctx, company, project, RepositoryBindingRequest{
		ProjectID: project, Action: "bind", RepositoryID: repository, Branch: "main", Actor: "operator", OperationID: "restore-visual", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	branch := roadmapRelease{ProjectID: project, ID: "depends-on-code", DeliveryBranches: []releaseDeliveryBranch{{RepositoryID: repository, Branch: "dev"}}}
	if _, err := db.Collection(releasesCollection).InsertOne(ctx, branch); err != nil {
		t.Fatal(err)
	}
	if _, err := remove("implementation", "blocked-code-remove", 1); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), branch.ID) {
		t.Fatalf("confirmed Release dependency must block removal: %v", err)
	}
	if _, err := db.Collection(releasesCollection).DeleteOne(ctx, bson.M{"project_id": project, "id": branch.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = db.Collection(todosCollection).InsertOne(ctx, bson.M{
		"id": "legacy-code-dependency", "project_id": project, "stage": "In-Progress",
		"diff_expectation": bson.A{bson.M{"repository_id": repository}},
	}, options.InsertOne().SetBypassDocumentValidation(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remove("implementation", "blocked-by-todo", 1); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "legacy-code-dependency") {
		t.Fatalf("active TODO dependency must block removal: %v", err)
	}
	if _, err := db.Collection(todosCollection).DeleteOne(ctx, bson.M{"project_id": project, "id": "legacy-code-dependency"}); err != nil {
		t.Fatal(err)
	}

	state, err = remove("implementation", "remove-code", 1)
	if err != nil || state.Visual.RepositoryID != repository || len(state.Implementation.Repositories) != 0 || state.Implementation.Revision != 2 {
		t.Fatalf("unexpected Code removal readback: %+v, %v", state, err)
	}

	if _, err := store.ChangeImplementationRepository(ctx, company, project, ImplementationRepositoryChange{
		ProjectID: project, Action: "confirm", RepositoryID: repository, Actor: "operator", OperationID: "restore-code", OwnerReference: "fixture", ExpectedRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	const raceRelease = "race-release"
	if _, err := db.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{ProjectID: project, ID: raceRelease, Name: "Race Release"}); err != nil {
		t.Fatal(err)
	}

	// Pause the removal at its first Project write. Its transaction has
	// already read membership and both dependency collections.
	writePending, resumeWrite := make(chan struct{}), make(chan struct{})
	var firstWrite sync.Once
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(&event.CommandMonitor{
		Started: func(commandContext context.Context, command *event.CommandStartedEvent) {
			if command.DatabaseName == db.Name() && command.CommandName == "update" &&
				command.Command.Lookup("update").StringValue() == projectsCollection {
				firstWrite.Do(func() {
					close(writePending)
					select {
					case <-resumeWrite:
					case <-commandContext.Done():
					}
				})
			}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	handler.store = &MongoStore{client: client, control: client.Database(stamp), tenantPrefix: stamp}
	removeDone := make(chan error, 1)
	go func() {
		_, err := remove("implementation", "race-remove-code", 3)
		removeDone <- err
	}()
	select {
	case <-writePending:
	case <-ctx.Done():
		t.Fatal("removal never reached the Project write barrier")
	}
	var roadmapRevision int64
	_, branchErr := store.ChangeRoadmap(ctx, company, "confirm-release-branch", RoadmapChange{
		ProjectID: project, ID: raceRelease, ExpectedRoadmapRevision: &roadmapRevision, RepositoryID: repository,
		Branch: "dev", OwnerReference: "fixture", OperationID: "race-confirm-branch",
	})
	close(resumeWrite)
	removeErr := <-removeDone
	if branchErr != nil || !errors.Is(removeErr, ErrConflict) || !strings.Contains(removeErr.Error(), raceRelease) {
		t.Fatalf("removal must observe the committed Release dependency after retry: confirmation=%v removal=%v", branchErr, removeErr)
	}
	var release roadmapRelease
	if err := db.Collection(releasesCollection).FindOne(ctx, bson.M{"project_id": project, "id": raceRelease}).Decode(&release); err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadImplementationRepositories(ctx, company, project)
	if err != nil || (len(release.DeliveryBranches) != 1 || release.DeliveryBranches[0].RepositoryID != repository || !containsTODOString(current.Items, repository)) {
		t.Fatalf("active branch and repository membership diverged: %+v %+v %v", release.DeliveryBranches, current.Items, err)
	}
}
