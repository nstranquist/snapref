package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Session is a long-lived Chromium owned by the harness, addressable by
// name. Persists in a session file so it survives the launching process
// going away — subsequent `pageskein exec <name>` reattaches via CDP.
type Session struct {
	Name             string              `json:"name"`
	PID              int                 `json:"pid"`
	Port             int                 `json:"port"`
	ProfileDir       string              `json:"profile_dir"`
	ChromePath       string              `json:"chrome_path"`
	ProcessStartedAt time.Time           `json:"process_started_at,omitempty"`
	StartedAt        time.Time           `json:"started_at"`
	LastActivityAt   time.Time           `json:"last_activity_at,omitempty"`
	IdleTimeout      string              `json:"idle_timeout,omitempty"`
	LifecyclePolicy  LifecyclePolicy     `json:"lifecycle_policy,omitempty"`
	PurgeOnExpiry    bool                `json:"purge_profile_on_expiry,omitempty"`
	Headful          bool                `json:"headful"`
	WSURL            string              `json:"ws_url,omitempty"` // populated after first Open
	ExtensionID      string              `json:"extension_id,omitempty"`
	RuntimeKind      RuntimeKind         `json:"runtime_kind,omitempty"`
	RuntimeVersion   string              `json:"runtime_version,omitempty"`
	RuntimeManaged   bool                `json:"runtime_managed,omitempty"`
	RuntimeManifest  string              `json:"runtime_manifest_digest,omitempty"`
	RuntimeCaps      RuntimeCapabilities `json:"runtime_capabilities,omitempty"`
}

// LifecyclePolicy makes the owner-selected lifetime of a saved session
// explicit. The empty value is reserved for library callers that manage the
// process themselves; CLIs should always record either an idle lease or an
// explicit keep-alive decision.
type LifecyclePolicy string

const (
	LifecycleUnmanaged LifecyclePolicy = ""
	LifecycleIdle      LifecyclePolicy = "idle"
	LifecycleKeepAlive LifecyclePolicy = "keep_alive"
)

// EffectiveLifecyclePolicy preserves compatibility with lease records written
// before LifecyclePolicy was added.
func (s *Session) EffectiveLifecyclePolicy() LifecyclePolicy {
	if s.LifecyclePolicy != LifecycleUnmanaged {
		return s.LifecyclePolicy
	}
	if s.IdleTimeout != "" {
		return LifecycleIdle
	}
	return LifecycleUnmanaged
}

// IdleExpiryReason explains why a watchdog should continue or stop.
type IdleExpiryReason string

const (
	IdleExpiryActive             IdleExpiryReason = "active"
	IdleExpiryMissing            IdleExpiryReason = "missing"
	IdleExpiryGenerationReplaced IdleExpiryReason = "generation_replaced"
	IdleExpiryPolicyReplaced     IdleExpiryReason = "policy_replaced"
	IdleExpiryProcessGone        IdleExpiryReason = "process_gone"
	IdleExpiryOwnershipLost      IdleExpiryReason = "ownership_lost"
	IdleExpiryLeaseExpired       IdleExpiryReason = "idle_lease_expired"
)

// IdleExpiryResult is the atomic lifecycle decision for one watchdog poll.
type IdleExpiryResult struct {
	Done   bool
	Closed bool
	Reason IdleExpiryReason
}

// SetSessionIdleTimeout records the owner-defined idle lease for a persistent
// session. PageSkein itself does not choose a policy; adapters such as ndev can
// attach a watchdog and make that lifecycle visible in the session record.
func SetSessionIdleTimeout(name string, timeout time.Duration) (*Session, error) {
	return SetSessionIdlePolicy(name, timeout, false)
}

