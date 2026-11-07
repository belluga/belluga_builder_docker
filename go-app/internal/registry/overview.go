package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	overviewVersionsCollection = "project_overview_versions"
	overviewMediaCollection    = "project_overview_media"
	maxOverviewPNGBytes        = 2 << 20
	maxOverviewWriteBytes      = 18 << 20
)

var overviewSlots = []string{"desktop", "mobile", "problem", "proposal", "impact", "audience"}
var overviewSections = []string{"problem", "proposal", "impact", "audience"}

type OverviewSection struct {
	Heading  string   `json:"heading" bson:"heading"`
	Body     string   `json:"body" bson:"body"`
	Points   []string `json:"points" bson:"points"`
	ImageAlt *string  `json:"image_alt,omitempty" bson:"image_alt,omitempty"`
}

type OverviewContent struct {
	SchemaVersion string                     `json:"schema_version" bson:"schema_version"`
	Tagline       string                     `json:"tagline" bson:"tagline"`
	Summary       string                     `json:"summary" bson:"summary"`
	DesktopAlt    string                     `json:"desktop_alt" bson:"desktop_alt"`
	MobileAlt     string                     `json:"mobile_alt" bson:"mobile_alt"`
	Sections      map[string]OverviewSection `json:"sections" bson:"sections"`
}

type OverviewKnowledgeOrigin struct {
	ProjectID    string                   `json:"project_id" bson:"project_id"`
	GenerationID string                   `json:"generation_id" bson:"generation_id"`
	Documents    []CanonicalManifestEntry `json:"documents" bson:"documents"`
}

// Source is documentary provenance, never the live authority. OriginAbsent
// explicitly distinguishes reviewed manifest enrichment from source parity.
type OverviewSource struct {
	RepositoryID  string `json:"repository_id,omitempty" bson:"repository_id,omitempty"`
	Revision      string `json:"revision,omitempty" bson:"revision,omitempty"`
	ContentDigest string `json:"content_digest,omitempty" bson:"content_digest,omitempty"`
	OriginAbsent  bool   `json:"origin_absent" bson:"origin_absent"`
}

type OverviewMediaInput struct {
	Slot string `json:"slot"`
	Data []byte `json:"data"`
}

type OverviewWriteRequest struct {
	ProjectID        string                  `json:"project_id"`
	ExpectedRevision int64                   `json:"expected_revision"`
	OperationID      string                  `json:"operation_id"`
	Actor            string                  `json:"actor"`
	Content          OverviewContent         `json:"content"`
	Media            []OverviewMediaInput    `json:"media"`
	KnowledgeOrigin  OverviewKnowledgeOrigin `json:"knowledge_origin"`
	Source           OverviewSource          `json:"source"`
}

type OverviewMediaReference struct {
	Slot   string `json:"slot" bson:"slot"`
	Digest string `json:"digest" bson:"digest"`
	Href   string `json:"href" bson:"-"`
}

type OverviewReadResponse struct {
	State           string                   `json:"state"`
	CompanyID       string                   `json:"company_id"`
	ProjectID       string                   `json:"project_id"`
	Revision        int64                    `json:"revision,omitempty"`
	Content         *OverviewContent         `json:"content,omitempty"`
	Media           []OverviewMediaReference `json:"media,omitempty"`
	KnowledgeOrigin *OverviewKnowledgeOrigin `json:"knowledge_origin,omitempty"`
	Source          *OverviewSource          `json:"source,omitempty"`
	Teach           string                   `json:"teach,omitempty"`
}

type overviewVersionDocument struct {
	ObjectID        bson.ObjectID            `bson:"_id,omitempty"`
	ProjectID       string                   `bson:"project_id"`
	Revision        int64                    `bson:"revision"`
	OperationID     string                   `bson:"operation_id"`
	Actor           string                   `bson:"actor"`
	RequestDigest   string                   `bson:"request_digest"`
	Content         OverviewContent          `bson:"content"`
	Media           []OverviewMediaReference `bson:"media"`
	KnowledgeOrigin OverviewKnowledgeOrigin  `bson:"knowledge_origin"`
	Source          OverviewSource           `bson:"source"`
}

type overviewMediaDocument struct {
	ObjectID    bson.ObjectID `bson:"_id,omitempty"`
	ProjectID   string        `bson:"project_id"`
	OperationID string        `bson:"operation_id"`
	Slot        string        `bson:"slot"`
	Digest      string        `bson:"digest"`
	Data        []byte        `bson:"data"`
}

