package artifact_catalog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDecodeSourceBindingsRejectsDuplicateAndUnknownShape(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":"1","schema_version":"1","project_id":"builder","company_id":"belluga-solutions","sources":{"default":null,"company":null,"project":null}}`,
		`{"schema_version":"1","project_id":"builder","company_id":"belluga-solutions","sources":{"default":null,"company":null,"project":null,"other":null}}`,
		`{"schema_version":"1","project_id":"builder","company_id":"belluga-solutions","sources":null}`,
	} {
		if _, err := DecodeSourceBindings([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed binding map: %s", raw)
		}
	}
	if _, err := DecodeSourceBindings([]byte(strings.Repeat("x", MaxSourceBindingsBytes+1))); err == nil {
		t.Fatal("accepted a source map above 64 KiB")
	}
}

func TestDecodeDesignSystemRequiresExactlyOneTokenFieldByJSONPresence(t *testing.T) {
	raw := `{"schema_version":"1","id":"system","name":"System","owner":{"level":"project","id":"builder"},"tokens":[{"id":"value","type":"number","value":1,"ref":null}],"components":[],"assets":[],"contrast_pairs":[]}`
	var definition DesignSystemDefinition
	if err := DecodeStrict([]byte(raw), &definition); err == nil {
		t.Fatalf("accepted token with both value and explicitly-null ref: %s", raw)
	}
}

func TestValidateSourceBindingsValidatesUnusedEntryShapesForSelectedMode(t *testing.T) {
	sha := strings.Repeat("a", 40)
	bindings := SourceBindings{SchemaVersion: "1", ProjectID: "builder", CompanyID: "belluga-solutions", Sources: BindingSources{Default: &SourceBinding{RepositoryID: "fallback", CheckoutRoot: "default-repo", Revision: nil, DefinitionPath: "design/system/design-system.json"}, Company: &SourceBinding{RepositoryID: "company", CheckoutRoot: "company-repo", Revision: &sha, DefinitionPath: "design/system/design-system.json"}}}
	if diagnostics := ValidateSourceBindings(bindings, "working-tree"); len(diagnostics) == 0 || diagnostics[0].Code != "invalid_schema" {
		t.Fatalf("unused committed binding shape passed a working-tree map: %#v", diagnostics)
	}
	bindings.Sources.Company.Revision = nil
	if diagnostics := ValidateSourceBindings(bindings, "committed"); len(diagnostics) == 0 {
		t.Fatal("unused null revision passed a committed source map")
	}
}

func TestSelectSourceBindingUsesWholeSourcePrecedence(t *testing.T) {
	bindings := SourceBindings{Sources: BindingSources{
		Default: &SourceBinding{RepositoryID: "default"}, Company: &SourceBinding{RepositoryID: "company"}, Project: &SourceBinding{RepositoryID: "project"},
	}}
	level, selected := SelectSourceBinding(bindings)
	if level != "project" || selected == nil || selected.RepositoryID != "project" {
		t.Fatalf("project did not win whole-source precedence: %s %#v", level, selected)
	}
	bindings.Sources.Project = nil
	level, selected = SelectSourceBinding(bindings)
	if level != "company" || selected == nil || selected.RepositoryID != "company" {
		t.Fatalf("company did not win over default: %s %#v", level, selected)
	}
	bindings.Sources.Company = nil
	level, selected = SelectSourceBinding(bindings)
	if level != "default" || selected == nil || selected.RepositoryID != "default" {
		t.Fatalf("default source was not selected: %s %#v", level, selected)
	}
}

func TestEvaluateDesignSystemChecksOwnerAndNeverReturnsPartialAcceptedData(t *testing.T) {
	definition := validDesignSystemFixture()
	definition.Owner.ID = "forged-owner"
	response := evaluateDesignSystemFixture(t, definition)
	if response.Outcome != "no_go" || response.Items == nil || len(response.Items) != 0 || response.InventoryDigest != nil || response.DefinitionDigest != nil || !designSystemHasCode(response, "owner_mismatch") {
		t.Fatalf("owner mismatch did not fail closed: %#v", response)
	}
}

