// Package artifact_catalog evaluates a bounded, local Prototype collection.
// It has no publication, rendering, or Design System resolution authority.
package artifact_catalog

import (
	"bytes"
	"crypto/sha256"
	"delphi-local-api/internal/codec"
	ks "delphi-local-api/internal/knowledge_status"
	"delphi-local-api/internal/source"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion          = "1"
	PrototypeSchemaVersion = "3"
	MaxPrototypes          = 256
	MaxScreens             = 256
	MaxStates              = 1024
	MaxFiles               = source.MaxFiles
	MaxFileBytes           = 2 << 20
	MaxTotalBytes          = 64 << 20
	MaxManifestBytes       = 256 << 10
	MaxPathBytes           = source.MaxPathBytes
	MaxTransitions         = 4096
	MaxScenarios           = 256
	MaxScenarioSteps       = 4096
	MaxRelated             = 64
	MaxApprovedReferences  = 256
	// Each admitted file can have at most MaxPathBytes/2 nested one-byte path
	// segments, so this caps directory nodes without an arbitrary file multiplier.
	MaxInventoryNodes   = source.MaxInventoryNodes
	AuthorityScope      = "local_structure_only"
	TargetPrototypes    = "prototypes"
	DesignSystemPending = "not_evaluated"
)

type FileMode = source.FileMode
type Entry = source.Entry
type Source = source.Source

const (
	ModeRegular   = source.ModeRegular
	ModeDirectory = source.ModeDirectory
	ModeSymlink   = source.ModeSymlink
	ModeSubmodule = source.ModeSubmodule
	ModeSpecial   = source.ModeSpecial
)

type Catalog struct {
	SchemaVersion string      `json:"schema_version"`
	ProjectID     string      `json:"project_id"`
	Prototypes    []Prototype `json:"prototypes"`
}

type Prototype struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Root        string  `json:"root"`
	Status      string  `json:"status"`
}

type Manifest struct {
	SchemaVersion   string           `json:"schema_version"`
	ID              string           `json:"id"`
	AuthoringMode   string           `json:"authoring_mode"`
	EntryPoint      string           `json:"entry_point"`
	Screens         []Screen         `json:"screens"`
	Sources         []string         `json:"sources"`
	Assets          []string         `json:"assets"`
	Transitions     []Transition     `json:"transitions"`
	Scenarios       []Scenario       `json:"scenarios"`
	Related         []Related        `json:"related"`
	DesignSystemRef *DesignSystemRef `json:"design_system_ref"`
}

type Screen struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Scope          *string `json:"scope"`
	DefaultStateID string  `json:"default_state_id"`
	States         []State `json:"states"`
}

type State struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	IdentifierImage    string              `json:"identifier_image"`
	ApprovedReferences []ApprovedReference `json:"approved_references"`
}

type ApprovedReference struct {
	ID               string `json:"id"`
	Image            string `json:"image"`
	ApprovalEvidence string `json:"approval_evidence"`
}

type Transition struct {
	FromScreenID string `json:"from_screen_id"`
	FromStateID  string `json:"from_state_id"`
	Action       string `json:"action"`
	ToScreenID   string `json:"to_screen_id"`
	ToStateID    string `json:"to_state_id"`
}

type Scenario struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Steps []ScenarioStep `json:"steps"`
}

type ScenarioStep struct {
	ScreenID string `json:"screen_id"`
	StateID  string `json:"state_id"`
}

type Related struct {
	Kind       string `json:"kind"`
	Path       string `json:"path,omitempty"`
	DocumentID string `json:"document_id,omitempty"`
}

type DesignSystemRef struct {
	ID            string `json:"id"`
	ContentDigest string `json:"content_digest"`
}

type Diagnostic struct {
	Code       string  `json:"code"`
	SourceID   *string `json:"source_id"`
	ArtifactID *string `json:"artifact_id"`
	Path       *string `json:"path"`
	Message    string  `json:"message"`
	Resolution string  `json:"resolution"`
}

type Item struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Description   *string      `json:"description"`
	Root          string       `json:"root"`
	Status        string       `json:"status"`
	AuthoringMode string       `json:"authoring_mode"`
	EntryPoint    string       `json:"entry_point"`
	Sources       []string     `json:"sources"`
	Assets        []string     `json:"assets"`
	Related       []Related    `json:"related"`
	AllowedPaths  []string     `json:"allowed_paths"`
	Screens       []Screen     `json:"screens"`
	Transitions   []Transition `json:"transitions"`
	Scenarios     []Scenario   `json:"scenarios"`
}

type Response struct {
	SchemaVersion          string       `json:"schema_version"`
	ProjectID              string       `json:"project_id"`
	Target                 string       `json:"target"`
	AuthorityScope         string       `json:"authority_scope"`
	Mode                   string       `json:"mode"`
	Revision               *string      `json:"revision"`
	Outcome                string       `json:"outcome"`
	InventoryDigest        *string      `json:"inventory_digest"`
	Items                  []Item       `json:"items"`
	Diagnostics            []Diagnostic `json:"diagnostics"`
	DesignSystemValidation string       `json:"design_system_validation"`
}

type DigestRecord struct {
	SourceID string
	Path     string
	Content  []byte
}

type diagnosticList []Diagnostic