type overviewMediaMetadata struct {
	ProjectID   string `bson:"project_id"`
	OperationID string `bson:"operation_id"`
	Slot        string `bson:"slot"`
	Digest      string `bson:"digest"`
}

func overviewRetryWasPublished(projectRevision int64, projectOperation, projectDigest string, existing overviewVersionDocument, current overviewVersionDocument, versionCount int64) bool {
	if projectRevision == existing.Revision {
		return projectOperation == existing.OperationID && projectDigest == existing.RequestDigest
	}
	return projectRevision > existing.Revision && projectOperation == current.OperationID && projectDigest == current.RequestDigest && versionCount == projectRevision
}

func overviewMetadataMatches(content OverviewContent, refs []OverviewMediaReference, documents []overviewMediaMetadata, projectID, operationID string) bool {
	if err := validateOverviewEditorial(content); err != nil || len(refs) < 2 || len(refs) > 6 || len(documents) != len(refs) {
		return false
	}
	want := make(map[string]string, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if !overviewSlot(ref.Slot) || !validSourceDigest(ref.Digest) || seen[ref.Slot] {
			return false
		}
		seen[ref.Slot] = true
		want[ref.Slot] = ref.Digest
	}
	if !overviewContentSlotsMatch(content, seen) {
		return false
	}
	for _, document := range documents {
		digest, ok := want[document.Slot]
		if !ok || document.ProjectID != projectID || document.OperationID != operationID || document.Digest != digest {
			return false
		}
		delete(want, document.Slot)
	}
	return len(want) == 0
}

func overviewText(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= 2000
}

func validateOverviewEditorial(content OverviewContent) error {
	if content.SchemaVersion != "1" || !overviewText(content.Tagline) || !overviewText(content.Summary) ||
		!overviewText(content.DesktopAlt) || !overviewText(content.MobileAlt) || len(content.Sections) != 4 {
		return todoTeach("content", "provide the bounded Overview v1 tagline, summary, hero alt text and four named sections", ErrInvalid)
	}
	for _, name := range overviewSections {
		section, ok := content.Sections[name]
		if !ok || !overviewText(section.Heading) || !overviewText(section.Body) || section.Points == nil || len(section.Points) > 3 {
			return todoTeach("content.sections."+name, "provide heading, body and zero to three points", ErrInvalid)
		}
		for _, point := range section.Points {
			if !overviewText(point) {
				return todoTeach("content.sections."+name+".points", "provide nonblank bounded plain text", ErrInvalid)
			}
		}
		if section.ImageAlt != nil && !overviewText(*section.ImageAlt) {
			return todoTeach("content.sections."+name+".image_alt", "provide nonblank bounded alt text only for a present PNG", ErrInvalid)
		}
	}
	return nil
}

func overviewContentSlotsMatch(content OverviewContent, seen map[string]bool) bool {
	if !seen["desktop"] || !seen["mobile"] {
		return false
	}
	for _, name := range overviewSections {
		if seen[name] != (content.Sections[name].ImageAlt != nil) {
			return false
		}
	}
	return true
}

