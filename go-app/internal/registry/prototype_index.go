package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	ac "delphi-local-api/artifactcatalog"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	prototypeGenerationsCollection = "prototype_navigation_generations"
	prototypeItemsCollection       = "prototype_navigation_items"
	prototypeActiveCollection      = "prototype_navigation_active"
	prototypeItemMaxBytes          = 4 << 20
	prototypeGenerationMaxBytes    = 16 << 20
)

type prototypeGenerationDocument struct {
	CompanyID           string    `bson:"company_id"`
	ProjectID           string    `bson:"project_id"`
	GenerationKey       string    `bson:"generation_key"`
	RepositoryID        string    `bson:"repository_id"`
	PublicationBranch   string    `bson:"publication_branch"`
	BindingRevision     int64     `bson:"binding_revision"`
	Revision            string    `bson:"revision"`
	EvaluatorVersion    string    `bson:"evaluator_version"`
	InventoryDigest     string    `bson:"inventory_digest"`
	KnowledgeGeneration string    `bson:"knowledge_generation_id,omitempty"`
	KnowledgeDigest     string    `bson:"knowledge_digest"`
	PrototypeIDs        []string  `bson:"prototype_ids"`
	Complete            bool      `bson:"complete"`
	CreatedAt           time.Time `bson:"created_at"`
}

type prototypeItemDocument struct {
	CompanyID     string  `bson:"company_id"`
	ProjectID     string  `bson:"project_id"`
	GenerationKey string  `bson:"generation_key"`
	PrototypeID   string  `bson:"prototype_id"`
	Item          ac.Item `bson:"item"`
}

type prototypeActiveDocument struct {
	CompanyID           string    `bson:"company_id"`
	ProjectID           string    `bson:"project_id"`
	GenerationKey       string    `bson:"generation_key"`
	RepositoryID        string    `bson:"repository_id"`
	PublicationBranch   string    `bson:"publication_branch"`
	BindingRevision     int64     `bson:"binding_revision"`
	Revision            string    `bson:"revision"`
	EvaluatorVersion    string    `bson:"evaluator_version"`
	InventoryDigest     string    `bson:"inventory_digest"`
	KnowledgeGeneration string    `bson:"knowledge_generation_id,omitempty"`
	KnowledgeDigest     string    `bson:"knowledge_digest"`
	UpdatedAt           time.Time `bson:"updated_at"`
}

