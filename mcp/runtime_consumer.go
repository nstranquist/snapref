package browsermcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nstranquist/snapref/browser"
)

// RuntimeConsumerSmokeReceipt is the typed result of the managed-runtime MCP
// smoke executed in-process by acceptance producers.
type RuntimeConsumerSmokeReceipt struct {
	RuntimeKind      browser.RuntimeKind     `json:"runtime_kind"`
	ResponsesDecoded int                     `json:"responses_decoded"`
	SnapshotTextSeen bool                    `json:"snapshot_text_seen"`
	SnapshotRefsSeen bool                    `json:"snapshot_refs_seen"`
	LifecyclePolicy  browser.LifecyclePolicy `json:"lifecycle_policy"`
}

// RunRuntimeConsumerSmoke proves that the linked MCP server can initialize and
// snapshot a real page through a managed Headless Shell session.
func RunRuntimeConsumerSmoke(ctx context.Context, open func(browser.OpenOptions) (*browser.Session, error)) (RuntimeConsumerSmokeReceipt, error) {
	if open == nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("managed session opener is required")
	}
	resolved, err := browser.ResolveRuntime(browser.RuntimeRequest{Kind: browser.RuntimeHeadlessShell})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("resolve managed Headless Shell: %w", err)
	}
	sessionName := fmt.Sprintf("runtime-consumer-mcp-%d", os.Getpid())
	session, err := open(browser.OpenOptions{Name: sessionName, Runtime: browser.RuntimeHeadlessShell, ReadyTimeout: 30 * time.Second})
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("open managed session: %w", err)
	}
	defer browser.Close(sessionName, true)
	if session.EffectiveLifecyclePolicy() == browser.LifecycleUnmanaged {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("managed session opener returned no lifecycle policy")
	}
	if err := browser.RemoteNavigate(ctx, session, `data:text/html,<button id="live">Live MCP</button>`, 15*time.Second); err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("navigate managed session: %w", err)
	}

	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"browser_snapshot","arguments":{"session":%q}}}`, sessionName) + "\n",
	)
	var output bytes.Buffer
	if err := Serve(input, &output, Options{Driver: NewDriver()}); err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("serve MCP smoke: %w", err)
	}
	decoder := json.NewDecoder(&output)
	var initialize rpcResponse
	if err := decoder.Decode(&initialize); err != nil || initialize.Error != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("decode initialize response: rpc_error=%v decode_error=%v", initialize.Error, err)
	}
	var snapshot rpcResponse
	if err := decoder.Decode(&snapshot); err != nil || snapshot.Error != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("decode snapshot response: rpc_error=%v decode_error=%v", snapshot.Error, err)
	}
	encoded, err := json.Marshal(snapshot.Result)
	if err != nil {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("encode snapshot result: %w", err)
	}
	textSeen := bytes.Contains(encoded, []byte("Live MCP"))
	refsSeen := bytes.Contains(encoded, []byte("sr-"))
	if !textSeen || !refsSeen {
		return RuntimeConsumerSmokeReceipt{}, fmt.Errorf("snapshot missing managed page inventory: text=%t refs=%t", textSeen, refsSeen)
	}
	return RuntimeConsumerSmokeReceipt{RuntimeKind: resolved.Kind, ResponsesDecoded: 2, SnapshotTextSeen: textSeen, SnapshotRefsSeen: refsSeen, LifecyclePolicy: session.EffectiveLifecyclePolicy()}, nil
}
