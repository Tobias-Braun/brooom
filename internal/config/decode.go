package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// utf8BOM is tolerated at the start of config files because Windows editors
// (notably Notepad) still add it when saving "UTF-8".
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// decodeStrict decodes JSON data into dst (a pointer to a struct) and turns
// every problem into an error that starts with label (the file path) and, for
// structural problems, names the full key path of the offending value.
//
// encoding/json alone cannot do this: DisallowUnknownFields reports the bare
// field name without its parents and UnmarshalTypeError paths are only
// approximate. So the data is first parsed generically and walked against the
// struct's json tags (checkKeys); the real decode afterwards only fills the
// values, and would only fail on cases the walk already covers.
func decodeStrict(label string, data []byte, dst any) error {
	data = bytes.TrimPrefix(data, utf8BOM)
	if len(bytes.TrimSpace(data)) == 0 {
		// An empty file is most likely a truncated write; silently treating
		// it as "all defaults" could hide a lost configuration.
		return fmt.Errorf("%s: file is empty (truncated write?); use {} for an empty configuration", label)
	}
	generic, err := parseSingleValue(data)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := checkDuplicateKeys(data); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := checkKeys(generic, reflect.TypeOf(dst).Elem(), ""); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// parseSingleValue parses data as exactly one JSON value (numbers kept as
// json.Number so integers are validated without float rounding) and reports
// syntax errors with line and column.
func parseSingleValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, syntaxError(data, err)
	}
	off := int(dec.InputOffset())
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		// Skip whitespace so the position points at the garbage itself.
		for off < len(data) && strings.IndexByte(" \t\r\n", data[off]) >= 0 {
			off++
		}
		line, col := position(data, off+1)
		return nil, fmt.Errorf("unexpected data after the top-level value at line %d, column %d", line, col)
	}
	return v, nil
}

// syntaxError converts a decoder error into a message with line and column.
func syntaxError(data []byte, err error) error {
	var se *json.SyntaxError
	switch {
	case errors.As(err, &se):
		line, col := position(data, int(se.Offset))
		return fmt.Errorf("syntax error at line %d, column %d: %s", line, col, se.Error())
	case errors.Is(err, io.ErrUnexpectedEOF):
		line, col := position(data, len(data))
		return fmt.Errorf("syntax error at line %d, column %d: unexpected end of JSON input", line, col)
	default:
		return err
	}
}

// position converts a byte offset (number of bytes consumed up to and
// including the offending one) into a 1-based line and column.
func position(data []byte, off int) (line, col int) {
	if off > len(data) {
		off = len(data)
	}
	if off < 1 {
		off = 1
	}
	prefix := data[:off]
	line = 1 + bytes.Count(prefix, []byte("\n"))
	col = off - (bytes.LastIndexByte(prefix, '\n') + 1)
	return line, col
}

// jsonFields maps the JSON key of every serialised field of struct type t to
// its field. Fields tagged "-" (the effective-config extras) are not part of
// the file format and therefore not accepted as keys.
func jsonFields(t reflect.Type) map[string]reflect.StructField {
	out := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f
	}
	return out
}

// rawMessage is the type of the Legacy fields, which take any JSON value.
var rawMessage = reflect.TypeFor[json.RawMessage]()

// checkKeys walks the generic JSON value v against type t and returns the
// first structural problem (unknown key, wrong JSON type) with its key path.
// Keys are visited in sorted order so the reported problem is deterministic.
func checkKeys(v any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if v == nil || t == rawMessage {
		return nil // null is a no-op for structs/scalars and empties slices/maps
	}
	switch t.Kind() {
	case reflect.Struct:
		return checkStruct(v, t, path)
	case reflect.Map:
		return checkMap(v, t, path)
	case reflect.Slice:
		return checkSlice(v, t, path)
	default:
		return checkScalar(v, t, path)
	}
}

func checkMap(v any, t reflect.Type, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return typeError(path, "object", v)
	}
	for _, k := range sortedKeys(obj) {
		if err := checkKeys(obj[k], t.Elem(), joinPath(path, k)); err != nil {
			return err
		}
	}
	return nil
}

func checkSlice(v any, t reflect.Type, path string) error {
	arr, ok := v.([]any)
	if !ok {
		return typeError(path, "array", v)
	}
	for i, e := range arr {
		if err := checkKeys(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func checkStruct(v any, t reflect.Type, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return typeError(path, "object", v)
	}
	fields := jsonFields(t)
	names := sortedKeys(fields)
	for _, k := range sortedKeys(obj) {
		f, ok := fields[k]
		if !ok {
			msg := fmt.Sprintf("unknown key %q", joinPath(path, k))
			if s := suggest(k, names); s != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", s)
			}
			return errors.New(msg)
		}
		if err := checkKeys(obj[k], f.Type, joinPath(path, k)); err != nil {
			return err
		}
	}
	return nil
}

func checkScalar(v any, t reflect.Type, path string) error {
	switch t.Kind() {
	case reflect.String:
		if _, ok := v.(string); !ok {
			return typeError(path, "string", v)
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			return typeError(path, "bool", v)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			return typeError(path, "integer", v)
		}
		if _, err := strconv.ParseInt(n.String(), 10, t.Bits()); err != nil {
			return fmt.Errorf("%s: expected integer, got %s", path, n.String())
		}
	}
	return nil
}

func typeError(path, want string, got any) error {
	if path == "" {
		path = "top level"
	}
	return fmt.Errorf("%s: expected %s, got %s", path, want, jsonTypeName(got))
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// suggest returns the candidate closest to key when it is within edit
// distance 2 (a typo), or "" when nothing is close enough to be helpful.
func suggest(key string, candidates []string) string {
	best, bestDist := "", 3
	for _, c := range candidates {
		if d := editDistance(key, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// checkDuplicateKeys rejects an object that repeats a key at any depth.
// encoding/json keeps the last value silently, which would let an untrusted
// .brooom.json hide a tightening key behind a later loosened copy (or the
// reverse), so the raw token stream is walked instead of the parsed value.
// data has already been parsed successfully, so token errors cannot occur
// and are only passed on defensively.
func checkDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return dupValue(dec, "")
}

// dupValue consumes one JSON value from dec, checking every object below it.
func dupValue(dec *json.Decoder, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '[' {
		for i := 0; dec.More(); i++ {
			if err := dupValue(dec, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		_, _ = dec.Token() // closing ]
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := kt.(string)
		if seen[key] {
			return fmt.Errorf("duplicate key %q", joinPath(path, key))
		}
		seen[key] = true
		if err := dupValue(dec, joinPath(path, key)); err != nil {
			return err
		}
	}
	_, _ = dec.Token() // closing }
	return nil
}
