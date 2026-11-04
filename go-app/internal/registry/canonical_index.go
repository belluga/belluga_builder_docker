package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CanonicalIndex is a bounded projection of active records, not a second
// document authority. The cursor is scoped to the Project, filters and write
// epoch; any intervening canonical write invalidates it.
type CanonicalIndexQuery struct {
	Family        CanonicalFamily
	ModuleID      string
	Method        string
	Q             string
	Limit         int
	Cursor        string
	DecisionsOnly bool
}

type CanonicalIndexItem struct {
	Family        CanonicalFamily `json:"family"`
	ID            string          `json:"id"`
	Revision      int64           `json:"revision"`
	ContentDigest string          `json:"content_digest"`
	Purpose       string          `json:"purpose,omitempty"`
	Method        string          `json:"method,omitempty"`
	Route         string          `json:"route,omitempty"`
	HostScope     string          `json:"host_scope,omitempty"`
	ModuleID      string          `json:"module_id,omitempty"`
	Decisions     []struct {
		ID        string `json:"id" bson:"id"`
		Statement string `json:"statement" bson:"statement"`
	} `json:"decisions,omitempty"`
}

type CanonicalIndexPage struct {
	ProjectID    string               `json:"project_id"`
	GenerationID string               `json:"generation_id"`
	WriteEpoch   int64                `json:"write_epoch"`
	Items        []CanonicalIndexItem `json:"items"`
	NextCursor   string               `json:"next_cursor,omitempty"`
}

type canonicalIndexCursor struct {
	CompanyID     string          `json:"company_id"`
	ProjectID     string          `json:"project_id"`
	GenerationID  string          `json:"generation_id"`
	Family        CanonicalFamily `json:"family"`
	ModuleID      string          `json:"module_id"`
	Method        string          `json:"method"`
	Q             string          `json:"q"`
	DecisionsOnly bool            `json:"decisions_only"`
	Epoch         int64           `json:"epoch"`
	LastFamily    CanonicalFamily `json:"last_family"`
	LastID        string          `json:"last_id"`
}

var canonicalIndexFamilies = []CanonicalFamily{FamilyDomain, FamilyEndpoint, FamilyModule, FamilyConstitution, FamilyMandate, FamilyPolicy, FamilyRoadmap}