func (d *diagnosticList) add(code, sourceID, artifactID, path, message, resolution string) {
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

func stringPointer(value string) *string { return &value }

func ValidateRelativePath(path string) error { return source.ValidateRelativePath(path) }

func Evaluate(source Source, projectID string) Response {
	return EvaluateWithKnowledge(source, projectID, nil)
}

// EvaluateWithKnowledge admits API document IDs only for an active Project.
// TODO and visual approval evidence remain regular files in the selected artifact source.
func EvaluateWithKnowledge(source Source, projectID string, knowledge *ks.CanonicalManifest) Response {
	response := Response{
		SchemaVersion: PrototypeSchemaVersion, ProjectID: projectID, Target: TargetPrototypes,
		AuthorityScope: AuthorityScope, Mode: source.Mode(), Revision: source.Revision(),
		Outcome: "no_go", Items: []Item{}, Diagnostics: []Diagnostic{},
		DesignSystemValidation: DesignSystemPending,
	}
	var diagnostics diagnosticList
	if knowledge != nil && (!knowledge.Active || knowledge.Validate(projectID) != nil) {
		diagnostics.add("invalid_reference", projectID, "", "prototypes/catalog.json", "canonical knowledge selection is invalid", "Reload this Project's active API manifest; do not use Markdown fallback.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	entries, err := source.List("prototypes")
	if err != nil {
		code := "read_failed"
		if errors.Is(err, ErrMissingCatalog) {
			code = "missing_catalog"
		} else if errors.Is(err, ErrLimit) {
			code = "limit_exceeded"
		} else if errors.Is(err, ErrUnsafePath) {
			code = "unsupported_file"
		}
		path := "prototypes/catalog.json"
		var pathFailure *PathFailure
		if errors.As(err, &pathFailure) {
			path = pathFailure.Path
		} else if errors.Is(err, ErrUnsafePath) {
			path = "prototypes"
		}
		message := "Prototype collection inventory could not be read safely"
		if code == "missing_catalog" {
			message = "Prototype catalog is absent from the selected artifact Git source"
		}
		if code == "limit_exceeded" {
			message = "Prototype collection inventory exceeds the bounded scan limit"
		}
		diagnostics.add(code, projectID, "", path, message, "Restore the required catalog and ensure the exact collection paths are readable regular files and directories.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	entryByPath := make(map[string]FileMode, len(entries))
	var observedFileCount int
	var observedTotalBytes int64
	for _, entry := range entries {
		entryByPath[entry.Path] = entry.Mode
		if entry.Mode == ModeRegular {
			observedFileCount++
			if entry.Size < 0 || entry.Size > MaxTotalBytes-observedTotalBytes {
				observedTotalBytes = MaxTotalBytes + 1
			} else {
				observedTotalBytes += entry.Size
			}
			if entry.Size > MaxFileBytes {
				diagnostics.add("limit_exceeded", projectID, "", entry.Path, "regular file exceeds the 2 MiB per-file limit", "Reduce the file to at most 2 MiB.")
			}
		}
	}
	if observedFileCount > MaxFiles || observedTotalBytes > MaxTotalBytes {
		diagnostics.add("limit_exceeded", projectID, "", "prototypes", "managed collection exceeds file or aggregate byte limit", "Keep the collection within 2048 files and 64 MiB total.")
	}
	if len(diagnostics) != 0 {
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	catalogBytes, mode, err := source.Read("prototypes/catalog.json", MaxManifestBytes)
	if err != nil || mode != ModeRegular {
		if err == nil {
			err = fmt.Errorf("catalog is not a regular file")
		}
		code := "read_failed"
		if errors.Is(err, ErrNotExist) {
			code = "missing_catalog"
		}
		if errors.Is(err, ErrLimit) {
			code = "limit_exceeded"
		}
		if errors.Is(err, ErrUnsafePath) {
			code = "unsupported_file"
		}
		message := "Prototype catalog could not be read as a regular file"
		if code == "missing_catalog" {
			message = "Prototype catalog is absent from the selected artifact Git source"
		}
		if code == "limit_exceeded" {
			message = "Prototype catalog exceeds its 256 KiB limit"
		}
		diagnostics.add(code, projectID, "", "prototypes/catalog.json", message, "Create a regular prototypes/catalog.json within the manifest size limit.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	if schemaVersion(catalogBytes) == "2" {
		diagnostics.add("unsupported_schema", projectID, "", "prototypes/catalog.json", "Prototype schema v2 is no longer admitted", prototypeV3MigrationResolution)
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	var catalog Catalog
	if err := DecodeStrict(catalogBytes, &catalog); err != nil {
		diagnostics.add("invalid_schema", projectID, "", "prototypes/catalog.json", err.Error(), "Correct the catalog to the exact schema_version 3 fields and valid UTF-8 JSON.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	if catalog.SchemaVersion != PrototypeSchemaVersion || catalog.ProjectID != projectID || catalog.Prototypes == nil {
		diagnostics.add("invalid_schema", projectID, "", "prototypes/catalog.json", "schema_version, project_id, or prototypes is invalid", "Use schema_version \"3\", the bound project ID, and a non-null prototypes array.")
	}
	if len(catalog.Prototypes) > MaxPrototypes {
		diagnostics.add("limit_exceeded", projectID, "", "prototypes/catalog.json", "prototype count exceeds 256", "Reduce the catalog to at most 256 Prototypes.")
	}
	if entryByPath["prototypes"] != ModeDirectory && len(catalog.Prototypes) > 0 {
		diagnostics.add("missing_file", projectID, "", "prototypes", "nonempty catalog has no Prototype root directory", "Create each registered Prototype directory under prototypes/.")
	}
	for path, fileMode := range entryByPath {
		if path == "prototypes/catalog.json" {
			continue
		}
		if strings.Count(path, "/") == 1 && fileMode != ModeDirectory {
			diagnostics.add("unsupported_file", projectID, "", path, "only catalog.json and Prototype directories are allowed at the collection root", "Move drafts outside prototypes/ and register each managed child directory.")
		}
	}
	registered := make(map[string]string)
	seenIDs := make(map[string]bool)
	items := make([]Item, 0, len(catalog.Prototypes))
	digestRecords := []DigestRecord{{SourceID: projectID, Path: "prototypes/catalog.json", Content: catalogBytes}}
	aggregateBytes := int64(len(catalogBytes))
	fileCount := 1
	for _, prototype := range catalog.Prototypes {
		if !validID(prototype.ID) || seenIDs[prototype.ID] {
			code := "invalid_schema"
			if seenIDs[prototype.ID] {
				code = "duplicate_id"
			}
			diagnostics.add(code, projectID, prototype.ID, "prototypes/catalog.json", "Prototype ID is invalid or duplicated", "Use unique stable IDs matching [a-z][a-z0-9-]{0,63}.")
		}
		seenIDs[prototype.ID] = true
		if !validDisplay(prototype.Name, 160) || (prototype.Description != nil && (!utf8.ValidString(*prototype.Description) || len([]rune(*prototype.Description)) > 2000)) {
			diagnostics.add("invalid_schema", projectID, prototype.ID, "prototypes/catalog.json", "Prototype name or description exceeds its contract", "Use a 1–160 character name and a null or at most 2000 character description.")
		}
		if prototype.Status != "active" && prototype.Status != "archived" {
			diagnostics.add("invalid_schema", projectID, prototype.ID, "prototypes/catalog.json", "Prototype lifecycle status is missing or unsupported", "Set status to active or archived.")
		}
		if err := validatePrototypeRoot(prototype.Root); err != nil {
			diagnostics.add("invalid_path", projectID, prototype.ID, prototype.Root, err.Error(), "Set root to exactly one direct child directory of prototypes/.")
			continue
		}
		if _, exists := registered[prototype.Root]; exists {
			diagnostics.add("duplicate_id", projectID, prototype.ID, prototype.Root, "Prototype root overlaps another registered root", "Assign every Prototype a unique direct-child root.")
		} else {
			registered[prototype.Root] = prototype.ID
		}
		if entryByPath[prototype.Root] != ModeDirectory {
			diagnostics.add("missing_file", projectID, prototype.ID, prototype.Root, "registered Prototype root is missing or not a directory", "Create the registered directory or remove the stale catalog record.")
			continue
		}
		manifestPath := prototype.Root + "/prototype.json"
		manifestBytes, manifestMode, readErr := source.Read(manifestPath, MaxManifestBytes)
		if readErr != nil || manifestMode != ModeRegular {
			if readErr == nil {
				readErr = errors.New("manifest is not a regular file")
			}
			code := "missing_file"
			if errors.Is(readErr, ErrLimit) {
				code = "limit_exceeded"
			}
			if errors.Is(readErr, ErrUnsafePath) || manifestMode != ModeRegular {
				code = "unsupported_file"
			}
			message := "Prototype manifest is absent or could not be read as a regular file"
			if code == "limit_exceeded" {
				message = "Prototype manifest exceeds its 256 KiB limit"
			}
			if code == "unsupported_file" {
				message = "Prototype manifest is not a regular file"
			}
			diagnostics.add(code, projectID, prototype.ID, manifestPath, message, "Add a regular prototype.json with the complete schema_version 3 contract.")
			continue
		}
		if schemaVersion(manifestBytes) == "2" {
			diagnostics.add("unsupported_schema", projectID, prototype.ID, manifestPath, "Prototype schema v2 is no longer admitted", prototypeV3MigrationResolution)
			continue
		}
		if !hasNonNullScenarios(manifestBytes) {
			diagnostics.add("invalid_schema", projectID, prototype.ID, manifestPath, "required scenarios array is missing or null", "Use schema_version \"3\" and provide a non-null scenarios array; use [] when there are no authored scenarios.")
			continue
		}
		var manifest Manifest
		if err := DecodeStrict(manifestBytes, &manifest); err != nil {
			diagnostics.add("invalid_schema", projectID, prototype.ID, manifestPath, err.Error(), "Correct the manifest to the exact Prototype schema with no unknown or duplicate keys.")
			continue
		}
		valid, records, count, bytesRead := validateManifest(source, projectID, prototype, manifest, entries, &diagnostics, knowledge)
		fileCount += count
		aggregateBytes += bytesRead
		digestRecords = append(digestRecords, records...)
		if valid {
			items = append(items, Item{ID: prototype.ID, Name: prototype.Name, Description: prototype.Description, Root: prototype.Root, Status: prototype.Status, AuthoringMode: manifest.AuthoringMode, EntryPoint: manifest.EntryPoint, Sources: manifest.Sources, Assets: manifest.Assets, Related: manifest.Related, AllowedPaths: allowedPaths(prototype.Root, manifest), Screens: manifest.Screens, Transitions: manifest.Transitions, Scenarios: manifest.Scenarios})
		}
	}
	for path, mode := range entryByPath {
		if path == "prototypes" || path == "prototypes/catalog.json" || strings.Count(path, "/") > 1 {
			continue
		}
		if mode == ModeDirectory {
			if _, ok := registered[path]; !ok {
				diagnostics.add("unregistered_artifact", projectID, "", path, "Prototype directory is not registered", "Add a stable catalog record or move this draft outside prototypes/.")
			}
		}
	}
	if observedFileCount > MaxFiles || fileCount > MaxFiles || observedTotalBytes > MaxTotalBytes || aggregateBytes > MaxTotalBytes {
		diagnostics.add("limit_exceeded", projectID, "", "prototypes", "managed collection exceeds file or aggregate byte limit", "Keep the collection within 2048 files and 64 MiB total.")
	}
	response.Diagnostics = sortedDiagnostics(diagnostics)
	if len(response.Diagnostics) != 0 {
		return response
	}
	if err := source.VerifyStable(); err != nil {
		response.Diagnostics = []Diagnostic{{Code: "read_failed", SourceID: stringPointer(projectID), Path: stringPointer("prototypes"), Message: "Artifact Git inventory or a read file changed during evaluation", Resolution: "Retry against a stable artifact checkout; use committed mode for an immutable local snapshot."}}
		return response
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	digest, err := InventoryDigest(projectID, digestRecords)
	if err != nil {
		response.Diagnostics = []Diagnostic{{Code: "invalid_path", SourceID: stringPointer(projectID), Path: stringPointer("prototypes"), Message: err.Error(), Resolution: "Correct the inventory paths and retry."}}
		return response
	}
	response.Outcome = "go"
	response.Items = items
	response.InventoryDigest = stringPointer(digest)
	return response
}

// allowedPaths returns the exact Prototype-owned files admitted by the
// manifest. Related evidence is intentionally excluded from viewer payloads.
func allowedPaths(root string, manifest Manifest) []string {
	paths := make([]string, 0, 1+len(manifest.Sources)+len(manifest.Assets))
	paths = append(paths, root+"/prototype.json")
	for _, path := range manifest.Sources {
		paths = append(paths, root+"/"+path)
	}
	for _, path := range manifest.Assets {
		paths = append(paths, root+"/"+path)
	}
	sort.Strings(paths)
	return paths
}

func validateManifest(source Source, projectID string, prototype Prototype, manifest Manifest, entries []Entry, diagnostics *diagnosticList, knowledge *ks.CanonicalManifest) (bool, []DigestRecord, int, int64) {
	valid := true
	fail := func(code, path, message, resolution string) {
		diagnostics.add(code, projectID, prototype.ID, path, message, resolution)
		valid = false
	}
	manifestPath := prototype.Root + "/prototype.json"
	if manifest.SchemaVersion != PrototypeSchemaVersion || manifest.ID != prototype.ID || manifest.Screens == nil || manifest.Sources == nil || manifest.Assets == nil || manifest.Transitions == nil || manifest.Scenarios == nil || manifest.Related == nil {
		fail("invalid_schema", manifestPath, "manifest identity, version, or required arrays are invalid", "Match catalog ID, use schema_version \"3\", and provide every required array (empty arrays are allowed; scenarios may be []).")
	}
	if manifest.AuthoringMode != "image_first" && manifest.AuthoringMode != "design_system_first" {
		fail("invalid_schema", manifestPath, "Prototype authoring mode is missing or unsupported", "Set authoring_mode to image_first or design_system_first.")
	}
	if manifest.DesignSystemRef != nil && (!validID(manifest.DesignSystemRef.ID) || !validDigest(manifest.DesignSystemRef.ContentDigest)) {
		fail("invalid_reference", manifestPath, "Design System reference identity or digest is malformed", "Use a stable Design System ID and full lowercase sha256 digest, or null; resolution is evaluated separately.")
	}
	if err := validateOwnedPath(prototype.Root, manifest.EntryPoint); err != nil {
		fail("invalid_path", manifestPath, err.Error(), "Use a safe Prototype-root-relative entry_point.")
	}
	invalidCollectionCount := len(manifest.Screens) == 0 || len(manifest.Screens) > MaxScreens || len(manifest.Sources) == 0 || len(manifest.Transitions) > MaxTransitions || len(manifest.Scenarios) > MaxScenarios || len(manifest.Related) > MaxRelated
	screenIDs := make(map[string]bool)
	screenPaths := make(map[string]bool)
	stateIDsByScreen := make(map[string]map[string]bool)
	approvedReferences := make([]ApprovedReference, 0)
	stateCount := 0
	for _, screen := range manifest.Screens {
		if !validID(screen.ID) || screenIDs[screen.ID] {
			fail("duplicate_id", manifestPath, "Screen ID is invalid or duplicated", "Use unique stable Screen IDs within this Prototype.")
		}
		screenIDs[screen.ID] = true
		if !validDisplay(screen.Name, 160) || screen.Scope != nil || screen.States == nil || len(screen.States) == 0 {
			fail("invalid_schema", manifestPath, "Screen name, explicit null scope, or required states are invalid", "Use a 1–160 character Screen name, scope:null, and at least one state.")
		}
		if err := validateOwnedPath(prototype.Root, screen.Path); err != nil {
			fail("invalid_path", manifestPath, err.Error(), "Use an exact safe relative path inside this Prototype root.")
		}
		if !strings.HasSuffix(strings.ToLower(screen.Path), ".html") {
			fail("invalid_schema", manifestPath, "Screen path is not an HTML source", "Set each Screen path to its declared .html source file.")
		}
		if screenPaths[screen.Path] {
			fail("duplicate_id", ownedDiagnosticPath(prototype.Root, screen.Path), "Screen path is duplicated", "Give each Screen a unique source path.")
		}
		screenPaths[screen.Path] = true
		stateIDsByScreen[screen.ID] = make(map[string]bool)
		defaultFound := false
		for _, state := range screen.States {
			stateCount++
			if !validID(state.ID) || stateIDsByScreen[screen.ID][state.ID] {
				fail("duplicate_id", manifestPath, "State ID is invalid or duplicated in this Screen", "Use unique stable State IDs within each Screen.")
			}
			stateIDsByScreen[screen.ID][state.ID] = true
			if state.ID == screen.DefaultStateID {
				defaultFound = true
			}
			if !validDisplay(state.Name, 160) || !validImagePath(state.IdentifierImage) || state.ApprovedReferences == nil {
				fail("invalid_schema", manifestPath, "State identity image or approved-reference array is invalid", "Use a 1–160 character name, a declared raster identifier image and a non-null approved_references array.")
			}
			referenceIDs := make(map[string]bool)
			for _, reference := range state.ApprovedReferences {
				if !validID(reference.ID) || referenceIDs[reference.ID] {
					fail("duplicate_id", manifestPath, "approved visual reference ID is invalid or duplicated in this State", "Use unique stable reference IDs within each State.")
				}
				referenceIDs[reference.ID] = true
			}
			approvedReferences = append(approvedReferences, state.ApprovedReferences...)
		}
		if !validID(screen.DefaultStateID) || !defaultFound {
			fail("invalid_reference", manifestPath, "Screen default_state_id does not identify one of its states", "Set default_state_id to a State ID declared by the same Screen.")
		}
	}
	if stateCount == 0 || stateCount > MaxStates {
		invalidCollectionCount = true
	}
	if len(approvedReferences) > MaxApprovedReferences {
		invalidCollectionCount = true
	}
	if invalidCollectionCount {
		fail("limit_exceeded", manifestPath, "Screen, State, source, transition, or evidence count is outside its contract", "Provide a Screen and source, at least one State per Screen, and stay within the declared collection limits.")
	}
	sourceSet, assetSet := make(map[string]bool), make(map[string]bool)
	for _, path := range manifest.Sources {
		if err := validateOwnedPath(prototype.Root, path); err != nil {
			fail("invalid_path", manifestPath, err.Error(), "Use an exact safe relative path inside this Prototype root.")
			continue
		}
		if sourceSet[path] {
			fail("duplicate_id", ownedDiagnosticPath(prototype.Root, path), "source path is duplicated", "List each source path once.")
		}
		sourceSet[path] = true
	}
	for _, path := range manifest.Assets {
		if err := validateOwnedPath(prototype.Root, path); err != nil {
			fail("invalid_path", manifestPath, err.Error(), "Use an exact safe relative path inside this Prototype root.")
			continue
		}
		if assetSet[path] || sourceSet[path] {
			fail("duplicate_id", ownedDiagnosticPath(prototype.Root, path), "asset path is duplicated or overlaps a source", "Keep sources and assets unique and disjoint.")
		}
		assetSet[path] = true
	}
	if !sourceSet[manifest.EntryPoint] {
		fail("invalid_reference", manifestPath, "entry_point is not a declared source", "Declare the Prototype-root-relative entry_point in sources.")
	}
	for _, screen := range manifest.Screens {
		if !sourceSet[screen.Path] {
			fail("invalid_reference", ownedDiagnosticPath(prototype.Root, screen.Path), "Screen path is not a declared source", "Add each Prototype-root-relative Screen path to sources.")
		}
	}
	if !screenPaths[manifest.EntryPoint] {
		fail("invalid_reference", manifestPath, "entry_point does not identify a declared Screen", "Set entry_point to one of the Screen paths so the default entry and viewer selection agree.")
	}
	for _, screen := range manifest.Screens {
		for _, state := range screen.States {
			if !assetSet[state.IdentifierImage] {
				fail("invalid_reference", manifestPath, "State identifier image is not a declared asset", "Declare each State identifier_image in assets.")
			}
			for _, reference := range state.ApprovedReferences {
				if !validID(reference.ID) || !validImagePath(reference.Image) {
					fail("invalid_schema", manifestPath, "approved visual reference identity or image path is invalid", "Use a stable reference ID and a declared raster image path.")
				}
				if !assetSet[reference.Image] {
					fail("invalid_reference", manifestPath, "Approved reference image is not a declared asset", "Declare each approved reference image in assets.")
				}
				if err := ValidateRelativePath(reference.ApprovalEvidence); err != nil || len([]byte(reference.ApprovalEvidence)) > MaxPathBytes {
					fail("invalid_path", reference.ApprovalEvidence, "approved reference evidence path is unsafe", "Use a safe artifact-repository-root-relative evidence path also declared in related.")
				}
			}
		}
	}
	relatedPaths := make(map[string]bool)
	relatedSeen := make(map[string]bool)
	for _, ref := range manifest.Related {
		if ref.Kind != "todo" && ref.Kind != "decision" && ref.Kind != "documentation" {
			fail("invalid_reference", ref.Path, "related reference kind is unsupported", "Use todo, decision, or documentation and treat the link as evidence only.")
			continue
		}
		if ref.DocumentID != "" {
			if knowledge == nil || ref.Path != "" || (ref.Kind != "documentation" && ref.Kind != "decision") {
				fail("invalid_reference", manifestPath, "document_id requires this Project's active API knowledge and no path", "Use document_id only for canonical documentation/decision references in an activated Project; keep TODO/evidence paths.")
				continue
			}
			if _, err := knowledge.ResolveDocumentID(ref.DocumentID); err != nil {
				fail("invalid_reference", manifestPath, "document_id is absent or ambiguous in the active Project", "Choose an exact unambiguous document ID from this Project's current API manifest.")
			}
			key := ref.Kind + "\x00id:" + ref.DocumentID
			if relatedSeen[key] {
				fail("duplicate_id", manifestPath, "related document reference is duplicated", "List each kind/document_id pair once.")
			}
			relatedSeen[key] = true
			continue
		}
		if knowledge != nil && ks.IsCanonicalMarkdown(ref.Path) {
			fail("invalid_reference", ref.Path, "canonical Markdown references are retired for this activated Project", "Use the exact document_id from this Project's active API manifest.")
			continue
		}
		if err := ValidateRelativePath(ref.Path); err != nil || len([]byte(ref.Path)) > MaxPathBytes {
			fail("invalid_path", ref.Path, "related reference path is unsafe", "Use an artifact-repository-root-relative regular-file path without .git segments or aliases.")
			continue
		}
		key := ref.Kind + "\x00" + ref.Path
		if relatedSeen[key] {
			fail("duplicate_id", ref.Path, "related reference pair is duplicated", "List each kind/path pair once.")
		}
		relatedSeen[key] = true
		relatedPaths[ref.Path] = true
		if !invalidCollectionCount {
			err := source.CheckRegular(ref.Path, MaxFileBytes)
			if err == nil {
				continue
			}
			if errors.Is(err, ErrLimit) {
				fail("limit_exceeded", ref.Path, "related evidence file exceeds 2 MiB", "Reference a regular evidence file no larger than 2 MiB.")
			} else {
				fail("invalid_reference", ref.Path, "related evidence file is missing, unsafe, or not regular", "Reference an existing regular artifact repository file in the selected revision.")
			}
		}
	}
	for _, reference := range approvedReferences {
		if !relatedPaths[reference.ApprovalEvidence] {
			fail("invalid_reference", manifestPath, "approved visual reference evidence is not declared in related", "Add the exact approval_evidence path to related so Builder can admit the evidence document safely.")
		}
	}
	transitionSeen := make(map[string]bool)
	for _, transition := range manifest.Transitions {
		fromExists := stateIDsByScreen[transition.FromScreenID][transition.FromStateID]
		toExists := stateIDsByScreen[transition.ToScreenID][transition.ToStateID]
		key := transition.FromScreenID + "\x00" + transition.FromStateID + "\x00" + transition.Action + "\x00" + transition.ToScreenID + "\x00" + transition.ToStateID
		if !fromExists || !toExists || !validDisplay(transition.Action, 160) || transitionSeen[key] {
			fail("invalid_reference", manifestPath, "State transition endpoint, action, or identity is invalid", "Reference existing Screen/State pairs and give each transition a unique action label.")
		}
		transitionSeen[key] = true
	}
	scenarioIDs := make(map[string]bool, len(manifest.Scenarios))
	scenarioStepCount := 0
	for _, scenario := range manifest.Scenarios {
		if !validID(scenario.ID) || scenarioIDs[scenario.ID] {
			fail("duplicate_id", manifestPath, "Scenario ID is invalid or duplicated", "Use unique stable Scenario IDs within this Prototype.")
		}
		scenarioIDs[scenario.ID] = true
		if !validDisplay(scenario.Name, 160) || scenario.Steps == nil || len(scenario.Steps) == 0 {
			fail("invalid_schema", manifestPath, "Scenario name or nonempty steps array is invalid", "Use a 1–160 character name and a non-null, nonempty ordered steps array.")
		}
		scenarioStepCount += len(scenario.Steps)
		for _, step := range scenario.Steps {
			if !stateIDsByScreen[step.ScreenID][step.StateID] {
				fail("invalid_reference", manifestPath, "Scenario step does not identify a declared Screen/State pair", "Set each step's screen_id and state_id to an existing pair in this Prototype; repeated and non-transition-adjacent pairs are allowed.")
			}
		}
	}
	if scenarioStepCount > MaxScenarioSteps {
		fail("limit_exceeded", manifestPath, "Scenario steps exceed the 4096-step Prototype limit", "Reduce the total steps across all scenarios to at most 4096.")
	}
	want := make(map[string]bool)
	want["prototype.json"] = true
	for p := range sourceSet {
		want[p] = true
	}
	for p := range assetSet {
		want[p] = true
	}
	discovered := make(map[string]bool)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, prototype.Root+"/") {
			continue
		}
		relative := strings.TrimPrefix(entry.Path, prototype.Root+"/")
		if entry.Mode == ModeDirectory {
			continue
		}
		if entry.Mode != ModeRegular {
			fail("unsupported_file", entry.Path, "Prototype contains a symlink, special file, or nested repository object", "Replace unsupported nodes with declared regular files.")
			continue
		}
		discovered[relative] = true
	}
	for path := range want {
		if !discovered[path] {
			fail("missing_file", prototype.Root+"/"+path, "declared managed file is missing", "Create the declared regular file or remove its manifest reference.")
		}
	}
	for path := range discovered {
		if !want[path] {
			fail("undeclared_file", prototype.Root+"/"+path, "managed Prototype file is absent from its manifest", "Declare the file as a source or asset, or move it outside the managed root.")
		}
	}
	nonemptyDirectories := make(map[string]bool)
	for relativeFile := range discovered {
		for separator := strings.LastIndex(relativeFile, "/"); separator >= 0; separator = strings.LastIndex(relativeFile[:separator], "/") {
			nonemptyDirectories[relativeFile[:separator]] = true
		}
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, prototype.Root+"/") || entry.Mode != ModeDirectory {
			continue
		}
		relativeDir := strings.TrimPrefix(entry.Path, prototype.Root+"/")
		if !nonemptyDirectories[relativeDir] {
			fail("undeclared_file", entry.Path, "Prototype contains an empty orphan directory", "Remove empty directories; only directories leading to declared files are allowed.")
		}
	}
	var records []DigestRecord
	var total int64
	count := 0
	for path := range want {
		artifactPath := prototype.Root + "/" + path
		data, mode, err := source.Read(artifactPath, MaxFileBytes)
		if err != nil || mode != ModeRegular {
			fail("read_failed", artifactPath, "managed file could not be read as a regular file", "Restore the regular file and rerun the complete evaluation.")
			continue
		}
		count++
		total += int64(len(data))
		if len(data) > MaxFileBytes {
			fail("limit_exceeded", path, "file exceeds 2 MiB", "Reduce the file size to at most 2 MiB.")
		}
		if screenPaths[path] {
			if construct := reviewerChromeConstruct(data); construct != "" {
				fail("reviewer_chrome", artifactPath, "Screen contains Builder-owned reviewer chrome: "+construct, "TEACH: remove the reviewer panel and Scenario selector from Project-authored Screen HTML; keep review controls in Builder-owned chrome, then rerun Prototype status.")
			}
		}
		records = append(records, DigestRecord{SourceID: projectID, Path: artifactPath, Content: data})
	}
	return valid, records, count, total
}

// reviewerChromeConstruct recognizes only a reviewer panel containing its
// Scenario selector. Input is already limited to MaxFileBytes by Source.Read.
func reviewerChromeConstruct(html []byte) string {
	lowerHTML := asciiLowerCopy(html)
	index := 0
	reviewerAsides := 0
	templateDepth := 0
	var asideStack []bool
	for index < len(html) {
		relative := bytes.IndexByte(html[index:], '<')
		if relative < 0 {
			break
		}
		start := index + relative
		if bytes.HasPrefix(html[start:], []byte("<!--")) {
			end := bytes.Index(html[start+4:], []byte("-->"))
			if end < 0 {
				break
			}
			index = start + 4 + end + 3
			continue
		}
		tag, next, ok := scanHTMLTag(html, start)
		if !ok {
			index = next
			continue
		}
		if tag.closing {
			if tag.name == "template" && templateDepth > 0 {
				templateDepth--
			} else if templateDepth == 0 && tag.name == "aside" && len(asideStack) > 0 {
				last := len(asideStack) - 1
				if asideStack[last] {
					reviewerAsides--
				}
				asideStack = asideStack[:last]
			}
			index = next
			continue
		}
		if tag.name == "template" {
			templateDepth++
			index = next
			continue
		}
		if tag.name == "script" || tag.name == "style" || tag.name == "textarea" {
			close := findRawTextClose(lowerHTML, tag.name, next)
			if close < 0 {
				break
			}
			index = close
			continue
		}
		if templateDepth > 0 {
			index = next
			continue
		}
		if tag.name == "aside" {
			reviewer := hasClassToken(tag.attrs["class"], "review")
			asideStack = append(asideStack, reviewer)
			if reviewer {
				reviewerAsides++
			}
		}
		if tag.name == "select" && reviewerAsides > 0 {
			if tag.attrs["id"] == "scenario" {
				return `<aside class="review"> containing <select id="scenario">`
			}
		}
		index = next
	}
	return ""
}

type scannedHTMLTag struct {
	name    string
	attrs   map[string]string
	closing bool
}

func scanHTMLTag(html []byte, start int) (scannedHTMLTag, int, bool) {
	end := findHTMLTagEnd(html, start+1)
	if end < 0 {
		return scannedHTMLTag{}, len(html), false
	}
	inside := html[start+1 : end]
	tag := scannedHTMLTag{}
	if len(inside) > 0 && inside[0] == '/' {
		tag.closing = true
		inside = inside[1:]
	}
	nameEnd := 0
	for nameEnd < len(inside) && !htmlSpace(inside[nameEnd]) && inside[nameEnd] != '/' {
		nameEnd++
	}
	if nameEnd == 0 {
		return scannedHTMLTag{}, end + 1, false
	}
	tag.name = strings.ToLower(string(inside[:nameEnd]))
	if !tag.closing && (tag.name == "aside" || tag.name == "select") {
		tag.attrs = parseHTMLAttributes(inside[nameEnd:])
	}
	return tag, end + 1, true
}

func findHTMLTagEnd(html []byte, index int) int {
	var quote byte
	for index < len(html) {
		if quote != 0 {
			if html[index] == quote {
				quote = 0
			}
		} else if html[index] == '\'' || html[index] == '"' {
			quote = html[index]
		} else if html[index] == '>' {
			return index
		}
		index++
	}
	return -1
}

func parseHTMLAttributes(data []byte) map[string]string {
	attributes := make(map[string]string)
	for index := 0; index < len(data); {
		for index < len(data) && htmlSpace(data[index]) {
			index++
		}
		start := index
		for index < len(data) && !htmlSpace(data[index]) && data[index] != '=' && data[index] != '/' {
			index++
		}
		if index == start {
			index++
			continue
		}
		name := strings.ToLower(string(data[start:index]))
		for index < len(data) && htmlSpace(data[index]) {
			index++
		}
		value := ""
		if index < len(data) && data[index] == '=' {
			index++
			for index < len(data) && htmlSpace(data[index]) {
				index++
			}
			if index < len(data) && (data[index] == '\'' || data[index] == '"') {
				quote := data[index]
				index++
				start = index
				for index < len(data) && data[index] != quote {
					index++
				}
				value = string(data[start:index])
				if index < len(data) {
					index++
				}
			} else {
				start = index
				for index < len(data) && !htmlSpace(data[index]) {
					if data[index] == '/' {
						end := index + 1
						for end < len(data) && htmlSpace(data[end]) {
							end++
						}
						if end == len(data) {
							break
						}
					}
					index++
				}
				value = string(data[start:index])
			}
		}
		if _, exists := attributes[name]; !exists {
			attributes[name] = stdhtml.UnescapeString(value)
		}
	}
	return attributes
}

func htmlSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\f'
}

func asciiLowerCopy(data []byte) []byte {
	lower := append([]byte(nil), data...)
	for index, value := range lower {
		if value >= 'A' && value <= 'Z' {
			lower[index] = value + ('a' - 'A')
		}
	}
	return lower
}

func hasClassToken(value, token string) bool {
	start := 0
	for index := 0; index <= len(value); index++ {
		if index < len(value) && !htmlSpace(value[index]) {
			continue
		}
		if value[start:index] == token {
			return true
		}
		start = index + 1
	}
	return false
}

func findRawTextClose(lowerHTML []byte, name string, start int) int {
	needle := []byte("</" + name)
	for start < len(lowerHTML) {
		relative := bytes.Index(lowerHTML[start:], needle)
		if relative < 0 {
			return -1
		}
		candidate := start + relative
		afterName := candidate + len(needle)
		if afterName == len(lowerHTML) || htmlSpace(lowerHTML[afterName]) || lowerHTML[afterName] == '/' || lowerHTML[afterName] == '>' {
			return candidate
		}
		start = afterName
	}
	return -1
}

func validatePrototypeRoot(path string) error {
	if err := ValidateRelativePath(path); err != nil {
		return err
	}
	if len([]byte(path)) > MaxPathBytes {
		return errors.New("Prototype root path exceeds 1024 UTF-8 bytes")
	}
	if strings.Count(path, "/") != 1 || !strings.HasPrefix(path, "prototypes/") || path == "prototypes/catalog.json" {
		return errors.New("root must be exactly one child directory of prototypes/")
	}
	return nil
}

func validateOwnedPath(_ string, path string) error {
	if err := ValidateRelativePath(path); err != nil {
		return err
	}
	if len([]byte(path)) > MaxPathBytes {
		return errors.New("Prototype-root-relative path exceeds 1024 UTF-8 bytes")
	}
	return nil
}

func ownedDiagnosticPath(root, path string) string {
	if validateOwnedPath(root, path) != nil {
		return root + "/prototype.json"
	}
	return root + "/" + path
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func validDisplay(value string, max int) bool {
	return utf8.ValidString(value) && len([]rune(value)) >= 1 && len([]rune(value)) <= max
}

func validImagePath(value string) bool {
	if validateOwnedPath("", value) != nil {
		return false
	}
	extension := value[strings.LastIndex(value, ".")+1:]
	switch strings.ToLower(extension) {
	case "png", "jpg", "jpeg", "webp", "avif":
		return true
	default:
		return false
	}
}

func validDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func InventoryDigest(sourceID string, records []DigestRecord) (string, error) {
	ordered := append([]DigestRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].SourceID != ordered[j].SourceID {
			return ordered[i].SourceID < ordered[j].SourceID
		}
		return ordered[i].Path < ordered[j].Path
	})
	hash := sha256.New()
	_, _ = hash.Write([]byte("builder-artifact-inventory-v1\x00"))
	var size [4]byte
	previous := ""
	for _, record := range ordered {
		if record.SourceID == "" {
			record.SourceID = sourceID
		}
		if record.SourceID != sourceID {
			return "", errors.New("inventory record source ID does not match bound project")
		}
		if err := ValidateRelativePath(record.Path); err != nil {
			return "", err
		}
		key := record.SourceID + "\x00" + record.Path
		if key == previous {
			return "", errors.New("inventory contains duplicate source/path records")
		}
		previous = key
		for _, value := range [][]byte{[]byte(record.SourceID), []byte(record.Path)} {
			if uint64(len(value)) > uint64(^uint32(0)) {
				return "", errors.New("inventory field exceeds encoding limit")
			}
			binary.BigEndian.PutUint32(size[:], uint32(len(value)))
			_, _ = hash.Write(size[:])
			_, _ = hash.Write(value)
		}
		contentDigest := sha256.Sum256(record.Content)
		_, _ = hash.Write(contentDigest[:])
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type objectRule struct {
	fields   map[string]*objectRule
	required []string
}

var catalogRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "project_id": nil, "prototypes": prototypeRule,
}, required: []string{"schema_version", "project_id", "prototypes"}}

var prototypeRule = &objectRule{fields: map[string]*objectRule{"id": nil, "name": nil, "description": nil, "root": nil, "status": nil}, required: []string{"id", "name", "description", "root", "status"}}

var manifestRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "id": nil, "authoring_mode": nil, "entry_point": nil, "screens": nil, "sources": nil, "assets": nil, "transitions": nil, "scenarios": nil, "related": nil,
	"design_system_ref": {fields: map[string]*objectRule{"id": nil, "content_digest": nil}, required: []string{"id", "content_digest"}},
}, required: []string{"schema_version", "id", "authoring_mode", "entry_point", "screens", "sources", "assets", "transitions", "scenarios", "related", "design_system_ref"}}

var sourceBindingRule = &objectRule{fields: map[string]*objectRule{
	"repository_id": nil, "checkout_root": nil, "revision": nil, "definition_path": nil,
}, required: []string{"repository_id", "checkout_root", "revision", "definition_path"}}

var sourceBindingsRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "project_id": nil, "company_id": nil,
	"sources": {fields: map[string]*objectRule{"default": sourceBindingRule, "company": sourceBindingRule, "project": sourceBindingRule}, required: []string{"default", "company", "project"}},
}, required: []string{"schema_version", "project_id", "company_id", "sources"}}

var designSystemRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "id": nil, "name": nil,
	"presentation": {fields: map[string]*objectRule{
		"default_context": nil,
		"contexts": {fields: map[string]*objectRule{
			"id": nil,
			"roles": {fields: map[string]*objectRule{
				"canvas": nil, "surface": nil, "text": nil, "muted_text": nil, "border": nil,
				"accent": nil, "accent_text": nil, "on_accent": nil, "focus": nil,
				"success_text": nil, "font_family": nil, "body_size": nil,
				"display_size": nil, "card_radius": nil, "control_radius": nil,
				"line_height": nil,
			}, required: []string{"canvas", "surface", "text", "muted_text", "border", "accent", "accent_text", "on_accent", "focus", "success_text", "font_family", "body_size", "display_size", "card_radius", "control_radius"}},
		}, required: []string{"id", "roles"}},
	}, required: []string{"default_context", "contexts"}},
	"owner":  {fields: map[string]*objectRule{"level": nil, "id": nil}, required: []string{"level", "id"}},
	"tokens": {fields: map[string]*objectRule{"id": nil, "type": nil, "value": nil, "ref": nil}, required: []string{"id", "type"}},
	"components": {fields: map[string]*objectRule{
		"id": nil, "name": nil, "documentation": nil, "token_ids": nil, "states": nil, "examples": nil,
		"variants": {fields: map[string]*objectRule{"id": nil, "name": nil, "token_ids": nil}, required: []string{"id", "name", "token_ids"}},
	}, required: []string{"id", "name", "documentation", "token_ids", "states", "variants", "examples"}},
	"assets":         nil,
	"contrast_pairs": {fields: map[string]*objectRule{"foreground_token": nil, "background_token": nil, "min_ratio": nil}, required: []string{"foreground_token", "background_token", "min_ratio"}},
}, required: []string{"schema_version", "id", "name", "owner", "tokens", "components", "assets", "contrast_pairs"}}

