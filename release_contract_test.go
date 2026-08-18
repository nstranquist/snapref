package snapref_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestReleaseVersionSurfacesMatchSemanticVersioning(t *testing.T) {
	versionRaw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(versionRaw))
	if !validSemanticVersion(version) {
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
	mainRaw, err := os.ReadFile(filepath.Join("cmd", "snapref", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	binding := regexp.MustCompile(`(?m)^const version = "([^"]+)"$`).FindSubmatch(mainRaw)
	if len(binding) != 2 {
		t.Fatal("cmd/snapref/main.go must contain exactly one simple version const")
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
		"EXTRACTION.json",
		"LICENSE",
		"NOTICE",
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
	if !strings.HasPrefix(module, "module github.com/nstranquist/snapref\n") {
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

var semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func validSemanticVersion(version string) bool {
	match := semanticVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return false
	}
	for _, identifier := range strings.Split(match[4], ".") {
		if len(identifier) < 2 || identifier[0] != '0' {
			continue
		}
		numeric := true
		for _, char := range identifier {
			if char < '0' || char > '9' {
				numeric = false
				break
			}
		}
		if numeric {
			return false
		}
	}
	return true
}

var thirdPartyDependencies = map[string]string{
	"github.com/nstranquist/processtree": "third_party/licenses/github.com/nstranquist/processtree/LICENSE",
	"github.com/chromedp/cdproto":        "third_party/licenses/github.com/chromedp/cdproto/LICENSE",
	"github.com/chromedp/chromedp":       "third_party/licenses/github.com/chromedp/chromedp/LICENSE",
	"github.com/chromedp/sysutil":        "third_party/licenses/github.com/chromedp/sysutil/LICENSE",
	"github.com/go-json-experiment/json": "third_party/licenses/github.com/go-json-experiment/json/LICENSE",
	"github.com/gobwas/httphead":         "third_party/licenses/github.com/gobwas/httphead/LICENSE",
	"github.com/gobwas/pool":             "third_party/licenses/github.com/gobwas/pool/LICENSE",
	"github.com/gobwas/ws":               "third_party/licenses/github.com/gobwas/ws/LICENSE",
	"go.starlark.net":                    "third_party/licenses/go.starlark.net/LICENSE",
	"go.yaml.in/yaml/v3":                 "third_party/licenses/go.yaml.in/yaml/v3/LICENSE",
	"golang.org/x/sys":                   "third_party/licenses/golang.org/x/sys/LICENSE",
	"golang.org/x/image":                 "third_party/licenses/golang.org/x/image/LICENSE",
	"golang.org/x/text":                  "third_party/licenses/golang.org/x/text/LICENSE",
}

func TestThirdPartyAttributionContract(t *testing.T) {
	noticesRaw, err := os.ReadFile("THIRD_PARTY_NOTICES.md")
	if err != nil {
		t.Fatal(err)
	}
	notices := string(noticesRaw)
	for module, license := range thirdPartyDependencies {
		if !strings.Contains(notices, "`"+module+"`") {
			t.Errorf("THIRD_PARTY_NOTICES.md does not identify %s", module)
		}
		if info, err := os.Lstat(license); err != nil || !info.Mode().IsRegular() {
			t.Errorf("license for %s is not a regular file: %v", module, err)
		}
	}
	if strings.Contains(notices, "gopkg.in/"+"yaml.v3") {
		t.Fatal("THIRD_PARTY_NOTICES.md contains the obsolete YAML module identity")
	}
	for _, name := range []string{
		"third_party/licenses/go/LICENSE",
		"third_party/licenses/go/PATENTS",
		"third_party/licenses/golang.org/x/sys/PATENTS",
		"third_party/licenses/golang.org/x/image/PATENTS",
		"third_party/licenses/golang.org/x/text/PATENTS",
	} {
		if info, err := os.Lstat(name); err != nil || !info.Mode().IsRegular() {
			t.Errorf("Go attribution file %s is not a regular file: %v", name, err)
		}
	}
	rootNotice, err := os.ReadFile("NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	yamlNotice, err := os.ReadFile("third_party/licenses/go.yaml.in/yaml/v3/NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	if string(rootNotice) != string(yamlNotice) || !strings.Contains(string(rootNotice), "Copyright 2011-2016 Canonical Ltd.") {
		t.Fatal("root NOTICE does not reproduce the reviewed go.yaml.in/yaml/v3 notice")
	}
}

func TestThirdPartyAttributionCoversBuiltPrograms(t *testing.T) {
	command := exec.Command(
		"go", "list", "-mod=readonly", "-deps",
		"-f", `{{with .Module}}{{if ne .Path "github.com/nstranquist/snapref"}}{{.Path}}{{end}}{{end}}`,
		"./cmd/snapref", "./cmd/snapref-release",
	)
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("enumerate built-program dependencies: %v: %s", err, output)
	}
	observed := make(map[string]bool)
	for _, module := range strings.Fields(string(output)) {
		observed[module] = true
	}
	var mismatches []string
	for module := range observed {
		if _, ok := thirdPartyDependencies[module]; !ok {
			mismatches = append(mismatches, "missing attribution for "+module)
		}
	}
	for module := range thirdPartyDependencies {
		if !observed[module] {
			mismatches = append(mismatches, "attribution is not in the built-program graph: "+module)
		}
	}
	sort.Strings(mismatches)
	if len(mismatches) > 0 {
		t.Fatal(strings.Join(mismatches, "\n"))
	}
}