func validateOverviewContent(content OverviewContent, media []OverviewMediaInput) ([]OverviewMediaReference, error) {
	if err := validateOverviewEditorial(content); err != nil {
		return nil, err
	}
	if len(media) < 2 || len(media) > 6 {
		return nil, todoTeach("media", "provide desktop and mobile PNGs and at most four section PNGs", ErrInvalid)
	}
	seen := make(map[string]bool, len(media))
	refs := make([]OverviewMediaReference, 0, len(media))
	for _, item := range media {
		if !overviewSlot(item.Slot) || seen[item.Slot] {
			return nil, todoTeach("media.slot", "use each fixed slot at most once: desktop, mobile, problem, proposal, impact or audience", ErrInvalid)
		}
		seen[item.Slot] = true
		if len(item.Data) == 0 || len(item.Data) > maxOverviewPNGBytes || !bytes.HasPrefix(item.Data, []byte("\x89PNG\r\n\x1a\n")) {
			return nil, todoTeach("media."+item.Slot, "provide a PNG of at most 2 MiB", ErrInvalid)
		}
		config, err := png.DecodeConfig(bytes.NewReader(item.Data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16<<20 {
			return nil, todoTeach("media."+item.Slot, "provide a decodable PNG within 4096x4096 and 16 million pixels", ErrInvalid)
		}
		if _, err := png.Decode(bytes.NewReader(item.Data)); err != nil {
			return nil, todoTeach("media."+item.Slot, "provide a complete decodable PNG", ErrInvalid)
		}
		sum := sha256.Sum256(item.Data)
		refs = append(refs, OverviewMediaReference{Slot: item.Slot, Digest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	if !overviewContentSlotsMatch(content, seen) {
		return nil, todoTeach("media", "include both required hero PNGs", ErrInvalid)
	}
	sort.Slice(refs, func(i, j int) bool { return overviewSlotIndex(refs[i].Slot) < overviewSlotIndex(refs[j].Slot) })
	return refs, nil
}

func overviewSlot(slot string) bool { return overviewSlotIndex(slot) >= 0 }
func overviewSlotIndex(slot string) int {
	for i, candidate := range overviewSlots {
		if slot == candidate {
			return i
		}
	}
	return -1
}

func overviewOriginMatches(origin OverviewKnowledgeOrigin, manifest CanonicalManifest) bool {
	if !manifest.Active || manifest.State != "active" || origin.ProjectID != manifest.ProjectID || origin.GenerationID != manifest.GenerationID ||
		len(origin.Documents) != len(manifest.Documents) || len(origin.Documents) == 0 {
		return false
	}
	want := make(map[string]CanonicalManifestEntry, len(manifest.Documents))
	for _, item := range manifest.Documents {
		want[string(item.Family)+"/"+item.ID] = item
	}
	for _, item := range origin.Documents {
		key := string(item.Family) + "/" + item.ID
		matched, ok := want[key]
		if !ok || matched.Revision != item.Revision || matched.ContentDigest != item.ContentDigest {
			return false
		}
		delete(want, key)
	}
	return len(want) == 0
}

func (m *MongoStore) overviewProject(ctx context.Context, companyID, projectID string) (*mongo.Database, projectDocument, error) {
	if !validID(companyID) || !validID(projectID) {
		return nil, projectDocument{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return nil, projectDocument{}, err
	}
	db := m.client.Database(company.DatabaseName)
	var project projectDocument
	if err := db.Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}, {Key: "company_id", Value: companyID}}).Decode(&project); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, projectDocument{}, ErrNotFound
		}
		return nil, projectDocument{}, ErrUnavailable
	}
	if project.ObjectID.IsZero() || project.CompanyID != companyID {
		return nil, projectDocument{}, ErrUnavailable
	}
	return db, project, nil
}

func (m *MongoStore) WriteOverview(ctx context.Context, companyID, projectID string, input OverviewWriteRequest) (OverviewReadResponse, error) {
	if input.ProjectID != projectID || !validID(input.OperationID) || !validName(input.Actor) || input.ExpectedRevision < 0 || input.ExpectedRevision == math.MaxInt64 {
		return OverviewReadResponse{}, todoTeach("identity", "provide exact Project ID, stable operation ID, actor and current nonnegative revision", ErrInvalid)
	}
	refs, err := validateOverviewContent(input.Content, input.Media)
	if err != nil {
		return OverviewReadResponse{}, err
	}
	if input.Source.RepositoryID != "" && (!validGitRepositoryIdentity(input.Source.RepositoryID) || !validFullGitSHA(input.Source.Revision) || !validSourceDigest(input.Source.ContentDigest)) {
		return OverviewReadResponse{}, todoTeach("source", "provide an exact Git repository, full revision and content digest or omit all source fields", ErrInvalid)
	}
	if input.Source.RepositoryID == "" && (input.Source.Revision != "" || input.Source.ContentDigest != "") {
		return OverviewReadResponse{}, todoTeach("source", "omit all source fields together for API-authored content", ErrInvalid)
	}
	db, project, err := m.overviewProject(ctx, companyID, projectID)
	if err != nil {
		return OverviewReadResponse{}, err
	}
	encoded, _ := json.Marshal(input)
	sum := sha256.Sum256(encoded)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	versions := db.Collection(overviewVersionsCollection)
	var existing overviewVersionDocument
	readErr := versions.FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "operation_id", Value: input.OperationID}}).Decode(&existing)
	recoverExisting := false
	if readErr == nil {
		if existing.RequestDigest != digest {
			return OverviewReadResponse{}, todoTeach("operation_id", "retry only with the identical payload", ErrConflict)
		}
		if project.OverviewRevision == existing.Revision {
			if !overviewRetryWasPublished(project.OverviewRevision, project.OverviewOperation, project.OverviewDigest, existing, overviewVersionDocument{}, 0) {
				return OverviewReadResponse{}, todoTeach("operation_id", "the matching revision is not the published result of this operation", ErrConflict)
			}
			return m.ReadOverview(ctx, companyID, projectID, existing.Revision)
		}
		if project.OverviewRevision > existing.Revision {
			// A prior exact revision is replayable only when the current pointer
			// proves a later publication and the immutable revision sequence has
			// no gaps. A pre-CAS orphan cannot satisfy this invariant.
			var current overviewVersionDocument
			currentErr := versions.FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "revision", Value: project.OverviewRevision}}).Decode(&current)
			count, countErr := versions.CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "revision", Value: bson.M{"$lte": project.OverviewRevision}}})
			if currentErr != nil || countErr != nil {
				return OverviewReadResponse{}, ErrUnavailable
			}
			if !overviewRetryWasPublished(project.OverviewRevision, project.OverviewOperation, project.OverviewDigest, existing, current, count) {
				return OverviewReadResponse{}, todoTeach("operation_id", "the stored revision is not proven published; reload the current Overview", ErrConflict)
			}
			return m.ReadOverview(ctx, companyID, projectID, existing.Revision)
		}
		if project.OverviewRevision == input.ExpectedRevision && existing.Revision == project.OverviewRevision+1 {
			// Recover an exact operation that was staged by an older non-atomic
			// writer, or whose caller lost the first response. The transaction
			// below publishes this immutable version with the pointer.
			recoverExisting = true
		} else {
			return OverviewReadResponse{}, todoTeach("operation_id", "the matching operation is not the next unpublished revision; reload the current Overview", ErrConflict)
		}
	} else if !errors.Is(readErr, mongo.ErrNoDocuments) {
		return OverviewReadResponse{}, ErrUnavailable
	}
	if project.OverviewRevision != input.ExpectedRevision {
		return OverviewReadResponse{}, todoTeach("expected_revision", "reload the current Overview revision before changing it", ErrConflict)
	}
	if err := m.ensureOverviewIndexes(ctx, db); err != nil {
		return OverviewReadResponse{}, ErrUnavailable
	}
	if !recoverExisting {
		manifest, err := m.CanonicalManifest(ctx, companyID, projectID, "")
		if err != nil {
			return OverviewReadResponse{}, err
		}
		if !overviewOriginMatches(input.KnowledgeOrigin, manifest) {
			return OverviewReadResponse{}, todoTeach("knowledge_origin", "use every exact document from this Project's current active knowledge manifest; source provenance is recorded separately", ErrConflict)
		}
	}
	for _, item := range input.Media {
		sum := sha256.Sum256(item.Data)
		media := overviewMediaDocument{ObjectID: bson.NewObjectID(), ProjectID: projectID, OperationID: input.OperationID, Slot: item.Slot, Digest: "sha256:" + hex.EncodeToString(sum[:]), Data: item.Data}
		_, err := db.Collection(overviewMediaCollection).InsertOne(ctx, media)
		if errors.Is(normalizeMongoError(err), ErrConflict) {
			var prior overviewMediaDocument
			if db.Collection(overviewMediaCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "operation_id", Value: input.OperationID}, {Key: "slot", Value: item.Slot}}).Decode(&prior) != nil || prior.Digest != media.Digest || !bytes.Equal(prior.Data, item.Data) {
				return OverviewReadResponse{}, todoTeach("media."+item.Slot, "retry only with the identical immutable PNG", ErrConflict)
			}
		} else if err != nil {
			return OverviewReadResponse{}, normalizeMongoError(err)
		}
	}
	revision := input.ExpectedRevision + 1
	version := existing
	if !recoverExisting {
		version = overviewVersionDocument{ObjectID: bson.NewObjectID(), ProjectID: projectID, Revision: revision, OperationID: input.OperationID, Actor: input.Actor, RequestDigest: digest, Content: input.Content, Media: refs, KnowledgeOrigin: input.KnowledgeOrigin, Source: input.Source}
	}
	if err := m.publishOverviewVersion(ctx, db, project, version, !recoverExisting, input.ExpectedRevision); err != nil {
		return OverviewReadResponse{}, err
	}
	return m.ReadOverview(ctx, companyID, projectID, revision)
}