func TestEvaluateDesignSystemResolvesAliasesIterativelyAndRejectsFailures(t *testing.T) {
	definition := validDesignSystemFixture()
	definition.Tokens = make([]DesignToken, MaxTokens)
	definition.Components = []DesignComponent{}
	definition.ContrastPairs = []ContrastPair{}
	definition.Assets = []string{}
	for index := range definition.Tokens {
		id := fmt.Sprintf("token-%04d", index)
		if index+1 == len(definition.Tokens) {
			definition.Tokens[index] = DesignToken{ID: id, Type: "string", Value: json.RawMessage(`"resolved"`)}
		} else {
			next := fmt.Sprintf("token-%04d", index+1)
			definition.Tokens[index] = DesignToken{ID: id, Type: "string", Ref: &next}
		}
	}
	definition.Components = []DesignComponent{}
	definition.ContrastPairs = []ContrastPair{}
	definition.Assets = []string{}
	definition.Tokens[0].ID = "primary"
	definition.Tokens[len(definition.Tokens)-1].ID = "terminal"
	definition.Tokens[len(definition.Tokens)-2].Ref = stringPointer("terminal")
	response := evaluateDesignSystemFixture(t, definition)
	if response.Outcome != "go" {
		t.Fatalf("maximum iterative alias chain rejected: %#v", response.Diagnostics)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*DesignSystemDefinition)
		code   string
	}{
		{name: "cycle", mutate: func(d *DesignSystemDefinition) {
			d.Tokens = []DesignToken{{ID: "primary", Type: "string", Ref: stringPointer("other")}, {ID: "other", Type: "string", Ref: stringPointer("primary")}}
		}, code: "alias_cycle"},
		{name: "dangling", mutate: func(d *DesignSystemDefinition) { d.Tokens[0].Value = nil; d.Tokens[0].Ref = stringPointer("missing") }, code: "invalid_reference"},
		{name: "wrong alias type", mutate: func(d *DesignSystemDefinition) {
			d.Tokens = []DesignToken{{ID: "primary", Type: "string", Ref: stringPointer("other")}, {ID: "other", Type: "number", Value: json.RawMessage(`1`)}}
		}, code: "invalid_reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := validDesignSystemFixture()
			tc.mutate(&broken)
			result := evaluateDesignSystemFixture(t, broken)
			if result.Outcome != "no_go" || !designSystemHasCode(result, tc.code) {
				t.Fatalf("expected %s no-go, got %#v", tc.code, result)
			}
		})
	}
}

func TestDesignSystemRejectsDuplicateNamespacesAndRequiredNullArrays(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*DesignSystemDefinition)
		code   string
	}{
		{name: "duplicate token", mutate: func(d *DesignSystemDefinition) { d.Tokens = append(d.Tokens, d.Tokens[0]) }, code: "duplicate_id"},
		{name: "duplicate component", mutate: func(d *DesignSystemDefinition) { d.Components = append(d.Components, d.Components[0]) }, code: "duplicate_id"},
		{name: "duplicate variant", mutate: func(d *DesignSystemDefinition) {
			d.Components[0].Variants = append(d.Components[0].Variants, d.Components[0].Variants[0])
		}, code: "duplicate_id"},
		{name: "duplicate component token", mutate: func(d *DesignSystemDefinition) {
			d.Components[0].TokenIDs = append(d.Components[0].TokenIDs, d.Components[0].TokenIDs[0])
		}, code: "duplicate_id"},
		{name: "null components", mutate: func(d *DesignSystemDefinition) { d.Components = nil }, code: "invalid_schema"},
		{name: "null token IDs", mutate: func(d *DesignSystemDefinition) { d.Components[0].TokenIDs = nil }, code: "invalid_schema"},
		{name: "null states", mutate: func(d *DesignSystemDefinition) { d.Components[0].States = nil }, code: "invalid_schema"},
		{name: "null variants", mutate: func(d *DesignSystemDefinition) { d.Components[0].Variants = nil }, code: "invalid_schema"},
		{name: "null examples", mutate: func(d *DesignSystemDefinition) { d.Components[0].Examples = nil }, code: "invalid_schema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := validDesignSystemFixture()
			tc.mutate(&definition)
			response := evaluateDesignSystemFixture(t, definition)
			if response.Outcome != "no_go" || !designSystemHasCode(response, tc.code) {
				t.Fatalf("expected %s no-go, got %#v", tc.code, response.Diagnostics)
			}
		})
	}
}