func init() {
	manifestRule.fields["screens"] = &objectRule{fields: map[string]*objectRule{"id": nil, "name": nil, "path": nil, "scope": nil, "default_state_id": nil, "states": stateRule}, required: []string{"id", "name", "path", "scope", "default_state_id", "states"}}
	manifestRule.fields["transitions"] = &objectRule{fields: map[string]*objectRule{"from_screen_id": nil, "from_state_id": nil, "action": nil, "to_screen_id": nil, "to_state_id": nil}, required: []string{"from_screen_id", "from_state_id", "action", "to_screen_id", "to_state_id"}}
	manifestRule.fields["scenarios"] = &objectRule{fields: map[string]*objectRule{"id": nil, "name": nil, "steps": {fields: map[string]*objectRule{"screen_id": nil, "state_id": nil}, required: []string{"screen_id", "state_id"}}}, required: []string{"id", "name", "steps"}}
	manifestRule.fields["related"] = &objectRule{fields: map[string]*objectRule{"kind": nil, "path": nil, "document_id": nil}, required: []string{"kind"}}
}

var stateRule = &objectRule{fields: map[string]*objectRule{"id": nil, "name": nil, "identifier_image": nil, "approved_references": approvedReferenceRule}, required: []string{"id", "name", "identifier_image", "approved_references"}}

