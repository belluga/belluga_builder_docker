package artifact_catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxSourceBindingsBytes  = 64 << 10
	MaxTokens               = 2048
	MaxComponents           = 256
	MaxStatesPerComponent   = 64
	MaxVariantsPerComponent = 64
	MaxContrastPairs        = 256
	TargetDesignSystem      = "design-system"
)

type SourceBindings struct {
	SchemaVersion string         `json:"schema_version"`
	ProjectID     string         `json:"project_id"`
	CompanyID     string         `json:"company_id"`
	Sources       BindingSources `json:"sources"`
}

type BindingSources struct {
	Default *SourceBinding `json:"default"`
	Company *SourceBinding `json:"company"`
	Project *SourceBinding `json:"project"`
}

type SourceBinding struct {
	RepositoryID   string  `json:"repository_id"`
	CheckoutRoot   string  `json:"checkout_root"`
	Revision       *string `json:"revision"`
	DefinitionPath string  `json:"definition_path"`
}

type DesignSystemOwner struct {
	Level string `json:"level"`
	ID    string `json:"id"`
}

type DesignSystemDefinition struct {
	SchemaVersion string            `json:"schema_version"`
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Owner         DesignSystemOwner `json:"owner"`
	Tokens        []DesignToken     `json:"tokens"`
	Components    []DesignComponent `json:"components"`
	Assets        []string          `json:"assets"`
	ContrastPairs []ContrastPair    `json:"contrast_pairs"`
}

type DesignToken struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
	Ref   *string         `json:"ref,omitempty"`
}

type DesignComponent struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Documentation string          `json:"documentation"`
	TokenIDs      []string        `json:"token_ids"`
	States        []string        `json:"states"`
	Variants      []DesignVariant `json:"variants"`
	Examples      []string        `json:"examples"`
}

type DesignVariant struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	TokenIDs []string `json:"token_ids"`
}

type ContrastPair struct {
	ForegroundToken string  `json:"foreground_token"`
	BackgroundToken string  `json:"background_token"`
	MinRatio        float64 `json:"min_ratio"`
}

type DesignSystemSource struct {
	RepositoryID   string  `json:"repository_id"`
	OwnerLevel     string  `json:"owner_level"`
	OwnerID        string  `json:"owner_id"`
	DefinitionPath string  `json:"definition_path"`
	Revision       *string `json:"revision"`
}

type DesignSystemResponse struct {
	SchemaVersion          string                   `json:"schema_version"`
	ProjectID              string                   `json:"project_id"`
	CompanyID              string                   `json:"company_id"`
	Target                 string                   `json:"target"`
	AuthorityScope         string                   `json:"authority_scope"`
	Mode                   string                   `json:"mode"`
	Revision               *string                  `json:"revision"`
	Outcome                string                   `json:"outcome"`
	InventoryDigest        *string                  `json:"inventory_digest"`
	Items                  []DesignSystemDefinition `json:"items"`
	Diagnostics            []Diagnostic             `json:"diagnostics"`
	SelectedLevel          *string                  `json:"selected_level"`
	Source                 *DesignSystemSource      `json:"source"`
	DefinitionDigest       *string                  `json:"definition_digest"`
	ContrastPairsChecked   int                      `json:"contrast_pairs_checked"`
	DesignSystemValidation string                   `json:"design_system_validation"`
}

type designSystemDiagnosticList []Diagnostic

func (d *designSystemDiagnosticList) add(code, sourceID, artifactID, path, message, resolution string) {
	var source, artifact, target *string
	if sourceID != "" {
		source = stringPointer(sourceID)
	}
	if artifactID != "" {
		artifact = stringPointer(artifactID)
	}
	if path != "" {
		target = stringPointer(path)
	}
	*d = append(*d, Diagnostic{Code: code, SourceID: source, ArtifactID: artifact, Path: target, Message: message, Resolution: resolution})
}

