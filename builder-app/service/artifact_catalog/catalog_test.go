package artifact_catalog

import "testing"

func TestValidateRelativePathRejectsTraversalAndAliases(t *testing.T) {
	for _, path := range []string{"../outside", "a/../b", "a//b", "/absolute", `a\\b`, ".git/config"} {
		t.Run(path, func(t *testing.T) {
			if err := ValidateRelativePath(path); err == nil {
				t.Fatalf("ValidateRelativePath(%q) accepted an unsafe path", path)
			}
		})
	}
	if err := ValidateRelativePath("design/system/tokens.json"); err != nil {
		t.Fatalf("valid relative path rejected: %v", err)
	}
}

func TestInventoryDigestIsDeterministicAndIncludesContent(t *testing.T) {
	first, err := InventoryDigest("builder", []DigestRecord{
		{SourceID: "builder", Path: "design/system/a.json", Content: []byte("one")},
		{SourceID: "builder", Path: "design/system/definition.json", Content: []byte("definition")},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := InventoryDigest("builder", []DigestRecord{
		{SourceID: "builder", Path: "design/system/definition.json", Content: []byte("definition")},
		{SourceID: "builder", Path: "design/system/a.json", Content: []byte("one")},
	})
	if err != nil || second != first {
		t.Fatalf("digest depends on record order: first=%q second=%q err=%v", first, second, err)
	}
	changed, err := InventoryDigest("builder", []DigestRecord{{SourceID: "builder", Path: "design/system/a.json", Content: []byte("changed")}})
	if err != nil || changed == first {
		t.Fatalf("digest did not include content: first=%q changed=%q err=%v", first, changed, err)
	}
}
