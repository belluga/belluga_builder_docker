// Package knowledge_status evaluates Builder's local, non-authoritative
// Landing and Roadmap review signals.
package knowledge_status

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SchemaVersion     = "1"
	ProjectID         = "builder"
	MaxInputs         = 2048
	MaxDocumentBytes  = 2 << 20
	MaxAggregateBytes = 64 << 20
	MaxMarkerBytes    = 64 << 10
)

type Input struct {
	Path          string `json:"path"`
	ContentDigest string `json:"content_digest,omitempty"`
}
type Review struct {
	ReviewedSourceDigest   string `json:"reviewed_source_digest"`
	ReviewedDocumentDigest string `json:"reviewed_document_digest"`
	ReviewedPacketDigest   string `json:"reviewed_packet_digest"`
	ReviewedRevision       string `json:"reviewed_revision"`
	ReviewedBy             string `json:"reviewed_by"`
	ReviewReference        string `json:"review_reference"`
}
type Marker struct {
	SchemaVersion string  `json:"schema_version"`
	ProjectID     string  `json:"project_id"`
	Landing       *Review `json:"landing"`
	Roadmap       *Review `json:"roadmap"`
}
type TargetStatus struct {
	Status                 string  `json:"status"`
	ReasonCode             string  `json:"reason_code"`
	ObservedSourceDigest   *string `json:"observed_source_digest"`
	ReviewedSourceDigest   *string `json:"reviewed_source_digest"`
	ObservedDocumentDigest *string `json:"observed_document_digest"`
	ReviewedDocumentDigest *string `json:"reviewed_document_digest"`
}
type Response struct {
	SchemaVersion  string       `json:"schema_version"`
	ProjectID      string       `json:"project_id"`
	AuthorityScope string       `json:"authority_scope"`
	Landing        TargetStatus `json:"landing"`
	Roadmap        TargetStatus `json:"roadmap"`
}
type Packet struct {
	Target         string  `json:"target"`
	SourceRevision string  `json:"source_revision"`
	SourceDigest   string  `json:"source_digest"`
	OutputPath     string  `json:"output_path"`
	DocumentDigest string  `json:"document_digest"`
	Inputs         []Input `json:"inputs"`
	PacketDigest   string  `json:"packet_digest"`
}

type Snapshot struct {
	Digest    string
	Inputs    []Input
	BytesRead int64
}

