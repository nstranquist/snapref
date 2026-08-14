package pageskein_test

import (
	"os"
	"strings"
	"testing"
)

func TestStandaloneProductContract(t *testing.T) {
	for _, name := range []string{
		".nicos/product.yaml",
		"RELEASE_CHECKLIST.md",
		"portfolio/manifest.yaml",
	} {
		info, err := os.Lstat(name)
		if err != nil {
			t.Fatalf("standalone product file %s: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("standalone product file %s is not regular", name)
		}
	}

	product, err := os.ReadFile(".nicos/product.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(product), "id: product.pageskein\n") {
		t.Fatalf("unexpected product identity:\n%s", product)
	}
	if !strings.Contains(string(product), "launch: built\n") {
		t.Fatalf("product must remain local-only until release gates close:\n%s", product)
	}
}