func (m *MongoStore) CanonicalIndex(ctx context.Context, companyID, projectID string, query CanonicalIndexQuery) (CanonicalIndexPage, error) {
	if query.Limit == 0 {
		query.Limit = 25
	}
	if query.Limit < 1 || query.Limit > 100 || len(query.Q) > 120 || len(query.Cursor) > 2048 ||
		query.ModuleID != "" && !canonicalID.MatchString(query.ModuleID) ||
		query.Family != "" && !indexFamilyKnown(query.Family) ||
		query.Method != "" && !regexp.MustCompile(`^[A-Z]{1,12}$`).MatchString(query.Method) {
		return CanonicalIndexPage{}, teachCanonical(projectID, "index", "use a supported family, exact module ID, uppercase method, q up to 120 characters and limit 1–100", ErrInvalid)
	}
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalIndexPage{}, err
	}
	if project.KnowledgeGenerationID == "" {
		return CanonicalIndexPage{}, canonicalPending()
	}
	readEpoch := func() (int64, error) {
		var state struct {
			Epoch int64 `bson:"canonical_write_epoch"`
		}
		if err := db.Collection(projectsCollection).FindOne(ctx, bson.M{"id": projectID, "company_id": companyID}, options.FindOne().SetProjection(bson.M{"canonical_write_epoch": 1})).Decode(&state); err != nil {
			return 0, ErrUnavailable
		}
		return state.Epoch, nil
	}
	epoch, err := readEpoch()
	if err != nil {
		return CanonicalIndexPage{}, err
	}
	var previous canonicalIndexCursor
	if query.Cursor != "" {
		data, e := base64.RawURLEncoding.DecodeString(query.Cursor)
		if e != nil || json.Unmarshal(data, &previous) != nil || previous.CompanyID != companyID || previous.ProjectID != projectID || previous.GenerationID != project.KnowledgeGenerationID || previous.Family != query.Family || previous.ModuleID != query.ModuleID || previous.Method != query.Method || previous.Q != query.Q || previous.DecisionsOnly != query.DecisionsOnly || previous.LastID == "" {
			return CanonicalIndexPage{}, teachCanonical(projectID, "cursor", "restart the index with the current Project and filters", ErrInvalid)
		}
		if previous.Epoch != epoch {
			return CanonicalIndexPage{}, teachCanonical(projectID, "cursor", "canonical knowledge changed; restart pagination", ErrConflict)
		}
	}
	page := CanonicalIndexPage{ProjectID: projectID, GenerationID: project.KnowledgeGenerationID, WriteEpoch: epoch, Items: []CanonicalIndexItem{}}
	families := append([]CanonicalFamily(nil), canonicalIndexFamilies...)
	sort.Slice(families, func(i, j int) bool { return families[i] < families[j] })
	for _, family := range families {
		if query.Family != "" && query.Family != family || previous.LastFamily != "" && family < previous.LastFamily {
			continue
		}
		if (query.ModuleID != "" || query.Method != "") && family != FamilyEndpoint {
			continue
		}
		if query.DecisionsOnly && family != FamilyModule && family != FamilyConstitution && family != FamilyPolicy {
			continue
		}
		collection, _ := canonicalCollection(family)
		filter := bson.M{"project_id": projectID, "generation_id": project.KnowledgeGenerationID, "deleted": bson.M{"$ne": true}}
		if previous.LastFamily == family {
			filter["id"] = bson.M{"$gt": previous.LastID}
		}
		if query.ModuleID != "" {
			filter["payload.module_id"] = query.ModuleID
		}
		if query.Method != "" {
			filter["payload.method"] = query.Method
		}
		if query.DecisionsOnly {
			filter["payload.decisions.0"] = bson.M{"$exists": true}
		}
		if query.Q != "" {
			pattern := bson.Regex{Pattern: regexp.QuoteMeta(strings.TrimSpace(query.Q)), Options: "i"}
			filter["$or"] = bson.A{bson.M{"id": pattern}, bson.M{"payload.purpose": pattern}, bson.M{"payload.route": pattern}, bson.M{"payload.method": pattern}, bson.M{"payload.module_id": pattern}, bson.M{"payload.decisions.id": pattern}, bson.M{"payload.decisions.statement": pattern}}
		}
		projection := bson.M{"id": 1, "revision": 1, "content_digest": 1, "payload.purpose": 1, "payload.method": 1, "payload.route": 1, "payload.host_scope": 1, "payload.module_id": 1, "payload.decisions.id": 1, "payload.decisions.statement": 1}
		cursor, e := db.Collection(collection).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}).SetProjection(projection).SetLimit(int64(query.Limit+1-len(page.Items))))
		if e != nil {
			return CanonicalIndexPage{}, ErrUnavailable
		}
		for cursor.Next(ctx) {
			var row struct {
				ID       string `bson:"id"`
				Revision int64  `bson:"revision"`
				Digest   string `bson:"content_digest"`
				Payload  struct {
					Purpose   string `bson:"purpose"`
					Method    string `bson:"method"`
					Route     string `bson:"route"`
					HostScope string `bson:"host_scope"`
					ModuleID  string `bson:"module_id"`
					Decisions []struct {
						ID        string `json:"id" bson:"id"`
						Statement string `json:"statement" bson:"statement"`
					} `bson:"decisions"`
				} `bson:"payload"`
			}
			if cursor.Decode(&row) != nil {
				cursor.Close(ctx)
				return CanonicalIndexPage{}, ErrUnavailable
			}
			page.Items = append(page.Items, CanonicalIndexItem{Family: family, ID: row.ID, Revision: row.Revision, ContentDigest: row.Digest, Purpose: row.Payload.Purpose, Method: row.Payload.Method, Route: row.Payload.Route, HostScope: row.Payload.HostScope, ModuleID: row.Payload.ModuleID, Decisions: row.Payload.Decisions})
		}
		if cursor.Err() != nil {
			cursor.Close(ctx)
			return CanonicalIndexPage{}, ErrUnavailable
		}
		cursor.Close(ctx)
		if len(page.Items) > query.Limit {
			break
		}
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		encoded, _ := json.Marshal(canonicalIndexCursor{CompanyID: companyID, ProjectID: projectID, GenerationID: project.KnowledgeGenerationID, Family: query.Family, ModuleID: query.ModuleID, Method: query.Method, Q: query.Q, DecisionsOnly: query.DecisionsOnly, Epoch: epoch, LastFamily: last.Family, LastID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	if current, err := readEpoch(); err != nil || current != epoch {
		return CanonicalIndexPage{}, teachCanonical(projectID, "index", "canonical knowledge changed during the read; restart pagination", ErrConflict)
	}
	return page, nil
}

func indexFamilyKnown(family CanonicalFamily) bool {
	for _, candidate := range canonicalIndexFamilies {
		if candidate == family {
			return true
		}
	}
	return false
}
