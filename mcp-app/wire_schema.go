package main

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	fastjson "github.com/segmentio/encoding/json"
)

var (
	errArgumentShape = errors.New("arguments do not match the input schema")
	errOverviewMedia = errors.New("Overview media is outside the PNG slot byte limit")
)

// wireSchema checks the subset of JSON Schema used by the shipped catalog
// directly on raw tokens. Request containers are never expanded into a JSON
// value tree, even when a valid owner payload has a large, wide collection.
type wireSchema struct {
	root     *jsonschema.Schema
	patterns map[string]*regexp.Regexp
}

func compileWireSchema(schema *jsonschema.Schema) (wireSchema, error) {
	wire := wireSchema{root: schema, patterns: map[string]*regexp.Regexp{}}
	var collect func(*jsonschema.Schema) error
	collect = func(s *jsonschema.Schema) error {
		if s == nil {
			return nil
		}
		if len(s.Types) != 0 || len(s.AllOf) != 0 || s.If != nil || s.PropertyNames != nil ||
			len(s.PatternProperties) != 0 || len(s.PrefixItems) != 0 || len(s.ItemsArray) != 0 ||
			s.AdditionalItems != nil || s.Contains != nil || s.MinProperties != nil ||
			s.MultipleOf != nil || s.ExclusiveMinimum != nil || s.ExclusiveMaximum != nil ||
			s.UnevaluatedItems != nil || s.UnevaluatedProperties != nil || len(s.DependentRequired) != 0 ||
			len(s.DependentSchemas) != 0 || len(s.Extra) != 0 || s.DynamicRef != "" || len(s.Definitions) != 0 ||
			s.Format != "" && s.Format != "date-time" || s.ContentEncoding != "" && s.ContentEncoding != "base64" {
			return errors.New("unsupported keyword in the closed Builder input schema")
		}
		// Ref, anyOf and false nodes have no sibling assertions in this
		// catalog. Reject future combinations rather than ignoring constraints.
		if s.Ref != "" || len(s.AnyOf) != 0 || s.Not != nil {
			stripped := *s
			stripped.Ref, stripped.AnyOf, stripped.Not = "", nil, nil
			if !reflect.DeepEqual(stripped, jsonschema.Schema{}) ||
				s.Not != nil && !reflect.DeepEqual(*s.Not, jsonschema.Schema{}) ||
				s.Ref != "" && (len(s.AnyOf) != 0 || s.Not != nil) ||
				len(s.AnyOf) != 0 && s.Not != nil {
				return errors.New("unsupported siblings beside Builder input schema composition")
			}
		}
		for _, choice := range s.OneOf {
			// The catalog uses one-of solely for exclusive object field sets.
			stripped := *choice
			stripped.Required = nil
			if len(choice.Required) == 0 || !reflect.DeepEqual(stripped, jsonschema.Schema{}) {
				return errors.New("unsupported Builder input schema oneOf branch")
			}
		}
		if s.Ref != "" {
			name, ok := strings.CutPrefix(s.Ref, "#/$defs/")
			if !ok || schema.Defs[name] == nil {
				return errors.New("unresolved Builder input schema definition")
			}
		}
		if s.Pattern != "" && wire.patterns[s.Pattern] == nil {
			pattern, err := regexp.Compile(s.Pattern)
			if err != nil {
				return err
			}
			wire.patterns[s.Pattern] = pattern
		}
		for _, child := range s.Defs {
			if err := collect(child); err != nil {
				return err
			}
		}
		for _, child := range s.Properties {
			if err := collect(child); err != nil {
				return err
			}
		}
		for _, child := range s.AnyOf {
			if err := collect(child); err != nil {
				return err
			}
		}
		for _, child := range []*jsonschema.Schema{s.Items, s.AdditionalProperties} {
			if err := collect(child); err != nil {
				return err
			}
		}
		return nil
	}
	return wire, collect(schema)
}

type argumentTokens struct {
	wire   wireSchema
	src    []byte
	tokens *fastjson.Tokenizer
	ctx    context.Context
	steps  uint
}

func (w wireSchema) validate(ctx context.Context, raw []byte, shape *jsonschema.Schema, depth int) error {
	stream := argumentTokens{wire: w, src: raw, tokens: fastjson.NewTokenizer(raw), ctx: ctx}
	defer stream.tokens.Reset(nil)
	if !stream.next() {
		return errArgumentShape
	}
	return stream.value(shape, depth)
}

func (a *argumentTokens) next() bool {
	for a.tokens.Next() {
		a.steps++
		if a.steps%4096 == 0 && a.ctx.Err() != nil {
			return false
		}
		if a.tokens.Delim != ':' && a.tokens.Delim != ',' {
			return true
		}
	}
	return false
}

func (a *argumentTokens) offset() int { return len(a.src) - a.tokens.Remaining() }

