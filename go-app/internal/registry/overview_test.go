package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func testOverviewPNG(t *testing.T) []byte {
	return testOverviewPNGColor(t, color.RGBA{R: 10, G: 20, B: 30, A: 255})
}

func testOverviewPNGColor(t *testing.T, pixel color.RGBA) []byte {
	t.Helper()
	var data bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageData.Set(0, 0, pixel)
	if err := png.Encode(&data, imageData); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func testOverviewContent() OverviewContent {
	sections := make(map[string]OverviewSection, len(overviewSections))
	for _, name := range overviewSections {
		sections[name] = OverviewSection{Heading: "Heading " + name, Body: "Body " + name, Points: []string{}}
	}
	return OverviewContent{SchemaVersion: "1", Tagline: "A clear tagline", Summary: "A concise summary", DesktopAlt: "Desktop illustration", MobileAlt: "Mobile illustration", Sections: sections}
}

func testOverviewOrigin() (OverviewKnowledgeOrigin, CanonicalManifest) {
	documents := []CanonicalManifestEntry{
		{Family: "module", ID: "accounts", Revision: 3, ContentDigest: "sha256:" + strings.Repeat("a", 64)},
		{Family: "project_constitution", ID: "constitution", Revision: 2, ContentDigest: "sha256:" + strings.Repeat("b", 64)},
	}
	return OverviewKnowledgeOrigin{ProjectID: "project-a", GenerationID: "generation-a", Documents: documents}, CanonicalManifest{ProjectID: "project-a", GenerationID: "generation-a", Active: true, State: "active", Documents: documents}
}

func TestValidateOverviewContentRequiresFixedSlotsAndMatchingAltText(t *testing.T) {
	validPNG := testOverviewPNG(t)
	content := testOverviewContent()
	media := []OverviewMediaInput{{Slot: "desktop", Data: validPNG}, {Slot: "mobile", Data: validPNG}}
	refs, err := validateOverviewContent(content, media)
	if err != nil || len(refs) != 2 || refs[0].Slot != "desktop" || refs[1].Slot != "mobile" {
		t.Fatalf("valid bounded Overview rejected: refs=%+v err=%v", refs, err)
	}

	if _, err := validateOverviewContent(content, []OverviewMediaInput{{Slot: "desktop", Data: validPNG}}); err == nil {
		t.Fatal("Overview without the required mobile image was accepted")
	}
	if _, err := validateOverviewContent(content, []OverviewMediaInput{{Slot: "desktop", Data: validPNG}, {Slot: "mobile", Data: validPNG}, {Slot: "other", Data: validPNG}}); err == nil {
		t.Fatal("unrecognized media slot was accepted")
	}
	content.Sections["problem"] = OverviewSection{Heading: "Problem", Body: "A problem", Points: []string{}, ImageAlt: ptrString("Problem illustration")}
	if _, err := validateOverviewContent(content, media); err == nil {
		t.Fatal("alt text without its declared section image was accepted")
	}
	content = testOverviewContent()
	content.Sections["problem"] = OverviewSection{Heading: "Problem", Body: "A problem", Points: []string{}, ImageAlt: ptrString("Problem illustration")}
	if _, err := validateOverviewContent(content, append(media, OverviewMediaInput{Slot: "problem", Data: validPNG})); err != nil {
		t.Fatalf("matching section media and alt text rejected: %v", err)
	}
}

func TestOverviewOriginRequiresTheCompleteActiveManifest(t *testing.T) {
	origin, manifest := testOverviewOrigin()
	if !overviewOriginMatches(origin, manifest) {
		t.Fatal("exact complete active manifest was rejected")
	}
	missing := origin
	missing.Documents = append([]CanonicalManifestEntry(nil), origin.Documents[:1]...)
	if overviewOriginMatches(missing, manifest) {
		t.Fatal("incomplete manifest origin was accepted")
	}
	changed := origin
	changed.Documents = append([]CanonicalManifestEntry(nil), origin.Documents...)
	changed.Documents[0].Revision++
	if overviewOriginMatches(changed, manifest) {
		t.Fatal("stale manifest revision was accepted")
	}
	manifest.Active = false
	if overviewOriginMatches(origin, manifest) {
		t.Fatal("inactive knowledge manifest was accepted")
	}
}

func TestOverviewIdempotentReplayRequiresPublishedPointerEvidence(t *testing.T) {
	first := overviewVersionDocument{Revision: 2, OperationID: "op-a", RequestDigest: "sha256:first"}
	if !overviewRetryWasPublished(2, "op-a", "sha256:first", first, overviewVersionDocument{}, 0) {
		t.Fatal("matching current pointer did not permit an identical retry")
	}
	if overviewRetryWasPublished(2, "op-other", "sha256:first", first, overviewVersionDocument{}, 0) {
		t.Fatal("same revision with a different pointer operation was accepted")
	}
	if overviewRetryWasPublished(2, "op-a", "sha256:other", first, overviewVersionDocument{}, 0) {
		t.Fatal("same revision with a different pointer digest was accepted")
	}
	current := overviewVersionDocument{Revision: 4, OperationID: "op-current", RequestDigest: "sha256:current"}
	if !overviewRetryWasPublished(4, "op-current", "sha256:current", first, current, 4) {
		t.Fatal("complete immutable revision sequence and matching current pointer did not prove a prior publication")
	}
	if overviewRetryWasPublished(4, "op-current", "sha256:current", first, current, 3) {
		t.Fatal("a prior version with an incomplete revision sequence was accepted")
	}
	if overviewRetryWasPublished(4, "op-other", "sha256:current", first, current, 4) {
		t.Fatal("later revision with a mismatched current pointer operation was accepted")
	}
}

func TestMongoOverviewPublicationTransactionRollsBackAndConflictsConcurrently(t *testing.T) {
	store, ctx := overviewMongoFixture(t, "publish")
	db, project, err := store.overviewProject(ctx, "company-overview", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureOverviewIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	makeVersion := func(operation, digest string) overviewVersionDocument {
		return overviewVersionDocument{ObjectID: bson.NewObjectID(), ProjectID: project.ID, Revision: 1, OperationID: operation, RequestDigest: digest}
	}
	failed := makeVersion("failed-op", "sha256:failed")
	if err := store.publishOverviewVersion(ctx, db, project, failed, true, 99); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected stale pointer CAS to abort transaction, got %v", err)
	}
	if count, err := db.Collection(overviewVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: project.ID}}); err != nil || count != 0 {
		t.Fatalf("failed CAS left an immutable revision behind: count=%d err=%v", count, err)
	}

	versions := []overviewVersionDocument{makeVersion("parallel-a", "sha256:a"), makeVersion("parallel-b", "sha256:b")}
	results := make(chan error, len(versions))
	var group sync.WaitGroup
	for _, version := range versions {
		group.Add(1)
		go func(version overviewVersionDocument) {
			defer group.Done()
			results <- store.publishOverviewVersion(ctx, db, project, version, true, 0)
		}(version)
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent publication error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS results: successes=%d conflicts=%d", successes, conflicts)
	}
	var published projectDocument
	if err := db.Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "_id", Value: project.ObjectID}}).Decode(&published); err != nil {
		t.Fatal(err)
	}
	var committed overviewVersionDocument
	if err := db.Collection(overviewVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: project.ID}, {Key: "revision", Value: published.OverviewRevision}}).Decode(&committed); err != nil {
		t.Fatal(err)
	}
	if published.OverviewRevision != 1 || committed.OperationID != published.OverviewOperation || committed.RequestDigest != published.OverviewDigest {
		t.Fatalf("pointer and committed revision diverged: project=%+v version=%+v", published, committed)
	}
	if count, err := db.Collection(overviewVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: project.ID}}); err != nil || count != 1 {
		t.Fatalf("losing transaction left a revision behind: count=%d err=%v", count, err)
	}
}