func prototypeGenerationKey(companyID, projectID string, catalog PrototypeCatalogResponse) (string, error) {
	identity := struct {
		CompanyID, ProjectID, RepositoryID, Branch, Revision, Evaluator, Inventory, KnowledgeGeneration, KnowledgeDigest string
		BindingRevision                                                                                                  int64
	}{companyID, projectID, catalog.RepositoryID, catalog.PublicationBranch, catalog.Revision, catalog.EvaluatorVersion, catalog.InventoryDigest, catalog.KnowledgeGeneration, catalog.KnowledgeDigest, catalog.BindingRevision}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", ErrUnavailable
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (m *MongoStore) prototypeCollections(ctx context.Context, companyID string) (*mongo.Collection, *mongo.Collection, *mongo.Collection, error) {
	company, err := m.company(ctx, companyID)
	if err != nil {
		log.Printf("prototype collections company lookup failed (%T)", err)
		return nil, nil, nil, err
	}
	db := m.client.Database(company.DatabaseName)
	if _, ready := m.prototypeReady.Load(company.DatabaseName); !ready {
		existing, err := db.ListCollectionNames(ctx, bson.D{})
		if err != nil {
			log.Printf("prototype collections listing failed (%T)", err)
			return nil, nil, nil, ErrUnavailable
		}
		available := make(map[string]bool, len(existing))
		for _, name := range existing {
			available[name] = true
		}
		if !available[prototypeGenerationsCollection] || !available[prototypeItemsCollection] || !available[prototypeActiveCollection] {
			if err := m.ensurePrototypeCollections(ctx, company.DatabaseName); err != nil {
				var commandErr mongo.CommandError
				if errors.As(err, &commandErr) {
					log.Printf("prototype collections unavailable (Mongo code=%d name=%s)", commandErr.Code, commandErr.Name)
				} else {
					log.Printf("prototype collections unavailable (%T, deadline=%t)", err, errors.Is(err, context.DeadlineExceeded))
				}
				return nil, nil, nil, normalizeMongoError(err)
			}
		}
		m.prototypeReady.Store(company.DatabaseName, true)
	}
	return db.Collection(prototypeGenerationsCollection), db.Collection(prototypeItemsCollection), db.Collection(prototypeActiveCollection), nil
}

func prototypeGenerationToCatalog(generation prototypeGenerationDocument, items []ac.Item, companyID, projectID string) PrototypeCatalogResponse {
	items = append([]ac.Item(nil), items...)
	summaries := make([]PrototypeSummary, 0, len(items))
	for _, item := range items {
		summaries = append(summaries, PrototypeSummary{
			ID: item.ID, Name: item.Name, Description: item.Description, Root: item.Root, Status: item.Status,
			DetailURL: "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/prototypes/" + generation.Revision + "/" + item.ID,
		})
	}
	return PrototypeCatalogResponse{
		RepositoryID: generation.RepositoryID, PublicationBranch: generation.PublicationBranch,
		BindingRevision: generation.BindingRevision, Revision: generation.Revision,
		EvaluatorVersion: generation.EvaluatorVersion, InventoryDigest: generation.InventoryDigest,
		KnowledgeGeneration: generation.KnowledgeGeneration, KnowledgeDigest: generation.KnowledgeDigest,
		Items: summaries, navigation: items,
	}
}

func (m *MongoStore) readActivePrototypeProjection(ctx context.Context, companyID, projectID string, binding RepositoryBinding, revision, knowledgeGeneration, knowledgeDigest string) (PrototypeCatalogResponse, bool, error) {
	generations, itemCollection, activeCollection, err := m.prototypeCollections(ctx, companyID)
	if err != nil {
		return PrototypeCatalogResponse{}, false, err
	}
	var active prototypeActiveDocument
	if err := activeCollection.FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "company_id", Value: companyID}}).Decode(&active); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return PrototypeCatalogResponse{}, false, nil
		}
		log.Printf("prototype active projection read failed (%T)", err)
		return PrototypeCatalogResponse{}, false, ErrUnavailable
	}
	if active.RepositoryID != binding.RepositoryID || active.PublicationBranch != binding.PublicationBranch || active.BindingRevision != binding.Revision || active.Revision != revision || active.EvaluatorVersion != prototypeEvaluatorVersion || knowledgeGeneration != "" && active.KnowledgeGeneration != knowledgeGeneration || knowledgeDigest != "" && active.KnowledgeDigest != knowledgeDigest {
		return PrototypeCatalogResponse{}, false, nil
	}
	var generation prototypeGenerationDocument
	if err := generations.FindOne(ctx, bson.D{{Key: "company_id", Value: companyID}, {Key: "project_id", Value: projectID}, {Key: "generation_key", Value: active.GenerationKey}, {Key: "complete", Value: true}}).Decode(&generation); err != nil {
		log.Printf("prototype generation read failed (%T)", err)
		return PrototypeCatalogResponse{}, false, ErrUnavailable
	}
	items := make([]ac.Item, 0, len(generation.PrototypeIDs))
	if len(generation.PrototypeIDs) > ac.MaxPrototypes {
		return PrototypeCatalogResponse{}, false, ErrUnavailable
	}
	for _, id := range generation.PrototypeIDs {
		var stored prototypeItemDocument
		if err := itemCollection.FindOne(ctx, bson.D{{Key: "company_id", Value: companyID}, {Key: "project_id", Value: projectID}, {Key: "generation_key", Value: generation.GenerationKey}, {Key: "prototype_id", Value: id}}).Decode(&stored); err != nil || stored.Item.ID != id {
			return PrototypeCatalogResponse{}, false, ErrUnavailable
		}
		items = append(items, stored.Item)
	}
	if len(items) != len(generation.PrototypeIDs) {
		return PrototypeCatalogResponse{}, false, ErrUnavailable
	}
	return prototypeGenerationToCatalog(generation, items, companyID, projectID), true, nil
}

