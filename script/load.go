package browserscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Source labels for the run manifest, derived from the file extension.
const (
	SourceYAML = "yaml"
	SourceJSON = "json"
	SourceStar = "star"
	SourceSDK  = "sdk"
)

// LoadFile reads a playbook from disk and returns a Plan. The format is chosen
// by extension: .yaml/.yml, .json, or .star (Starlark). vars are merged into the
// plan (overriding plan-declared vars) for YAML/JSON, and passed as the `params`
// global for Starlark.
func LoadFile(path string, vars map[string]string) (Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".star":
		// Starlark executes eagerly (built-ins drive the browser live), so it
		// does not lower to a static Plan. The cmd layer routes .star to
		// RunStarlarkFile instead of LoadFile.
		return Plan{}, fmt.Errorf("starlark playbooks run eagerly; use the starlark runner, not LoadFile")
	case ".json":
		p, err := LoadJSON(b)
		if err != nil {
			return Plan{}, err
		}
		return mergeVars(p, vars), nil
	case ".yaml", ".yml", "":
		p, err := LoadYAML(b)
		if err != nil {
			return Plan{}, err
		}
		return mergeVars(p, vars), nil
	default:
		return Plan{}, fmt.Errorf("unsupported playbook extension %q (want .yaml, .json, or .star)", filepath.Ext(path))
	}
}

// SourceForPath returns the manifest source label for a playbook path.
func SourceForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".star":
		return SourceStar
	case ".json":
		return SourceJSON
	default:
		return SourceYAML
	}
}

// LoadJSON decodes a JSON playbook into a Plan, rejecting unknown fields so
// typos surface immediately.
func LoadJSON(b []byte) (Plan, error) {
	var p Plan
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Plan{}, fmt.Errorf("parse playbook: %w", err)
	}
	return p, nil
}

// LoadYAML decodes a YAML playbook into a Plan. It routes through JSON so the
// json tags (and Duration.UnmarshalJSON) are the single source of truth — YAML
// and JSON playbooks therefore have identical field names and semantics.
func LoadYAML(b []byte) (Plan, error) {
	var raw any
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return Plan{}, fmt.Errorf("parse playbook yaml: %w", err)
	}
	jb, err := json.Marshal(normalizeYAML(raw))
	if err != nil {
		return Plan{}, err
	}
	return LoadJSON(jb)
}

// normalizeYAML converts any map[interface{}]interface{} (possible with some
// YAML shapes) into map[string]interface{} so json.Marshal accepts it.
func normalizeYAML(v any) any {
	switch m := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = normalizeYAML(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[fmt.Sprintf("%v", k)] = normalizeYAML(val)
		}
		return out
	case []any:
		for i := range m {
			m[i] = normalizeYAML(m[i])
		}
		return m
	default:
		return v
	}
}

func mergeVars(p Plan, vars map[string]string) Plan {
	if len(vars) == 0 {
		return p
	}
	if p.Vars == nil {
		p.Vars = map[string]string{}
	}
	for k, v := range vars {
		p.Vars[k] = v
	}
	return p
}