func TestMongoOverviewFreshTenantIndexesAreUnfiltered(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("overview_fresh_index_%d", time.Now().UnixNano())
	db := client.Database(database)
	t.Cleanup(func() {
		if err := db.Drop(context.Background()); err != nil {
			t.Errorf("drop fresh Overview index fixture database: %v", err)
		}
		if err := client.Disconnect(context.Background()); err != nil {
			t.Errorf("disconnect fresh Overview index fixture client: %v", err)
		}
	})
	store := &MongoStore{client: client}
	if err := store.ensureTenantIndexes(ctx, database); err != nil {
		t.Fatalf("provision tenant indexes: %v", err)
	}
	for collection, names := range map[string][]string{
		overviewVersionsCollection: {"overview_project_revision_unique", "overview_project_operation_unique"},
		overviewMediaCollection:    {"overview_project_operation_slot_unique"},
	} {
		cursor, err := db.Collection(collection).Indexes().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var persisted []bson.M
		if err := cursor.All(ctx, &persisted); err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			t.Run(name, func(t *testing.T) {
				for _, index := range persisted {
					if index["name"] != name {
						continue
					}
					if index["unique"] != true {
						t.Fatalf("Overview index %s is not unique: %v", name, index)
					}
					if filter, present := index["partialFilterExpression"]; present {
						t.Fatalf("new Overview index %s has a partial filter: %v", name, filter)
					}
					return
				}
				t.Fatalf("Overview index %s was not created", name)
			})
		}
	}
}