// SetSessionIdlePolicy records a crash-safe idle lease and whether its
// watchdog may remove the canonical profile when the lease expires.
func SetSessionIdlePolicy(name string, timeout time.Duration, purgeProfileOnExpiry bool) (*Session, error) {
	if timeout <= 0 {
		return nil, errors.New("idle timeout must be positive")
	}
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil {
		return nil, err
	}
	s.IdleTimeout = timeout.String()
	s.LastActivityAt = time.Now().UTC()
	s.LifecyclePolicy = LifecycleIdle
	s.PurgeOnExpiry = purgeProfileOnExpiry
	if err := SaveSession(s); err != nil {
		return nil, err
	}
	return s, nil
}

// SetSessionKeepAlive records an explicit persistent-session decision. This is
// intentionally separate from a missing policy so audits can distinguish an
// operator choice from an unmanaged library caller.
func SetSessionKeepAlive(name string) (*Session, error) {
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil {
		return nil, err
	}
	s.IdleTimeout = ""
	s.LastActivityAt = time.Time{}
	s.LifecyclePolicy = LifecycleKeepAlive
	s.PurgeOnExpiry = false
	if err := SaveSession(s); err != nil {
		return nil, err
	}
	return s, nil
}

// TouchSession renews a configured idle lease. Sessions without an idle policy
// are left untouched so standalone PageSkein callers retain their existing
// persistent-session semantics.
func TouchSession(name string) error {
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := LoadSession(name)
	if err != nil {
		return err
	}
	if s.IdleTimeout == "" {
		return nil
	}
	s.LastActivityAt = time.Now().UTC()
	return SaveSession(s)
}

// ExpireIdleSession atomically decides whether one watchdog generation may
// close a session. The per-session lifecycle lock spans the final activity
// check and close, so a concurrent TouchSession either renews first or observes
// the completed close; it can never renew between the watchdog check and kill.
func ExpireIdleSession(name string, expectedPID int, idleTimeout time.Duration, purgeProfile bool, now time.Time) (IdleExpiryResult, error) {
	if expectedPID <= 0 {
		return IdleExpiryResult{}, errors.New("expected PID must be positive")
	}
	if idleTimeout <= 0 {
		return IdleExpiryResult{}, errors.New("idle timeout must be positive")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	unlock, err := acquireSessionLifecycleLock(name, 5*time.Second)
	if err != nil {
		return IdleExpiryResult{}, err
	}
	defer unlock()

	s, err := LoadSession(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return IdleExpiryResult{Done: true, Reason: IdleExpiryMissing}, nil
		}
		return IdleExpiryResult{}, err
	}
	if s.PID != expectedPID {
		return IdleExpiryResult{Done: true, Reason: IdleExpiryGenerationReplaced}, nil
	}
	if s.EffectiveLifecyclePolicy() != LifecycleIdle {
		return IdleExpiryResult{Done: true, Reason: IdleExpiryPolicyReplaced}, nil
	}
	recordedTimeout, err := time.ParseDuration(s.IdleTimeout)
	if err != nil || recordedTimeout <= 0 || recordedTimeout != idleTimeout {
		return IdleExpiryResult{Done: true, Reason: IdleExpiryPolicyReplaced}, nil
	}
	// The durable record is the authority for destructive profile cleanup. A
	// stale or forged watchdog argument can never broaden that permission.
	purgeProfile = s.PurgeOnExpiry
	if !s.IsAlive() {
		if err := removeSessionStateLocked(s, purgeProfile); err != nil {
			return IdleExpiryResult{}, err
		}
		return IdleExpiryResult{Done: true, Reason: IdleExpiryProcessGone}, nil
	}
	lastActivity := s.LastActivityAt
	if lastActivity.IsZero() {
		lastActivity = s.StartedAt
	}
	if lastActivity.IsZero() || now.Sub(lastActivity) < idleTimeout {
		return IdleExpiryResult{Reason: IdleExpiryActive}, nil
	}
	owned, err := s.ownsLaunchedProcess()
	if err != nil {
		return IdleExpiryResult{}, err
	}
	if !owned {
		if err := removeSessionStateLocked(s, purgeProfile); err != nil {
			return IdleExpiryResult{}, err
		}
		return IdleExpiryResult{Done: true, Reason: IdleExpiryOwnershipLost}, nil
	}
	if err := closeLocked(name, purgeProfile); err != nil {
		return IdleExpiryResult{}, err
	}
	return IdleExpiryResult{Done: true, Closed: true, Reason: IdleExpiryLeaseExpired}, nil
}

