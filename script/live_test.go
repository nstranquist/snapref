package browserscript

import (
	"context"
	"os"
	"testing"

	"github.com/nstranquist/pageskein/browser"
)

// TestLiveAgainstRealSession is an opt-in end-to-end smoke that drives a real
// Chrome session. It is skipped by default (and in `make verify` / `make
// test-go`); run it explicitly with:
//
//	PAGESKEIN_SCRIPT_LIVE=1 go test -tags sqlite_fts5 \
//	  -run TestLiveAgainstRealSession ./script/
//
// It needs the selected managed Headless Shell runtime.
func TestLiveAgainstRealSession(t *testing.T) {
	if os.Getenv("PAGESKEIN_SCRIPT_LIVE") == "" {
		t.Skip("set PAGESKEIN_SCRIPT_LIVE=1 to run the live browser smoke")
	}
	receipt, err := RunRuntimeConsumerSmoke(context.Background(), func(opts browser.OpenOptions) (*browser.Session, error) {
		session, err := browser.Open(opts)
		if err != nil {
			return nil, err
		}
		managed, err := browser.SetSessionKeepAlive(session.Name)
		if err != nil {
			_ = browser.Close(session.Name, true)
		}
		return managed, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Steps != 8 || !receipt.ManifestPersisted {
		t.Fatalf("unexpected smoke receipt: %+v", receipt)
	}
}