func TestMongoOverviewIndexAssuranceAcceptsExistingUnfilteredIndexes(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	indexes := []struct {
		collection string
		name       string
		keys       bson.D
	}{
		{overviewVersionsCollection, "overview_project_revision_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "revision", Value: 1}}},
		{overviewVersionsCollection, "overview_project_operation_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}}},
		{overviewMediaCollection, "overview_project_operation_slot_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "slot", Value: 1}}},
	}
	for _, legacy := range indexes {
		t.Run(legacy.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			client, err := mongo.Connect(options.Client().ApplyURI(uri))
			if err != nil {
				t.Fatal(err)
			}
			database := fmt.Sprintf("overview_legacy_index_%d", time.Now().UnixNano())
			db := client.Database(database)
			t.Cleanup(func() {
				if err := db.Drop(context.Background()); err != nil {
					t.Errorf("drop historical Overview index fixture database: %v", err)
				}
				if err := client.Disconnect(context.Background()); err != nil {
					t.Errorf("disconnect historical Overview index fixture client: %v", err)
				}
			})
			model := mongo.IndexModel{Keys: legacy.keys, Options: options.Index().SetName(legacy.name).SetUnique(true)}
			if _, err := db.Collection(legacy.collection).Indexes().CreateOne(ctx, model); err != nil {
				t.Fatalf("create existing unfiltered index: %v", err)
			}
			store := &MongoStore{client: client}
			if err := store.ensureOverviewIndexes(ctx, db); err != nil {
				t.Fatalf("existing Overview index %s must remain usable without manual Atlas repair: %v", legacy.name, err)
			}
		})
	}
}

func TestMongoOverviewHTTPWritesAcceptBothPersistedIndexCohorts(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	for _, cohort := range []struct {
		name      string
		setFilter bool
	}{
		{name: "historical-unfiltered"},
		{name: "current-empty-filter", setFilter: true},
	} {
		t.Run(cohort.name, func(t *testing.T) {
			store, ctx := overviewMongoFixtureWithExistingIndexes(t, cohort.name, cohort.setFilter)
			db, _, err := store.overviewProject(ctx, "company-overview", "project-a")
			if err != nil {
				t.Fatal(err)
			}
			manifest := activateOverviewTestManifest(t, ctx, store)
			origin := OverviewKnowledgeOrigin{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, Documents: manifest.Documents}
			firstDesktop := testOverviewPNGColor(t, color.RGBA{R: 20, G: 30, B: 40, A: 255})
			firstMobile := testOverviewPNGColor(t, color.RGBA{R: 50, G: 60, B: 70, A: 255})
			secondDesktop := testOverviewPNGColor(t, color.RGBA{R: 80, G: 90, B: 100, A: 255})
			secondMobile := testOverviewPNGColor(t, color.RGBA{R: 110, G: 120, B: 130, A: 255})
			input := OverviewWriteRequest{ProjectID: "project-a", ExpectedRevision: 0, OperationID: "overview-http-first", Actor: "owner", Content: testOverviewContent(), Media: []OverviewMediaInput{{Slot: "desktop", Data: firstDesktop}, {Slot: "mobile", Data: firstMobile}}, KnowledgeOrigin: origin}
			handler := NewHandler(store)
			post := func(companyID, projectID string, request OverviewWriteRequest) OverviewReadResponse {
				t.Helper()
				body, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				path := "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/overview"
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
				if response.Code != http.StatusOK {
					t.Fatalf("Overview HTTP write status=%d body=%s", response.Code, response.Body.String())
				}
				var result OverviewReadResponse
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			first := post("company-overview", "project-a", input)
			if first.Revision != 1 || first.State != "available" {
				t.Fatalf("first HTTP write=%+v", first)
			}
			edit := input
			edit.ExpectedRevision = 1
			edit.OperationID = "overview-http-second"
			edit.Content.Tagline = "An updated tagline"
			edit.Media = []OverviewMediaInput{{Slot: "desktop", Data: secondDesktop}, {Slot: "mobile", Data: secondMobile}}
			second := post("company-overview", "project-a", edit)
			if second.Revision != 2 || second.Content == nil || second.Content.Tagline != edit.Content.Tagline {
				t.Fatalf("second HTTP write=%+v", second)
			}
			replay := post("company-overview", "project-a", input)
			if replay.Revision != 1 {
				t.Fatalf("exact operation replay returned revision %d; want 1", replay.Revision)
			}
			readResponse := httptest.NewRecorder()
			handler.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-overview/projects/project-a/overview/2", nil))
			var exact OverviewReadResponse
			if readResponse.Code != http.StatusOK || json.Unmarshal(readResponse.Body.Bytes(), &exact) != nil || exact.Revision != 2 {
				t.Fatalf("exact HTTP read status=%d body=%s", readResponse.Code, readResponse.Body.String())
			}
			for _, exactMedia := range []struct {
				revision int
				slot     string
				want     []byte
			}{{1, "desktop", firstDesktop}, {1, "mobile", firstMobile}, {2, "desktop", secondDesktop}, {2, "mobile", secondMobile}} {
				mediaResponse := httptest.NewRecorder()
				mediaPath := fmt.Sprintf("/internal/v1/companies/company-overview/projects/project-a/overview/%d/media/%s", exactMedia.revision, exactMedia.slot)
				handler.ServeHTTP(mediaResponse, httptest.NewRequest(http.MethodGet, mediaPath, nil))
				if mediaResponse.Code != http.StatusOK || !bytes.Equal(mediaResponse.Body.Bytes(), exactMedia.want) {
					t.Fatalf("immutable media read revision=%d slot=%s status=%d bytes_match=%v", exactMedia.revision, exactMedia.slot, mediaResponse.Code, bytes.Equal(mediaResponse.Body.Bytes(), exactMedia.want))
				}
			}
			if count, err := db.Collection(overviewVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "project-a"}}); err != nil || count != 2 {
				t.Fatalf("distinct writes/replay produced revision count=%d err=%v", count, err)
			}
			assertOverviewTestIndexesUnchanged(t, ctx, db, cohort.setFilter)
			duplicateIndexes := []struct {
				collection string
				document   bson.D
			}{
				{overviewVersionsCollection, bson.D{{Key: "project_id", Value: "project-a"}, {Key: "revision", Value: int64(2)}, {Key: "operation_id", Value: "other-operation"}}},
				{overviewVersionsCollection, bson.D{{Key: "project_id", Value: "project-a"}, {Key: "revision", Value: int64(3)}, {Key: "operation_id", Value: "overview-http-second"}}},
				{overviewMediaCollection, bson.D{{Key: "project_id", Value: "project-a"}, {Key: "operation_id", Value: "overview-http-second"}, {Key: "slot", Value: "desktop"}}},
			}
			for _, duplicate := range duplicateIndexes {
				if _, err := db.Collection(duplicate.collection).InsertOne(ctx, duplicate.document); !errors.Is(normalizeMongoError(err), ErrConflict) {
					t.Fatalf("unique Overview index on %s did not reject duplicate: %v", duplicate.collection, err)
				}
			}

			if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-other", CompanyName: "Other company", ProjectID: "project-a", ProjectName: "Other project", Binding: validTestBinding()}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.client.Database(store.tenantDatabase("company-other")).Drop(context.Background()); err != nil {
					t.Errorf("drop second Company Overview fixture database: %v", err)
				}
			})
			if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-overview", CompanyName: "Overview", ProjectID: "project-b", ProjectName: "Second project", Binding: validTestBinding()}); err != nil {
				t.Fatal(err)
			}
			var secondProjectInput OverviewWriteRequest
			for i, identity := range []struct{ company, project string }{{"company-other", "project-a"}, {"company-overview", "project-b"}} {
				manifest := activateOverviewTestManifestForProject(t, ctx, store, identity.company, identity.project)
				identityDesktop := testOverviewPNGColor(t, color.RGBA{R: uint8(140 + i*20), G: 11, B: 12, A: 255})
				identityMobile := testOverviewPNGColor(t, color.RGBA{R: uint8(150 + i*20), G: 21, B: 22, A: 255})
				identityInput := OverviewWriteRequest{
					ProjectID: identity.project, ExpectedRevision: 0, OperationID: "overview-http-first",
					Actor: "owner", Content: testOverviewContent(),
					Media:           []OverviewMediaInput{{Slot: "desktop", Data: identityDesktop}, {Slot: "mobile", Data: identityMobile}},
					KnowledgeOrigin: OverviewKnowledgeOrigin{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, Documents: manifest.Documents},
				}
				if identity.project == "project-b" {
					secondProjectInput = identityInput
				}
				if result := post(identity.company, identity.project, identityInput); result.Revision != 1 {
					t.Fatalf("isolated Overview write %s/%s returned revision %d", identity.company, identity.project, result.Revision)
				}
				readResponse := httptest.NewRecorder()
				readPath := "/internal/v1/companies/" + identity.company + "/projects/" + identity.project + "/overview/1"
				handler.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, readPath, nil))
				var isolated OverviewReadResponse
				if readResponse.Code != http.StatusOK || json.Unmarshal(readResponse.Body.Bytes(), &isolated) != nil || isolated.Revision != 1 || isolated.ProjectID != identity.project {
					t.Fatalf("isolated exact Overview read %s/%s: status=%d body=%s", identity.company, identity.project, readResponse.Code, readResponse.Body.String())
				}
				for _, media := range []struct {
					slot string
					want []byte
				}{{"desktop", identityDesktop}, {"mobile", identityMobile}} {
					mediaResponse := httptest.NewRecorder()
					mediaPath := fmt.Sprintf("/internal/v1/companies/%s/projects/%s/overview/1/media/%s", identity.company, identity.project, media.slot)
					handler.ServeHTTP(mediaResponse, httptest.NewRequest(http.MethodGet, mediaPath, nil))
					if mediaResponse.Code != http.StatusOK || !bytes.Equal(mediaResponse.Body.Bytes(), media.want) {
						t.Fatalf("isolated media read %s/%s/%s returned wrong bytes: status=%d", identity.company, identity.project, media.slot, mediaResponse.Code)
					}
				}
				if bytes.Equal(identityDesktop, firstDesktop) || bytes.Equal(identityMobile, firstMobile) {
					t.Fatal("test fixture did not assign distinct media bytes across tenant/project identities")
				}
			}
			mismatched := secondProjectInput
			mismatched.ProjectID = "project-a"
			countsBefore := map[string][2]int64{}
			for _, projectID := range []string{"project-a", "project-b"} {
				versionCount, err := db.Collection(overviewVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}})
				if err != nil {
					t.Fatal(err)
				}
				mediaCount, err := db.Collection(overviewMediaCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}})
				if err != nil {
					t.Fatal(err)
				}
				countsBefore[projectID] = [2]int64{versionCount, mediaCount}
			}
			body, err := json.Marshal(mismatched)
			if err != nil {
				t.Fatal(err)
			}
			mismatchResponse := httptest.NewRecorder()
			mismatchPath := "/internal/v1/companies/company-overview/projects/project-b/overview"
			handler.ServeHTTP(mismatchResponse, httptest.NewRequest(http.MethodPost, mismatchPath, bytes.NewReader(body)))
			var mismatchTeach errorResponse
			if mismatchResponse.Code != http.StatusBadRequest || json.Unmarshal(mismatchResponse.Body.Bytes(), &mismatchTeach) != nil || mismatchTeach.Error != ErrInvalid.Error() || strings.TrimSpace(mismatchTeach.Teach) == "" {
				t.Fatalf("body/path identity mismatch did not return the expected invalid TEACH: status=%d body=%s", mismatchResponse.Code, mismatchResponse.Body.String())
			}
			projectRead := httptest.NewRecorder()
			handler.ServeHTTP(projectRead, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-overview/projects/project-b/overview/1", nil))
			var projectResponse OverviewReadResponse
			if projectRead.Code != http.StatusOK || json.Unmarshal(projectRead.Body.Bytes(), &projectResponse) != nil || projectResponse.Revision != 1 {
				t.Fatalf("mismatched body identity changed second Project overview: status=%d body=%s", projectRead.Code, projectRead.Body.String())
			}
			for _, projectID := range []string{"project-a", "project-b"} {
				versionCount, err := db.Collection(overviewVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}})
				if err != nil {
					t.Fatal(err)
				}
				mediaCount, err := db.Collection(overviewMediaCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}})
				if err != nil {
					t.Fatal(err)
				}
				if countsBefore[projectID] != [2]int64{versionCount, mediaCount} {
					t.Fatalf("mismatched body identity changed Project %s counts from %v to versions=%d media=%d", projectID, countsBefore[projectID], versionCount, mediaCount)
				}
			}
		})
	}
}

