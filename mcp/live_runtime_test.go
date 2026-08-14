package browsermcp

import (
	"context"
	"os"
	"testing"

	"github.com/nstranquist/pageskein/browser"
)

func TestLiveManagedHeadlessShellStdioSmoke(t *testing.T) {
	if os.Getenv("PAGESKEIN_MCP_LIVE") != "1" {
		t.Skip("set PAGESKEIN_MCP_LIVE=1 to run the managed Headless Shell MCP smoke")
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
	if receipt.ResponsesDecoded != 2 || !receipt.SnapshotTextSeen || !receipt.SnapshotRefsSeen {
		t.Fatalf("unexpected smoke receipt: %+v", receipt)
	}
}