func (m *MongoStore) persistPrototypeProjection(ctx context.Context, companyID, projectID string, catalog PrototypeCatalogResponse) error {
	generationKey, err := prototypeGenerationKey(companyID, projectID, catalog)
	if err != nil {
		return err
	}
	generations, itemCollection, activeCollection, err := m.prototypeCollections(ctx, companyID)
	if err != nil {
		return err
	}
	items := append([]ac.Item(nil), catalog.navigation...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	ids := make([]string, 0, len(items))
	var total int64
	for _, item := range items {
		data, marshalErr := json.Marshal(item)
		if marshalErr != nil || len(data) > prototypeItemMaxBytes || int64(len(data)) > prototypeGenerationMaxBytes-total {
			return teachCanonical(item.ID, "prototype_projection", "keep each navigation record under 4 MiB and the complete generation under 16 MiB", ErrInvalid)
		}
		total += int64(len(data))
		ids = append(ids, item.ID)
		document := prototypeItemDocument{CompanyID: companyID, ProjectID: projectID, GenerationKey: generationKey, PrototypeID: item.ID, Item: item}
		filter := bson.D{{Key: "company_id", Value: companyID}, {Key: "project_id", Value: projectID}, {Key: "generation_key", Value: generationKey}, {Key: "prototype_id", Value: item.ID}}
		if _, err := itemCollection.UpdateOne(ctx, filter, bson.M{"$setOnInsert": document}, options.UpdateOne().SetUpsert(true)); err != nil {
			return normalizeMongoError(err)
		}
		var stored prototypeItemDocument
		if err := itemCollection.FindOne(ctx, filter).Decode(&stored); err != nil || stored.Item.ID != item.ID {
			return ErrUnavailable
		}
		storedData, _ := json.Marshal(stored.Item)
		if string(storedData) != string(data) {
			return ErrConflict
		}
	}
	if len(ids) > ac.MaxPrototypes {
		return teachCanonical("", "prototype_projection", "keep the complete generation within the canonical 256-Prototype limit", ErrInvalid)
	}
	metadata := prototypeGenerationDocument{
		CompanyID: companyID, ProjectID: projectID, GenerationKey: generationKey,
		RepositoryID: catalog.RepositoryID, PublicationBranch: catalog.PublicationBranch,
		BindingRevision: catalog.BindingRevision, Revision: catalog.Revision,
		EvaluatorVersion: catalog.EvaluatorVersion, InventoryDigest: catalog.InventoryDigest,
		KnowledgeGeneration: catalog.KnowledgeGeneration, KnowledgeDigest: catalog.KnowledgeDigest,
		PrototypeIDs: ids, Complete: true, CreatedAt: time.Now().UTC(),
	}
	filter := bson.D{{Key: "company_id", Value: companyID}, {Key: "project_id", Value: projectID}, {Key: "generation_key", Value: generationKey}}
	if _, err := generations.UpdateOne(ctx, filter, bson.M{"$setOnInsert": metadata}, options.UpdateOne().SetUpsert(true)); err != nil {
		return normalizeMongoError(err)
	}
	var storedGeneration prototypeGenerationDocument
	if err := generations.FindOne(ctx, filter).Decode(&storedGeneration); err != nil || !storedGeneration.Complete || len(storedGeneration.PrototypeIDs) != len(ids) {
		return ErrUnavailable
	}
	if err := m.fencePrototypeCurrent(ctx, companyID, projectID, catalog); err != nil {
		return err
	}
	activeFilter := bson.D{{Key: "company_id", Value: companyID}, {Key: "project_id", Value: projectID}}
	var current prototypeActiveDocument
	findErr := activeCollection.FindOne(ctx, activeFilter).Decode(&current)
	var previous *prototypeActiveDocument
	if findErr == nil {
		copy := current
		previous = &copy
		if current.GenerationKey != generationKey {
			result, updateErr := activeCollection.UpdateOne(ctx, append(activeFilter, bson.E{Key: "generation_key", Value: current.GenerationKey}), bson.M{"$set": prototypeActiveFromGeneration(metadata)})
			if updateErr != nil {
				return normalizeMongoError(updateErr)
			}
			if result.MatchedCount != 1 {
				return ErrConflict
			}
		}
	} else if errors.Is(findErr, mongo.ErrNoDocuments) {
		if _, err := activeCollection.InsertOne(ctx, prototypeActiveFromGeneration(metadata)); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return ErrConflict
			}
			return ErrUnavailable
		}
	} else {
		return ErrUnavailable
	}
	if err := m.fencePrototypeCurrent(ctx, companyID, projectID, catalog); err != nil {
		if previous == nil {
			_, _ = activeCollection.DeleteOne(ctx, append(activeFilter, bson.E{Key: "generation_key", Value: generationKey}))
		} else {
			_, _ = activeCollection.UpdateOne(ctx, append(activeFilter, bson.E{Key: "generation_key", Value: generationKey}), bson.M{"$set": previous})
		}
		return err
	}
	return nil
}

