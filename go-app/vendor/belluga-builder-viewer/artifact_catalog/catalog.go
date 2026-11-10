// Package artifact_catalog contains shared bounded source and decoding helpers
// plus the Builder-owned Design System evaluator.
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
	MaxFiles         = 2048
	MaxFileBytes     = 2 << 20
	MaxTotalBytes    = 64 << 20
	MaxManifestBytes = 256 << 10
	MaxPathBytes     = 1024
	// Each admitted file can have at most MaxPathBytes/2 nested one-byte path
	// segments, so this caps directory nodes without an arbitrary file multiplier.
	MaxInventoryNodes = MaxFiles*(MaxPathBytes/2+1) + 1
	AuthorityScope    = "local_structure_only"
)

type Diagnostic struct {
	Code       string  `json:"code"`
	SourceID   *string `json:"source_id"`
	ArtifactID *string `json:"artifact_id"`
	Path       *string `json:"path"`
	Message    string  `json:"message"`
	Resolution string  `json:"resolution"`
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

// Source is the bounded file/tree boundary shared by artifact evaluators.
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

func stringPointer(value string) *string { return &value }

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
		return nil
	}
	for index, rawToken := range tokens {
		token, ok := rawToken.(map[string]any)
		if !ok {
			continue
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

func validateKeys(value any, rule *objectRule) error { return validateKeysAt(value, rule, "") }

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
	result := append([]Diagnostic(nil), input...)
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

var ErrNotExist = errors.New("file does not exist")
