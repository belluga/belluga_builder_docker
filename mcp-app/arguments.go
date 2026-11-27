package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	fastjson "github.com/segmentio/encoding/json"
)

type object map[string]any

var (
	identityID      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	nativeSessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	pathVariable    = regexp.MustCompile(`\{([a-zA-Z]+)\}`)
	pathSeparators  = regexp.MustCompile(`ID|[A-Z]`)
)

func (o object) text(key string) string {
	value, _ := o[key].(string)
	return value
}

func (o object) child(key string) object {
	value, _ := o[key].(object)
	return value
}

func decodeObject(data []byte) (object, error) {
	if len(data) == 0 {
		return object{}, nil
	}
	if !utf8.Valid(data) {
		return nil, errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	fields, ok := value.(object)
	if !ok {
		return nil, errors.New("expected one JSON object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing JSON")
	}
	return fields, nil
}

func decodeValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON object exceeds maximum nesting")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		fields := object{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, duplicate := fields[name]; duplicate {
				return nil, errors.New("duplicate object key")
			}
			fields[name], err = decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
		}
		_, err = decoder.Token()
		return fields, err
	case json.Delim('['):
		items := make([]any, 0)
		for decoder.More() {
			item, err := decodeValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		_, err = decoder.Token()
		return items, err
	default:
		// Go owner revisions are signed integers. Keep their original spelling
		// and reject integer overflow without converting through a float.
		if number, ok := token.(json.Number); ok {
			if strings.ContainsAny(number.String(), ".eE") {
				if _, err := number.Float64(); err != nil {
					return nil, errors.New("JSON number is outside the Registry's numeric range")
				}
				return number, nil
			}
			value, err := number.Int64()
			if err != nil {
				return nil, errors.New("expected a signed 64-bit JSON integer")
			}
			return value, nil
		}
		return token, nil
	}
}

func argumentString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case int64:
		return strconv.FormatInt(value, 10)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func (t toolSpec) path(args object, session string) string {
	return pathVariable.ReplaceAllStringFunc(t.selectPath(args), func(match string) string {
		switch match {
		case "{provider}":
			return provider
		case "{externalID}":
			return url.PathEscape(session)
		default:
			name := pathSeparators.ReplaceAllStringFunc(match[1:len(match)-1], func(c string) string {
				return "_" + strings.ToLower(c)
			})
			return url.PathEscape(argumentString(args[name]))
		}
	})
}

func (t toolSpec) queryValues(args object) url.Values {
	query := make(url.Values, len(t.Query))
	for _, name := range t.Query {
		if value, exists := args[name]; exists {
			text := argumentString(value)
			if (t.Name == "builder_company_search" || t.Name == "builder_project_search") && name == "name" {
				text = strings.TrimSpace(text)
			}
			query.Set(name, text)
		}
	}
	return query
}

var errArgumentBodyLimit = errors.New("body exceeds the owning Registry endpoint limit")

type bodyRecordIdentity struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	GenerationID string `json:"generation_id"`
}

type bodyIdentity struct {
	ID              string              `json:"id"`
	CompanyID       string              `json:"company_id"`
	ProjectID       string              `json:"project_id"`
	CompanyName     string              `json:"company_name"`
	ProjectName     string              `json:"project_name"`
	TODOID          string              `json:"todo_id"`
	Family          string              `json:"family"`
	EpicID          string              `json:"epic_id"`
	OperationID     string              `json:"operation_id"`
	RecordID        string              `json:"record_id"`
	GenerationID    string              `json:"generation_id"`
	Record          *bodyRecordIdentity `json:"record"`
	TODO            *bodyRecordIdentity `json:"todo"`
	KnowledgeOrigin *bodyRecordIdentity `json:"knowledge_origin"`
	Records         []struct {
		Record bodyRecordIdentity `json:"record"`
	} `json:"records"`
}

func (t toolSpec) readArguments(ctx context.Context, raw json.RawMessage) (object, json.RawMessage, bodyIdentity, error) {
	var preview struct {
		Body json.RawMessage `json:"body"`
	}
	var identity bodyIdentity
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if !utf8.Valid(raw) {
		return nil, nil, identity, errArgumentShape
	}
	if rest, err := fastjson.Parse(raw, &preview, fastjson.ZeroCopy); err != nil || len(rest) != 0 {
		return nil, nil, identity, errArgumentShape
	}
	if len(preview.Body) > t.bodyLimit() {
		return nil, nil, identity, errArgumentBodyLimit
	}
	if err := t.wire.validate(ctx, raw, t.wire.root, 0); err != nil {
		return nil, nil, identity, err
	}
	var fields map[string]json.RawMessage
	if _, err := fastjson.Parse(raw, &fields, fastjson.ZeroCopy); err != nil {
		return nil, nil, identity, errArgumentShape
	}
	args := make(object, len(fields))
	for name, field := range fields {
		if name == "body" {
			continue
		}
		// Only scalar route/query fields are decoded after the structure check.
		decoder := json.NewDecoder(bytes.NewReader(field))
		decoder.UseNumber()
		value, err := decodeValue(decoder, 0)
		if err != nil {
			return nil, nil, identity, errArgumentShape
		}
		args[name] = value
	}
	if len(preview.Body) != 0 {
		if _, err := fastjson.Parse(preview.Body, &identity, fastjson.ZeroCopy); err != nil {
			return nil, nil, identity, errArgumentShape
		}
	}
	return args, preview.Body, identity, nil
}

func validateIdentities(tool toolSpec, args object, body bodyIdentity) string {
	for key, actual := range map[string]string{
		"company_id": body.CompanyID, "project_id": body.ProjectID, "todo_id": body.TODOID,
		"family": body.Family, "epic_id": body.EpicID, "record_id": body.RecordID,
	} {
		if actual != "" && args[key] != nil && actual != args[key] {
			return "body." + key
		}
	}
	if body.ID != "" {
		switch {
		case strings.Contains(tool.Path, "{releaseID}") && body.ID != args.text("release_id"):
			return "body.id"
		case strings.Contains(tool.Path, "{epicID}") && !strings.HasSuffix(tool.Path, "/releases") &&
			!strings.HasSuffix(tool.Path, "/releases/order") && body.ID != args.text("epic_id"):
			return "body.id"
		}
	}
	if record := body.Record; record != nil {
		if record.ProjectID != args.text("project_id") {
			return "body.record.project_id"
		}
		if expected := args.text("record_id"); expected != "" && record.ID != expected {
			return "body.record.id"
		}
	}
	for _, entry := range body.Records {
		if entry.Record.ProjectID != args.text("project_id") || entry.Record.GenerationID != body.GenerationID {
			return "body.records.record"
		}
	}
	if todo := body.TODO; todo != nil && todo.ProjectID != args.text("project_id") {
		return "body.todo.project_id"
	}
	if origin := body.KnowledgeOrigin; origin != nil && origin.ProjectID != args.text("project_id") {
		return "body.knowledge_origin.project_id"
	}
	return ""
}