func prototypeActiveFromGeneration(generation prototypeGenerationDocument) prototypeActiveDocument {
	return prototypeActiveDocument{
		CompanyID: generation.CompanyID, ProjectID: generation.ProjectID, GenerationKey: generation.GenerationKey,
		RepositoryID: generation.RepositoryID, PublicationBranch: generation.PublicationBranch,
		BindingRevision: generation.BindingRevision, Revision: generation.Revision,
		EvaluatorVersion: generation.EvaluatorVersion, InventoryDigest: generation.InventoryDigest,
		KnowledgeGeneration: generation.KnowledgeGeneration, KnowledgeDigest: generation.KnowledgeDigest,
		UpdatedAt: time.Now().UTC(),
	}
}

func (m *MongoStore) ensurePrototypeCollections(ctx context.Context, database string) error {
	db := m.client.Database(database)
	collections := []struct {
		name       string
		required   bson.A
		properties bson.M
	}{
		{prototypeGenerationsCollection, bson.A{"company_id", "project_id", "generation_key", "repository_id", "publication_branch", "binding_revision", "revision", "evaluator_version", "inventory_digest", "knowledge_digest", "prototype_ids", "complete", "created_at"}, bson.M{
			"company_id": bson.M{"bsonType": "string"}, "project_id": bson.M{"bsonType": "string"}, "generation_key": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{64}$"},
			"repository_id": bson.M{"bsonType": "string"}, "publication_branch": bson.M{"bsonType": "string"}, "binding_revision": bson.M{"bsonType": "long", "minimum": 0},
			"revision": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{40}$"}, "evaluator_version": bson.M{"bsonType": "string"},
			"inventory_digest": bson.M{"bsonType": "string", "pattern": "^sha256:[a-f0-9]{64}$"}, "knowledge_generation_id": bson.M{"bsonType": "string"},
			"knowledge_digest": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{64}$"}, "prototype_ids": bson.M{"bsonType": "array", "maxItems": ac.MaxPrototypes, "items": bson.M{"bsonType": "string", "maxLength": 64}},
			"complete": bson.M{"bsonType": "bool"}, "created_at": bson.M{"bsonType": "date"},
		}},
		{prototypeItemsCollection, bson.A{"company_id", "project_id", "generation_key", "prototype_id", "item"}, bson.M{
			"company_id": bson.M{"bsonType": "string"}, "project_id": bson.M{"bsonType": "string"}, "generation_key": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{64}$"},
			"prototype_id": bson.M{"bsonType": "string", "maxLength": 64}, "item": bson.M{"bsonType": "object"},
		}},
		{prototypeActiveCollection, bson.A{"company_id", "project_id", "generation_key", "repository_id", "publication_branch", "binding_revision", "revision", "evaluator_version", "inventory_digest", "knowledge_digest", "updated_at"}, bson.M{
			"company_id": bson.M{"bsonType": "string"}, "project_id": bson.M{"bsonType": "string"}, "generation_key": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{64}$"},
			"repository_id": bson.M{"bsonType": "string"}, "publication_branch": bson.M{"bsonType": "string"}, "binding_revision": bson.M{"bsonType": "long", "minimum": 0},
			"revision": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{40}$"}, "evaluator_version": bson.M{"bsonType": "string"},
			"inventory_digest": bson.M{"bsonType": "string", "pattern": "^sha256:[a-f0-9]{64}$"}, "knowledge_generation_id": bson.M{"bsonType": "string"},
			"knowledge_digest": bson.M{"bsonType": "string", "pattern": "^[a-f0-9]{64}$"}, "updated_at": bson.M{"bsonType": "date"},
		}},
	}
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}, {Key: "generation_key", Value: 1}}, Options: options.Index().SetName("prototype_generation_identity_unique").SetUnique(true)},
		{Keys: bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}, {Key: "generation_key", Value: 1}, {Key: "prototype_id", Value: 1}}, Options: options.Index().SetName("prototype_item_identity_unique").SetUnique(true)},
		{Keys: bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}}, Options: options.Index().SetName("prototype_active_project_unique").SetUnique(true)},
	}
	for index, item := range collections {
		validator := bson.M{"$jsonSchema": bson.M{"bsonType": "object", "required": item.required, "properties": item.properties}}
		exists, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: item.name}})
		if err != nil {
			log.Printf("prototype provision failed listing %s", item.name)
			return err
		}
		if len(exists) == 0 {
			if err := db.CreateCollection(ctx, item.name, options.CreateCollection().SetValidator(validator)); err != nil && !namespaceExists(err) {
				log.Printf("prototype provision failed creating %s", item.name)
				return err
			}
		} else if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: item.name}, {Key: "validator", Value: validator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
			log.Printf("prototype provision failed validating %s", item.name)
			return err
		}
		if _, err := db.Collection(item.name).Indexes().CreateOne(ctx, indexes[index]); err != nil {
			log.Printf("prototype provision failed indexing %s", item.name)
			return err
		}
	}
	return nil
}