func overviewTestIndexSpecs() []overviewIndexSpec {
	return []overviewIndexSpec{
		{collection: overviewVersionsCollection, name: "overview_project_revision_unique", keys: bson.D{{Key: "project_id", Value: 1}, {Key: "revision", Value: 1}}},
		{collection: overviewVersionsCollection, name: "overview_project_operation_unique", keys: bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}}},
		{collection: overviewMediaCollection, name: "overview_project_operation_slot_unique", keys: bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "slot", Value: 1}}},
	}
}

func TestMongoOverviewFreshTenantHTTPPublicationAndReadback(t *testing.T) {
	store, ctx := overviewMongoFixture(t, "fresh-http")
	db, _, err := store.overviewProject(ctx, "company-overview", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	assertOverviewTestIndexesUnchanged(t, ctx, db, false)
	manifest := activateOverviewTestManifest(t, ctx, store)
	desktop := testOverviewPNGColor(t, color.RGBA{R: 201, G: 22, B: 33, A: 255})
	mobile := testOverviewPNGColor(t, color.RGBA{R: 202, G: 44, B: 55, A: 255})
	input := OverviewWriteRequest{
		ProjectID: "project-a", ExpectedRevision: 0, OperationID: "overview-fresh-http", Actor: "owner", Content: testOverviewContent(),
		Media:           []OverviewMediaInput{{Slot: "desktop", Data: desktop}, {Slot: "mobile", Data: mobile}},
		KnowledgeOrigin: OverviewKnowledgeOrigin{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, Documents: manifest.Documents},
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store)
	base := "/internal/v1/companies/company-overview/projects/project-a/overview"
	write := httptest.NewRecorder()
	handler.ServeHTTP(write, httptest.NewRequest(http.MethodPost, base, bytes.NewReader(body)))
	var result OverviewReadResponse
	if write.Code != http.StatusOK || json.Unmarshal(write.Body.Bytes(), &result) != nil || result.Revision != 1 || result.State != "available" {
		t.Fatalf("fresh tenant HTTP publication status=%d body=%s", write.Code, write.Body.String())
	}
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, base+"/1", nil))
	var exact OverviewReadResponse
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &exact) != nil || exact.Revision != 1 || exact.ProjectID != "project-a" {
		t.Fatalf("fresh tenant exact read status=%d body=%s", read.Code, read.Body.String())
	}
	for _, media := range []struct {
		slot string
		want []byte
	}{{"desktop", desktop}, {"mobile", mobile}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/1/media/"+media.slot, nil))
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), media.want) {
			t.Fatalf("fresh tenant exact media read %s status=%d", media.slot, response.Code)
		}
	}
}