func (m *MongoStore) publishOverviewVersion(ctx context.Context, db *mongo.Database, project projectDocument, version overviewVersionDocument, insertVersion bool, expectedRevision int64) error {
	session, err := db.Client().StartSession()
	if err != nil {
		return ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if insertVersion {
			if _, e := db.Collection(overviewVersionsCollection).InsertOne(txCtx, version); e != nil {
				return nil, e
			}
		}
		filter := bson.D{{Key: "_id", Value: project.ObjectID}, {Key: "company_id", Value: project.CompanyID}, {Key: "id", Value: project.ID}}
		if expectedRevision == 0 {
			filter = append(filter, bson.E{Key: "overview_revision", Value: bson.M{"$in": []any{nil, int64(0)}}})
		} else {
			filter = append(filter, bson.E{Key: "overview_revision", Value: expectedRevision})
		}
		result, e := db.Collection(projectsCollection).UpdateOne(txCtx, filter, bson.M{"$set": bson.M{"overview_revision": version.Revision, "overview_operation": version.OperationID, "overview_digest": version.RequestDigest}})
		if e != nil {
			return nil, e
		}
		if result.MatchedCount != 1 {
			return nil, todoTeach("expected_revision", "reload the current Overview revision after a concurrent write", ErrConflict)
		}
		return nil, nil
	})
	if err != nil {
		var commandError mongo.CommandError
		if errors.As(err, &commandError) && commandError.Code == 112 {
			return todoTeach("expected_revision", "reload the current Overview revision after a concurrent write", ErrConflict)
		}
		return normalizeMongoError(err)
	}
	return nil
}