const (
	prototypeFileCacheEntries = 512
	prototypeFileCacheBytes   = 64 << 20
	prototypeHotProjects      = 4
)

// A verified catalog is immutable for its Git revision. Keep the small active
// navigation projection in the Go process so detail and file routes do not
// repeat Mongo reads while a user browses that revision. Refreshing the
// catalog replaces (or clears) this lease.
type prototypeHotEntry struct {
	catalog PrototypeCatalogResponse
	allowed map[string]struct{}
}

type prototypeHotCache struct {
	mu      sync.Mutex
	entries map[string]prototypeHotEntry
	order   []string
}

func newPrototypeHotCache() *prototypeHotCache {
	return &prototypeHotCache{entries: make(map[string]prototypeHotEntry)}
}

func prototypeHotKey(companyID, projectID string) string { return companyID + "\x00" + projectID }

func (c *prototypeHotCache) get(companyID, projectID, revision string) (prototypeHotEntry, bool) {
	if c == nil {
		return prototypeHotEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := prototypeHotKey(companyID, projectID)
	entry, ok := c.entries[key]
	if !ok || entry.catalog.Revision != revision {
		return prototypeHotEntry{}, false
	}
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
	return entry, true
}

func (c *prototypeHotCache) replace(companyID, projectID string, catalog *PrototypeCatalogResponse) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := prototypeHotKey(companyID, projectID)
	delete(c.entries, key)
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	if catalog == nil {
		return
	}
	for len(c.order) >= prototypeHotProjects {
		oldest := c.order[0]
		delete(c.entries, oldest)
		c.order = c.order[1:]
	}
	c.entries[key] = prototypeHotEntry{catalog: *catalog, allowed: prototypeAllowedPaths(*catalog)}
	c.order = append(c.order, key)
}

type prototypeFileCache struct {
	mu      sync.Mutex
	entries map[string]prototypeFileResponse
	order   []string
	bytes   int64
}

func newPrototypeFileCache() *prototypeFileCache {
	return &prototypeFileCache{entries: make(map[string]prototypeFileResponse)}
}

func prototypeFileCacheKey(companyID, projectID string, catalog PrototypeCatalogResponse, path string) string {
	return companyID + "\x00" + projectID + "\x00" + catalog.RepositoryID + "\x00" + catalog.PublicationBranch + "\x00" + catalog.Revision + "\x00" + catalog.InventoryDigest + "\x00" + catalog.KnowledgeDigest + "\x00" + path
}

func (c *prototypeFileCache) get(key string) (prototypeFileResponse, bool) {
	if c == nil {
		return prototypeFileResponse{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.entries[key]
	if !ok {
		return prototypeFileResponse{}, false
	}
	for index, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:index], c.order[index+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
	value.Data = append([]byte(nil), value.Data...)
	return value, true
}

func (c *prototypeFileCache) has(key string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.entries[key]
	return ok
}

func (c *prototypeFileCache) put(key string, value prototypeFileResponse) {
	if c == nil || len(value.Data) > prototypeFileCacheBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[key]; ok {
		c.bytes -= int64(len(existing.Data))
		delete(c.entries, key)
		for index, candidate := range c.order {
			if candidate == key {
				c.order = append(c.order[:index], c.order[index+1:]...)
				break
			}
		}
	}
	for len(c.order) > 0 && (len(c.order) >= prototypeFileCacheEntries || c.bytes+int64(len(value.Data)) > prototypeFileCacheBytes) {
		oldest := c.order[0]
		c.bytes -= int64(len(c.entries[oldest].Data))
		delete(c.entries, oldest)
		c.order = c.order[1:]
	}
	value.Data = append([]byte(nil), value.Data...)
	c.entries[key] = value
	c.order = append(c.order, key)
	c.bytes += int64(len(value.Data))
}
