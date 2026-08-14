package pageskein_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseVersionSurfacesMatchSemanticVersioning(t *testing.T) {
	versionRaw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(versionRaw))
	semver := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	if !semver.MatchString(version) {
		t.Fatalf("VERSION %q is not Semantic Versioning 2.0.0", version)
	}
	packageRaw, err := os.ReadFile("package.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
		Private bool   `json:"private"`
	}
	if err := json.Unmarshal(packageRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	mainRaw, err := os.ReadFile(filepath.Join("cmd", "pageskein", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	binding := regexp.MustCompile(`(?m)^const version = "([^"]+)"$`).FindSubmatch(mainRaw)
	if len(binding) != 2 {
		t.Fatal("cmd/pageskein/main.go must contain exactly one simple version const")
	}
	if manifest.Version != version || string(binding[1]) != version {
		t.Fatalf("release version drift: VERSION=%s package.json=%s Go=%s", version, manifest.Version, binding[1])
	}
	if !manifest.Private {
		t.Fatal("package.json must remain private until publication is explicitly approved")
	}
}

func TestReleaseSchemasAreJSONSchemaV2Contracts(t *testing.T) {
	for _, name := range []string{"telemetry-v2.schema.json", "script-run-v2.schema.json"} {
		body, err := os.ReadFile(filepath.Join("schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			ID       string         `json:"$id"`
			Required []string       `json:"required"`
			Props    map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(body, &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if schema.ID == "" || len(schema.Required) == 0 {
			t.Fatalf("%s is missing a stable id or required contract", name)
		}
		versionProperty, ok := schema.Props["schema_version"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema_version property has type %T, want object", name, schema.Props["schema_version"])
		}
		version, ok := versionProperty["const"].(float64)
		if !ok || version != 2 {
			t.Fatalf("%s schema_version const = %v, want 2", name, version)
		}
	}
}

func TestReleaseRepositoryContract(t *testing.T) {
	for _, name := range []string{
		"AGENTS.md",
		"CHANGELOG.md",
		"LICENSE",
		"Makefile",
		"README.md",
		"SECURITY.md",
		"THIRD_PARTY_NOTICES.md",
		"VERSION",
		"docs/VERSIONING.md",
	} {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatalf("release file %s: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("release file %s is not a regular file", name)
		}
	}

	moduleBody, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	module := string(moduleBody)
	if !strings.HasPrefix(module, "module github.com/nstranquist/pageskein\n") {
		t.Fatalf("unexpected module identity:\n%s", module)
	}
	const localProcessTree = "replace github.com/nstranquist/processtree => ../processtree"
	if !strings.Contains(module, localProcessTree) || strings.Count(module, "replace ") != 1 {
		t.Fatalf("local release candidate must have only the sibling ProcessTree replacement:\n%s", module)
	}
	if strings.Contains(module, "nicos.tools/") {
		t.Fatalf("standalone module contains a private nicos.tools dependency:\n%s", module)
	}

	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "verify-release:") || !strings.Contains(string(makefile), "GOWORK=off") {
		t.Fatalf("Makefile is missing standalone release verification:\n%s", makefile)
	}
}
