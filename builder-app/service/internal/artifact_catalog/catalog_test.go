package artifact_catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateRelativePathRejectsTraversalAndAliases(t *testing.T) {
	for _, path := range []string{"../outside", "a/../b", "a//b", "/absolute", `a\\b`, ".git/config"} {
		t.Run(path, func(t *testing.T) {
			if err := ValidateRelativePath(path); err == nil {
				t.Fatalf("ValidateRelativePath(%q) accepted an unsafe path", path)
			}
		})
	}
	if err := ValidateRelativePath("prototypes/checkout/main.html"); err != nil {
		t.Fatalf("valid relative path rejected: %v", err)
	}
}

func TestDecodeStrictRejectsDuplicateAndCaseAliasFields(t *testing.T) {
	var value Catalog
	for _, raw := range []string{
		`{"schema_version":"1","schema_version":"1","project_id":"builder","prototypes":[]}`,
		`{"schema_version":"1","Schema_Version":"1","project_id":"builder","prototypes":[]}`,
	} {
		if err := DecodeStrict([]byte(raw), &value); err == nil {
			t.Fatalf("DecodeStrict accepted malformed object: %s", raw)
		}
	}
	if err := DecodeStrict([]byte(`{"schema_version":"1","project_id":"builder","prototypes":[]}`), &value); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	for _, raw := range []string{
		`{"schema_version":"1","project_id":"builder","prototypes":[],"extra":true}`,
		`{"schema_version":"1","project_id":"builder","Prototypes":[]}`,
		`{"schema_version":1,"project_id":"builder","prototypes":[]}`,
		`{"schema_version":"1","project_id":"builder","prototypes":[]} {}`,
	} {
		if err := DecodeStrict([]byte(raw), &value); err == nil {
			t.Fatalf("DecodeStrict accepted malformed catalog field/type/value shape: %s", raw)
		}
	}
	var manifest Manifest
	missingScope := []byte(`{"schema_version":"1","id":"sample","entry_point":"index.html","screens":[{"id":"home","name":"Home","path":"index.html"}],"sources":["index.html"],"assets":[],"links":[],"related":[],"design_system_ref":null}`)
	if err := DecodeStrict(missingScope, &manifest); err == nil {
		t.Fatal("DecodeStrict accepted a Screen without its required explicit scope:null field")
	}
	validManifest := []byte(`{"schema_version":"1","id":"sample","entry_point":"index.html","screens":[{"id":"home","name":"Home","path":"index.html","scope":null}],"sources":["index.html"],"assets":[],"links":[],"related":[],"design_system_ref":null}`)
	if err := DecodeStrict(validManifest, &manifest); err != nil {
		t.Fatalf("valid manifest with explicit null scope rejected: %v", err)
	}
	for _, raw := range []string{
		`{"schema_version":"1","id":"sample","entry_point":"index.html","screens":[],"sources":[],"assets":[],"links":[],"related":[],"design_system_ref":null,"extra":true}`,
		`{"schema_version":"1","id":"sample","entry_point":"index.html","screens":[{"id":"home","name":"Home","path":"index.html","scope":null,"extra":true}],"sources":[],"assets":[],"links":[],"related":[],"design_system_ref":null}`,
		`{"schema_version":"1","id":"sample","entry_point":"index.html","screens":"not-an-array","sources":[],"assets":[],"links":[],"related":[],"design_system_ref":null}`,
	} {
		if err := DecodeStrict([]byte(raw), &manifest); err == nil {
			t.Fatalf("DecodeStrict accepted malformed manifest shape: %s", raw)
		}
	}
}