func TestMongoOverviewIndexAssuranceUsesTwoBoundedSpecReads(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	var listReads atomic.Int64
	var createCalls atomic.Int64
	var dropCalls atomic.Int64
	monitor := &event.CommandMonitor{Started: func(_ context.Context, command *event.CommandStartedEvent) {
		switch command.CommandName {
		case "listIndexes":
			listReads.Add(1)
		case "createIndexes":
			createCalls.Add(1)
		case "dropIndexes":
			dropCalls.Add(1)
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(monitor))
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("overview_index_commands_%d", time.Now().UnixNano())
	db := client.Database(database)
	t.Cleanup(func() {
		if err := db.Drop(context.Background()); err != nil {
			t.Errorf("drop bounded-read fixture database: %v", err)
		}
		if err := client.Disconnect(context.Background()); err != nil {
			t.Errorf("disconnect bounded-read fixture client: %v", err)
		}
	})
	store := &MongoStore{client: client}
	if err := store.ensureOverviewIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureOverviewIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := listReads.Load(); got != 4 {
		t.Fatalf("two assurance calls used %d index-spec reads; want two per call", got)
	}
	if got := createCalls.Load(); got != 6 {
		t.Fatalf("two assurance calls used %d idempotent create calls; want three per call", got)
	}
	if got := dropCalls.Load(); got != 0 {
		t.Fatalf("two assurance calls issued %d dropIndexes commands; want none", got)
	}
}

func overviewMongoFixtureWithExistingIndexes(t *testing.T, suffix string, emptyFilter bool) (*MongoStore, context.Context) {
	t.Helper()
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	stamp := fmt.Sprintf("overview_%s_%d", suffix, time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	db := store.client.Database(store.tenantDatabase("company-overview"))
	for _, spec := range overviewTestIndexSpecs() {
		indexOptions := options.Index().SetName(spec.name).SetUnique(true)
		if emptyFilter {
			indexOptions.SetPartialFilterExpression(bson.D{})
		}
		if _, err := db.Collection(spec.collection).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: spec.keys, Options: indexOptions}); err != nil {
			t.Fatalf("seed pre-existing Overview index %s: %v", spec.name, err)
		}
	}
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-overview", CompanyName: "Overview", ProjectID: "project-a", ProjectName: "Project A", Binding: validTestBinding()}); err != nil {
		t.Fatalf("tenant provisioning with pre-existing Overview indexes: %v", err)
	}
	t.Cleanup(func() {
		if err := store.client.Database(store.tenantDatabase("company-overview")).Drop(context.Background()); err != nil {
			t.Errorf("drop pre-existing-index fixture tenant database: %v", err)
		}
		if err := store.control.Drop(context.Background()); err != nil {
			t.Errorf("drop pre-existing-index fixture control database: %v", err)
		}
		if err := store.Close(context.Background()); err != nil {
			t.Errorf("close pre-existing-index fixture store: %v", err)
		}
	})
	assertOverviewTestIndexesUnchanged(t, ctx, db, emptyFilter)
	return store, ctx
}

func assertOverviewTestIndexesUnchanged(t *testing.T, ctx context.Context, db *mongo.Database, emptyFilter bool) {
	t.Helper()
	for _, spec := range overviewTestIndexSpecs() {
		cursor, err := db.Collection(spec.collection).Indexes().List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for cursor.Next(ctx) {
			raw := cursor.Current
			if raw.Lookup("name").Type != bson.TypeString || raw.Lookup("name").StringValue() != spec.name {
				continue
			}
			found = true
			if raw.Lookup("unique").Type != bson.TypeBoolean || !raw.Lookup("unique").Boolean() {
				t.Errorf("Overview index %s lost uniqueness after assurance", spec.name)
			}
			filter := raw.Lookup("partialFilterExpression")
			if emptyFilter {
				if filter.Type != bson.TypeEmbeddedDocument {
					t.Errorf("Overview index %s empty-filter shape changed: %v", spec.name, filter)
				} else if elements, decodeErr := filter.Document().Elements(); decodeErr != nil || len(elements) != 0 {
					t.Errorf("Overview index %s empty-filter shape changed: %v", spec.name, filter)
				}
			} else if filter.Type != 0 {
				t.Errorf("Overview index %s unfiltered shape changed: %v", spec.name, filter)
			}
		}
		if err := cursor.Err(); err != nil {
			t.Fatal(err)
		}
		_ = cursor.Close(ctx)
		if !found {
			t.Errorf("Overview index %s disappeared after assurance", spec.name)
		}
	}
}

