package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed catalog.json
var catalogFiles embed.FS

const catalogSize = 70

var schemaReference = regexp.MustCompile(`"#/\$defs/([A-Za-z][A-Za-z0-9]*)"`)

type toolSpec struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Method        string          `json:"method"`
	Path          string          `json:"path"`
	AlternatePath string          `json:"alternate_path"`
	Query         []string        `json:"query"`
	Schema        json.RawMessage `json:"schema"`
	wire          wireSchema
}

func loadCatalog() ([]toolSpec, error) {
	data, err := catalogFiles.ReadFile("catalog.json")
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Version     int                        `json:"version"`
		Tools       []toolSpec                 `json:"tools"`
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, err
	}
	if catalog.Version != 1 || len(catalog.Tools) != catalogSize {
		return nil, fmt.Errorf("unrecognized Builder MCP catalog version or size")
	}
	for i := range catalog.Tools {
		tool := &catalog.Tools[i]
		if i > 0 && catalog.Tools[i-1].Name >= tool.Name {
			return nil, fmt.Errorf("unsorted or duplicate catalog tool %s", tool.Name)
		}
		if err := tool.resolveSchema(catalog.Definitions); err != nil {
			return nil, fmt.Errorf("resolve catalog schema %s: %w", tool.Name, err)
		}
	}
	return catalog.Tools, nil
}

func (t *toolSpec) resolveSchema(definitions map[string]json.RawMessage) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(t.Schema, &root); err != nil {
		return err
	}
	root["$schema"] = json.RawMessage(`"https://json-schema.org/draft/2020-12/schema"`)
	refs := make(map[string]json.RawMessage)
	var collect func(json.RawMessage) error
	collect = func(schema json.RawMessage) error {
		for _, match := range schemaReference.FindAllSubmatch(schema, -1) {
			name := string(match[1])
			if _, exists := refs[name]; exists {
				continue
			}
			definition, exists := definitions[name]
			if !exists {
				return fmt.Errorf("missing definition %s", name)
			}
			refs[name] = definition
			if err := collect(definition); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(t.Schema); err != nil {
		return err
	}
	if len(refs) > 0 {
		raw, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		root["$defs"] = raw
	}
	var err error
	t.Schema, err = json.Marshal(root)
	if err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(t.Schema, &schema); err != nil {
		return err
	}
	if _, err := schema.Resolve(nil); err != nil {
		return err
	}
	t.wire, err = compileWireSchema(&schema)
	return err
}

// These bytes are the current Registry handler limits for JSON bodies.
func (t toolSpec) bodyLimit() int {
	switch t.Name {
	case "builder_overview_write":
		return 18 << 20
	case "builder_todo_create", "builder_todo_update", "builder_todo_restore",
		"builder_todo_evidence_update", "builder_todo_place", "builder_todo_next_step_update":
		return 4 << 20
	case "builder_knowledge_create", "builder_knowledge_update", "builder_knowledge_delete",
		"builder_knowledge_restore", "builder_knowledge_activate", "builder_knowledge_draft_create":
		return 4 << 20
	case "builder_set_investigation", "builder_set_sequencing_plan":
		return (512 + 4) << 10
	case "builder_implementation_repository_change", "builder_design_system_begin", "builder_clear_sequencing_plan":
		return 8 << 10
	case "builder_repository_binding_change":
		return 4 << 10
	default:
		return 128 << 10
	}
}

func (t toolSpec) timeout() time.Duration {
	switch {
	case t.Method != http.MethodGet:
		return 120 * time.Second
	case t.Name == "builder_company_search" || t.Name == "builder_project_search" || t.Name == "builder_session_get":
		return 10 * time.Second
	case strings.HasPrefix(t.Name, "builder_design_system_"):
		return 110 * time.Second
	default:
		return 30 * time.Second
	}
}

func (t toolSpec) selectPath(args object) string {
	if t.AlternatePath == "" {
		return t.Path
	}
	if t.Name == "builder_design_system_revision_validate" {
		if args.text("source") == "validate" {
			return t.AlternatePath
		}
	} else if _, ok := args["revision"]; ok {
		return t.AlternatePath
	}
	return t.Path
}

func (t toolSpec) isDestructive() bool {
	return t.Method == http.MethodDelete || slices.Contains([]string{
		"builder_repository_binding_change", "builder_implementation_repository_change",
		"builder_todo_remove", "builder_design_system_begin", "builder_design_system_recover",
	}, t.Name)
}
