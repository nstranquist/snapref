# Workflow: policy-gated MCP

Drive, host-read, host-write, and origin grants are independent. This
demo does not launch Chrome. It runs the shipped MCP stdio server against
a fake driver and prints a receipt.

```sh
snapref demo mcp-policy
```

Expected receipt:

- `snapshot.ok: true` — `browser_snapshot` without `SNAPREF_MCP_ALLOW_DRIVE`
- `click_denied.allowed: false` — click refused; driver not called
- `navigate_denied.allowed: false` — navigate refused without drive
- `navigate_granted.allowed: true` — navigate with drive + `example.com`
- `origin_blocked.allowed: false` — off-list host refused; driver not called
