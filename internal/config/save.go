package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// Save validates cfg and writes it to path atomically, creating parent
// directories (0700). Only values that differ from Default() are written
// (plus "version"), so future changes of a default reach users who never
// customised that value; arrays and maps are compared and written whole. An
// invalid configuration is never written.
func Save(path string, cfg *Config) error {
	return save(path, cfg, false)
}

// SaveFull is like Save but writes the complete document including defaults
// (used by `brooom config init`).
func SaveFull(path string, cfg *Config) error {
	return save(path, cfg, true)
}

func save(path string, cfg *Config, full bool) error {
	c := *cfg
	c.Version = CurrentVersion
	if err := c.Validate(); err != nil {
		return fmt.Errorf("refusing to write %s: %w", path, err)
	}
	data, err := Marshal(&c, full)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// Marshal renders cfg as indented JSON with a trailing newline. With full it
// is the complete document (every key, struct order); otherwise only values
// that differ from Default() and "version". It does not validate.
func Marshal(cfg *Config, full bool) ([]byte, error) {
	c := *cfg
	c.Version = CurrentVersion
	if full {
		return indent(&c)
	}
	cur, err := toGeneric(&c)
	if err != nil {
		return nil, err
	}
	def, err := toGeneric(Default())
	if err != nil {
		return nil, err
	}
	diff := diffStruct(reflect.TypeOf(c), cur, def)
	diff["version"] = cur["version"]
	return indent(diff)
}

func indent(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// toGeneric converts v to generic JSON maps; numbers stay json.Number so
// large byte counts survive comparison and re-encoding exactly.
func toGeneric(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// diffStruct returns the entries of cur that differ from def. It recurses
// only into struct-typed fields (per-detector blocks, thresholds, ...);
// slices and maps are compared and kept whole so that a written list or map
// fully replaces the default on load, matching the merge semantics of Load.
func diffStruct(t reflect.Type, cur, def map[string]any) map[string]any {
	out := map[string]any{}
	fields := jsonFields(t)
	for k, v := range cur {
		dv, has := def[k]
		if has && reflect.DeepEqual(v, dv) {
			continue
		}
		sub, isObj := v.(map[string]any)
		dsub, defObj := dv.(map[string]any)
		if f := fields[k]; isObj && defObj && f.Type.Kind() == reflect.Struct {
			if d := diffStruct(f.Type, sub, dsub); len(d) > 0 {
				out[k] = d
			}
			continue
		}
		out[k] = v
	}
	return out
}

// writeFileAtomic writes data to path so that readers see either the old or
// the complete new content: a temp file in the same directory (same
// filesystem, so the rename is atomic) is written, fsynced, given mode and
// renamed over the target. The temp file is removed on any failure and an
// existing target stays untouched.
func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".brooom-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp.Name(), err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
