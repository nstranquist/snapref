package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	snapshotRefSchemaVersion          = 2
	snapshotRefTransitionGrace        = time.Second
	snapshotRefTransitionProbe        = 250 * time.Millisecond
	snapshotRefTransitionPollInterval = 50 * time.Millisecond
)

var executableSnapshotRefRE = regexp.MustCompile(`^(sr-[0-9a-f]{12}):(e[1-9][0-9]*)$`)
var executableSnapshotRefPrefixRE = regexp.MustCompile(`^sr-[0-9a-f]{12}`)

var (
	ErrMalformedSnapshotRef = errors.New("malformed snapshot ref")
	ErrStaleSnapshotRef     = errors.New("stale snapshot ref")
)

type SnapshotRefError struct {
	Ref    string
	Reason string
	Err    error
}

func (e *SnapshotRefError) Error() string {
	return fmt.Sprintf("snapshot ref %q %s: %v", e.Ref, e.Reason, e.Err)
}

func (e *SnapshotRefError) Unwrap() error { return e.Err }

type snapshotRefTarget struct {
	Ref         string         `json:"ref"`
	Selector    string         `json:"selector"`
	Context     []SnapshotRoot `json:"context,omitempty"`
	Fingerprint string         `json:"fingerprint"`
}

type snapshotRefStore struct {
	SchemaVersion int                          `json:"schema_version"`
	Session       string                       `json:"session"`
	Revision      string                       `json:"revision"`
	URL           string                       `json:"url"`
	CapturedAt    time.Time                    `json:"captured_at"`
	Targets       map[string]snapshotRefTarget `json:"targets"`
}

// ValidateExecutableSnapshotRef checks the explicit ref-input contract. CSS
// selector positions should call ResolveTarget instead because an ordinary
// custom-element selector may legitimately begin with "sr-".
func ValidateExecutableSnapshotRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if executableSnapshotRefRE.MatchString(ref) {
		return nil
	}
	return &SnapshotRefError{Ref: ref, Reason: "is malformed", Err: ErrMalformedSnapshotRef}
}

func snapshotRefsPath(session string) (string, error) {
	if err := ValidateSessionName(session); err != nil {
		return "", err
	}
	root, err := SessionRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "snapshot-refs", session+".json"), nil
}

