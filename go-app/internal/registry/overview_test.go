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
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func testOverviewPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	imageData := image.NewRGBA(image.Rect(0, 0, 2, 2))
	imageData.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
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
		_ = store.client.Database(store.tenantDatabase("company-overview")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-overview", CompanyName: "Overview", ProjectID: "project-a", ProjectName: "Project A", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func activateOverviewTestManifest(t *testing.T, ctx context.Context, store *MongoStore) CanonicalManifest {
	t.Helper()
	input := CanonicalImport{ProjectID: "project-a", GenerationID: "overview-knowledge", SourceRevision: strings.Repeat("a", 40), OperationID: "overview-import", Actor: "migration-agent", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyMandate, "project-a", `{"purpose":"Build","scope":["web"]}`),
		canonicalTestRecord(FamilyDomain, "project-a", `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`),
		canonicalTestRecord(FamilyConstitution, "project-a", `{"promotion_mode":"mvp","capability_namespaces":["go"],"rules":[{"id":"scope","status":"current","statement":"Scope reads"}]}`),
		canonicalTestRecord(FamilyRoadmap, "project-a", `{"stages":[{"id":"launch","name":"Launch","status":"planned"}]}`),
		canonicalTestRecord(FamilyModule, "projects", `{"purpose":"Owns project work","decisions":[{"id":"DEC-ART-01","status":"current","statement":"Module owns routes"}],"schemas":[{"id":"project","name":"projects","kind":"collection","fields":[{"name":"name","type":"string"}]}]}`),
		canonicalTestRecord(FamilyPolicy, "access-policy", `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-ART-01"}]}`),
	}}
	for i := range input.Records {
		input.Records[i].Record.ProjectID = input.ProjectID
		input.Records[i].Record.GenerationID = input.GenerationID
	}
	if _, err := store.ImportCanonicalDraft(ctx, "company-overview", input); err != nil {
		t.Fatal(err)
	}
	manifest, err := store.ActivateCanonical(ctx, "company-overview", input.ProjectID, input.GenerationID, "activate-overview", "owner")
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
