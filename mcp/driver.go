// Package browsermcp is the agent-facing MCP surface over `pageskein`: it
// lets a Perfect World / Claude / Codex agent DRIVE an already-open browser
// session (navigate / click / type) and read it back (snapshot / text /
// screenshot / list), so the read-only browserobserve panel can show the
// agent's activity live. browser_snapshot is the ungated "eyes on the page"
// discovery tool — a structured inventory of interactive elements with unique
// selectors — so an agent can find what to act on without hand-writing JS.
//
// This is deliberately the opposite of the browserobserve core's read-only law:
// driving actuates a real browser. So it is gated. Actuation tools are DISABLED
// by default and only run when the operator explicitly opts in
// (PAGESKEIN_MCP_ALLOW_DRIVE=1); every privileged attempt — allowed or
// refused — is appended to an audit log. Read-only tools (list / text / managed
// screenshot) are always available. Caller-selected host reads/writes have
// separate explicit gates. The agent never launches or closes sessions; the
// operator opens them with `pageskein open`.
package browsermcp

import "github.com/nstranquist/pageskein/browser"

// The Driver surface and its production implementation live in internal/browser
// so both this MCP server and the browserscript engine consume one definition.
// These aliases keep the historical browsermcp.* spellings stable.

// SessionInfo is a read-only view of one open browser session.
type SessionInfo = browser.SessionInfo

// Driver is the actuation + read surface the MCP server exposes.
type Driver = browser.Driver

// NewDriver returns the production driver backed by internal/browser.
func NewDriver() Driver { return browser.NewRemoteDriver() }