func removeSessionStateLocked(s *Session, purgeProfile bool) error {
	if err := RemoveSession(s.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := RemoveSnapshotRefs(s.Name); err != nil {
		return fmt.Errorf("remove snapshot refs: %w", err)
	}
	if purgeProfile {
		profileDir, err := ProfileDirFor(s.Name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(profileDir); err != nil {
			return fmt.Errorf("remove profile %s: %w", profileDir, err)
		}
	}
	return nil
}

// SessionStatus is the live view of a session: file metadata + whether
// the pid is alive + whether the CDP endpoint is reachable.
type SessionStatus struct {
	Session
	Alive     bool   `json:"alive"`
	Reachable bool   `json:"reachable"`
	HTTPURL   string `json:"http_url"`
	Error     string `json:"error,omitempty"`
}

var sessionNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateSessionName rejects names that would escape the per-session
// directory or look like flags/paths.
func ValidateSessionName(name string) error {
	if name == "" {
		return errors.New("session name is required")
	}
	if !sessionNameRE.MatchString(name) {
		return fmt.Errorf("session name %q must match [a-z0-9][a-z0-9_-]{0,63}", name)
	}
	return nil
}

// SessionRoot returns ~/.pageskein/browser by default. Override via
// PAGESKEIN_HOME (used by tests + agents that want isolation).
func SessionRoot() (string, error) {
	if env := os.Getenv("PAGESKEIN_HOME"); env != "" {
		return env, nil
	}
	// NDEV_BROWSER_HOME is the compatibility seam for the shipped ndev adapter.
	// PageSkein-native callers should prefer PAGESKEIN_HOME.
	if env := os.Getenv("NDEV_BROWSER_HOME"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pageskein", "browser"), nil
}

func sessionsDir() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sessions"), nil
}

func profilesDir() (string, error) {
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "profiles"), nil
}

// SessionPath is the JSON file for a named session.
func SessionPath(name string) (string, error) {
	if err := ValidateSessionName(name); err != nil {
		return "", err
	}
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// LoadSession reads the session file. Returns (nil, fs.ErrNotExist) if no
// such session exists; callers can errors.Is against os.ErrNotExist.
func LoadSession(name string) (*Session, error) {
	p, err := SessionPath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("session %s: corrupt JSON: %w", name, err)
	}
	if err := validateSessionRecord(name, &s); err != nil {
		return nil, fmt.Errorf("session %s: invalid record: %w", name, err)
	}
	return &s, nil
}

func validateSessionRecord(expectedName string, s *Session) error {
	if s == nil {
		return errors.New("record is nil")
	}
	if s.Name != expectedName {
		return fmt.Errorf("embedded name %q does not match filename %q", s.Name, expectedName)
	}
	if s.ProfileDir != "" {
		expectedProfile, err := ProfileDirFor(expectedName)
		if err != nil {
			return err
		}
		actualAbs, err := filepath.Abs(s.ProfileDir)
		if err != nil {
			return fmt.Errorf("resolve profile directory: %w", err)
		}
		expectedAbs, err := filepath.Abs(expectedProfile)
		if err != nil {
			return fmt.Errorf("resolve canonical profile directory: %w", err)
		}
		if filepath.Clean(actualAbs) != filepath.Clean(expectedAbs) {
			return fmt.Errorf("profile directory %q is not canonical %q", s.ProfileDir, expectedProfile)
		}
	}
	switch s.LifecyclePolicy {
	case LifecycleUnmanaged:
		// Legacy records with IdleTimeout set are interpreted as idle leases.
	case LifecycleIdle:
		if s.IdleTimeout == "" {
			return errors.New("idle lifecycle policy has no timeout")
		}
	case LifecycleKeepAlive:
		if s.IdleTimeout != "" {
			return errors.New("keep-alive lifecycle policy has an idle timeout")
		}
		if s.PurgeOnExpiry {
			return errors.New("keep-alive lifecycle policy cannot purge on expiry")
		}
	default:
		return fmt.Errorf("unknown lifecycle policy %q", s.LifecyclePolicy)
	}
	if s.IdleTimeout != "" {
		timeout, err := time.ParseDuration(s.IdleTimeout)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("invalid idle timeout %q", s.IdleTimeout)
		}
	} else if s.PurgeOnExpiry {
		return errors.New("profile purge on expiry requires an idle timeout")
	}
	if s.RuntimeKind != "" {
		if s.RuntimeKind == RuntimeAuto {
			return errors.New("session runtime kind cannot be auto")
		}
		if _, err := ParseRuntimeKind(string(s.RuntimeKind)); err != nil {
			return err
		}
		if s.RuntimeVersion == "" || s.ChromePath == "" {
			return errors.New("session runtime identity is incomplete")
		}
		if s.RuntimeManaged {
			if s.RuntimeKind != RuntimeHeadlessShell && s.RuntimeKind != RuntimeChromeForTesting {
				return errors.New("managed session has a non-managed runtime kind")
			}
			if !validSHA256(s.RuntimeManifest) {
				return errors.New("managed session has an invalid runtime manifest digest")
			}
		} else if s.RuntimeKind != RuntimeExternal {
			return errors.New("unmanaged session runtime must be external")
		}
	}
	return nil
}

