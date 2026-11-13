package registry

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestProvisionRegisteredTODOsDiscoversAndReconcilesCompanies(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated Mongo fixture not configured")
	}
	ctx := context.Background()
	stamp := "test" + strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		for _, id := range []string{"company-a", "company-b", "company-c"} {
			_ = store.client.Database(store.tenantDatabase(id)).Drop(context.Background())
		}
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := store.ProvisionRegisteredTODOs(ctx); err == nil || !strings.Contains(err.Error(), "companies") {
		t.Fatalf("empty inventory was accepted: %v", err)
	}
	addCompany := func(id string) {
		t.Helper()
		company, err := normalizeCompany(Company{ID: id, Name: id, DatabaseName: store.tenantDatabase(id)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
			t.Fatal(err)
		}
	}
	addCompany("company-b")
	addCompany("company-a")
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Database(store.tenantDatabase("company-a")).RunCommand(ctx, bson.D{
		{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: TODOCollectionValidatorBeforeAdministrativeAcceptance()},
		{Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Database(store.tenantDatabase("company-a")).Collection(todosCollection).Indexes().DropOne(ctx, "todos_project_affected_file"); err != nil {
		t.Fatal(err)
	}
	report, err := store.ProvisionRegisteredTODOs(ctx)
	if err != nil || !report.Verified || len(report.Companies) != 2 || report.Companies[0].CompanyID != "company-a" || report.Companies[1].CompanyID != "company-b" {
		t.Fatalf("registered Company migration: %+v, %v", report, err)
	}
	for _, item := range report.Companies {
		if !item.Verified || item.Phase != "verified" {
			t.Fatalf("Company was not verified: %+v", item)
		}
	}
	content := TODOAuthoredContent{TODOContent: validTODOContent(),
		Title: "TODO: Strict local check schema", Objective: "Verify tenant-scoped content schema enforcement.",
		DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	content.TODOContent.LocalChecks = []TODOLocalCheck{{
		ID: "G-TEST", RepositoryID: "owner/repo", Argv: []string{"go", "test", "./..."},
		WorkingDirectory: ".", InputPaths: []string{"go-app/**"},
	}}
	contentBytes, err := bson.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var contentDocument bson.M
	if err := bson.Unmarshal(contentBytes, &contentDocument); err != nil {
		t.Fatal(err)
	}
	for _, companyID := range []string{"company-a", "company-b"} {
		collection := store.client.Database(store.tenantDatabase(companyID)).Collection(todoContentVersionsCollection)
		base := bson.D{{Key: "_id", Value: bson.NewObjectID()}, {Key: "project_id", Value: "builder"},
			{Key: "todo_id", Value: "strict-local-check"}, {Key: "revision", Value: int64(1)},
			{Key: "content", Value: contentDocument}}
		if _, err := collection.InsertOne(ctx, base); err != nil {
			t.Fatalf("Company %s rejected valid typed local check: %v", companyID, err)
		}
		invalidContent := bson.M{}
		for key, value := range contentDocument {
			invalidContent[key] = value
		}
		invalidContent["local_checks"] = bson.A{bson.M{
			"id": "G-TEST", "repository_id": "owner/repo", "argv": bson.A{"go", "test"},
			"working_directory": ".", "input_paths": bson.A{"go-app/**"}, "unexpected": true,
		}}
		invalid := append(bson.D(nil), base...)
		invalid[0].Value = bson.NewObjectID()
		invalid[2].Value = "strict-local-check-extra-field"
		invalid[4].Value = invalidContent
		if _, err := collection.InsertOne(ctx, invalid); err == nil {
			t.Fatalf("Company %s accepted an unknown local check field", companyID)
		}
	}
	database := store.client.Database(store.tenantDatabase("company-a"))
	project := Project{ID: "builder", CompanyID: "company-a", Name: "Builder", Binding: ptrTestBinding(validTestBinding()), RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano), PreparationState: "not_ready"}
	projectRecord, err := projectToDocument(project, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(projectsCollection).InsertOne(ctx, projectRecord); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "e-06", Name: "Database", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-08", EpicID: "e-06", Name: "Mongo", Position: 0}); err != nil {
		t.Fatal(err)
	}
	request := genericTODOAdmissionFixture()
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", request); err != nil {
		t.Fatal(err)
	}
	var before bson.Raw
	if err := database.Collection(todosCollection).FindOne(ctx, bson.D{{Key: "id", Value: request.TODO.ID}}).Decode(&before); err != nil {
		t.Fatal(err)
	}
	beforeHistory, err := store.PreflightTODOHistory(ctx, "company-a")
	if err != nil || !beforeHistory.CoverageComplete {
		t.Fatalf("before history: %+v, %v", beforeHistory, err)
	}
	if rerun, err := store.ProvisionRegisteredTODOs(ctx); err != nil || !rerun.Verified || len(rerun.Companies) != 2 {
		t.Fatalf("idempotent rerun: %+v, %v", rerun, err)
	}
	var after bson.Raw
	if err := database.Collection(todosCollection).FindOne(ctx, bson.D{{Key: "id", Value: request.TODO.ID}}).Decode(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rerun changed admitted TODO: %v", err)
	}
	afterHistory, err := store.PreflightTODOHistory(ctx, "company-a")
	if err != nil || beforeHistory.ContentVersions != afterHistory.ContentVersions || beforeHistory.Events != afterHistory.Events || beforeHistory.Baselines != afterHistory.Baselines {
		t.Fatalf("rerun changed existing history: before=%+v after=%+v err=%v", beforeHistory, afterHistory, err)
	}
	filter := bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}}
	if _, err := database.Collection(todoEventsCollection).DeleteMany(ctx, filter); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(todoContentVersionsCollection).DeleteMany(ctx, filter); err != nil {
		t.Fatal(err)
	}
	if repaired, err := store.ProvisionRegisteredTODOs(ctx); err != nil || !repaired.Verified || repaired.Companies[0].Events == 0 || repaired.Companies[0].Baselines == 0 {
		t.Fatalf("partial history was not restored: %+v, %v", repaired, err)
	}
	addCompany("company-c")
	if discovered, err := store.ProvisionRegisteredTODOs(ctx); err != nil || !discovered.Verified || len(discovered.Companies) != 3 {
		t.Fatalf("new Company was not discovered: %+v, %v", discovered, err)
	}
}

