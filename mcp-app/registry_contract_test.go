package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type frozenCatalog struct {
	Definitions map[string]json.RawMessage `json:"definitions"`
	Tools       []struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	} `json:"tools"`
}

type contractOracle struct {
	SchemaVersion   int               `json:"schema_version"`
	Authority       string            `json:"authority"`
	RegistrySources []string          `json:"registry_sources"`
	Tools           map[string]string `json:"tools"`
	Definitions     map[string]string `json:"definitions"`
}

func TestCatalogSchemasMatchIndependentContractOracle(t *testing.T) {
	catalogBytes, err := catalogFiles.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog frozenCatalog
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		t.Fatal(err)
	}
	oracleBytes, err := os.ReadFile("testdata/catalog-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle contractOracle
	if err := json.Unmarshal(oracleBytes, &oracle); err != nil {
		t.Fatal(err)
	}
	if oracle.SchemaVersion != 1 || strings.TrimSpace(oracle.Authority) == "" || len(oracle.RegistrySources) == 0 {
		t.Fatal("independent Registry contract oracle metadata is incomplete")
	}
	if len(catalog.Tools) != catalogSize || len(oracle.Tools) != catalogSize {
		t.Fatalf("tool oracle coverage: catalog=%d oracle=%d; want %d", len(catalog.Tools), len(oracle.Tools), catalogSize)
	}
	if len(catalog.Definitions) != len(oracle.Definitions) {
		t.Fatalf("definition oracle coverage: catalog=%d oracle=%d", len(catalog.Definitions), len(oracle.Definitions))
	}
	seenTools := make(map[string]bool, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		if seenTools[tool.Name] {
			t.Fatalf("duplicate catalog tool %q", tool.Name)
		}
		seenTools[tool.Name] = true
		want, ok := oracle.Tools[tool.Name]
		if !ok {
			t.Errorf("tool %q is missing from the independent contract oracle", tool.Name)
			continue
		}
		if got := compactJSONDigest(t, tool.Schema); got != want {
			t.Errorf("tool %q schema contract changed: got sha256:%s, want sha256:%s", tool.Name, got, want)
		}
	}
	for name, schema := range catalog.Definitions {
		want, ok := oracle.Definitions[name]
		if !ok {
			t.Errorf("schema definition %q is missing from the independent contract oracle", name)
			continue
		}
		if got := compactJSONDigest(t, schema); got != want {
			t.Errorf("schema definition %q contract changed: got sha256:%s, want sha256:%s", name, got, want)
		}
	}
	for name := range oracle.Tools {
		if !seenTools[name] {
			t.Errorf("independent contract oracle contains unknown tool %q", name)
		}
	}
	for name := range oracle.Definitions {
		if _, ok := catalog.Definitions[name]; !ok {
			t.Errorf("independent contract oracle contains unknown definition %q", name)
		}
	}
}

func compactJSONDigest(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compact.Bytes())
	return hex.EncodeToString(digest[:])
}

// This test reads the Registry's Go DTO declarations, not the MCP catalog,
// and compares their JSON field names and JSON value kinds with every catalog
// definition. The frozen digest oracle separately pins required fields,
// enums, bounds, patterns, and closed-object assertions.
func TestCatalogDefinitionsMatchRegistryDTOFieldsAndTypes(t *testing.T) {
	registryDir := filepath.Join("..", "go-app", "internal", "registry")
	entries, err := os.ReadDir(registryDir)
	if err != nil {
		t.Fatalf("read Registry DTO source at %s: %v", registryDir, err)
	}
	structures := make(map[string]map[string]ast.Expr)
	typeExprs := make(map[string]ast.Expr)
	structExprs := make(map[string]*ast.StructType)
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(registryDir, entry.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse Registry source %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, spec := range group.Specs {
				typeSpec := spec.(*ast.TypeSpec)
				typeExprs[typeSpec.Name.Name] = typeSpec.Type
				if structure, ok := typeSpec.Type.(*ast.StructType); ok {
					structExprs[typeSpec.Name.Name] = structure
				}
			}
		}
	}
	for name, structure := range structExprs {
		structures[name] = registryStructFields(structure, structExprs, make(map[string]bool))
	}

	catalogBytes, err := catalogFiles.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Definitions) < 100 {
		t.Fatalf("expected full Registry DTO schema coverage, found %d definitions", len(catalog.Definitions))
	}
	for name, rawSchema := range catalog.Definitions {
		t.Run(name, func(t *testing.T) {
			wantFields, ok := structures[name]
			if !ok {
				t.Fatalf("catalog definition %q has no same-named Registry DTO struct", name)
			}
			var schema map[string]any
			if err := json.Unmarshal(rawSchema, &schema); err != nil {
				t.Fatal(err)
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("Registry DTO definition %q has no object properties", name)
			}
			for fieldName := range properties {
				if _, ok := wantFields[fieldName]; !ok {
					t.Errorf("catalog field %q is absent from the Registry DTO", fieldName)
				}
			}
			for fieldName := range properties {
				fieldType, ok := wantFields[fieldName]
				if !ok {
					continue
				}
				wantKind := registryJSONKind(fieldType, typeExprs, make(map[string]bool))
				if wantKind == "any" {
					continue
				}
				gotKinds := schemaKinds(properties[fieldName], catalog.Definitions, make(map[string]bool))
				if !gotKinds[wantKind] {
					t.Errorf("field %q has Registry JSON kind %q but catalog schema has kinds %v", fieldName, wantKind, sortedKeys(gotKinds))
				}
			}
		})
	}
}

