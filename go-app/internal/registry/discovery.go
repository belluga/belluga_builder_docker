package registry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	companyDiscoveryIndex   = "companies_discovery_name_id"
	projectDiscoveryIndex   = "projects_discovery_name_id"
	companyDiscoveryIDIndex = "companies_id_unique"
)

type discoveryQuery struct {
	Company string
	Field   string
	Value   string
	Scope   []string
	BoundID string
	Limit   int
	After   string
}

type discoveryItem struct {
	ID        string `bson:"id" json:"id"`
	Name      string `bson:"name" json:"name"`
	CompanyID string `bson:"company_id,omitempty" json:"company_id,omitempty"`
}

type discoveryPage struct {
	Items      []discoveryItem `json:"items"`
	MatchState string          `json:"match_state"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

type discoveryCursor struct {
	Version int    `json:"v"`
	After   string `json:"after"`
	Filter  string `json:"filter"`
}

func discoveryCompanyIDs(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > 64 {
		return nil, discoveryInputError("company_ids", "Supply between 1 and 64 Company IDs.")
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for i, id := range sorted {
		if !validID(id) || (i > 0 && id == sorted[i-1]) {
			return nil, discoveryInputError("company_ids", "Supply distinct valid Company IDs.")
		}
	}
	return sorted, nil
}

func (q discoveryQuery) filterDigest() string {
	// All members are strings or an array of strings, so marshaling cannot fail.
	data, _ := json.Marshal([]any{q.Company, q.Field, q.Value, q.Scope, q.BoundID})
	digest := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (q discoveryQuery) encodeCursor(id string) string {
	data, _ := json.Marshal(discoveryCursor{Version: 1, After: id, Filter: q.filterDigest()})
	return base64.RawURLEncoding.EncodeToString(data)
}

func (q discoveryQuery) decodeCursor(value string) (string, error) {
	invalid := discoveryInputError("cursor", "Use the cursor from a previous page of the same search and Company scope.")
	if len(value) > 256 {
		return "", invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return "", invalid
	}
	var cursor discoveryCursor
	if err := json.Unmarshal(data, &cursor); err != nil || !validID(cursor.After) || value != q.encodeCursor(cursor.After) {
		return "", invalid
	}
	return cursor.After, nil
}

func discoveryNameCollation() *options.Collation {
	return &options.Collation{Locale: "en", Strength: 2, Normalization: true}
}

func (m *MongoStore) SearchCompanies(ctx context.Context, q discoveryQuery) (discoveryPage, error) {
	return searchDiscovery(ctx, m.control.Collection(companiesCollection), q, companyDiscoveryIndex, companyDiscoveryIDIndex)
}

func (m *MongoStore) SearchProjects(ctx context.Context, q discoveryQuery) (discoveryPage, error) {
	company, err := m.discoveryCompany(ctx, q.Company)
	if err != nil {
		return discoveryPage{}, err
	}
	collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
	return searchDiscovery(ctx, collection, q, projectDiscoveryIndex, "projects_id_unique")
}

func discoveryCompanyQuery(id string) (bson.D, *options.FindOneOptionsBuilder) {
	filter := bson.D{{Key: "id", Value: bson.D{{Key: "$eq", Value: id}, {Key: "$type", Value: "string"}}}}
	projection := bson.D{{Key: "_id", Value: 1}, {Key: "id", Value: 1}, {Key: "database_name", Value: 1}}
	return filter, options.FindOne().SetHint(companyDiscoveryIDIndex).SetProjection(projection)
}

func (m *MongoStore) discoveryCompany(ctx context.Context, id string) (Company, error) {
	filter, find := discoveryCompanyQuery(id)
	var company Company
	if err := m.control.Collection(companiesCollection).FindOne(ctx, filter, find).Decode(&company); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Company{}, ErrNotFound
		}
		return Company{}, discoveryUnavailable()
	}
	if company.ObjectID.IsZero() || company.ID != id || company.DatabaseName != m.tenantDatabase(id) {
		return Company{}, discoveryUnavailable()
	}
	return company, nil
}

func (q discoveryQuery) mongoQuery(nameIndex, idIndex string, paginated bool, limit int64) (bson.D, *options.FindOptionsBuilder) {
	filter := bson.D{{Key: q.Field, Value: q.Value}}
	var identity bson.D
	// The explicit type predicate agrees with the historical partial ID index.
	if q.Field == "id" {
		identity = append(identity, bson.E{Key: "$type", Value: "string"})
	}
	scope := q.Scope
	if q.BoundID != "" {
		scope = []string{q.BoundID}
	}
	if len(scope) > 0 {
		identity = append(identity, bson.E{Key: "$in", Value: scope})
	}
	if paginated && q.After != "" {
		identity = append(identity, bson.E{Key: "$gt", Value: q.After})
	}
	if len(identity) > 0 {
		if q.Field == "id" {
			filter[0].Value = append(identity, bson.E{Key: "$eq", Value: q.Value})
		} else {
			filter = append(filter, bson.E{Key: "id", Value: identity})
		}
	}

	projection := bson.D{{Key: "_id", Value: 0}, {Key: "id", Value: 1}, {Key: "name", Value: 1}}
	if q.Company != "" {
		projection = append(projection, bson.E{Key: "company_id", Value: 1})
	}
	find := options.Find().SetProjection(projection).SetSort(bson.D{{Key: "id", Value: 1}}).SetLimit(limit)
	if q.Field == "name" {
		find.SetHint(nameIndex).SetCollation(discoveryNameCollation())
	} else {
		find.SetHint(idIndex)
	}
	return filter, find
}

func searchDiscovery(ctx context.Context, collection *mongo.Collection, q discoveryQuery, nameIndex, idIndex string) (discoveryPage, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	lookup := func(paginated bool, limit int64) ([]discoveryItem, error) {
		filter, find := q.mongoQuery(nameIndex, idIndex, paginated, limit)
		cursor, err := collection.Find(ctx, filter, find)
		if err != nil {
			return nil, discoveryUnavailable()
		}
		items := make([]discoveryItem, 0, limit)
		if err := cursor.All(ctx, &items); err != nil {
			return nil, discoveryUnavailable()
		}
		for _, item := range items {
			if !validID(item.ID) || item.Name == "" || item.CompanyID != q.Company {
				return nil, discoveryUnavailable()
			}
		}
		return items, nil
	}

	matches, err := lookup(false, 2)
	if err != nil {
		return discoveryPage{}, err
	}
	items, err := lookup(true, int64(q.Limit+1))
	if err != nil {
		return discoveryPage{}, err
	}
	page := discoveryPage{Items: items, MatchState: [...]string{"none", "unique", "ambiguous"}[len(matches)]}
	if len(items) > q.Limit {
		page.Items = items[:q.Limit]
		page.NextCursor = q.encodeCursor(page.Items[q.Limit-1].ID)
	}
	return page, nil
}

func discoveryUnavailable() error {
	return withTeachDiagnostic(ErrUnavailable, *readTeach("discovery_unavailable", "search", "Verify Registry connectivity and run discovery-indexes for the selected Company before retrying."))
}

// PrepareDiscoveryIndexes provisions only the additive name/ID indexes. Existing
// ID indexes, schema migrations and collection validators remain owner-managed.
func (m *MongoStore) PrepareDiscoveryIndexes(ctx context.Context, companyIDs []string) ([]string, error) {
	ids, err := discoveryCompanyIDs(companyIDs)
	if err != nil {
		return nil, err
	}
	// Resolve all tenants before making any index changes.
	companies := make([]Company, 0, len(ids))
	for _, id := range ids {
		company, err := m.discoveryCompany(ctx, id)
		if err != nil {
			return nil, err
		}
		companies = append(companies, company)
	}
	ensure := func(collection *mongo.Collection, name string) error {
		model := mongo.IndexModel{
			Keys:    bson.D{{Key: "name", Value: 1}, {Key: "id", Value: 1}},
			Options: options.Index().SetName(name).SetCollation(discoveryNameCollation()),
		}
		// CreateOne is an idempotent server-side specification check. A conflicting
		// key pattern, name or collation returns an error rather than being replaced.
		if _, err := collection.Indexes().CreateOne(ctx, model); err != nil {
			return discoveryUnavailable()
		}
		return nil
	}
	if err := ensure(m.control.Collection(companiesCollection), companyDiscoveryIndex); err != nil {
		return nil, err
	}
	for _, company := range companies {
		if err := ensure(m.client.Database(company.DatabaseName).Collection(projectsCollection), projectDiscoveryIndex); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
