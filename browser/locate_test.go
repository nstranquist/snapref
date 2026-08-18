package browser

import (
	"errors"
	"os"
	"testing"
)

func TestLocateChromeRespectsEnvOverride(t *testing.T) {
	tmp, err := os.CreateTemp("", "fake-chrome-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	tmp.Close()

	t.Setenv("SNAPREF_CHROME", tmp.Name())
	got, err := LocateChrome()
	if err != nil {
		t.Fatalf("LocateChrome: %v", err)
	}
	if got != tmp.Name() {
		t.Fatalf("got %q, want %q", got, tmp.Name())
	}
}

func TestLocateChromeMissingEnvOverrideErrors(t *testing.T) {
	t.Setenv("SNAPREF_CHROME", "/no/such/path/chrome-xyz")
	_, err := LocateChrome()
	if err == nil {
		t.Fatal("expected error for missing env override path")
	}
}

func TestDoctorReturnsErrorWhenChromeMissing(t *testing.T) {
	t.Setenv("SNAPREF_CHROME", "/no/such/path/chrome-xyz")
	res := Doctor()
	if res.Error == "" {
		t.Fatalf("expected Error to be populated, got: %+v", res)
	}
	if res.Source != "env:SNAPREF_CHROME" {
		t.Errorf("Source = %q, want env tag", res.Source)
	}
}

func TestLocateChromeRetainsNDevAdapterAlias(t *testing.T) {
	t.Setenv("SNAPREF_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "/no/such/legacy-chrome")
	_, err := LocateChrome()
	if err == nil || Doctor().Source != "env:NDEV_BROWSER_CHROME" {
		t.Fatalf("legacy adapter alias was not retained: %v", err)
	}
}

func TestChromeSupportsLoadExtension(t *testing.T) {
	cases := map[string]bool{
		"Google Chrome 147.0.7727.138":            false,
		"Google Chrome for Testing 147.0.7727.15": true,
		"Chromium 131.0.6778.33":                  true,
		"Google Chrome Canary 147.0.7727.10":      true,
		"Google Chrome Beta 147.0.7727.20":        true,
		"Google Chrome Dev 147.0.7727.30":         true,
	}
	for version, want := range cases {
		if got := chromeSupportsLoadExtension(version); got != want {
			t.Errorf("chromeSupportsLoadExtension(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestLocateChromeWithoutExplicitOverrideFailsClosed(t *testing.T) {
	t.Setenv("SNAPREF_CHROME", "")
	t.Setenv("NDEV_BROWSER_CHROME", "")
	if _, err := LocateChrome(); !errors.Is(err, ErrNoChrome) {
		t.Fatalf("LocateChrome without override error = %v, want ErrNoChrome", err)
	}
}

func TestValidateURL(t *testing.T) {
	cases := map[string]bool{
		"":                    false,
		"http://example.com":  true,
		"https://example.com": true,
		"ftp://example.com":   false,
		"about:blank":         false,
		"abc":                 false,
	}
	for in, wantOK := range cases {
		err := validateURL(in)
		gotOK := err == nil
		if gotOK != wantOK {
			t.Errorf("validateURL(%q) ok=%v, want %v (err=%v)", in, gotOK, wantOK, err)
		}
	}
}