func TestOverviewPersistedIndexSpecificationIsExact(t *testing.T) {
	keys := bson.D{{Key: "project_id", Value: 1}, {Key: "revision", Value: 1}}
	base := bson.D{{Key: "v", Value: int32(2)}, {Key: "key", Value: keys}, {Key: "name", Value: "overview_project_revision_unique"}, {Key: "ns", Value: "tenant.project_overview_versions"}, {Key: "unique", Value: true}}
	tests := []struct {
		name  string
		extra bson.D
		valid bool
	}{
		{name: "unfiltered", valid: true},
		{name: "empty filter", extra: bson.D{{Key: "partialFilterExpression", Value: bson.D{}}}, valid: true},
		{name: "wrong ordered keys", extra: bson.D{{Key: "key", Value: bson.D{{Key: "revision", Value: 1}, {Key: "project_id", Value: 1}}}}},
		{name: "nonunique", extra: bson.D{{Key: "unique", Value: false}}},
		{name: "nonempty filter", extra: bson.D{{Key: "partialFilterExpression", Value: bson.D{{Key: "project_id", Value: "project-a"}}}}},
		{name: "null filter", extra: bson.D{{Key: "partialFilterExpression", Value: nil}}},
		{name: "non-simple collation", extra: bson.D{{Key: "collation", Value: bson.D{{Key: "locale", Value: "en"}, {Key: "strength", Value: 2}}}}},
		{name: "extra semantic option", extra: bson.D{{Key: "sparse", Value: false}}},
		{name: "wrong namespace", extra: bson.D{{Key: "ns", Value: "tenant.other_collection"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := append(bson.D(nil), base...)
			for _, replacement := range test.extra {
				updated := false
				for i := range document {
					if document[i].Key == replacement.Key {
						document[i].Value = replacement.Value
						updated = true
						break
					}
				}
				if !updated {
					document = append(document, replacement)
				}
			}
			raw, err := bson.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			got := validOverviewIndexSpec(bson.Raw(raw), "tenant.project_overview_versions", "overview_project_revision_unique", keys)
			if got != test.valid {
				t.Fatalf("validOverviewIndexSpec=%v; want %v", got, test.valid)
			}
		})
	}
}

func TestMongoOverviewHTTPRejectsIncompatibleIndexWithoutRepair(t *testing.T) {
	store, ctx := overviewMongoFixture(t, "invalid-index-http")
	db, _, err := store.overviewProject(ctx, "company-overview", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	index := overviewTestIndexSpecs()[0]
	indexes := db.Collection(index.collection).Indexes()
	if err := indexes.DropOne(ctx, index.name); err != nil {
		t.Fatal(err)
	}
	if _, err := indexes.CreateOne(ctx, mongo.IndexModel{Keys: index.keys, Options: options.Index().SetName(index.name)}); err != nil {
		t.Fatal(err)
	}
	manifest := activateOverviewTestManifest(t, ctx, store)
	input := OverviewWriteRequest{ProjectID: "project-a", ExpectedRevision: 0, OperationID: "invalid-index-write", Actor: "owner", Content: testOverviewContent(), Media: []OverviewMediaInput{{Slot: "desktop", Data: testOverviewPNG(t)}, {Slot: "mobile", Data: testOverviewPNG(t)}}, KnowledgeOrigin: OverviewKnowledgeOrigin{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, Documents: manifest.Documents}}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	path := "/internal/v1/companies/company-overview/projects/project-a/overview"
	NewHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(strings.ToLower(response.Body.String()), "index") || strings.Contains(strings.ToLower(response.Body.String()), "mongo") {
		t.Fatalf("incompatible index response exposed internals or was not unavailable: status=%d body=%s", response.Code, response.Body.String())
	}
	cursor, err := indexes.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var persisted []bson.M
	if err := cursor.All(ctx, &persisted); err != nil {
		t.Fatal(err)
	}
	for _, item := range persisted {
		if item["name"] == index.name {
			if item["unique"] == true {
				t.Fatalf("index assurance rewrote the incompatible index: %v", item)
			}
			return
		}
	}
	t.Fatalf("incompatible persisted index %q was removed", index.name)
}

func TestMongoOverviewPersistedInvalidSpecsRejectProvisioningAndWriteAssurance(t *testing.T) {
	if os.Getenv("REGISTRY_TEST_MONGODB_URI") == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	for _, invalid := range []struct{ name, kind string }{
		{"wrong-ordered-keys", "wrong-keys"},
		{"nonunique", "nonunique"},
		{"nonempty-filter", "filter"},
		{"extra-semantic-option", "sparse"},
		{"inherited-non-simple-collation", "collation"},
	} {
		for _, path := range []string{"provisioning", "write-time"} {
			t.Run(invalid.name+"/"+path, func(t *testing.T) {
				var store *MongoStore
				var db *mongo.Database
				var ctx context.Context
				if path == "write-time" {
					store, ctx = overviewMongoFixture(t, "invalid-"+invalid.kind)
					db = store.client.Database(store.tenantDatabase("company-overview"))
				} else {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
					t.Cleanup(cancel)
					client, err := mongo.Connect(options.Client().ApplyURI(os.Getenv("REGISTRY_TEST_MONGODB_URI")))
					if err != nil {
						t.Fatal(err)
					}
					database := fmt.Sprintf("overview_invalid_provision_%s_%d", invalid.kind, time.Now().UnixNano())
					db = client.Database(database)
					t.Cleanup(func() {
						if err := db.Drop(context.Background()); err != nil {
							t.Errorf("drop invalid-index provisioning fixture: %v", err)
						}
						if err := client.Disconnect(context.Background()); err != nil {
							t.Errorf("disconnect invalid-index provisioning fixture: %v", err)
						}
					})
					store = &MongoStore{client: client}
				}
				spec := overviewTestIndexSpecs()[0]
				if invalid.kind == "collation" {
					if path == "write-time" {
						if err := db.Collection(spec.collection).Drop(ctx); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.CreateCollection(ctx, spec.collection, options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 2})); err != nil {
						t.Fatal(err)
					}
				} else {
					if path == "write-time" {
						if err := db.Collection(spec.collection).Indexes().DropOne(ctx, spec.name); err != nil {
							t.Fatal(err)
						}
					}
					keys := spec.keys
					indexOptions := options.Index().SetName(spec.name).SetUnique(true)
					switch invalid.kind {
					case "wrong-keys":
						keys = bson.D{{Key: "revision", Value: 1}, {Key: "project_id", Value: 1}}
					case "nonunique":
						indexOptions.SetUnique(false)
					case "filter":
						indexOptions.SetPartialFilterExpression(bson.D{{Key: "project_id", Value: bson.D{{Key: "$type", Value: "string"}}}})
					case "sparse":
						indexOptions.SetSparse(true)
					}
					if _, err := db.Collection(spec.collection).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: keys, Options: indexOptions}); err != nil {
						t.Fatalf("seed invalid persisted index: %v", err)
					}
				}
				var err error
				if path == "provisioning" {
					err = store.ensureTenantIndexes(ctx, db.Name())
				} else {
					err = store.ensureOverviewIndexes(ctx, db)
				}
				if err == nil {
					t.Fatalf("%s assurance accepted persisted %s index", path, invalid.name)
				}
				cursor, listErr := db.Collection(spec.collection).Indexes().List(ctx)
				if listErr != nil {
					t.Fatal(listErr)
				}
				defer cursor.Close(ctx)
				foundInvalid := false
				for cursor.Next(ctx) {
					raw := cursor.Current
					if raw.Lookup("name").Type == bson.TypeString && raw.Lookup("name").StringValue() == spec.name {
						foundInvalid = !validOverviewIndexSpec(raw, db.Name()+"."+spec.collection, spec.name, spec.keys)
					}
				}
				if !foundInvalid {
					t.Fatalf("%s assurance did not preserve the incompatible %s spec", path, invalid.name)
				}
			})
		}
	}
}

