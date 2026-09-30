package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// loadJSON reads a JSON file into a generic value.
func loadJSON(t *testing.T, path ...string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", filepath.Join(path...), err)
	}
	return v
}

func loadSchema(t *testing.T) map[string]any {
	t.Helper()
	return loadJSON(t, "..", "..", "docs", "findings.schema.json").(map[string]any)
}

func defs(schema map[string]any) map[string]any { return schema["$defs"].(map[string]any) }

// jsonFields returns the json tag names of a struct and the subset without
// omitempty.
func jsonFields(t reflect.Type) (all, required []string) {
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		all = append(all, name)
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}
	return all, required
}

func stringKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	slices.Sort(out)
	return out
}

// structsOf collects every findings struct reachable from t through slices,
// maps and pointers. time.Time and other foreign types are leaves.
func structsOf(t reflect.Type, into map[string]reflect.Type) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		structsOf(t.Elem(), into)
	case reflect.Struct:
		if t.PkgPath() != reflect.TypeOf(findings.Report{}).PkgPath() || into[t.Name()] != nil {
			return
		}
		into[t.Name()] = t
		for i := 0; i < t.NumField(); i++ {
			structsOf(t.Field(i).Type, into)
		}
	}
}

// TestSchemaMatchesGoTypes is the reflection guard of the contract: every JSON
// field of the Go types must be documented in docs/findings.schema.json and
// required must equal the fields without omitempty.
func TestSchemaMatchesGoTypes(t *testing.T) {
	d := defs(loadSchema(t))
	types := map[string]reflect.Type{}
	structsOf(reflect.TypeOf(findings.Report{}), types)
	if len(types) != len(d) {
		t.Errorf("schema has %d definitions, Go has %d structs (%v)", len(d), len(types), stringKeys(d))
	}
	for name, typ := range types {
		def, ok := d[name].(map[string]any)
		if !ok {
			t.Errorf("schema lacks definition %s", name)
			continue
		}
		all, required := jsonFields(typ)
		slices.Sort(all)
		slices.Sort(required)
		if got := stringKeys(def["properties"].(map[string]any)); !slices.Equal(got, all) {
			t.Errorf("%s properties = %v, Go json tags = %v", name, got, all)
		}
		reqSchema := []string{}
		if r, ok := def["required"]; ok {
			reqSchema = toStrings(r)
		}
		if required == nil {
			required = []string{}
		}
		if !slices.Equal(reqSchema, required) {
			t.Errorf("%s required = %v, want %v", name, reqSchema, required)
		}
	}
}

// schemaType is the JSON Schema type a Go type must be described with.
func schemaType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int64:
		return "integer"
	case reflect.Slice:
		return "array"
	case reflect.Map, reflect.Struct:
		return "object"
	case reflect.Pointer:
		return schemaType(t.Elem())
	}
	return ""
}

// TestSchemaPropertyTypes checks that each property is typed like its Go
// field (following $ref, time.Time being a string, any being unconstrained).
func TestSchemaPropertyTypes(t *testing.T) {
	schema := loadSchema(t)
	d := defs(schema)
	types := map[string]reflect.Type{}
	structsOf(reflect.TypeOf(findings.Report{}), types)
	for name, typ := range types {
		props := d[name].(map[string]any)["properties"].(map[string]any)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			jn, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			prop := resolve(schema, props[jn].(map[string]any))
			want := schemaType(f.Type)
			if f.Type == reflect.TypeOf(time.Time{}) || f.Type == reflect.TypeOf(&time.Time{}) {
				want = "string"
			}
			if f.Type.Kind() == reflect.Interface {
				if _, has := prop["type"]; has {
					t.Errorf("%s.%s must not constrain its type", name, jn)
				}
				continue
			}
			if got, _ := prop["type"].(string); got != want {
				t.Errorf("%s.%s type = %q, want %q", name, jn, got, want)
			}
		}
	}
}