func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HashRecords uses the v1 byte-level contract. Empty ContentDigest means a
// path-only record, as used for the Roadmap inventory.
func HashRecords(inputs []Input) (string, error) {
	if len(inputs) > MaxInputs {
		return "", errors.New("input limit exceeded")
	}
	ordered := append([]Input(nil), inputs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	h := sha256.New()
	previous := ""
	for _, input := range ordered {
		if err := ValidatePath(input.Path); err != nil {
			return "", err
		}
		if input.Path == previous {
			return "", fmt.Errorf("duplicate normalized path %q", input.Path)
		}
		previous = input.Path
		pathBytes := []byte(input.Path)
		if len(pathBytes) > int(^uint32(0)) {
			return "", errors.New("path too long")
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(pathBytes)))
		h.Write(size[:])
		h.Write(pathBytes)
		if input.ContentDigest != "" {
			decoded, err := decodeDigest(input.ContentDigest)
			if err != nil {
				return "", err
			}
			h.Write(decoded)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func ValidatePath(path string) error {
	if !utf8.ValidString(path) || path == "" || strings.ContainsAny(path, "\\\x00") || strings.HasPrefix(path, "/") || filepath.IsAbs(path) {
		return fmt.Errorf("invalid path")
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return fmt.Errorf("control character in path")
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid path segment")
		}
	}
	return nil
}

// IsMarkdownPath recognizes the conventional Markdown extensions used by both
// local inventories and committed review packets.
func IsMarkdownPath(path string) bool {
	ext := filepath.Ext(path)
	return strings.EqualFold(ext, ".md") || strings.EqualFold(ext, ".markdown")
}

func decodeDigest(value string) ([]byte, error) {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return nil, errors.New("invalid sha256 digest")
	}
	b, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	if err != nil || hex.EncodeToString(b) != strings.TrimPrefix(value, "sha256:") {
		return nil, errors.New("invalid sha256 digest")
	}
	return b, nil
}

func LoadMarker(path string) (Marker, bool, error) {
	data, err := ReadBounded(path, MaxMarkerBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, nil
	}
	if err != nil {
		return Marker{}, true, err
	}
	if !utf8.Valid(data) {
		return Marker{}, true, errors.New("marker is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Marker{}, true, err
	}
	var marker Marker
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return Marker{}, true, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Marker{}, true, errors.New("marker has trailing JSON data")
	}
	return marker, true, nil
}

// ValidateMarker checks semantic marker requirements after strict JSON parsing.
func ValidateMarker(marker Marker) error {
	if marker.SchemaVersion != SchemaVersion || marker.ProjectID != ProjectID {
		return errors.New("marker schema version or project ID is invalid")
	}
	for _, review := range []*Review{marker.Landing, marker.Roadmap} {
		if review == nil {
			continue
		}
		if _, err := decodeDigest(review.ReviewedSourceDigest); err != nil {
			return errors.New("marker reviewed source digest is invalid")
		}
		if _, err := decodeDigest(review.ReviewedDocumentDigest); err != nil {
			return errors.New("marker reviewed document digest is invalid")
		}
		if _, err := decodeDigest(review.ReviewedPacketDigest); err != nil || review.ReviewedRevision == "" || review.ReviewedBy == "" || review.ReviewReference == "" {
			return errors.New("marker review provenance is invalid")
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var readValue func(string) error
	readValue = func(object string) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				allowed := markerFields
				if object == "review" {
					allowed = reviewFields
				}
				if _, ok := allowed[key]; !ok {
					return fmt.Errorf("unknown or non-canonical marker field %q", key)
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				child := ""
				if object == "marker" && (key == "landing" || key == "roadmap") {
					child = "review"
				}
				if err := readValue(child); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
			if required, ok := requiredMarkerFields[object]; ok {
				for key := range required {
					if _, exists := seen[key]; !exists {
						return fmt.Errorf("required marker field %q is missing", key)
					}
				}
			}
		case '[':
			for decoder.More() {
				if err := readValue(""); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := readValue("marker"); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

var markerFields = map[string]struct{}{
	"schema_version": {}, "project_id": {}, "landing": {}, "roadmap": {},
}

var reviewFields = map[string]struct{}{
	"reviewed_source_digest": {}, "reviewed_document_digest": {}, "reviewed_packet_digest": {},
	"reviewed_revision": {}, "reviewed_by": {}, "review_reference": {},
}

var requiredMarkerFields = map[string]map[string]struct{}{
	"marker": markerFields,
	"review": reviewFields,
}

func Evaluate(landing, roadmap Snapshot, landingDocument, roadmapDocument []byte, marker Marker, markerExists bool, landingErr, roadmapErr error) Response {
	response := Response{SchemaVersion: SchemaVersion, ProjectID: ProjectID, AuthorityScope: "local_review_only"}
	if markerExists && ValidateMarker(marker) != nil {
		marker = Marker{}
	}
	response.Landing = evaluateTarget(landing, landingDocument, marker.Landing, marker, markerExists, landingErr)
	response.Roadmap = evaluateTarget(roadmap, roadmapDocument, marker.Roadmap, marker, markerExists, roadmapErr)
	return response
}

func evaluateTarget(source Snapshot, document []byte, review *Review, marker Marker, markerExists bool, inputErr error) TargetStatus {
	result := TargetStatus{Status: "unverifiable", ReasonCode: "invalid_input"}
	if inputErr != nil {
		return result
	}
	observedSource, observedDocument := source.Digest, DigestBytes(document)
	result.ObservedSourceDigest = &observedSource
	result.ObservedDocumentDigest = &observedDocument
	if !markerExists {
		result.ReasonCode = "review_missing"
		return result
	}
	if marker.SchemaVersion != SchemaVersion || marker.ProjectID != ProjectID {
		result.ReasonCode = "invalid_marker"
		return result
	}
	if review == nil {
		result.ReasonCode = "review_missing"
		return result
	}
	if _, err := decodeDigest(review.ReviewedSourceDigest); err != nil {
		result.ReasonCode = "invalid_marker"
		return result
	}
	if _, err := decodeDigest(review.ReviewedDocumentDigest); err != nil {
		result.ReasonCode = "invalid_marker"
		return result
	}
	if _, err := decodeDigest(review.ReviewedPacketDigest); err != nil || review.ReviewedRevision == "" || review.ReviewedBy == "" || review.ReviewReference == "" {
		result.ReasonCode = "invalid_marker"
		return result
	}
	result.ReviewedSourceDigest = &review.ReviewedSourceDigest
	result.ReviewedDocumentDigest = &review.ReviewedDocumentDigest
	sourceChanged := observedSource != review.ReviewedSourceDigest
	documentChanged := observedDocument != review.ReviewedDocumentDigest
	switch {
	case !sourceChanged && !documentChanged:
		result.Status = "current"
		result.ReasonCode = "match"
	case sourceChanged && documentChanged:
		result.Status = "review_required"
		result.ReasonCode = "source_and_document_changed"
	case sourceChanged:
		result.Status = "review_required"
		result.ReasonCode = "source_changed"
	default:
		result.Status = "review_required"
		result.ReasonCode = "document_changed"
	}
	return result
}

func ReadBounded(path string, limit int64) ([]byte, error) {
	if err := RejectSymlinkComponents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, fmt.Errorf("invalid or oversized file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed while opening: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds size limit: %s", path)
	}
	return data, nil
}

// RejectSymlinkComponents prevents a project root or any path ancestor from
// redirecting a local evaluation outside its explicitly bound checkout.
func RejectSymlinkComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path component rejected")
		}
	}
	return nil
}

func CollectLanding(root string) (Snapshot, error) {
	if err := RejectSymlinkComponents(root); err != nil {
		return Snapshot{}, err
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return Snapshot{}, errors.New("Foundation root is missing or not a directory")
	}
	paths := []string{"project_mandate.md", "domain_entities.md", "project_constitution.md", "system_roadmap.md"}
	if len(paths) > MaxInputs {
		return Snapshot{}, errors.New("input limit exceeded")
	}
	for _, dir := range []string{"policies", "modules"} {
		base := filepath.Join(root, dir)
		if err := RejectSymlinkComponents(base); err != nil {
			return Snapshot{}, err
		}
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink in source tree: %s", path)
			}
			if path == base {
				return nil
			}
			if entry.IsDir() {
				if ignoredLandingSegment(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type().IsRegular() && IsMarkdownPath(entry.Name()) {
				rel, e := filepath.Rel(root, path)
				if e != nil {
					return e
				}
				rel = filepath.ToSlash(rel)
				if ignoredLandingSegment(rel) {
					return nil
				}
				paths = append(paths, rel)
				if len(paths) > MaxInputs {
					return errors.New("input limit exceeded")
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, err
		}
	}
	return collectContent(root, paths)
}

func CollectRoadmap(root string, candidates []string) (Snapshot, error) {
	if err := RejectSymlinkComponents(root); err != nil {
		return Snapshot{}, err
	}
	if err := RejectSymlinkComponents(filepath.Join(root, "todos")); err != nil {
		return Snapshot{}, err
	}
	paths := make([]string, 0)
	for _, path := range candidates {
		if !strings.HasPrefix(path, "todos/") || strings.HasPrefix(path, "todos/ephemeral/") || !IsMarkdownPath(path) || ignoredSegment(path) {
			continue
		}
		if err := ValidatePath(path); err != nil {
			return Snapshot{}, err
		}
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := RejectSymlinkComponents(fullPath); err != nil {
			return Snapshot{}, err
		}
		info, err := os.Lstat(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		if !info.Mode().IsRegular() {
			return Snapshot{}, fmt.Errorf("invalid TODO path: %s", path)
		}
		paths = append(paths, path)
		if len(paths) > MaxInputs {
			return Snapshot{}, errors.New("input limit exceeded")
		}
	}
	if len(paths) > MaxInputs {
		return Snapshot{}, errors.New("input limit exceeded")
	}
	inputs := make([]Input, 0, len(paths))
	for _, path := range paths {
		inputs = append(inputs, Input{Path: path})
	}
	digest, err := HashRecords(inputs)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Digest: digest, Inputs: inputs}, nil
}

func collectContent(root string, paths []string) (Snapshot, error) {
	if len(paths) > MaxInputs {
		return Snapshot{}, errors.New("input limit exceeded")
	}
	sort.Strings(paths)
	inputs := make([]Input, 0, len(paths))
	var total int64
	for _, path := range paths {
		if err := ValidatePath(path); err != nil {
			return Snapshot{}, err
		}
		data, err := ReadBounded(filepath.Join(root, filepath.FromSlash(path)), MaxDocumentBytes)
		if err != nil {
			return Snapshot{}, err
		}
		total += int64(len(data))
		if total > MaxAggregateBytes {
			return Snapshot{}, errors.New("aggregate input limit exceeded")
		}
		inputs = append(inputs, Input{Path: path, ContentDigest: DigestBytes(data)})
	}
	digest, err := HashRecords(inputs)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Digest: digest, Inputs: inputs, BytesRead: total}, nil
}

func ignoredSegment(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		p := strings.ToLower(part)
		if p == "tmp" || p == "temp" || p == "generated" {
			return true
		}
	}
	return false
}

func ignoredLandingSegment(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		p := strings.ToLower(part)
		if p == "tmp" || p == "temp" || p == "generated" {
			return true
		}
	}
	return false
}

// PacketDigest is the non-self-referential binary v1 review packet digest.
func PacketDigest(packet Packet) (string, error) {
	if packet.Target != "landing" && packet.Target != "roadmap" {
		return "", errors.New("invalid review target")
	}
	h := sha256.New()
	h.Write([]byte("builder-review-packet-v1"))
	h.Write([]byte{0})
	writeString := func(value string) error {
		if !utf8.ValidString(value) {
			return errors.New("invalid UTF-8 packet value")
		}
		if len(value) > int(^uint32(0)) {
			return errors.New("packet value too long")
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(value)))
		h.Write(size[:])
		h.Write([]byte(value))
		return nil
	}
	for _, value := range []string{packet.Target, packet.SourceRevision, packet.SourceDigest, packet.OutputPath, packet.DocumentDigest} {
		if err := writeString(value); err != nil {
			return "", err
		}
	}
	inputs := append([]Input(nil), packet.Inputs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	if len(inputs) > MaxInputs {
		return "", errors.New("input limit exceeded")
	}
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(inputs)))
	h.Write(count[:])
	previous := ""
	for _, input := range inputs {
		if err := ValidatePath(input.Path); err != nil {
			return "", err
		}
		if input.Path == previous {
			return "", errors.New("duplicate packet input")
		}
		previous = input.Path
		content := input.ContentDigest
		if packet.Target == "roadmap" {
			content = ""
		} else if _, err := decodeDigest(content); err != nil {
			return "", err
		}
		if err := writeString(input.Path); err != nil {
			return "", err
		}
		if err := writeString(content); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