// SaveSession writes (or replaces) the session file atomically.
func SaveSession(s *Session) error {
	if s == nil {
		return errors.New("session is nil")
	}
	if err := ValidateSessionName(s.Name); err != nil {
		return err
	}
	if err := validateSessionRecord(s.Name, s); err != nil {
		return fmt.Errorf("invalid session record: %w", err)
	}
	p, err := SessionPath(s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-session-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, p)
}

// RemoveSession deletes the session file. Does not kill the process or
// remove the profile directory — callers do that explicitly.
func RemoveSession(name string) error {
	p, err := SessionPath(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// ListSessions returns all known sessions in deterministic order.
func ListSessions() ([]Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		s, err := LoadSession(name)
		if err != nil {
			return nil, fmt.Errorf("load session registry entry %q: %w", e.Name(), err)
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// IsAlive returns true if the recorded pid is still running using the native
// process probe for the host operating system.
func (s *Session) IsAlive() bool {
	return processAlive(s.PID)
}

// OwnsProcess verifies that the live PID still belongs to the Chromium process
// launched for this exact session profile. Liveness alone is insufficient:
// operating systems reuse PIDs, so signaling a merely-live PID can terminate an
// unrelated process after Chromium exits.
func (s *Session) OwnsProcess() (bool, error) {
	owned, err := s.ownsLaunchedProcess()
	if err != nil || !owned {
		return owned, err
	}
	return sessionRuntimeIdentityValid(s)
}

// ownsLaunchedProcess is the destructive-operation authority. It intentionally
// ignores mutable runtime files: a repaired or externally corrupted install
// must never make PageSkein lose the ability to stop the exact process/profile
// generation it launched.
func (s *Session) ownsLaunchedProcess() (bool, error) {
	return sessionOwnsProcess(s)
}

// ProfileDirFor returns the canonical profile path for a session name.
// Used at open time before the session file exists yet.
func ProfileDirFor(name string) (string, error) {
	if err := ValidateSessionName(name); err != nil {
		return "", err
	}
	dir, err := profilesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// SignalGracefulStop sends SIGTERM, waits up to grace, then SIGKILL.
// Returns nil if the process was already gone or stopped cleanly.
func (s *Session) SignalGracefulStop(grace time.Duration) error {
	return sessionSignalGracefulStop(s, grace)
}