func RemoveSnapshotRefs(session string) error {
	path, err := snapshotRefsPath(session)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SaveSnapshotRefs atomically replaces the executable-ref mapping for a
// session. Replacing the mapping is the stale-ref boundary: a prior revision
// can never resolve after a newer, different inventory is captured.
func SaveSnapshotRefs(session string, snapshot *PageSnapshot) error {
	if snapshot == nil || snapshot.Revision == "" {
		return errors.New("snapshot revision is required")
	}
	path, err := snapshotRefsPath(session)
	if err != nil {
		return err
	}
	store := snapshotRefStore{
		SchemaVersion: snapshotRefSchemaVersion,
		Session:       session,
		Revision:      snapshot.Revision,
		URL:           snapshot.URL,
		CapturedAt:    time.Now().UTC(),
		Targets:       make(map[string]snapshotRefTarget, len(snapshot.Elements)),
	}
	for _, element := range snapshot.Elements {
		store.Targets[element.Ref] = snapshotRefTarget{
			Ref:         element.Ref,
			Selector:    element.Selector,
			Context:     element.Context,
			Fingerprint: element.Fingerprint,
		}
	}
	body, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-snapshot-refs-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func loadSnapshotRefs(session string) (*snapshotRefStore, error) {
	path, err := snapshotRefsPath(session)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var store snapshotRefStore
	if err := json.Unmarshal(body, &store); err != nil {
		return nil, fmt.Errorf("decode snapshot refs: %w", err)
	}
	if store.SchemaVersion != snapshotRefSchemaVersion || store.Session != session || store.Revision == "" {
		return nil, errors.New("snapshot ref store has an unsupported or mismatched contract")
	}
	return &store, nil
}

// ResolveTarget accepts either a normal CSS selector or an executable snapshot
// ref. Refs are revision-checked and their semantic fingerprint is re-read from
// the live DOM before the selector is returned to an actuation primitive. A
// short same-fingerprint transition can recover, but SnapRef never relocates a
// ref or accepts a changed semantic target.
func ResolveTarget(ctx context.Context, session *Session, target string, timeout time.Duration) (string, error) {
	target = strings.TrimSpace(target)
	match := executableSnapshotRefRE.FindStringSubmatch(target)
	if match == nil {
		// Reserve only the unambiguous revision prefix. Treating every "sr-"
		// prefix as a ref breaks valid CSS custom-element selectors such as
		// sr-button and violates selector compatibility.
		if executableSnapshotRefPrefixRE.MatchString(target) {
			return "", &SnapshotRefError{Ref: target, Reason: "is malformed", Err: ErrMalformedSnapshotRef}
		}
		return target, nil
	}
	store, err := loadSnapshotRefs(session.Name)
	if err != nil {
		return "", &SnapshotRefError{Ref: target, Reason: "cannot load the latest snapshot", Err: ErrStaleSnapshotRef}
	}
	if match[1] != store.Revision {
		return "", &SnapshotRefError{Ref: target, Reason: fmt.Sprintf("was superseded by revision %s", store.Revision), Err: ErrStaleSnapshotRef}
	}
	stored, ok := store.Targets[target]
	if !ok {
		return "", &SnapshotRefError{Ref: target, Reason: "is unknown in the latest snapshot", Err: ErrStaleSnapshotRef}
	}

	started := time.Now()
	deadline := started.Add(snapshotRefTransitionGrace)
	if timeout > 0 && timeout < snapshotRefTransitionGrace {
		deadline = started.Add(timeout)
	}
	contextDeadline := false
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
		contextDeadline = true
	}

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		latest, err := loadSnapshotRefs(session.Name)
		if err != nil {
			return "", &SnapshotRefError{Ref: target, Reason: "cannot confirm the latest snapshot during transition retry", Err: ErrStaleSnapshotRef}
		}
		if latest.Revision != store.Revision {
			return "", &SnapshotRefError{Ref: target, Reason: fmt.Sprintf("was superseded by revision %s", latest.Revision), Err: ErrStaleSnapshotRef}
		}
		latestStored, ok := latest.Targets[target]
		if !ok {
			return "", &SnapshotRefError{Ref: target, Reason: "is unknown in the latest snapshot", Err: ErrStaleSnapshotRef}
		}
		if latest.URL != store.URL || !snapshotRefTargetsEqual(latestStored, stored) {
			return "", &SnapshotRefError{Ref: target, Reason: "changed in the latest snapshot mapping; take a new snapshot", Err: ErrStaleSnapshotRef}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		probeTimeout := min(remaining, snapshotRefTransitionProbe)
		var raw rawSnapshot
		probeErr := RemoteEval(ctx, session, "", snapshotJSExpr(snapshotHardCap, false), &raw, probeTimeout)
		if probeErr == nil {
			if raw.URL != store.URL {
				return "", &SnapshotRefError{Ref: target, Reason: "belongs to a page whose URL changed; take a new snapshot", Err: ErrStaleSnapshotRef}
			}
			live, matches := liveSnapshotRefTarget(raw, stored)
			if matches > 1 {
				return "", &SnapshotRefError{Ref: target, Reason: "matches multiple live elements and is ambiguous; take a new snapshot", Err: ErrStaleSnapshotRef}
			}
			if matches == 1 && snapshotElementFingerprint(live) == stored.Fingerprint {
				return encodeTargetLocator(TargetLocator{Selector: stored.Selector, Context: stored.Context})
			}
		} else if err := ctx.Err(); err != nil {
			return "", err
		}

		remaining = time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if err := sleepCtx(ctx, min(remaining, snapshotRefTransitionPollInterval)); err != nil {
			return "", err
		}
	}
	if contextDeadline {
		return "", context.DeadlineExceeded
	}
	retriedFor := time.Since(started).Round(10 * time.Millisecond)
	return "", &SnapshotRefError{
		Ref:    target,
		Reason: fmt.Sprintf("did not recover its live element fingerprint after %s of transition retry; take a new snapshot", retriedFor),
		Err:    ErrStaleSnapshotRef,
	}
}

func liveSnapshotRefTarget(raw rawSnapshot, stored snapshotRefTarget) (SnapshotElement, int) {
	matches := 0
	var live SnapshotElement
	for _, element := range raw.Elements {
		if element.Selector == stored.Selector && snapshotContextsEqual(element.Context, stored.Context) {
			matches++
			live = element
		}
	}
	redactSnapshotElementValue(&live)
	return live, matches
}

func snapshotContextsEqual(left, right []SnapshotRoot) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func snapshotRefTargetsEqual(left, right snapshotRefTarget) bool {
	return left.Ref == right.Ref &&
		left.Selector == right.Selector &&
		left.Fingerprint == right.Fingerprint &&
		snapshotContextsEqual(left.Context, right.Context)
}