func (m *MongoStore) ensureOverviewIndexes(ctx context.Context, db *mongo.Database) error {
	// Existing registered Projects predate the Overview collections. These named
	// indexes match the tenant indexes installed at registration and are safe to
	// create again before the first Overview write.
	indexes := []struct {
		collection string
		keys       bson.D
		name       string
	}{
		{overviewVersionsCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "revision", Value: 1}}, "overview_project_revision_unique"},
		{overviewVersionsCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}}, "overview_project_operation_unique"},
		{overviewMediaCollection, bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "slot", Value: 1}}, "overview_project_operation_slot_unique"},
	}
	for _, item := range indexes {
		// Match ensureTenantIndexes exactly, including its empty partial filter.
		// MongoDB rejects a same-named index with otherwise identical keys when
		// one definition omits that option.
		model := mongo.IndexModel{Keys: item.keys, Options: options.Index().SetName(item.name).SetUnique(true).SetPartialFilterExpression(bson.M{})}
		if _, err := db.Collection(item.collection).Indexes().CreateOne(ctx, model); err != nil {
			return err
		}
	}
	return nil
}

func (m *MongoStore) ReadOverview(ctx context.Context, companyID, projectID string, requestedRevision int64) (OverviewReadResponse, error) {
	db, project, err := m.overviewProject(ctx, companyID, projectID)
	if err != nil {
		return OverviewReadResponse{}, err
	}
	if requestedRevision < 0 {
		return OverviewReadResponse{}, ErrInvalid
	}
	if project.OverviewRevision == 0 {
		if requestedRevision > 0 {
			return OverviewReadResponse{}, ErrNotFound
		}
		return OverviewReadResponse{State: "not_authored", CompanyID: companyID, ProjectID: projectID, Teach: "Create an Overview through the Project-scoped API."}, nil
	}
	revision := requestedRevision
	if revision == 0 {
		revision = project.OverviewRevision
	}
	if revision > project.OverviewRevision {
		return OverviewReadResponse{}, ErrNotFound
	}
	var version overviewVersionDocument
	if err := db.Collection(overviewVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "revision", Value: revision}}).Decode(&version); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			if requestedRevision > 0 {
				return OverviewReadResponse{}, ErrNotFound
			}
			return OverviewReadResponse{State: "invalid", CompanyID: companyID, ProjectID: projectID, Teach: "The declared current Overview revision is missing; repair it before use."}, nil
		}
		return OverviewReadResponse{}, ErrUnavailable
	}
	if version.ProjectID != projectID || version.Revision != revision || len(version.Media) < 2 || len(version.Media) > 6 {
		return OverviewReadResponse{State: "invalid", CompanyID: companyID, ProjectID: projectID, Teach: "The stored Overview revision is inconsistent; repair it before use."}, nil
	}
	slots := make([]string, 0, len(version.Media))
	for i := range version.Media {
		ref := &version.Media[i]
		if !overviewSlot(ref.Slot) || !validSourceDigest(ref.Digest) {
			return OverviewReadResponse{State: "invalid", CompanyID: companyID, ProjectID: projectID, Teach: "The stored Overview media reference is invalid."}, nil
		}
		slots = append(slots, ref.Slot)
		ref.Href = fmt.Sprintf("/internal/v1/companies/%s/projects/%s/overview/%d/media/%s", companyID, projectID, revision, ref.Slot)
	}
	cursor, err := db.Collection(overviewMediaCollection).Find(ctx,
		bson.D{{Key: "project_id", Value: projectID}, {Key: "operation_id", Value: version.OperationID}, {Key: "slot", Value: bson.M{"$in": slots}}},
		options.Find().SetProjection(bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "slot", Value: 1}, {Key: "digest", Value: 1}}),
	)
	if err != nil {
		return OverviewReadResponse{}, ErrUnavailable
	}
	defer cursor.Close(ctx)
	var metadata []overviewMediaMetadata
	if err := cursor.All(ctx, &metadata); err != nil {
		return OverviewReadResponse{}, ErrUnavailable
	}
	if !overviewMetadataMatches(version.Content, version.Media, metadata, projectID, version.OperationID) {
		return OverviewReadResponse{State: "invalid", CompanyID: companyID, ProjectID: projectID, Teach: "A declared Overview media slot is missing or changed, or the stored content/reference schema is invalid."}, nil
	}
	manifest, err := m.CanonicalManifest(ctx, companyID, projectID, "")
	if err != nil {
		return OverviewReadResponse{}, err
	}
	state := "available"
	if !overviewOriginMatches(version.KnowledgeOrigin, manifest) {
		state = "stale"
	}
	return OverviewReadResponse{State: state, CompanyID: companyID, ProjectID: projectID, Revision: revision, Content: &version.Content, Media: version.Media, KnowledgeOrigin: &version.KnowledgeOrigin, Source: &version.Source}, nil
}

