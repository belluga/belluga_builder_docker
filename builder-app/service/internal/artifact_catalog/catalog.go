// Package artifact_catalog evaluates a bounded, local Prototype collection.
// It has no publication, rendering, or Design System resolution authority.
package artifact_catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion    = "1"
	DefaultProjectID = "builder"
	MaxPrototypes    = 256
	MaxScreens       = 256
	MaxFiles         = 2048
	MaxFileBytes     = 2 << 20
	MaxTotalBytes    = 64 << 20
	MaxManifestBytes = 256 << 10
	MaxPathBytes     = 1024
	MaxLinks         = 4096
	MaxRelated       = 64
	// Each admitted file can have at most MaxPathBytes/2 nested one-byte path
	// segments, so this caps directory nodes without an arbitrary file multiplier.
	MaxInventoryNodes   = MaxFiles*(MaxPathBytes/2+1) + 1
	AuthorityScope      = "local_structure_only"
	TargetPrototypes    = "prototypes"
	DesignSystemPending = "not_evaluated"
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
}

type Manifest struct {
	SchemaVersion   string           `json:"schema_version"`
	ID              string           `json:"id"`
	EntryPoint      string           `json:"entry_point"`
	Screens         []Screen         `json:"screens"`
	Sources         []string         `json:"sources"`
	Assets          []string         `json:"assets"`
	Links           []Link           `json:"links"`
	Related         []Related        `json:"related"`
	DesignSystemRef *DesignSystemRef `json:"design_system_ref"`
}

type Screen struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	Scope *string `json:"scope"`
}

type Link struct {
	FromScreenID string `json:"from_screen_id"`
	ToScreenID   string `json:"to_screen_id"`
}

type Related struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
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
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Root        string   `json:"root"`
	EntryPoint  string   `json:"entry_point"`
	Screens     []Screen `json:"screens"`
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

type FileMode uint8

const (
	ModeRegular FileMode = iota + 1
	ModeDirectory
	ModeSymlink
	ModeSubmodule
	ModeSpecial
)

type Entry struct {
	Path string
	Mode FileMode
	Size int64
}