func registryStructFields(structure *ast.StructType, structures map[string]*ast.StructType, seen map[string]bool) map[string]ast.Expr {
	fields := make(map[string]ast.Expr)
	for _, field := range structure.Fields.List {
		if field.Names == nil {
			if embedded, ok := field.Type.(*ast.Ident); ok && !seen[embedded.Name] {
				if nested, exists := structures[embedded.Name]; exists {
					seen[embedded.Name] = true
					for name, expression := range registryStructFields(nested, structures, seen) {
						fields[name] = expression
					}
				}
			}
			continue
		}
		for _, fieldName := range field.Names {
			if !ast.IsExported(fieldName.Name) {
				continue
			}
			name := registryJSONName(fieldName.Name, field.Tag)
			if name == "-" {
				continue
			}
			fields[name] = field.Type
		}
	}
	return fields
}

func registryJSONName(fieldName string, tag *ast.BasicLit) string {
	if tag == nil {
		return fieldName
	}
	value, err := strconv.Unquote(tag.Value)
	if err != nil {
		return fieldName
	}
	name := reflect.StructTag(value).Get("json")
	if name == "" {
		return fieldName
	}
	return strings.Split(name, ",")[0]
}

func registryJSONKind(expression ast.Expr, typeExprs map[string]ast.Expr, seen map[string]bool) string {
	switch value := expression.(type) {
	case *ast.StarExpr:
		return registryJSONKind(value.X, typeExprs, seen)
	case *ast.ArrayType:
		if ident, ok := value.Elt.(*ast.Ident); ok && (ident.Name == "byte" || ident.Name == "uint8") {
			return "string" // encoding/json represents []byte as base64 text.
		}
		return "array"
	case *ast.MapType, *ast.StructType:
		return "object"
	case *ast.InterfaceType:
		return "any"
	case *ast.SelectorExpr:
		if selector, ok := value.X.(*ast.Ident); ok {
			if selector.Name == "json" && value.Sel.Name == "RawMessage" {
				return "any"
			}
			if selector.Name == "time" && value.Sel.Name == "Time" {
				return "string"
			}
		}
		return "object"
	case *ast.Ident:
		switch value.Name {
		case "string":
			return "string"
		case "bool":
			return "boolean"
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "float32", "float64":
			return "number"
		}
		if !seen[value.Name] {
			if target, ok := typeExprs[value.Name]; ok {
				seen[value.Name] = true
				return registryJSONKind(target, typeExprs, seen)
			}
		}
		return "object"
	default:
		return "any"
	}
}

func schemaKinds(raw any, definitions map[string]json.RawMessage, seen map[string]bool) map[string]bool {
	kinds := make(map[string]bool)
	var walk func(any)
	walk = func(value any) {
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		if ref, ok := object["$ref"].(string); ok {
			const prefix = "#/$defs/"
			if strings.HasPrefix(ref, prefix) {
				name := strings.TrimPrefix(ref, prefix)
				if !seen[name] {
					seen[name] = true
					var resolved any
					if json.Unmarshal(definitions[name], &resolved) == nil {
						walk(resolved)
					}
				}
			}
		}
		if kind, ok := object["type"].(string); ok {
			if kind == "integer" || kind == "number" {
				kinds["number"] = true
			} else {
				kinds[kind] = true
			}
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			if choices, ok := object[key].([]any); ok {
				for _, choice := range choices {
					walk(choice)
				}
			}
		}
	}
	walk(raw)
	return kinds
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestCatalogOracleSourcePathsExist(t *testing.T) {
	data, err := os.ReadFile("testdata/catalog-contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle contractOracle
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatal(err)
	}
	for _, source := range oracle.RegistrySources {
		if _, err := os.Stat(filepath.Join("..", source)); err != nil {
			t.Errorf("Registry contract source %q is unavailable: %v", source, err)
		}
	}
}
