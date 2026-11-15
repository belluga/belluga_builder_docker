package registry

import "testing"

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