// TestSchemaEnums pins every enum. When you add a constant to
// internal/findings, add it to the list below and to docs/findings.schema.json.
func TestSchemaEnums(t *testing.T) {
	schema := loadSchema(t)
	d := defs(schema)
	prop := func(def, name string) map[string]any {
		return d[def].(map[string]any)["properties"].(map[string]any)[name].(map[string]any)
	}
	var flags []string
	for _, f := range findings.AllRiskFlags() {
		flags = append(flags, string(f))
	}
	slices.Sort(flags)
	want := map[string][]string{
		"scope.type": {"repo", "root", "user"},
		"kind": {"file", "dir", "branch", "worktree", "worktree-missing", "git-loose-objects", "git-packs",
			"git-reflog", "git-large-blob"},
		"confidence": {"high", "medium", "low"},
		"action": {"none", "trash", "delete-branch", "remove-worktree", "prune-worktrees", "git-gc", "git-prune",
			"git-reflog-expire"},
		"risk_flags": flags,
	}
	got := map[string][]string{
		"scope.type": toStrings(prop("Scope", "type")["enum"]),
		"kind":       toStrings(prop("Finding", "kind")["enum"]),
		"confidence": toStrings(prop("Finding", "confidence")["enum"]),
		"action":     toStrings(prop("SuggestedAction", "type")["enum"]),
		"risk_flags": toStrings(prop("Finding", "risk_flags")["items"].(map[string]any)["enum"]),
	}
	for k, w := range want {
		w = slices.Clone(w)
		slices.Sort(w)
		if !slices.Equal(got[k], w) {
			t.Errorf("%s enum = %v, want %v", k, got[k], w)
		}
	}
}

func TestSchemaMetadata(t *testing.T) {
	s := loadSchema(t)
	if s["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$schema = %v", s["$schema"])
	}
	if !strings.HasPrefix(s["$id"].(string), "https://raw.githubusercontent.com/Tobias-Braun/brooom/") ||
		!strings.HasSuffix(s["$id"].(string), "/docs/findings.schema.json") {
		t.Errorf("$id = %v", s["$id"])
	}
	if s["title"] == nil || s["$ref"] != "#/$defs/Report" {
		t.Error("title missing or root is not a $ref to Report")
	}
	sv := defs(s)["Report"].(map[string]any)["properties"].(map[string]any)["schema_version"].(map[string]any)
	if _, isConst := sv["const"]; isConst || sv["minimum"] != float64(1) {
		t.Errorf("schema_version = %v", sv)
	}
}

// resolve follows a local "#/$defs/X" reference; other schemas are returned
// as they are.
func resolve(root, s map[string]any) map[string]any {
	ref, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	name, found := strings.CutPrefix(ref, "#/$defs/")
	if !found {
		panic("unsupported $ref " + ref)
	}
	return resolve(root, defs(root)[name].(map[string]any))
}

// validate is a minimal JSON Schema validator covering exactly the keywords
// the findings schema uses: $ref, type, enum, required, properties, items,
// additionalProperties (as a schema for map values), minimum and format
// date-time. Unknown keywords and types are errors so the schema cannot drift
// beyond what the tests can check.
func validate(root, schema map[string]any, v any, at string) []string {
	schema = resolve(root, schema)
	errs := unsupportedKeywords(schema, at)
	if typ, ok := schema["type"].(string); ok {
		if err := checkType(typ, v); err != "" {
			return append(errs, at+": "+err)
		}
	}
	errs = append(errs, validateScalar(schema, v, at)...)
	errs = append(errs, validateObject(root, schema, v, at)...)
	if items, ok := schema["items"].(map[string]any); ok {
		for i, e := range v.([]any) {
			errs = append(errs, validate(root, items, e, fmt.Sprintf("%s[%d]", at, i))...)
		}
	}
	return errs
}

func unsupportedKeywords(schema map[string]any, at string) []string {
	known := []string{"type", "enum", "required", "properties", "items", "additionalProperties", "minimum", "format",
		"description", "$ref", "$defs", "$schema", "$id", "title"}
	var errs []string
	for k := range schema {
		if !slices.Contains(known, k) {
			errs = append(errs, fmt.Sprintf("%s: unsupported schema keyword %q", at, k))
		}
	}
	return errs
}

// validateScalar checks enum, minimum and format, which only constrain
// scalar values.
func validateScalar(schema map[string]any, v any, at string) []string {
	var errs []string
	if enum, ok := schema["enum"].([]any); ok && !slices.Contains(enum, v) {
		errs = append(errs, fmt.Sprintf("%s: %v not in enum %v", at, v, enum))
	}
	if min, ok := schema["minimum"].(float64); ok {
		if n, isNum := v.(float64); isNum && n < min {
			errs = append(errs, fmt.Sprintf("%s: %v below minimum %v", at, n, min))
		}
	}
	if s, _ := v.(string); schema["format"] == "date-time" && s != "" {
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			errs = append(errs, fmt.Sprintf("%s: not a date-time: %v", at, err))
		}
	}
	return errs
}