func TestEvaluateDesignSystemChecksWCAGUnroundedContrastBoundaries(t *testing.T) {
	definition := validDesignSystemFixture()
	definition.Tokens = []DesignToken{{ID: "black", Type: "color", Value: json.RawMessage(`"#000000"`)}, {ID: "white", Type: "color", Value: json.RawMessage(`"#FFFFFF"`)}, {ID: "near-black", Type: "color", Value: json.RawMessage(`"#0A0A0A"`)}, {ID: "gray", Type: "color", Value: json.RawMessage(`"#777777"`)}}
	definition.Components = []DesignComponent{}
	definition.Assets = []string{}
	definition.ContrastPairs = []ContrastPair{{ForegroundToken: "black", BackgroundToken: "white", MinRatio: 21}, {ForegroundToken: "black", BackgroundToken: "near-black", MinRatio: 1}, {ForegroundToken: "gray", BackgroundToken: "gray", MinRatio: 1}}
	response := evaluateDesignSystemFixture(t, definition)
	if response.Outcome != "go" || response.ContrastPairsChecked != 3 {
		t.Fatalf("exact contrast boundaries were rejected: %#v", response)
	}
	definition.ContrastPairs = []ContrastPair{{ForegroundToken: "gray", BackgroundToken: "white", MinRatio: 4.48}}
	response = evaluateDesignSystemFixture(t, definition)
	if response.Outcome != "no_go" || !designSystemHasCode(response, "contrast_failed") {
		t.Fatalf("unrounded over-threshold requirement passed: %#v", response)
	}
	breakpoint := relativeChannel(0.04045)
	if breakpoint != 0.04045/12.92 {
		t.Fatalf("WCAG channel breakpoint used the nonlinear branch at equality: %.12f", breakpoint)
	}
}

func TestDesignSystemInventoryDigestIncludesSupportFilesNotDefinitionDigest(t *testing.T) {
	first := designSystemMemoryFixture(validDesignSystemFixture())
	response1 := EvaluateDesignSystem(first, "builder", "belluga-solutions", "project", validBinding())
	first.files["design/design-system/guide.md"] = []byte("changed support file")
	response2 := EvaluateDesignSystem(first, "builder", "belluga-solutions", "project", validBinding())
	if response1.Outcome != "go" || response2.Outcome != "go" || response1.InventoryDigest == nil || response2.InventoryDigest == nil || *response1.InventoryDigest == *response2.InventoryDigest || response1.DefinitionDigest == nil || response2.DefinitionDigest == nil || *response1.DefinitionDigest != *response2.DefinitionDigest {
		t.Fatalf("support-file change did not affect only full inventory digest: first=%#v second=%#v", response1, response2)
	}
}

func TestDesignSystemInventoryRejectsOrphansAndAssetsOverlappingDocs(t *testing.T) {
	definition := validDesignSystemFixture()
	definition.Assets = []string{"guide.md"}
	response := evaluateDesignSystemFixture(t, definition)
	if response.Outcome != "no_go" || !designSystemHasCode(response, "invalid_reference") {
		t.Fatalf("asset/document overlap was accepted: %#v", response)
	}
	source := designSystemMemoryFixture(validDesignSystemFixture())
	source.entries = append(source.entries, Entry{Path: "design/design-system/orphan.txt", Mode: ModeRegular, Size: 1})
	source.files["design/design-system/orphan.txt"] = []byte("x")
	response = EvaluateDesignSystem(source, "builder", "belluga-solutions", "project", validBinding())
	if response.Outcome != "no_go" || !designSystemHasCode(response, "undeclared_file") {
		t.Fatalf("orphan managed file was accepted: %#v", response)
	}
}