func (a *argumentTokens) value(s *jsonschema.Schema, depth int) error {
	if depth > 64 {
		return errArgumentShape
	}
	if s == nil {
		return a.skipValue(depth)
	}
	if s.Ref != "" {
		s = a.wire.root.Defs[strings.TrimPrefix(s.Ref, "#/$defs/")]
	}
	if s.Not != nil {
		// Boolean false is decoded by jsonschema as {"not":{}}.
		return errArgumentShape
	}
	if len(s.AnyOf) > 0 {
		start := a.offset() - len(a.tokens.Value)
		if err := a.skipValue(depth); err != nil {
			return err
		}
		raw := a.src[start:a.offset()]
		for _, choice := range s.AnyOf {
			if a.wire.validate(a.ctx, raw, choice, depth) == nil {
				return nil
			}
		}
		return errArgumentShape
	}
	token := a.tokens
	switch s.Type {
	case "object":
		return a.object(s, depth)
	case "array":
		return a.array(s, depth)
	case "string":
		return a.text(s)
	case "integer", "number":
		return a.number(s)
	case "boolean":
		if token.Value.True() || token.Value.False() {
			if s.Const == nil || *s.Const == token.Value.True() {
				return nil
			}
		}
	case "null":
		if token.Value.Null() {
			return nil
		}
	case "":
		return a.skipValue(depth)
	}
	return errArgumentShape
}

func (a *argumentTokens) skipValue(depth int) error {
	if a.tokens.Delim != '[' && a.tokens.Delim != '{' {
		return nil
	}
	openDepth := a.tokens.Depth
	for a.next() {
		if a.tokens.Depth-openDepth+depth > 64 {
			return errArgumentShape
		}
		if (a.tokens.Delim == ']' || a.tokens.Delim == '}') && a.tokens.Depth == openDepth {
			return nil
		}
	}
	return errArgumentShape
}

func (a *argumentTokens) object(s *jsonschema.Schema, depth int) error {
	if a.tokens.Delim != '{' {
		return errArgumentShape
	}
	seen := map[string]bool{}
	for a.next() {
		if a.tokens.Delim == '}' {
			for _, key := range s.Required {
				if !seen[key] {
					return errArgumentShape
				}
			}
			if len(s.OneOf) > 0 {
				matches := 0
				for _, choice := range s.OneOf {
					match := true
					for _, key := range choice.Required {
						match = match && seen[key]
					}
					if match {
						matches++
					}
				}
				if matches != 1 {
					return errArgumentShape
				}
			}
			return nil
		}
		if !a.tokens.IsKey {
			return errArgumentShape
		}
		key := string(a.tokens.String())
		field := s.Properties[key]
		if field == nil {
			field = s.AdditionalProperties
			if field == nil || field.Not != nil {
				return errArgumentShape
			}
		}
		if seen[key] || s.MaxProperties != nil && len(seen) >= *s.MaxProperties || !a.next() {
			return errArgumentShape
		}
		seen[key] = true
		if err := a.value(field, depth+1); err != nil {
			return err
		}
	}
	return errArgumentShape
}

func (a *argumentTokens) array(s *jsonschema.Schema, depth int) error {
	if a.tokens.Delim != '[' {
		return errArgumentShape
	}
	count := 0
	var unique []string
	for a.next() {
		if a.tokens.Delim == ']' {
			if s.MinItems != nil && count < *s.MinItems {
				return errArgumentShape
			}
			return nil
		}
		count++
		if s.MaxItems != nil && count > *s.MaxItems {
			return errArgumentShape
		}
		if s.UniqueItems {
			// The only uniqueItems collections are bounded arrays of Registry IDs.
			item := string(a.tokens.String())
			if slices.Contains(unique, item) {
				return errArgumentShape
			}
			unique = append(unique, item)
		}
		if err := a.value(s.Items, depth+1); err != nil {
			return err
		}
	}
	return errArgumentShape
}

func (a *argumentTokens) text(s *jsonschema.Schema) error {
	if !a.tokens.Value.String() {
		return errArgumentShape
	}
	if s.Pattern == "" && s.Format == "" && s.ContentEncoding == "" && s.MinLength == nil && s.MaxLength == nil && s.Enum == nil {
		return nil
	}
	text := a.tokens.String()
	if s.ContentEncoding == "base64" {
		size := base64.StdEncoding.DecodedLen(len(text))
		for i := len(text) - 1; i >= 0 && i >= len(text)-2 && text[i] == '='; i-- {
			size--
		}
		if len(text)%4 != 0 || size <= 0 || size > 2<<20 {
			return errOverviewMedia
		}
	}
	if s.MinLength != nil || s.MaxLength != nil {
		size := utf8.RuneCount(text)
		if s.MinLength != nil && size < *s.MinLength || s.MaxLength != nil && size > *s.MaxLength {
			return errArgumentShape
		}
	}
	if pattern := a.wire.patterns[s.Pattern]; pattern != nil && !pattern.Match(text) {
		return errArgumentShape
	}
	if s.Enum != nil && !slices.Contains(s.Enum, any(string(text))) {
		return errArgumentShape
	}
	if s.Format == "date-time" {
		if _, err := time.Parse(time.RFC3339Nano, string(text)); err != nil {
			return errArgumentShape
		}
	}
	return nil
}

func (a *argumentTokens) number(s *jsonschema.Schema) error {
	if !a.tokens.Value.Number() {
		return errArgumentShape
	}
	text := string(a.tokens.Value)
	var numeric float64
	if s.Type == "integer" {
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return errArgumentShape
		}
		numeric = float64(value) // Only schema bounds use this representation. The wire retains the exact token.
	} else {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return errArgumentShape
		}
		numeric = value
	}
	if s.Minimum != nil && numeric < *s.Minimum || s.Maximum != nil && numeric > *s.Maximum ||
		s.Const != nil && numeric != *s.Const {
		return errArgumentShape
	}
	return nil
}