func TestEvaluateRejectsPrototypeRelativeContractViolations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*memorySource, *Manifest)
		code   string
	}{
		{name: "duplicate screen id", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Screens = append(manifest.Screens, manifest.Screens[0])
		}, code: "duplicate_id"},
		{name: "invalid entry point", mutate: func(_ *memorySource, manifest *Manifest) { manifest.EntryPoint = "../outside.html" }, code: "invalid_path"},
		{name: "invalid link endpoint", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Links = []Link{{FromScreenID: "home", ToScreenID: "missing"}}
		}, code: "invalid_reference"},
		{name: "duplicate link pair", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Screens = append(manifest.Screens, Screen{ID: "next", Name: "Next", Path: "next.html"})
			manifest.Sources = append(manifest.Sources, "next.html")
			manifest.Links = []Link{{FromScreenID: "home", ToScreenID: "next"}, {FromScreenID: "home", ToScreenID: "next"}}
		}, code: "invalid_reference"},
		{name: "duplicate source path", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Sources = append(manifest.Sources, "index.html")
		}, code: "duplicate_id"},
		{name: "asset overlaps source", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Assets = []string{"index.html"}
		}, code: "duplicate_id"},
		{name: "non-null screen scope", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Screens[0].Scope = stringPointer("global")
		}, code: "invalid_schema"},
		{name: "invalid related kind", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Related = []Related{{Kind: "approval", Path: "artifacts/evidence.md"}}
		}, code: "invalid_reference"},
		{name: "related Git metadata path", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Related = []Related{{Kind: "documentation", Path: "artifacts/.git/config"}}
		}, code: "invalid_path"},
		{name: "missing related evidence", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.Related = []Related{{Kind: "todo", Path: "artifacts/missing.md"}}
		}, code: "invalid_reference"},
		{name: "malformed design system identity", mutate: func(_ *memorySource, manifest *Manifest) {
			manifest.DesignSystemRef = &DesignSystemRef{ID: "Bad ID", ContentDigest: "sha256:bad"}
		}, code: "invalid_reference"},
		{name: "duplicate catalog identity", mutate: func(source *memorySource, _ *Manifest) {
			source.files["prototypes/catalog.json"] = []byte(`{"schema_version":"1","project_id":"builder","prototypes":[{"id":"sample","name":"Sample","description":null,"root":"prototypes/sample"},{"id":"sample","name":"Again","description":null,"root":"prototypes/sample"}]}`)
		}, code: "duplicate_id"},
		{name: "catalog unknown field", mutate: func(source *memorySource, _ *Manifest) {
			source.files["prototypes/catalog.json"] = []byte(`{"schema_version":"1","project_id":"builder","prototypes":[],"extra":true}`)
		}, code: "invalid_schema"},
		{name: "catalog wrong-case field", mutate: func(source *memorySource, _ *Manifest) {
			source.files["prototypes/catalog.json"] = []byte(`{"schema_version":"1","project_id":"builder","prototypes":[],"Project_ID":"builder"}`)
		}, code: "invalid_schema"},
		{name: "null required array", mutate: func(_ *memorySource, manifest *Manifest) { manifest.Sources = nil }, code: "invalid_schema"},
		{name: "orphan directory", mutate: func(source *memorySource, _ *Manifest) {
			source.entries = append(source.entries, Entry{Path: "prototypes/sample/empty", Mode: ModeDirectory})
		}, code: "undeclared_file"},
		{name: "per-file byte bound", mutate: func(source *memorySource, _ *Manifest) {
			for index := range source.entries {
				if source.entries[index].Path == "prototypes/sample/index.html" {
					source.entries[index].Size = MaxFileBytes + 1
				}
			}
		}, code: "limit_exceeded"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := validFixture("sample")
			manifestPath := "prototypes/sample/prototype.json"
			var manifest Manifest
			if err := DecodeStrict(source.files[manifestPath], &manifest); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(source, &manifest)
			updated, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			source.files[manifestPath] = updated
			response := Evaluate(source, DefaultProjectID)
			if response.Outcome != "no_go" || len(response.Items) != 0 || response.InventoryDigest != nil || !diagnosticCode(response, testCase.code) {
				t.Fatalf("expected no_go with %s and no partial result: %#v", testCase.code, response)
			}
		})
	}
}