func TestDesignSystemLimitPreflightStopsBeforeReadingManagedPayloads(t *testing.T) {
	for _, scenario := range []string{"aggregate-bytes", "file-count"} {
		t.Run(scenario, func(t *testing.T) {
			source := designSystemMemoryFixture(validDesignSystemFixture())
			if scenario == "aggregate-bytes" {
				for index := range source.entries {
					if source.entries[index].Path == "design/design-system/assets/logo.svg" {
						source.entries[index].Size = MaxTotalBytes + 1
					}
				}
			} else {
				for index := 0; index < MaxFiles; index++ {
					source.entries = append(source.entries, Entry{Path: fmt.Sprintf("design/design-system/extra-%04d", index), Mode: ModeRegular, Size: 1})
				}
			}
			response := EvaluateDesignSystem(source, "builder", "belluga-solutions", "project", validBinding())
			if response.Outcome != "no_go" || len(response.Items) != 0 || response.InventoryDigest != nil || response.DefinitionDigest != nil || !designSystemHasCode(response, "limit_exceeded") {
				t.Fatalf("oversized inventory did not fail closed: %#v", response)
			}
			if source.readCalls != 1 {
				t.Fatalf("read %d source files after the definition despite inventory limits; expected only the manifest read", source.readCalls)
			}
		})
	}
}

func validDesignSystemFixture() DesignSystemDefinition {
	return DesignSystemDefinition{SchemaVersion: "1", ID: "builder-system", Name: "Builder System", Owner: DesignSystemOwner{Level: "project", ID: "builder"},
		Tokens:     []DesignToken{{ID: "ink", Type: "color", Value: json.RawMessage(`"#000000"`)}, {ID: "paper", Type: "color", Value: json.RawMessage(`"#FFFFFF"`)}},
		Components: []DesignComponent{{ID: "button", Name: "Button", Documentation: "guide.md", TokenIDs: []string{"ink"}, States: []string{"default"}, Variants: []DesignVariant{{ID: "primary", Name: "Primary", TokenIDs: []string{"paper"}}}, Examples: []string{"examples/button.md"}}},
		Assets:     []string{"assets/logo.svg"}, ContrastPairs: []ContrastPair{{ForegroundToken: "ink", BackgroundToken: "paper", MinRatio: 21}}}
}

func validBinding() SourceBinding {
	return SourceBinding{RepositoryID: "builder-foundation", CheckoutRoot: "belluga_builder_foundation_documentation", Revision: nil, DefinitionPath: "design/design-system/design-system.json"}
}

func evaluateDesignSystemFixture(t *testing.T, definition DesignSystemDefinition) DesignSystemResponse {
	t.Helper()
	return EvaluateDesignSystem(designSystemMemoryFixture(definition), "builder", "belluga-solutions", "project", validBinding())
}

func designSystemMemoryFixture(definition DesignSystemDefinition) *memorySource {
	definitionBytes, _ := json.Marshal(definition)
	root := "design/design-system"
	files := map[string][]byte{}
	entries := []Entry{{Path: root, Mode: ModeDirectory}}
	addFile := func(relative string, data []byte) {
		full := root + "/" + relative
		files[full] = data
		for directory := full[:strings.LastIndex(full, "/")]; strings.HasPrefix(directory, root+"/"); directory = directory[:strings.LastIndex(directory, "/")] {
			found := false
			for _, entry := range entries {
				if entry.Path == directory {
					found = true
					break
				}
			}
			if !found {
				entries = append(entries, Entry{Path: directory, Mode: ModeDirectory})
			}
			if directory == root {
				break
			}
		}
		entries = append(entries, Entry{Path: full, Mode: ModeRegular, Size: int64(len(data))})
	}
	addFile("design-system.json", definitionBytes)
	for _, component := range definition.Components {
		addFile(component.Documentation, []byte("guide"))
		for _, example := range component.Examples {
			addFile(example, []byte("example"))
		}
	}
	for _, asset := range definition.Assets {
		addFile(asset, []byte("asset"))
	}
	return &memorySource{files: files, entries: entries}
}

func designSystemHasCode(response DesignSystemResponse, code string) bool {
	for _, diagnostic := range response.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