// Source is the bounded file/tree boundary shared with future artifact types.
type Source interface {
	Mode() string
	Revision() *string
	List(prefix string) ([]Entry, error)
	Read(path string, limit int64) ([]byte, FileMode, error)
	CheckRegular(path string, limit int64) error
	VerifyStable() error
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

func Evaluate(source Source, projectID string) Response {
	if projectID == "" {
		projectID = DefaultProjectID
	}
	response := Response{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Target: TargetPrototypes,
		AuthorityScope: AuthorityScope, Mode: source.Mode(), Revision: source.Revision(),
		Outcome: "no_go", Items: []Item{}, Diagnostics: []Diagnostic{},
		DesignSystemValidation: DesignSystemPending,
	}
	var diagnostics diagnosticList
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
			message = "Prototype catalog is absent from the bound Foundation state"
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
			message = "Prototype catalog is absent from the bound Foundation state"
		}
		if code == "limit_exceeded" {
			message = "Prototype catalog exceeds its 256 KiB limit"
		}
		diagnostics.add(code, projectID, "", "prototypes/catalog.json", message, "Create a regular prototypes/catalog.json within the manifest size limit.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	var catalog Catalog
	if err := DecodeStrict(catalogBytes, &catalog); err != nil {
		diagnostics.add("invalid_schema", projectID, "", "prototypes/catalog.json", err.Error(), "Correct the catalog to the exact schema_version 1 fields and valid UTF-8 JSON.")
		response.Diagnostics = sortedDiagnostics(diagnostics)
		return response
	}
	if catalog.SchemaVersion != SchemaVersion || catalog.ProjectID != projectID || catalog.Prototypes == nil {
		diagnostics.add("invalid_schema", projectID, "", "prototypes/catalog.json", "schema_version, project_id, or prototypes is invalid", "Use schema_version \"1\", the bound project ID, and a non-null prototypes array.")
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
			diagnostics.add(code, projectID, prototype.ID, manifestPath, message, "Add a regular prototype.json with the complete schema_version 1 contract.")
			continue
		}
		var manifest Manifest
		if err := DecodeStrict(manifestBytes, &manifest); err != nil {
			diagnostics.add("invalid_schema", projectID, prototype.ID, manifestPath, err.Error(), "Correct the manifest to the exact Prototype schema with no unknown or duplicate keys.")
			continue
		}
		valid, records, count, bytesRead := validateManifest(source, projectID, prototype, manifest, entries, &diagnostics)
		fileCount += count
		aggregateBytes += bytesRead
		digestRecords = append(digestRecords, records...)
		if valid {
			items = append(items, Item{ID: prototype.ID, Name: prototype.Name, Description: prototype.Description, Root: prototype.Root, EntryPoint: manifest.EntryPoint, Screens: manifest.Screens})
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
		response.Diagnostics = []Diagnostic{{Code: "read_failed", SourceID: stringPointer(projectID), Path: stringPointer("prototypes"), Message: "Foundation inventory or a read file changed during evaluation", Resolution: "Retry against a stable working tree; use committed mode for an immutable snapshot."}}
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

func validateManifest(source Source, projectID string, prototype Prototype, manifest Manifest, entries []Entry, diagnostics *diagnosticList) (bool, []DigestRecord, int, int64) {
	valid := true
	fail := func(code, path, message, resolution string) {
		diagnostics.add(code, projectID, prototype.ID, path, message, resolution)
		valid = false
	}
	manifestPath := prototype.Root + "/prototype.json"
	if manifest.SchemaVersion != SchemaVersion || manifest.ID != prototype.ID || manifest.Screens == nil || manifest.Sources == nil || manifest.Assets == nil || manifest.Links == nil || manifest.Related == nil {
		fail("invalid_schema", manifestPath, "manifest identity, version, or required arrays are invalid", "Match catalog ID, use schema_version \"1\", and provide every required array (empty arrays are allowed).")
	}
	if manifest.DesignSystemRef != nil && (!validID(manifest.DesignSystemRef.ID) || !validDigest(manifest.DesignSystemRef.ContentDigest)) {
		fail("invalid_reference", manifestPath, "Design System reference identity or digest is malformed", "Use a stable Design System ID and full lowercase sha256 digest, or null; resolution is evaluated separately.")
	}
	if err := validateOwnedPath(prototype.Root, manifest.EntryPoint); err != nil {
		fail("invalid_path", manifestPath, err.Error(), "Use a safe Prototype-root-relative entry_point.")
	}
	invalidCollectionCount := len(manifest.Screens) == 0 || len(manifest.Screens) > MaxScreens || len(manifest.Sources) == 0 || len(manifest.Links) > MaxLinks || len(manifest.Related) > MaxRelated
	if invalidCollectionCount {
		fail("limit_exceeded", manifestPath, "Screen, source, link, or related-reference count is outside its contract", "Provide at least one Screen and source and stay within each declared collection limit.")
	}
	screenIDs := make(map[string]bool)
	screenPaths := make(map[string]bool)
	for _, screen := range manifest.Screens {
		if !validID(screen.ID) || screenIDs[screen.ID] {
			fail("duplicate_id", manifestPath, "Screen ID is invalid or duplicated", "Use unique stable Screen IDs within this Prototype.")
		}
		screenIDs[screen.ID] = true
		if !validDisplay(screen.Name, 160) || screen.Scope != nil {
			fail("invalid_schema", manifestPath, "Screen name is invalid or scope is not explicitly null", "Use a 1–160 character Screen name and scope:null; do not invent scope keys.")
		}
		if err := validateOwnedPath(prototype.Root, screen.Path); err != nil {
			fail("invalid_path", manifestPath, err.Error(), "Use an exact safe relative path inside this Prototype root.")
		}
		if screenPaths[screen.Path] {
			fail("duplicate_id", ownedDiagnosticPath(prototype.Root, screen.Path), "Screen path is duplicated", "Give each Screen a unique source path.")
		}
		screenPaths[screen.Path] = true
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
	linkSeen := make(map[string]bool)
	for _, link := range manifest.Links {
		key := link.FromScreenID + "\x00" + link.ToScreenID
		if !screenIDs[link.FromScreenID] || !screenIDs[link.ToScreenID] || linkSeen[key] {
			fail("invalid_reference", manifestPath, "link endpoint is missing or pair is duplicated", "Link only existing Screens and list each directed pair once.")
		}
		linkSeen[key] = true
	}
	relatedSeen := make(map[string]bool)
	for _, ref := range manifest.Related {
		if ref.Kind != "todo" && ref.Kind != "decision" && ref.Kind != "documentation" {
			fail("invalid_reference", ref.Path, "related reference kind is unsupported", "Use todo, decision, or documentation and treat the link as evidence only.")
			continue
		}
		if err := ValidateRelativePath(ref.Path); err != nil || len([]byte(ref.Path)) > MaxPathBytes || hasGitSegment(ref.Path) {
			fail("invalid_path", ref.Path, "related reference path is unsafe", "Use a Foundation-root-relative regular-file path without .git segments or aliases.")
			continue
		}
		key := ref.Kind + "\x00" + ref.Path
		if relatedSeen[key] {
			fail("duplicate_id", ref.Path, "related reference pair is duplicated", "List each kind/path pair once.")
		}
		relatedSeen[key] = true
		if !invalidCollectionCount {
			err := source.CheckRegular(ref.Path, MaxFileBytes)
			if err == nil {
				continue
			}
			if errors.Is(err, ErrLimit) {
				fail("limit_exceeded", ref.Path, "related evidence file exceeds 2 MiB", "Reference a regular evidence file no larger than 2 MiB.")
			} else {
				fail("invalid_reference", ref.Path, "related evidence file is missing, unsafe, or not regular", "Reference an existing regular Foundation file in the selected revision.")
			}
		}
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
		foundationPath := prototype.Root + "/" + path
		data, mode, err := source.Read(foundationPath, MaxFileBytes)
		if err != nil || mode != ModeRegular {
			fail("read_failed", foundationPath, "managed file could not be read as a regular file", "Restore the regular file and rerun the complete evaluation.")
			continue
		}
		count++
		total += int64(len(data))
		if len(data) > MaxFileBytes {
			fail("limit_exceeded", path, "file exceeds 2 MiB", "Reduce the file size to at most 2 MiB.")
		}
		records = append(records, DigestRecord{SourceID: projectID, Path: foundationPath, Content: data})
	}
	return valid, records, count, total
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

func ValidateRelativePath(path string) error {
	if !utf8.ValidString(path) || path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") {
		return errors.New("path is not a valid relative UTF-8 slash path")
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return errors.New("path contains a control character")
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path contains an empty, dot, or dot-dot segment")
		}
		if segment == ".git" {
			return errors.New("path contains a reserved .git segment")
		}
	}
	return nil
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
func hasGitSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == ".git" {
			return true
		}
	}
	return false
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

var prototypeRule = &objectRule{fields: map[string]*objectRule{"id": nil, "name": nil, "description": nil, "root": nil}, required: []string{"id", "name", "description", "root"}}

var manifestRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "id": nil, "entry_point": nil, "screens": nil, "sources": nil, "assets": nil, "links": nil, "related": nil,
	"design_system_ref": {fields: map[string]*objectRule{"id": nil, "content_digest": nil}, required: []string{"id", "content_digest"}},
}, required: []string{"schema_version", "id", "entry_point", "screens", "sources", "assets", "links", "related", "design_system_ref"}}

var sourceBindingRule = &objectRule{fields: map[string]*objectRule{
	"repository_id": nil, "checkout_root": nil, "revision": nil, "definition_path": nil,
}, required: []string{"repository_id", "checkout_root", "revision", "definition_path"}}

var sourceBindingsRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "project_id": nil, "company_id": nil,
	"sources": {fields: map[string]*objectRule{"default": sourceBindingRule, "company": sourceBindingRule, "project": sourceBindingRule}, required: []string{"default", "company", "project"}},
}, required: []string{"schema_version", "project_id", "company_id", "sources"}}