func DecodeSourceBindings(data []byte) (SourceBindings, error) {
	if len(data) > MaxSourceBindingsBytes {
		return SourceBindings{}, ErrLimit
	}
	var result SourceBindings
	if err := DecodeStrict(data, &result); err != nil {
		return SourceBindings{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return SourceBindings{}, err
	}
	if len(fields["sources"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["sources"]), []byte("null")) {
		return SourceBindings{}, errors.New("source bindings object must be non-null")
	}
	return result, nil
}

// SelectSourceBinding applies whole-source precedence. A broken selected source is never skipped.
func SelectSourceBinding(bindings SourceBindings) (string, *SourceBinding) {
	if bindings.Sources.Project != nil {
		return "project", bindings.Sources.Project
	}
	if bindings.Sources.Company != nil {
		return "company", bindings.Sources.Company
	}
	if bindings.Sources.Default != nil {
		return "default", bindings.Sources.Default
	}
	return "", nil
}

func ValidateSourceBindings(bindings SourceBindings, mode string) []Diagnostic {
	var diagnostics designSystemDiagnosticList
	if bindings.SchemaVersion != SchemaVersion || bindings.ProjectID != DefaultProjectID || bindings.CompanyID != "belluga-solutions" {
		diagnostics.add("invalid_schema", "", "", "local-api/source-bindings.json", "source binding identity or schema version is invalid", "Use schema_version 1, project_id builder, and company_id belluga-solutions.")
	}
	if mode != "working-tree" && mode != "committed" {
		diagnostics.add("invalid_schema", "", "", "local-api/source-bindings.json", "source binding evaluation mode is invalid", "Choose working-tree or committed mode.")
	}
	for _, entry := range []struct {
		level string
		value *SourceBinding
	}{{"default", bindings.Sources.Default}, {"company", bindings.Sources.Company}, {"project", bindings.Sources.Project}} {
		if entry.value == nil {
			continue
		}
		binding := entry.value
		path := "local-api/source-bindings.json"
		if !validID(binding.RepositoryID) || binding.CheckoutRoot == "" || ValidateRelativePath(binding.CheckoutRoot) != nil || len([]byte(binding.CheckoutRoot)) > MaxPathBytes || hasGitSegment(binding.CheckoutRoot) || !strings.HasSuffix(binding.DefinitionPath, "/design-system.json") || ValidateRelativePath(binding.DefinitionPath) != nil || len([]byte(binding.DefinitionPath)) > MaxPathBytes {
			diagnostics.add("invalid_path", binding.RepositoryID, "", path, "source binding paths or repository identity are invalid", "Use a stable repository ID, a workspace-relative Git root, and a Git-relative path ending in design-system.json.")
		}
		if binding.Revision != nil && !isFullSHA(*binding.Revision) {
			diagnostics.add("invalid_schema", binding.RepositoryID, "", path, "source binding revision is not a full lowercase Git SHA", "Use null for working-tree evaluation or an explicit full resolved commit SHA.")
		}
		if mode == "working-tree" && binding.Revision != nil {
			diagnostics.add("invalid_schema", binding.RepositoryID, "", path, "working-tree source binding revision must be null", "Set revision to null for working-tree evaluation.")
		}
		if mode == "committed" && binding.Revision == nil {
			diagnostics.add("revision_unavailable", binding.RepositoryID, "", path, "committed source binding has no explicit full SHA", "Bind each supplied source revision to its exact full commit SHA.")
		}
	}
	return sortedDiagnostics(diagnostics)
}

func EvaluateDesignSystem(source Source, projectID, companyID, level string, binding SourceBinding) DesignSystemResponse {
	if projectID == "" {
		projectID = DefaultProjectID
	}
	response := DesignSystemResponse{
		SchemaVersion: SchemaVersion, ProjectID: projectID, CompanyID: companyID, Target: TargetDesignSystem,
		AuthorityScope: AuthorityScope, Mode: source.Mode(), Revision: source.Revision(), Outcome: "no_go",
		Items: []DesignSystemDefinition{}, Diagnostics: []Diagnostic{}, DesignSystemValidation: "not_evaluated",
	}
	selectedLevel := level
	response.SelectedLevel = &selectedLevel
	ownerLevel, ownerID := expectedOwner(level, projectID, companyID)
	response.Source = &DesignSystemSource{RepositoryID: binding.RepositoryID, OwnerLevel: ownerLevel, OwnerID: ownerID, DefinitionPath: binding.DefinitionPath, Revision: source.Revision()}
	var diagnostics designSystemDiagnosticList
	if ownerLevel == "" {
		diagnostics.add("invalid_schema", binding.RepositoryID, "", binding.DefinitionPath, "selected source level is invalid", "Select project, company, or default using whole-source precedence.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	if source.Mode() == "working-tree" && binding.Revision != nil {
		diagnostics.add("invalid_schema", binding.RepositoryID, "", binding.DefinitionPath, "working-tree source binding must have a null revision", "Set revision to null for working-tree evaluation.")
	}
	if source.Mode() == "committed" && (binding.Revision == nil || source.Revision() == nil || *binding.Revision != *source.Revision()) {
		diagnostics.add("revision_unavailable", binding.RepositoryID, "", binding.DefinitionPath, "committed source requires its exact explicit full SHA", "Bind this source to the full resolved commit SHA; implicit HEAD is not accepted.")
	}
	if source.Revision() != nil && binding.Revision != nil && *source.Revision() != *binding.Revision {
		diagnostics.add("revision_unavailable", binding.RepositoryID, "", binding.DefinitionPath, "resolved source revision does not match the explicit binding", "Use the exact revision resolved for this source binding.")
	}
	if len(diagnostics) != 0 {
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	root := strings.TrimSuffix(binding.DefinitionPath, "/design-system.json")
	entries, err := source.List(root)
	if err != nil {
		code := "read_failed"
		if errors.Is(err, ErrLimit) {
			code = "limit_exceeded"
		}
		if errors.Is(err, ErrMissingCatalog) || errors.Is(err, ErrNotExist) {
			code = "missing_file"
		}
		path := root
		var pathFailure *PathFailure
		if errors.As(err, &pathFailure) {
			path = pathFailure.Path
		}
		diagnostics.add(code, binding.RepositoryID, "", path, "selected Design System inventory could not be read", "Restore the selected managed Design System root and retry the complete evaluation.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	var definitionBytes []byte
	definitionBytes, mode, err := source.Read(binding.DefinitionPath, MaxManifestBytes)
	if err != nil || mode != ModeRegular {
		code := "missing_file"
		if errors.Is(err, ErrLimit) {
			code = "limit_exceeded"
		}
		if errors.Is(err, ErrUnsafePath) || (err == nil && mode != ModeRegular) {
			code = "unsupported_file"
		}
		diagnostics.add(code, binding.RepositoryID, "", binding.DefinitionPath, "selected Design System definition is absent or not a regular file", "Provide a regular design-system.json within the manifest size limit.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	var definition DesignSystemDefinition
	if err := DecodeStrict(definitionBytes, &definition); err != nil {
		diagnostics.add("invalid_schema", binding.RepositoryID, "", binding.DefinitionPath, err.Error(), "Correct design-system.json to the exact schema_version 1 contract.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	valid, managedRecords, checked := validateDesignSystem(source, binding.RepositoryID, root, binding.DefinitionPath, definition, definitionBytes, ownerLevel, ownerID, entries, &diagnostics)
	if !valid || len(diagnostics) != 0 {
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	if err := source.VerifyStable(); err != nil {
		diagnostics.add("read_failed", binding.RepositoryID, definition.ID, root, "selected Design System inventory changed during evaluation", "Retry against a stable source; use committed mode for an immutable snapshot.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	digest, err := InventoryDigest(binding.RepositoryID, managedRecords)
	if err != nil {
		diagnostics.add("invalid_path", binding.RepositoryID, definition.ID, root, "managed Design System inventory could not be encoded", "Correct managed paths and retry.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	definitionHash := sha256.Sum256(definitionBytes)
	response.Outcome = "go"
	response.InventoryDigest = stringPointer(digest)
	response.DefinitionDigest = stringPointer("sha256:" + hex.EncodeToString(definitionHash[:]))
	response.ContrastPairsChecked = checked
	response.DesignSystemValidation = "valid"
	response.Items = []DesignSystemDefinition{definition}
	return response
}

func expectedOwner(level, projectID, companyID string) (string, string) {
	switch level {
	case "project":
		return "project", projectID
	case "company":
		return "company", companyID
	case "default":
		return "default", "builder-platform"
	default:
		return "", ""
	}
}

func validateDesignSystem(source Source, repositoryID, root, definitionPath string, definition DesignSystemDefinition, definitionBytes []byte, ownerLevel, ownerID string, entries []Entry, diagnostics *designSystemDiagnosticList) (bool, []DigestRecord, int) {
	valid := true
	fail := func(code, path, message, resolution string) {
		diagnostics.add(code, repositoryID, definition.ID, path, message, resolution)
		valid = false
	}
	if definition.Owner.Level != ownerLevel || definition.Owner.ID != ownerID {
		fail("owner_mismatch", definitionPath, "definition owner does not match the selected binding slot", "Set owner.level and owner.id to the owner of the selected source slot.")
	}
	if definition.SchemaVersion != SchemaVersion || !validID(definition.ID) || !validDisplay(definition.Name, 160) || definition.Tokens == nil || definition.Components == nil || definition.Assets == nil || definition.ContrastPairs == nil {
		fail("invalid_schema", definitionPath, "definition identity, version, or required arrays are invalid", "Use schema_version 1, a stable identity, and non-null arrays for every required collection.")
	}
	if len(definition.Tokens) == 0 || len(definition.Tokens) > MaxTokens || len(definition.Components) > MaxComponents || len(definition.ContrastPairs) > MaxContrastPairs {
		fail("limit_exceeded", definitionPath, "token, component, or contrast-pair count is outside its contract", "Provide at least one token and stay within the declared item limits.")
	}
	tokenByID := make(map[string]DesignToken, len(definition.Tokens))
	for _, token := range definition.Tokens {
		if !validID(token.ID) {
			fail("invalid_schema", definitionPath, "token ID is invalid", "Use a stable lowercase token ID.")
		}
		if _, exists := tokenByID[token.ID]; exists {
			fail("duplicate_id", definitionPath, "token ID is duplicated", "Use unique token IDs within the Design System.")
		}
		tokenByID[token.ID] = token
		if token.Type != "color" && token.Type != "number" && token.Type != "string" {
			fail("invalid_schema", definitionPath, "token type is unsupported", "Use color, number, or string token types.")
		}
		if (token.Ref == nil) == (len(token.Value) == 0) {
			fail("invalid_schema", definitionPath, "token must define exactly one of value or ref", "Provide exactly one literal value or local token ref.")
		}
		if token.Ref != nil && !validID(*token.Ref) {
			fail("invalid_reference", definitionPath, "token alias target ID is invalid", "Reference a stable token ID in this selected Design System.")
		}
	}
	resolved, tokenValidity := resolveTokenValues(definition.Tokens, tokenByID, repositoryID, definition.ID, definitionPath, diagnostics)
	componentIDs := make(map[string]bool)
	want := map[string]bool{"design-system.json": true}
	for _, component := range definition.Components {
		if !validID(component.ID) || !validDisplay(component.Name, 160) {
			fail("invalid_schema", definitionPath, "component identity is invalid", "Use a stable ID and a 1–160 character name.")
		}
		if _, exists := componentIDs[component.ID]; exists {
			fail("duplicate_id", definitionPath, "component ID is duplicated", "Use unique component IDs within the Design System.")
		}
		componentIDs[component.ID] = true
		if component.States == nil || component.Variants == nil || component.Examples == nil || component.TokenIDs == nil {
			fail("invalid_schema", definitionPath, "component arrays must be present and non-null", "Provide token_ids, states, variants, and examples arrays, using [] when empty.")
		}
		if err := validateDesignPath(root, component.Documentation); err != nil {
			fail("invalid_path", definitionPath, "component documentation path is unsafe", "Use a safe path relative to the managed Design System root.")
		}
		addManagedPath(component.Documentation, definitionPath, root, want, diagnostics, repositoryID, definition.ID, &valid)
		validateTokenIDs(component.TokenIDs, tokenByID, definitionPath, repositoryID, definition.ID, diagnostics, &valid)
		if len(component.States) > MaxStatesPerComponent || len(component.Variants) > MaxVariantsPerComponent {
			fail("limit_exceeded", definitionPath, "component state or variant count exceeds its limit", "Keep each component within 64 states and 64 variants.")
		}
		stateIDs := make(map[string]bool)
		for _, state := range component.States {
			if !validID(state) || stateIDs[state] {
				fail("duplicate_id", definitionPath, "state ID is invalid or duplicated", "Use unique stable state IDs within each component.")
			}
			stateIDs[state] = true
		}
		variantIDs := make(map[string]bool)
		for _, variant := range component.Variants {
			if !validID(variant.ID) || !validDisplay(variant.Name, 160) || variantIDs[variant.ID] {
				fail("duplicate_id", definitionPath, "variant ID or name is invalid or duplicated", "Use unique stable variant IDs and valid names within each component.")
			}
			variantIDs[variant.ID] = true
			if variant.TokenIDs == nil {
				fail("invalid_schema", definitionPath, "variant token_ids must be a non-null array", "Provide token_ids, using [] when no tokens apply.")
			}
			validateTokenIDs(variant.TokenIDs, tokenByID, definitionPath, repositoryID, definition.ID, diagnostics, &valid)
		}
		exampleSeen := make(map[string]bool)
		for _, example := range component.Examples {
			if exampleSeen[example] {
				fail("duplicate_id", definitionPath, "example path is duplicated in a component", "List each example path once per component.")
			}
			exampleSeen[example] = true
			if err := validateDesignPath(root, example); err != nil {
				fail("invalid_path", definitionPath, "component example path is unsafe", "Use a safe path relative to the managed Design System root.")
			}
			addManagedPath(example, definitionPath, root, want, diagnostics, repositoryID, definition.ID, &valid)
		}
	}
	assetSeen := make(map[string]bool)
	for _, asset := range definition.Assets {
		if assetSeen[asset] {
			fail("duplicate_id", definitionPath, "asset path is duplicated", "List each asset path once.")
		}
		assetSeen[asset] = true
		if err := validateDesignPath(root, asset); err != nil {
			fail("invalid_path", definitionPath, "asset path is unsafe", "Use a safe path relative to the managed Design System root.")
		}
		if _, overlap := want[asset]; overlap {
			fail("invalid_reference", definitionPath, "asset overlaps a documentation or example file", "Keep assets disjoint from component documentation and examples.")
		}
		want[asset] = true
	}
	checked := validateContrastPairs(definition.ContrastPairs, tokenByID, resolved, tokenValidity, definitionPath, repositoryID, definition.ID, diagnostics, &valid)
	discovered := make(map[string]bool)
	var observedBytes int64
	limitsExceeded := false
	for _, entry := range entries {
		if entry.Path == root || !strings.HasPrefix(entry.Path, root+"/") {
			continue
		}
		relative := strings.TrimPrefix(entry.Path, root+"/")
		if entry.Mode == ModeDirectory {
			continue
		}
		if entry.Mode != ModeRegular {
			fail("unsupported_file", entry.Path, "managed Design System contains a symlink, special file, or nested Git object", "Replace unsupported nodes with declared regular files.")
			continue
		}
		discovered[relative] = true
		if entry.Size > MaxFileBytes {
			fail("limit_exceeded", entry.Path, "managed file exceeds the 2 MiB per-file limit", "Reduce each managed file to at most 2 MiB.")
			limitsExceeded = true
		}
		if entry.Size < 0 || entry.Size > MaxTotalBytes-observedBytes {
			observedBytes = MaxTotalBytes + 1
		} else {
			observedBytes += entry.Size
		}
	}
	for path := range want {
		if !discovered[path] {
			fail("missing_file", root+"/"+path, "declared managed file is missing", "Create the regular file or remove its definition reference.")
		}
	}
	for path := range discovered {
		if !want[path] {
			fail("undeclared_file", root+"/"+path, "managed file is not declared by design-system.json", "Declare the file as documentation, example, or asset, or move it outside the managed root.")
		}
	}
	nonemptyDirectories := make(map[string]bool)
	for relativeFile := range discovered {
		for separator := strings.LastIndex(relativeFile, "/"); separator >= 0; separator = strings.LastIndex(relativeFile[:separator], "/") {
			nonemptyDirectories[relativeFile[:separator]] = true
		}
	}
	for _, entry := range entries {
		if entry.Mode == ModeDirectory && strings.HasPrefix(entry.Path, root+"/") {
			relative := strings.TrimPrefix(entry.Path, root+"/")
			if !nonemptyDirectories[relative] {
				fail("undeclared_file", entry.Path, "managed Design System contains an empty orphan directory", "Remove empty directories; only directories leading to declared files are allowed.")
			}
		}
	}
	if len(discovered) > MaxFiles {
		fail("limit_exceeded", root, "managed Design System exceeds 2048 files", "Keep the selected managed root within 2048 files.")
		limitsExceeded = true
	}
	if observedBytes > MaxTotalBytes {
		fail("limit_exceeded", root, "managed Design System exceeds 64 MiB total", "Keep all managed files within 64 MiB total.")
		limitsExceeded = true
	}
	if limitsExceeded {
		return false, nil, checked
	}
	records := []DigestRecord{{SourceID: repositoryID, Path: definitionPath, Content: definitionBytes}}
	var total int64 = int64(len(definitionBytes))
	for path := range want {
		if path == "design-system.json" {
			continue
		}
		fullPath := root + "/" + path
		data, mode, err := source.Read(fullPath, MaxFileBytes)
		if err != nil || mode != ModeRegular {
			if errors.Is(err, ErrLimit) {
				fail("limit_exceeded", fullPath, "managed file exceeds the 2 MiB per-file limit", "Reduce the file to at most 2 MiB.")
			} else {
				fail("read_failed", fullPath, "managed file could not be read as a regular file", "Restore the regular file and retry.")
			}
			continue
		}
		total += int64(len(data))
		if total > MaxTotalBytes {
			fail("limit_exceeded", root, "managed Design System exceeds 64 MiB total", "Keep all managed files within 64 MiB total.")
			return false, nil, checked
		}
		records = append(records, DigestRecord{SourceID: repositoryID, Path: fullPath, Content: data})
	}
	return valid, records, checked
}

func addManagedPath(path, definitionPath, root string, want map[string]bool, diagnostics *designSystemDiagnosticList, sourceID, artifactID string, valid *bool) {
	if path == "" || path == "design-system.json" {
		diagnostics.add("invalid_reference", sourceID, artifactID, definitionPath, "managed support file overlaps the definition or is empty", "Use a distinct path relative to the Design System root.")
		*valid = false
		return
	}
	want[path] = true
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, root+"/") {
		diagnostics.add("invalid_path", sourceID, artifactID, definitionPath, "support path must be relative to the managed root", "Remove repository/root prefixes from the declared support path.")
		*valid = false
	}
}

func validateDesignPath(root, path string) error {
	if err := ValidateRelativePath(path); err != nil {
		return err
	}
	if len([]byte(path)) > MaxPathBytes || hasGitSegment(path) {
		return errors.New("invalid managed path")
	}
	if strings.HasPrefix(path, root+"/") {
		return errors.New("managed path includes repository prefix")
	}
	return nil
}

func validateTokenIDs(ids []string, tokens map[string]DesignToken, path, sourceID, artifactID string, diagnostics *designSystemDiagnosticList, valid *bool) {
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			diagnostics.add("duplicate_id", sourceID, artifactID, path, "token ID is duplicated in a component or variant", "List each token ID once.")
			*valid = false
		}
		seen[id] = true
		if _, ok := tokens[id]; !ok {
			diagnostics.add("invalid_reference", sourceID, artifactID, path, "component or variant references an unknown token", "Reference token IDs declared by the selected Design System.")
			*valid = false
		}
	}
}

type resolvedToken struct {
	value any
	color [3]float64
	valid bool
}

func resolveTokenValues(tokens []DesignToken, tokenByID map[string]DesignToken, sourceID, artifactID, path string, diagnostics *designSystemDiagnosticList) (map[string]resolvedToken, map[string]bool) {
	state := make(map[string]uint8, len(tokens))
	resolved := make(map[string]resolvedToken, len(tokens))
	valid := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if state[token.ID] == 2 {
			continue
		}
		chain := []string{}
		current := token.ID
		bad := false
		for {
			if state[current] == 2 {
				break
			}
			if state[current] == 1 {
				diagnostics.add("alias_cycle", sourceID, artifactID, path, "token aliases contain a cycle", "Break the reference cycle so every alias resolves to a literal token.")
				bad = true
				break
			}
			currentToken, ok := tokenByID[current]
			if !ok {
				diagnostics.add("invalid_reference", sourceID, artifactID, path, "token alias target does not exist", "Reference a token in the selected Design System.")
				bad = true
				break
			}
			state[current] = 1
			chain = append(chain, current)
			if currentToken.Ref == nil {
				literal, color, err := parseTokenLiteral(currentToken)
				if err != nil {
					diagnostics.add("invalid_schema", sourceID, artifactID, path, err.Error(), "Use a valid literal matching the declared token type.")
					resolved[current] = resolvedToken{}
					valid[current] = false
				} else {
					resolved[current] = resolvedToken{value: literal, color: color, valid: true}
					valid[current] = true
				}
				state[current] = 2
				break
			}
			targetToken, targetExists := tokenByID[*currentToken.Ref]
			if !targetExists {
				diagnostics.add("invalid_reference", sourceID, artifactID, path, "token alias target does not exist", "Reference a token declared by the selected Design System.")
				bad = true
				break
			}
			if currentToken.Type != targetToken.Type {
				diagnostics.add("invalid_reference", sourceID, artifactID, path, "token alias target has a different declared type", "Reference a token with the same declared type.")
				bad = true
				break
			}
			current = *currentToken.Ref
		}
		if bad {
			for _, id := range chain {
				state[id] = 2
				valid[id] = false
				resolved[id] = resolvedToken{}
			}
			continue
		}
		for i := len(chain) - 1; i >= 0; i-- {
			id := chain[i]
			currentToken := tokenByID[id]
			if currentToken.Ref == nil {
				continue
			}
			target := resolved[*currentToken.Ref]
			if !target.valid {
				valid[id] = false
				resolved[id] = resolvedToken{}
			} else {
				valid[id] = true
				resolved[id] = target
			}
			state[id] = 2
		}
	}
	return resolved, valid
}

func parseTokenLiteral(token DesignToken) (any, [3]float64, error) {
	var zero [3]float64
	decoder := json.NewDecoder(bytes.NewReader(token.Value))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, zero, errors.New("token literal is not valid JSON")
	}
	switch token.Type {
	case "color":
		text, ok := value.(string)
		if !ok || len(text) != 7 || text[0] != '#' {
			return nil, zero, errors.New("color token must be opaque #RRGGBB sRGB")
		}
		var channels [3]float64
		for i := 0; i < 3; i++ {
			parsed, err := hex.DecodeString(text[1+i*2 : 3+i*2])
			if err != nil || len(parsed) != 1 {
				return nil, zero, errors.New("color token must be opaque #RRGGBB sRGB")
			}
			channels[i] = float64(parsed[0]) / 255
		}
		return text, channels, nil
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return nil, zero, errors.New("number token must be a finite JSON number")
		}
		parsed, err := number.Float64()
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return nil, zero, errors.New("number token must be a finite JSON number")
		}
		return parsed, zero, nil
	case "string":
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) || len([]rune(text)) < 1 || len([]rune(text)) > 2000 {
			return nil, zero, errors.New("string token must contain 1–2000 Unicode characters")
		}
		return text, zero, nil
	default:
		return nil, zero, errors.New("token type is unsupported")
	}
}

func validateContrastPairs(pairs []ContrastPair, tokenByID map[string]DesignToken, resolved map[string]resolvedToken, tokenValid map[string]bool, path, sourceID, artifactID string, diagnostics *designSystemDiagnosticList, valid *bool) int {
	seen := make(map[string]bool)
	checked := 0
	for _, pair := range pairs {
		key := pair.ForegroundToken + "\x00" + pair.BackgroundToken
		if seen[key] {
			diagnostics.add("duplicate_id", sourceID, artifactID, path, "contrast pair is duplicated", "List each foreground/background pair once.")
			*valid = false
		}
		seen[key] = true
		foreground, foregroundOK := tokenByID[pair.ForegroundToken]
		background, backgroundOK := tokenByID[pair.BackgroundToken]
		if !foregroundOK || !backgroundOK || foreground.Type != "color" || background.Type != "color" || !tokenValid[pair.ForegroundToken] || !tokenValid[pair.BackgroundToken] || pair.MinRatio < 1 || pair.MinRatio > 21 || math.IsNaN(pair.MinRatio) || math.IsInf(pair.MinRatio, 0) {
			diagnostics.add("invalid_reference", sourceID, artifactID, path, "contrast pair must reference resolved color tokens and a min_ratio from 1 to 21", "Reference two resolved color tokens and declare a finite min_ratio in the inclusive 1–21 range.")
			*valid = false
			continue
		}
		checked++
		actual := contrastRatio(resolved[pair.ForegroundToken].color, resolved[pair.BackgroundToken].color)
		if actual < pair.MinRatio {
			diagnostics.add("contrast_failed", sourceID, artifactID, path, "declared color contrast pair is below its min_ratio", "Adjust the colors or lower the explicitly intended min_ratio; the check is not overall accessibility certification.")
			*valid = false
		}
	}
	return checked
}

func contrastRatio(first, second [3]float64) float64 {
	firstL := 0.2126*relativeChannel(first[0]) + 0.7152*relativeChannel(first[1]) + 0.0722*relativeChannel(first[2])
	secondL := 0.2126*relativeChannel(second[0]) + 0.7152*relativeChannel(second[1]) + 0.0722*relativeChannel(second[2])
	if firstL < secondL {
		firstL, secondL = secondL, firstL
	}
	return (firstL + 0.05) / (secondL + 0.05)
}

func relativeChannel(value float64) float64 {
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func isFullSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func designSystemDigest(definitionBytes []byte) string {
	hash := sha256.Sum256(definitionBytes)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func sortDiagnosticsInPlace(items []Diagnostic) {
	sort.Slice(items, func(i, j int) bool { return strings.Compare(items[i].Code, items[j].Code) < 0 })
}

func formatDesignSystemError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