func TestMongoOverviewIndexAssuranceRejectsInheritedNonSimpleCollation(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("overview_collation_index_%d", time.Now().UnixNano())
	db := client.Database(database)
	t.Cleanup(func() {
		if err := db.Drop(context.Background()); err != nil {
			t.Errorf("drop inherited-collation fixture database: %v", err)
		}
		if err := client.Disconnect(context.Background()); err != nil {
			t.Errorf("disconnect inherited-collation fixture client: %v", err)
		}
	})
	if err := db.CreateCollection(ctx, overviewVersionsCollection,
		options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 2})); err != nil {
		t.Fatal(err)
	}
	store := &MongoStore{client: client}
	if err := store.ensureOverviewIndexes(ctx, db); err == nil {
		t.Fatal("Overview assurance accepted indexes inheriting a non-simple collection collation")
	}
}

func TestMongoOverviewIdenticalReplayPrecedesCurrentManifestValidation(t *testing.T) {
	store, ctx := overviewMongoFixture(t, "manifest-replay")
	manifest := activateOverviewTestManifest(t, ctx, store)
	content := testOverviewContent()
	media := []OverviewMediaInput{{Slot: "desktop", Data: testOverviewPNG(t)}, {Slot: "mobile", Data: testOverviewPNG(t)}}
	input := OverviewWriteRequest{ProjectID: "project-a", ExpectedRevision: 0, OperationID: "overview-replay", Actor: "author", Content: content, Media: media, KnowledgeOrigin: OverviewKnowledgeOrigin{ProjectID: manifest.ProjectID, GenerationID: manifest.GenerationID, Documents: manifest.Documents}}
	first, err := store.WriteOverview(ctx, "company-overview", "project-a", input)
	if err != nil || first.State != "available" || first.Revision != 1 {
		t.Fatalf("initial Overview write=%+v err=%v", first, err)
	}
	mandate, err := store.ReadCanonical(ctx, "company-overview", "project-a", FamilyMandate, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	mandate.Payload = json.RawMessage(`{"purpose":"Updated after Overview publication","scope":["web"]}`)
	if _, err := store.EditCanonical(ctx, "company-overview", CanonicalMutation{ProjectID: "project-a", Family: FamilyMandate, Record: mandate, ExpectedRevision: mandate.Revision, OperationID: "edit-after-overview", Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.WriteOverview(ctx, "company-overview", "project-a", input)
	if err != nil || replayed.State != "stale" || replayed.Revision != first.Revision {
		t.Fatalf("identical replay was rejected after manifest changed: response=%+v err=%v", replayed, err)
	}
}

func overviewMongoFixture(t *testing.T, suffix string) (*MongoStore, context.Context) {
	t.Helper()
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB replica-set fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	stamp := fmt.Sprintf("overview_%s_%d", suffix, time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		if err := store.client.Database(store.tenantDatabase("company-overview")).Drop(context.Background()); err != nil {
			t.Errorf("drop Overview fixture tenant database: %v", err)
		}
		if err := store.control.Drop(context.Background()); err != nil {
			t.Errorf("drop Overview fixture control database: %v", err)
		}
		if err := store.Close(context.Background()); err != nil {
			t.Errorf("close Overview fixture store: %v", err)
		}
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-overview", CompanyName: "Overview", ProjectID: "project-a", ProjectName: "Project A", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func activateOverviewTestManifest(t *testing.T, ctx context.Context, store *MongoStore) CanonicalManifest {
	return activateOverviewTestManifestForProject(t, ctx, store, "company-overview", "project-a")
}

func activateOverviewTestManifestForProject(t *testing.T, ctx context.Context, store *MongoStore, companyID, projectID string) CanonicalManifest {
	t.Helper()
	input := CanonicalImport{ProjectID: projectID, GenerationID: "overview-knowledge", SourceRevision: strings.Repeat("a", 40), OperationID: "overview-import-" + projectID, Actor: "migration-agent", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyMandate, projectID, `{"purpose":"Build","scope":["web"]}`),
		canonicalTestRecord(FamilyDomain, projectID, `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`),
		canonicalTestRecord(FamilyConstitution, projectID, `{"promotion_mode":"mvp","capability_namespaces":["go"],"rules":[{"id":"scope","status":"current","statement":"Scope reads"}]}`),
		canonicalTestRecord(FamilyRoadmap, projectID, `{"stages":[{"id":"launch","name":"Launch","status":"planned"}]}`),
		canonicalTestRecord(FamilyModule, "projects", `{"purpose":"Owns project work","decisions":[{"id":"DEC-ART-01","status":"current","statement":"Module owns routes"}],"schemas":[{"id":"project","name":"projects","kind":"collection","fields":[{"name":"name","type":"string"}]}]}`),
		canonicalTestRecord(FamilyPolicy, "access-policy", `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`),
	}}
	for i := range input.Records {
		input.Records[i].Record.ProjectID = input.ProjectID
		input.Records[i].Record.GenerationID = input.GenerationID
	}
	if _, err := store.ImportCanonicalDraft(ctx, companyID, input); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.ActivateCanonical(ctx, companyID, input.ProjectID, input.GenerationID, "activate-overview-"+projectID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestOverviewMediaReadVerifiesBytesAndPNG(t *testing.T) {
	data := testOverviewPNG(t)
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	ref := OverviewMediaReference{Slot: "desktop", Digest: digest}
	stored := overviewMediaDocument{ProjectID: "project-a", OperationID: "op-a", Slot: "desktop", Digest: digest, Data: data}
	if !overviewMediaMatches(ref, stored) {
		t.Fatal("valid immutable PNG media did not match its reference")
	}
	stored.Data = append([]byte(nil), data...)
	stored.Data[len(stored.Data)-1] ^= 0xff
	if overviewMediaMatches(ref, stored) {
		t.Fatal("corrupted bytes matched the stored media digest")
	}
	stored.Data = []byte("not a PNG")
	if overviewMediaMatches(ref, stored) {
		t.Fatal("invalid PNG bytes matched a media reference")
	}
}

func TestOverviewMetadataReadRequiresExactUniqueDeclaredMedia(t *testing.T) {
	content := testOverviewContent()
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	refs := []OverviewMediaReference{{Slot: "desktop", Digest: digestA}, {Slot: "mobile", Digest: digestB}}
	documents := []overviewMediaMetadata{
		{ProjectID: "project-a", OperationID: "op-a", Slot: "desktop", Digest: digestA},
		{ProjectID: "project-a", OperationID: "op-a", Slot: "mobile", Digest: digestB},
	}
	if !overviewMetadataMatches(content, refs, documents, "project-a", "op-a") {
		t.Fatal("complete committed reference metadata was rejected")
	}
	if overviewMetadataMatches(content, refs, documents[:1], "project-a", "op-a") {
		t.Fatal("missing media metadata was accepted")
	}
	duplicate := append(append([]overviewMediaMetadata(nil), documents...), documents[1])
	if overviewMetadataMatches(content, refs, duplicate, "project-a", "op-a") {
		t.Fatal("duplicate media metadata was accepted")
	}
	duplicateRef := append(append([]OverviewMediaReference(nil), refs...), refs[1])
	if overviewMetadataMatches(content, duplicateRef, append(documents, documents[1]), "project-a", "op-a") {
		t.Fatal("duplicate declared media reference was accepted")
	}
}

type overviewHTTPFixture struct {
	Service
	writeRequest OverviewWriteRequest
	readRevision int64
	mediaData    []byte
}

func (f *overviewHTTPFixture) WriteOverview(_ context.Context, companyID, projectID string, request OverviewWriteRequest) (OverviewReadResponse, error) {
	f.writeRequest = request
	return OverviewReadResponse{State: "available", CompanyID: companyID, ProjectID: projectID, Revision: 1}, nil
}

func (f *overviewHTTPFixture) ReadOverview(_ context.Context, companyID, projectID string, revision int64) (OverviewReadResponse, error) {
	f.readRevision = revision
	return OverviewReadResponse{State: "not_authored", CompanyID: companyID, ProjectID: projectID}, nil
}

func (f *overviewHTTPFixture) ReadOverviewMedia(_ context.Context, _, _ string, _ int64, _ string) ([]byte, string, error) {
	sum := sha256.Sum256(f.mediaData)
	return f.mediaData, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func TestOverviewHTTPRoutesAreProjectScopedAndServeExactMedia(t *testing.T) {
	fixture := &overviewHTTPFixture{mediaData: testOverviewPNG(t)}
	handler := NewHandler(fixture)
	base := "/internal/v1/companies/company-a/projects/project-a/overview"

	writeBody, err := json.Marshal(OverviewWriteRequest{ProjectID: "project-a", ExpectedRevision: 0, OperationID: "overview-op-1", Actor: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	writeResponse := httptest.NewRecorder()
	writeRequest := httptest.NewRequest(http.MethodPost, base, bytes.NewReader(writeBody))
	handler.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusOK || !strings.Contains(writeResponse.Body.String(), `"company_id":"company-a"`) || !strings.Contains(writeResponse.Body.String(), `"project_id":"project-a"`) {
		t.Fatalf("write response omitted proxy identities: status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}
	if fixture.writeRequest.ProjectID != "project-a" {
		t.Fatalf("write passed Project ID %q", fixture.writeRequest.ProjectID)
	}

	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, httptest.NewRequest(http.MethodGet, base+"/7", nil))
	if readResponse.Code != http.StatusOK || fixture.readRevision != 7 || !strings.Contains(readResponse.Body.String(), `"company_id":"company-a"`) {
		t.Fatalf("exact read did not preserve route identity/revision: status=%d revision=%d body=%s", readResponse.Code, fixture.readRevision, readResponse.Body.String())
	}

	mediaResponse := httptest.NewRecorder()
	handler.ServeHTTP(mediaResponse, httptest.NewRequest(http.MethodGet, base+"/7/media/desktop", nil))
	if mediaResponse.Code != http.StatusOK || mediaResponse.Header().Get("Content-Type") != "image/png" || !bytes.Equal(mediaResponse.Body.Bytes(), fixture.mediaData) {
		t.Fatalf("fixed-slot media response mismatch: status=%d headers=%v", mediaResponse.Code, mediaResponse.Header())
	}
	if mediaResponse.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("media response is missing nosniff")
	}

	badRevision := httptest.NewRecorder()
	handler.ServeHTTP(badRevision, httptest.NewRequest(http.MethodGet, base+"/0", nil))
	if badRevision.Code != http.StatusBadRequest {
		t.Fatalf("zero exact revision status=%d body=%s", badRevision.Code, badRevision.Body.String())
	}
}

func ptrString(value string) *string { return &value }
