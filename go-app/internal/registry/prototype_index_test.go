package registry

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	ac "delphi-local-api/artifactcatalog"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestPrototypeCollectionsReadyWithUnfilteredCatalog(t *testing.T) {
	complete := []string{"projects", prototypeGenerationsCollection, "todos", prototypeItemsCollection, prototypeActiveCollection}
	if !prototypeCollectionsReady(complete) {
		t.Fatal("all required collections should be accepted among unrelated collections")
	}
	for _, missing := range []string{prototypeGenerationsCollection, prototypeItemsCollection, prototypeActiveCollection} {
		remaining := make([]string, 0, len(complete)-1)
		for _, name := range complete {
			if name != missing {
				remaining = append(remaining, name)
			}
		}
		if prototypeCollectionsReady(remaining) {
			t.Fatalf("missing %s should require migration", missing)
		}
	}
}

func TestMongoPrototypeProjectionBatchesNearLimit(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated Mongo replica-set fixture not configured")
	}
	var findCommands atomic.Int64
	var getMoreCommands atomic.Int64
	var updateCommands atomic.Int64
	monitor := &event.CommandMonitor{Started: func(_ context.Context, command *event.CommandStartedEvent) {
		if command.DatabaseName != "prototype_projection_batch_test" {
			return
		}
		switch command.CommandName {
		case "find":
			findCommands.Add(1)
		case "getMore":
			getMoreCommands.Add(1)
		case "update":
			updateCommands.Add(1)
		}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(monitor))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("prototype_projection_batch_test")
	t.Cleanup(func() {
		_ = db.Drop(context.Background())
		_ = client.Disconnect(context.Background())
	})
	collection := db.Collection(prototypeItemsCollection)
	if _, err := collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}, {Key: "generation_key", Value: 1}, {Key: "prototype_id", Value: 1}},
		Options: options.Index().SetName("prototype_item_identity_unique").SetUnique(true),
	}); err != nil {
		t.Fatal(err)
	}
	items := make([]ac.Item, ac.MaxPrototypes)
	for i := range items {
		items[i] = ac.Item{ID: fmt.Sprintf("prototype-%03d", i), Name: "Prototype", Root: fmt.Sprintf("prototypes/prototype-%03d", i), Status: "active"}
	}
	items, ids, err := preparePrototypeProjectionItems(items)
	if err != nil || len(items) != ac.MaxPrototypes || len(ids) != ac.MaxPrototypes {
		t.Fatalf("prepare near-limit generation: items=%d ids=%d err=%v", len(items), len(ids), err)
	}
	const companyID, projectID, generationKey = "company-prototype", "builder", "generation-near-limit"
	writeStarted := time.Now()
	if err := writePrototypeProjectionItemBatch(ctx, collection, companyID, projectID, generationKey, items, ids); err != nil {
		t.Fatal(err)
	}
	writeElapsed := time.Since(writeStarted)
	if got := updateCommands.Load(); got != 1 {
		t.Fatalf("near-limit first build emitted %d update commands; want one ordered batch", got)
	}
	if got := findCommands.Load(); got != 1 || getMoreCommands.Load() != 0 {
		t.Fatalf("near-limit first build emitted %d find and %d getMore commands; want one bounded find response", got, getMoreCommands.Load())
	}
	findCommands.Store(0)
	updateCommands.Store(0)
	hitStarted := time.Now()
	loaded, err := readPrototypeProjectionItems(ctx, collection, companyID, projectID, generationKey, ids)
	if err != nil || len(loaded) != ac.MaxPrototypes {
		t.Fatalf("read near-limit cache hit: items=%d err=%v", len(loaded), err)
	}
	for i := range items {
		if got := loaded[items[i].ID]; !reflect.DeepEqual(got, items[i]) {
			t.Fatalf("cache hit changed prototype %s: got=%+v want=%+v", items[i].ID, got, items[i])
		}
	}
	hitElapsed := time.Since(hitStarted)
	if got := findCommands.Load(); got != 1 || getMoreCommands.Load() != 0 || updateCommands.Load() != 0 {
		t.Fatalf("near-limit cache hit issued %d find, %d getMore and %d update commands; want one bounded find response and no writes", got, getMoreCommands.Load(), updateCommands.Load())
	}
	t.Logf("near-limit projection observed durations: ordered first-build batch+readback=%s, cache-hit item hydration=%s (no timing threshold)", writeElapsed, hitElapsed)
	if _, err := readPrototypeProjectionItems(ctx, collection, companyID, projectID, generationKey, append(ids[:len(ids)-1], "missing-prototype")); err == nil {
		t.Fatal("incomplete near-limit item set was accepted")
	}
	duplicateIDs := append([]string(nil), ids...)
	duplicateIDs[len(duplicateIDs)-1] = duplicateIDs[0]
	if _, err := readPrototypeProjectionItems(ctx, collection, companyID, projectID, generationKey, duplicateIDs); err == nil {
		t.Fatal("duplicate expected identities were accepted")
	}
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "company_id", Value: companyID}, {Key: "project_id", Value: projectID}, {Key: "generation_key", Value: generationKey}, {Key: "prototype_id", Value: ids[0]}}, bson.D{{Key: "$set", Value: bson.D{{Key: "prototype_id", Value: "unexpected-prototype"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrototypeProjectionItems(ctx, collection, companyID, projectID, generationKey, ids); err == nil {
		t.Fatal("stored context with an unexpected prototype identity was accepted")
	}
	tooMany := append(append([]ac.Item(nil), items...), ac.Item{ID: "prototype-over-limit"})
	if _, _, err := preparePrototypeProjectionItems(tooMany); err == nil {
		t.Fatal("over-limit inventory passed preflight")
	}
}