func overviewMediaMatches(ref OverviewMediaReference, media overviewMediaDocument) bool {
	if media.ProjectID == "" || media.OperationID == "" || media.Slot != ref.Slot || media.Digest != ref.Digest || len(media.Data) == 0 || len(media.Data) > maxOverviewPNGBytes {
		return false
	}
	sum := sha256.Sum256(media.Data)
	if "sha256:"+hex.EncodeToString(sum[:]) != ref.Digest {
		return false
	}
	config, err := png.DecodeConfig(bytes.NewReader(media.Data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16<<20 {
		return false
	}
	_, err = png.Decode(bytes.NewReader(media.Data))
	return err == nil
}

func (m *MongoStore) ReadOverviewMedia(ctx context.Context, companyID, projectID string, revision int64, slot string) ([]byte, string, error) {
	if revision <= 0 || !overviewSlot(slot) {
		return nil, "", ErrInvalid
	}
	db, project, err := m.overviewProject(ctx, companyID, projectID)
	if err != nil {
		return nil, "", err
	}
	if revision > project.OverviewRevision {
		return nil, "", ErrNotFound
	}
	var version overviewVersionDocument
	if err := db.Collection(overviewVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "revision", Value: revision}}).Decode(&version); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, "", ErrNotFound
		}
		return nil, "", ErrUnavailable
	}
	if version.ProjectID != projectID || version.Revision != revision || version.OperationID == "" {
		return nil, "", ErrUnavailable
	}
	var ref *OverviewMediaReference
	for i := range version.Media {
		if version.Media[i].Slot == slot {
			ref = &version.Media[i]
			break
		}
	}
	if ref == nil {
		return nil, "", ErrNotFound
	}
	var media overviewMediaDocument
	if err := db.Collection(overviewMediaCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "operation_id", Value: version.OperationID}, {Key: "slot", Value: slot}}).Decode(&media); err != nil {
		return nil, "", ErrUnavailable
	}
	if !overviewMediaMatches(*ref, media) {
		return nil, "", ErrUnavailable
	}
	return media.Data, ref.Digest, nil
}