var designSystemRule = &objectRule{fields: map[string]*objectRule{
	"schema_version": nil, "id": nil, "name": nil,
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
	manifestRule.fields["screens"] = &objectRule{fields: map[string]*objectRule{"id": nil, "name": nil, "path": nil, "scope": nil}, required: []string{"id", "name", "path", "scope"}}
	manifestRule.fields["links"] = &objectRule{fields: map[string]*objectRule{"from_screen_id": nil, "to_screen_id": nil}, required: []string{"from_screen_id", "to_screen_id"}}
	manifestRule.fields["related"] = &objectRule{fields: map[string]*objectRule{"kind": nil, "path": nil}, required: []string{"kind", "path"}}
}

func DecodeStrict(data []byte, target any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not valid UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
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
	if err := validateKeys(generic, rule); err != nil {
		return err
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = true
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("malformed JSON object")
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("malformed JSON array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := readValue(); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func validateKeys(value any, rule *objectRule) error {
	if rule == nil {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			next, ok := rule.fields[key]
			if !ok {
				return fmt.Errorf("unknown or non-canonical JSON field %q", key)
			}
			if next != nil {
				if err := validateKeys(child, next); err != nil {
					return err
				}
			}
		}
		for _, key := range rule.required {
			if _, ok := typed[key]; !ok {
				return fmt.Errorf("required JSON field %q is missing", key)
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateKeys(child, rule); err != nil {
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
	ErrNotExist       = errors.New("file does not exist")
	ErrMissingCatalog = errors.New("Prototype collection is missing")
)
