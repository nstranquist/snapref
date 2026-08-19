package browsermcp

import (
	"strings"
	"testing"
)

func TestPolicyWorkflowReceipt(t *testing.T) {
	receipt, err := RunPolicyWorkflow()
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.OK || receipt.Workflow != "mcp-policy" || receipt.Schema != PolicyWorkflowSchema {
		t.Fatalf("receipt = %+v", receipt)
	}
	if !receipt.Snapshot.OK || !receipt.Snapshot.Allowed || receipt.Snapshot.Tool != "browser_snapshot" {
		t.Fatalf("snapshot = %+v", receipt.Snapshot)
	}
	if !strings.Contains(receipt.Snapshot.Response, policySnapshotRef) {
		t.Fatalf("snapshot response missing executable ref: %q", receipt.Snapshot.Response)
	}
	if receipt.ClickDenied.OK || receipt.ClickDenied.Allowed || !receipt.ClickDenied.Audited {
		t.Fatalf("click denied = %+v", receipt.ClickDenied)
	}
	if !strings.Contains(receipt.ClickDenied.Response, "disabled") {
		t.Fatalf("click denied response = %q", receipt.ClickDenied.Response)
	}
	if receipt.NavigateDenied.OK || receipt.NavigateDenied.Allowed || !receipt.NavigateDenied.Audited {
		t.Fatalf("navigate denied = %+v", receipt.NavigateDenied)
	}
	if !receipt.NavigateGranted.Allowed || !receipt.NavigateGranted.OK || !receipt.NavigateGranted.Audited {
		t.Fatalf("navigate granted = %+v", receipt.NavigateGranted)
	}
	if receipt.OriginBlocked.Allowed || receipt.OriginBlocked.OK || !receipt.OriginBlocked.Audited {
		t.Fatalf("origin blocked = %+v", receipt.OriginBlocked)
	}
	if !strings.Contains(receipt.OriginBlocked.Detail, "origin not allowlisted") &&
		!strings.Contains(receipt.OriginBlocked.Response, "allowlist") {
		t.Fatalf("origin blocked detail = %+v", receipt.OriginBlocked)
	}
}