func TestProvisionRegisteredTODOsRejectsExtraUniqueAndContinues(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated Mongo fixture not configured")
	}
	ctx := context.Background()
	stamp := "test" + strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		for _, id := range []string{"company-a", "company-b"} {
			_ = store.client.Database(store.tenantDatabase(id)).Drop(context.Background())
		}
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	for _, id := range []string{"company-a", "company-b"} {
		company, err := normalizeCompany(Company{ID: id, Name: id, DatabaseName: store.tenantDatabase(id)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	timedOut := store.provisionRegisteredCompanyTODOs(expired, Company{ID: "company-a", DatabaseName: store.tenantDatabase("company-a")})
	if timedOut.Verified || timedOut.Phase != "inspect" || timedOut.Error == "" {
		t.Fatalf("expired Company phase was not reported: %+v", timedOut)
	}
	collection := store.client.Database(store.tenantDatabase("company-a")).Collection(todosCollection)
	if _, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "title", Value: 1}}, Options: options.Index().SetName("unexpected_title_unique").SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ProvisionRegisteredTODOs(ctx)
	if err != nil || report.Verified || len(report.Companies) != 2 || report.Companies[0].Verified || !report.Companies[1].Verified || !strings.Contains(report.Companies[0].Error, "unexpected_title_unique") {
		t.Fatalf("partial failure not reported accurately: %+v, %v", report, err)
	}
	if err := collection.Indexes().DropOne(ctx, "unexpected_title_unique"); err != nil {
		t.Fatal(err)
	}
	unknown := bson.D{{Key: "$jsonSchema", Value: bson.D{{Key: "bsonType", Value: "object"}}}}
	if err := store.client.Database(store.tenantDatabase("company-a")).RunCommand(ctx, bson.D{
		{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: unknown},
		{Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		t.Fatal(err)
	}
	report, err = store.ProvisionRegisteredTODOs(ctx)
	if err != nil || report.Verified || !strings.Contains(report.Companies[0].Error, "todos.validator") || !report.Companies[1].Verified {
		t.Fatalf("unknown validator did not fail closed: %+v, %v", report, err)
	}
	if err := store.client.Database(store.tenantDatabase("company-a")).RunCommand(ctx, bson.D{
		{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: TODOCollectionValidator()},
		{Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"},
	}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := collection.Indexes().DropOne(ctx, "todos_project_affected_file"); err != nil {
		t.Fatal(err)
	}
	if _, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "title", Value: 1}}, Options: options.Index().SetName("todos_project_affected_file")}); err != nil {
		t.Fatal(err)
	}
	report, err = store.ProvisionRegisteredTODOs(ctx)
	if err != nil || report.Verified || !strings.Contains(report.Companies[0].Error, "todos_project_affected_file") || !report.Companies[1].Verified {
		t.Fatalf("conflicting required index did not fail closed: %+v, %v", report, err)
	}
	if err := collection.Indexes().DropOne(ctx, "todos_project_affected_file"); err != nil {
		t.Fatal(err)
	}
	if _, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "title", Value: 1}}, Options: options.Index().SetName("extra_title_nonunique")}); err != nil {
		t.Fatal(err)
	}
	report, err = store.ProvisionRegisteredTODOs(ctx)
	if err != nil || !report.Verified || len(report.Companies[0].NonuniqueExtraIndexes) != 1 || report.Companies[0].NonuniqueExtraIndexes[0] != "extra_title_nonunique" {
		t.Fatalf("nonunique extra index was not observed: %+v, %v", report, err)
	}
}