var approvedReferenceRule = &objectRule{fields: map[string]*objectRule{"id": nil, "image": nil, "approval_evidence": nil}, required: []string{"id", "image", "approval_evidence"}}

const prototypeV3MigrationResolution = "Run fresh Prototype status; adapt every offending v2 catalog/manifest, including archived candidates, to schema_version 3; confirm the complete collection is go; then publish the changes to the bound artifact Git branch. Local status does not establish Builder admission; check its separate reader/index after that cutover."

func schemaVersion(data []byte) string {
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return ""
	}
	return envelope.SchemaVersion
}

func hasNonNullScenarios(data []byte) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil {
		return false
	}
	value, ok := envelope["scenarios"]
	return ok && len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func DecodeStrict(data []byte, target any) error {
	if err := codec.ValidateJSON(data, 0); err != nil {
		return err
	}
	var generic any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&generic); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}
	var rule *objectRule
	switch target.(type) {
	case *Catalog:
		rule = catalogRule
	case *Manifest:
		rule = manifestRule
	case *SourceBindings:
		rule = sourceBindingsRule
	case *DesignSystemDefinition:
		rule = designSystemRule
	default:
		return errors.New("strict JSON target has no schema")
	}
	if _, ok := target.(*DesignSystemDefinition); ok {
		if err := validatePresentationRoleTypes(generic); err != nil {
			return err
		}
	}
	if err := validateKeys(generic, rule); err != nil {
		return err
	}
	if _, ok := target.(*Manifest); ok {
		if root, ok := generic.(map[string]any); ok {
			if related, ok := root["related"].([]any); ok {
				for _, raw := range related {
					row, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					path, hasPath := row["path"]
					document, hasDocument := row["document_id"]
					if hasPath == hasDocument {
						return errors.New("related requires exactly one of path or document_id")
					}
					value := path
					if hasDocument {
						value = document
					}
					if text, ok := value.(string); !ok || text == "" {
						return errors.New("related reference must be a nonempty string")
					}
				}
			}
		}
	}
	if _, ok := target.(*DesignSystemDefinition); ok {
		if err := validateDesignSystemTokenFieldPresence(generic); err != nil {
			return err
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func validatePresentationRoleTypes(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	presentation, ok := root["presentation"].(map[string]any)
	if !ok {
		return nil
	}
	contexts, ok := presentation["contexts"].([]any)
	if !ok {
		return nil
	}
	for index, entry := range contexts {
		context, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		roles, ok := context["roles"].(map[string]any)
		if !ok {
			continue
		}
		for key, raw := range roles {
			if _, ok := raw.(string); !ok {
				return fmt.Errorf("presentation.contexts[%d].roles.%s must be a token ID string", index, key)
			}
		}
	}
	return nil
}

func validateDesignSystemTokenFieldPresence(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return errors.New("Design System definition must be an object")
	}
	tokens, ok := root["tokens"].([]any)
	if !ok {
		return nil // Typed decoding and evaluator report malformed or null arrays.
	}
	for index, rawToken := range tokens {
		token, ok := rawToken.(map[string]any)
		if !ok {
			continue // Typed decoding reports the element type.
		}
		_, hasValue := token["value"]
		_, hasRef := token["ref"]
		if hasValue == hasRef {
			return fmt.Errorf("token at index %d must contain exactly one of value or ref", index)
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	return codec.ValidateJSON(data, 0)
}

func validateKeys(value any, rule *objectRule) error {
	return validateKeysAt(value, rule, "")
}

func validateKeysAt(value any, rule *objectRule, path string) error {
	if rule == nil {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			field := key
			if path != "" {
				field = path + "." + key
			}
			next, ok := rule.fields[key]
			if !ok {
				return fmt.Errorf("unknown or non-canonical JSON field %q", field)
			}
			if next != nil {
				if err := validateKeysAt(child, next, field); err != nil {
					return err
				}
			}
		}
		for _, key := range rule.required {
			if _, ok := typed[key]; !ok {
				field := key
				if path != "" {
					field = path + "." + key
				}
				return fmt.Errorf("required JSON field %q is missing", field)
			}
		}
	case []any:
		for index, child := range typed {
			if err := validateKeysAt(child, rule, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func sortedDiagnostics(input []Diagnostic) []Diagnostic {
	result := make([]Diagnostic, len(input))
	copy(result, input)
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if deref(a.SourceID) != deref(b.SourceID) {
			return deref(a.SourceID) < deref(b.SourceID)
		}
		if deref(a.Path) != deref(b.Path) {
			return deref(a.Path) < deref(b.Path)
		}
		return deref(a.ArtifactID) < deref(b.ArtifactID)
	})
	return result
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var (
	ErrNotExist       = source.ErrNotExist
	ErrMissingCatalog = source.ErrNotExist
)