func TestInventoryDigestIsDeterministicAndIncludesContent(t *testing.T) {
	first, err := InventoryDigest("builder", []DigestRecord{
		{SourceID: "builder", Path: "prototypes/a/index.html", Content: []byte("one")},
		{SourceID: "builder", Path: "prototypes/a/prototype.json", Content: []byte("manifest")},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := InventoryDigest("builder", []DigestRecord{
		{SourceID: "builder", Path: "prototypes/a/prototype.json", Content: []byte("manifest")},
		{SourceID: "builder", Path: "prototypes/a/index.html", Content: []byte("one")},
	})
	if err != nil || first != second {
		t.Fatalf("record ordering changed digest: %q %q err=%v", first, second, err)
	}
	changed, err := InventoryDigest("builder", []DigestRecord{
		{SourceID: "builder", Path: "prototypes/a/index.html", Content: []byte("two")},
		{SourceID: "builder", Path: "prototypes/a/prototype.json", Content: []byte("manifest")},
	})
	if err != nil || first == changed || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("content change did not produce a sha256 inventory digest: %q %q err=%v", first, changed, err)
	}
}

func TestEvaluateReturnsCompleteStablePrototypeInventory(t *testing.T) {
	response := Evaluate(validFixture("zeta", "alpha"), DefaultProjectID)
	if response.Outcome != "go" || len(response.Items) != 2 || response.InventoryDigest == nil {
		t.Fatalf("expected complete go inventory, got %#v", response)
	}
	if response.Diagnostics == nil || len(response.Diagnostics) != 0 {
		t.Fatalf("successful nonempty inventory must carry an empty diagnostics array: %#v", response.Diagnostics)
	}
	serialized, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("serialize successful response: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(serialized, &wire); err != nil || string(wire["diagnostics"]) != "[]" {
		t.Fatalf("successful response diagnostics must serialize as []: %s (err=%v)", serialized, err)
	}
	if response.Items[0].ID != "alpha" || response.Items[1].ID != "zeta" {
		t.Fatalf("items are not stably sorted by ID: %#v", response.Items)
	}
	if response.Items[0].EntryPoint != "index.html" || response.Items[0].Screens[0].Path != "index.html" {
		t.Fatalf("manifest paths did not retain their Prototype-root-relative contract: %#v", response.Items[0])
	}
	if response.DesignSystemValidation != "not_evaluated" || response.AuthorityScope != "local_structure_only" {
		t.Fatalf("Prototype evaluation claimed an unrelated authority: %#v", response)
	}
}

func TestEvaluateAcceptsExplicitEmptyCatalog(t *testing.T) {
	data, _ := json.Marshal(Catalog{SchemaVersion: SchemaVersion, ProjectID: DefaultProjectID, Prototypes: []Prototype{}})
	source := &memorySource{files: map[string][]byte{"prototypes/catalog.json": data}, entries: []Entry{{Path: "prototypes", Mode: ModeDirectory}, {Path: "prototypes/catalog.json", Mode: ModeRegular}}}
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "go" || response.Items == nil || len(response.Items) != 0 || response.InventoryDigest == nil || response.Diagnostics == nil || len(response.Diagnostics) != 0 {
		t.Fatalf("explicit empty collection is not a valid empty inventory: %#v", response)
	}
}

func TestEvaluateAcceptsNestedPrototypeRootRelativeFiles(t *testing.T) {
	source := validFixture("sample")
	delete(source.files, "prototypes/sample/index.html")
	manifestPath := "prototypes/sample/prototype.json"
	var manifest Manifest
	if err := DecodeStrict(source.files[manifestPath], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.EntryPoint = "pages/index.html"
	manifest.Screens[0].Path = "pages/index.html"
	manifest.Sources = []string{"pages/index.html"}
	manifest.Assets = []string{"images/logo.svg"}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	source.files[manifestPath] = manifestBytes
	source.files["prototypes/sample/pages/index.html"] = []byte("<main>nested</main>")
	source.files["prototypes/sample/images/logo.svg"] = []byte("<svg/>")
	source.entries = []Entry{{Path: "prototypes", Mode: ModeDirectory}, {Path: "prototypes/catalog.json", Mode: ModeRegular}, {Path: "prototypes/sample", Mode: ModeDirectory}, {Path: manifestPath, Mode: ModeRegular}, {Path: "prototypes/sample/pages", Mode: ModeDirectory}, {Path: "prototypes/sample/pages/index.html", Mode: ModeRegular}, {Path: "prototypes/sample/images", Mode: ModeDirectory}, {Path: "prototypes/sample/images/logo.svg", Mode: ModeRegular}}
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "go" || len(response.Items) != 1 || response.Items[0].EntryPoint != "pages/index.html" || response.InventoryDigest == nil {
		t.Fatalf("nested root-relative file inventory was rejected: %#v", response)
	}
}

func TestEvaluateRejectsOverBudgetInventoryBeforeReadingPayloads(t *testing.T) {
	for _, profile := range []string{"file count", "aggregate bytes"} {
		t.Run(profile, func(t *testing.T) {
			source := validFixture("sample")
			entryCount := MaxFiles
			fileSize := int64(1)
			if profile == "aggregate bytes" {
				entryCount = MaxTotalBytes/MaxFileBytes + 1
				fileSize = MaxFileBytes
			}
			for index := 0; index < entryCount; index++ {
				source.entries = append(source.entries, Entry{Path: fmt.Sprintf("prototypes/sample/extra-%d", index), Mode: ModeRegular, Size: fileSize})
			}
			response := Evaluate(source, DefaultProjectID)
			if response.Outcome != "no_go" || !diagnosticCode(response, "limit_exceeded") || source.readCalls != 0 {
				t.Fatalf("over-budget inventory read payloads before rejection: reads=%d response=%#v", source.readCalls, response)
			}
		})
	}
}

func TestEvaluateRejectsUnregisteredFilesWithoutPartialItems(t *testing.T) {
	source := validFixture("sample")
	source.entries = append(source.entries, Entry{Path: "prototypes/sample/ignored.txt", Mode: ModeRegular})
	source.files["prototypes/sample/ignored.txt"] = []byte("not actually ignored")
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "no_go" || len(response.Items) != 0 || response.InventoryDigest != nil || !diagnosticCode(response, "undeclared_file") {
		t.Fatalf("unregistered file did not fail closed: %#v", response)
	}
}

func TestEvaluateRejectsRelatedSymlinkReference(t *testing.T) {
	source := validFixture("sample")
	manifest := Manifest{SchemaVersion: SchemaVersion, ID: "sample", EntryPoint: "index.html", Screens: []Screen{{ID: "home", Name: "Home", Path: "index.html"}}, Sources: []string{"index.html"}, Assets: []string{}, Links: []Link{}, Related: []Related{{Kind: "todo", Path: "artifacts/link/evidence.md"}}}
	manifestData, _ := json.Marshal(manifest)
	source.files["prototypes/sample/prototype.json"] = manifestData
	source.modes["artifacts/link/evidence.md"] = ModeSymlink
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "no_go" || !diagnosticCode(response, "invalid_reference") {
		t.Fatalf("related symlink was accepted: %#v", response)
	}
}

func TestEvaluateAppliesBoundedReadToRelatedEvidence(t *testing.T) {
	source := validFixture("sample")
	manifestPath := "prototypes/sample/prototype.json"
	var manifest Manifest
	if err := DecodeStrict(source.files[manifestPath], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Related = []Related{{Kind: "documentation", Path: "artifacts/oversized.md"}}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	source.files[manifestPath] = manifestBytes
	source.files["artifacts/oversized.md"] = make([]byte, MaxFileBytes+1)
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "no_go" || !diagnosticCode(response, "limit_exceeded") {
		t.Fatalf("oversized related evidence was not rejected by the bounded reader: %#v", response)
	}
}

func TestEvaluateChecksRelatedRegularityWithoutReadingOrHashingItsPayload(t *testing.T) {
	source := validFixture("sample")
	manifestPath := "prototypes/sample/prototype.json"
	var manifest Manifest
	if err := DecodeStrict(source.files[manifestPath], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Related = []Related{{Kind: "documentation", Path: "artifacts/evidence.md"}}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	source.files[manifestPath] = manifestBytes
	source.files["artifacts/evidence.md"] = make([]byte, MaxFileBytes)
	first := Evaluate(source, DefaultProjectID)
	if first.Outcome != "go" || source.readPaths["artifacts/evidence.md"] != 0 || source.checkPaths["artifacts/evidence.md"] != 1 {
		t.Fatalf("related evidence was read or not checked as a regular file: response=%#v reads=%d checks=%d", first, source.readPaths["artifacts/evidence.md"], source.checkPaths["artifacts/evidence.md"])
	}
	firstDigest := *first.InventoryDigest
	source.files["artifacts/evidence.md"] = bytes.Repeat([]byte{'x'}, MaxFileBytes)
	second := Evaluate(source, DefaultProjectID)
	if second.Outcome != "go" || *second.InventoryDigest != firstDigest {
		t.Fatalf("related payload bytes changed the managed inventory digest: first=%#v second=%#v", first, second)
	}
}

func TestEvaluateStopsRelatedPayloadReadsAfterInvalidCollectionCount(t *testing.T) {
	for _, scenario := range []string{"empty sources", "too many screens", "too many links", "too many related"} {
		t.Run(scenario, func(t *testing.T) {
			source := validFixture("sample")
			manifestPath := "prototypes/sample/prototype.json"
			var manifest Manifest
			if err := DecodeStrict(source.files[manifestPath], &manifest); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "empty sources":
				manifest.Sources = []string{}
			case "too many screens":
				manifest.Screens = make([]Screen, MaxScreens+1)
			case "too many links":
				manifest.Links = make([]Link, MaxLinks+1)
			case "too many related":
				manifest.Related = make([]Related, MaxRelated+1)
			}
			for index := range manifest.Related {
				manifest.Related[index] = Related{Kind: "documentation", Path: "artifacts/evidence.md"}
			}
			manifestBytes, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			source.files[manifestPath] = manifestBytes
			source.files["artifacts/evidence.md"] = []byte("valid regular evidence")
			response := Evaluate(source, DefaultProjectID)
			if response.Outcome != "no_go" || !diagnosticCode(response, "limit_exceeded") {
				t.Fatalf("invalid collection count did not fail structurally: %#v", response)
			}
			if source.readPaths["artifacts/evidence.md"] != 0 || source.checkPaths["artifacts/evidence.md"] != 0 {
				t.Fatalf("related evidence was accessed after invalid %s count: reads=%d metadata checks=%d", scenario, source.readPaths["artifacts/evidence.md"], source.checkPaths["artifacts/evidence.md"])
			}
		})
	}
}

func TestEvaluateRejectsInventoryChangedAfterAFileWasRead(t *testing.T) {
	source := validFixture("sample")
	source.mutateAfterRead = "prototypes/sample/index.html"
	response := Evaluate(source, DefaultProjectID)
	if response.Outcome != "no_go" || len(response.Items) != 0 || response.InventoryDigest != nil || !diagnosticCode(response, "read_failed") {
		t.Fatalf("changed working-tree bytes were accepted as a stable inventory: %#v", response)
	}
}

func TestDiagnosticsUseRelativePathsAndDoNotLeakCheckoutRoots(t *testing.T) {
	privateRoot := "/tmp/private-foundation-checkout"
	source := &memorySource{listErr: &PathFailure{Path: "prototypes/blocked", Err: errors.New(privateRoot + ": permission denied")}}
	response := Evaluate(source, DefaultProjectID)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), privateRoot) || len(response.Diagnostics) != 1 || response.Diagnostics[0].Path == nil || *response.Diagnostics[0].Path != "prototypes/blocked" {
		t.Fatalf("diagnostic leaked an absolute checkout path or lost the relative path: %s", data)
	}
}

func validFixture(ids ...string) *memorySource {
	source := &memorySource{files: map[string][]byte{}, modes: map[string]FileMode{}, entries: []Entry{{Path: "prototypes", Mode: ModeDirectory}}}
	catalog := Catalog{SchemaVersion: SchemaVersion, ProjectID: DefaultProjectID, Prototypes: []Prototype{}}
	for _, id := range ids {
		root := "prototypes/" + id
		catalog.Prototypes = append(catalog.Prototypes, Prototype{ID: id, Name: strings.ToUpper(id), Root: root})
		manifest := Manifest{SchemaVersion: SchemaVersion, ID: id, EntryPoint: "index.html", Screens: []Screen{{ID: "home", Name: "Home", Path: "index.html"}}, Sources: []string{"index.html"}, Assets: []string{}, Links: []Link{}, Related: []Related{}}
		manifestData, _ := json.Marshal(manifest)
		source.files[root+"/prototype.json"] = manifestData
		source.files[root+"/index.html"] = []byte("<main>" + id + "</main>")
		source.entries = append(source.entries,
			Entry{Path: root, Mode: ModeDirectory},
			Entry{Path: root + "/prototype.json", Mode: ModeRegular},
			Entry{Path: root + "/index.html", Mode: ModeRegular},
		)
	}
	catalogBytes, _ := json.Marshal(catalog)
	source.files["prototypes/catalog.json"] = catalogBytes
	source.entries = append(source.entries, Entry{Path: "prototypes/catalog.json", Mode: ModeRegular})
	return source
}

func diagnosticCode(response Response, code string) bool {
	for _, item := range response.Diagnostics {
		if item.Code == code {
			return true
		}
	}
	return false
}

type memorySource struct {
	files           map[string][]byte
	modes           map[string]FileMode
	entries         []Entry
	mutateAfterRead string
	changed         bool
	listErr         error
	readCalls       int
	readPaths       map[string]int
	checkPaths      map[string]int
}

func (s *memorySource) Mode() string      { return "working-tree" }
func (s *memorySource) Revision() *string { return nil }
func (s *memorySource) List(prefix string) ([]Entry, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	result := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.Path == prefix || strings.HasPrefix(entry.Path, prefix+"/") {
			result = append(result, entry)
		}
	}
	if len(result) == 0 {
		return nil, ErrNotExist
	}
	return result, nil
}
func (s *memorySource) Read(path string, limit int64) ([]byte, FileMode, error) {
	s.readCalls++
	if s.readPaths == nil {
		s.readPaths = map[string]int{}
	}
	s.readPaths[path]++
	if mode, ok := s.modes[path]; ok {
		return nil, mode, errors.New("unsafe node")
	}
	data, ok := s.files[path]
	if !ok {
		return nil, ModeSpecial, ErrNotExist
	}
	if int64(len(data)) > limit {
		return nil, ModeRegular, ErrLimit
	}
	read := append([]byte(nil), data...)
	if path == s.mutateAfterRead && !s.changed {
		s.files[path] = append([]byte(nil), data...)
		s.files[path] = append(s.files[path], []byte(" changed after read")...)
		s.changed = true
	}
	return read, ModeRegular, nil
}

func (s *memorySource) CheckRegular(path string, limit int64) error {
	if s.checkPaths == nil {
		s.checkPaths = map[string]int{}
	}
	s.checkPaths[path]++
	if mode, ok := s.modes[path]; ok {
		if mode != ModeRegular {
			return ErrUnsafePath
		}
	}
	data, ok := s.files[path]
	if !ok {
		return ErrNotExist
	}
	if int64(len(data)) > limit {
		return ErrLimit
	}
	return nil
}

func (s *memorySource) VerifyStable() error {
	if s.changed {
		return errors.New("fixture mutation detected")
	}
	return nil
}