func checkType(typ string, v any) string {
	ok := false
	switch typ {
	case "object":
		_, ok = v.(map[string]any)
	case "array":
		_, ok = v.([]any)
	case "string":
		_, ok = v.(string)
	case "integer":
		n, isNum := v.(float64)
		ok = isNum && n == float64(int64(n))
	default:
		return fmt.Sprintf("unsupported schema type %q", typ)
	}
	if !ok {
		return fmt.Sprintf("want %s, got %T", typ, v)
	}
	return ""
}

func validateObject(root, schema map[string]any, v any, at string) []string {
	obj, isObj := v.(map[string]any)
	if !isObj {
		return nil
	}
	var errs []string
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if _, has := obj[r.(string)]; !has {
				errs = append(errs, fmt.Sprintf("%s: missing required %q", at, r))
			}
		}
	}
	props, _ := schema["properties"].(map[string]any)
	valueSchema, _ := schema["additionalProperties"].(map[string]any)
	for k, val := range obj {
		switch {
		case props[k] != nil:
			errs = append(errs, validate(root, props[k].(map[string]any), val, at+"."+k)...)
		case valueSchema != nil:
			errs = append(errs, validate(root, valueSchema, val, at+"."+k)...)
		}
	}
	return errs
}

func validateReport(t *testing.T, name string, doc any) {
	t.Helper()
	schema := loadSchema(t)
	if errs := validate(schema, schema, doc, "$"); len(errs) > 0 {
		t.Errorf("%s does not match the schema:\n%s", name, strings.Join(errs, "\n"))
	}
}

func TestSchemaValidatesExampleAndFormatterOutput(t *testing.T) {
	validateReport(t, "example-report.json", loadJSON(t, "..", "findings", "testdata", "example-report.json"))
	validateReport(t, "json golden", loadJSON(t, "testdata", "json.golden"))

	var doc any
	if err := json.Unmarshal([]byte(render(t, "json", nilReport(), Options{})), &doc); err != nil {
		t.Fatal(err)
	}
	validateReport(t, "nil-slice report", doc)
}

// TestValidatorRejects proves the validator is not vacuous: unknown enum
// values, wrong types, missing fields and bad map values must fail.
func TestValidatorRejects(t *testing.T) {
	var good map[string]any
	if err := json.Unmarshal([]byte(render(t, "json", extendedReport(), Options{})), &good); err != nil {
		t.Fatal(err)
	}
	schema := loadSchema(t)
	first := func(m map[string]any) map[string]any { return m["findings"].([]any)[0].(map[string]any) }
	tests := []struct {
		name   string
		mutate func(m map[string]any)
	}{
		{"unknown kind", func(m map[string]any) { first(m)["kind"] = "blob" }},
		{"unknown confidence", func(m map[string]any) { first(m)["confidence"] = "certain" }},
		{"unknown risk flag", func(m map[string]any) { first(m)["risk_flags"] = []any{"explosive"} }},
		{"unknown action", func(m map[string]any) { first(m)["suggested_action"].(map[string]any)["type"] = "shred" }},
		{"size as string", func(m map[string]any) { first(m)["size_bytes"] = "12" }},
		{"missing evidence", func(m map[string]any) { delete(first(m), "evidence") }},
		{"null evidence", func(m map[string]any) { first(m)["evidence"] = nil }},
		{"bad date", func(m map[string]any) { m["generated_at"] = "yesterday" }},
		{"version zero", func(m map[string]any) { m["schema_version"] = float64(0) }},
		{"meta value type", func(m map[string]any) { first(m)["meta"] = map[string]any{"a": 1.0} }},
		{"totals value missing", func(m map[string]any) {
			m["totals"].(map[string]any)["by_detector"].(map[string]any)["worktrees"] = map[string]any{}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]any
			raw, _ := json.Marshal(good)
			_ = json.Unmarshal(raw, &m)
			tt.mutate(m)
			if errs := validate(schema, schema, m, "$"); len(errs) == 0 {
				t.Error("validator accepted invalid document")
			}
		})
	}
	// Additional properties stay allowed on purpose.
	var m map[string]any
	raw, _ := json.Marshal(good)
	_ = json.Unmarshal(raw, &m)
	m["future_field"] = true
	first(m)["future_field"] = []any{1.0}
	if errs := validate(schema, schema, m, "$"); len(errs) > 0 {
		t.Errorf("additional properties rejected: %v", errs)
	}
}
