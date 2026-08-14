package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type GCOptions struct {
	OlderThan time.Duration
	Apply     bool
	Now       func() time.Time
}

type GCEntry struct {
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	AgeSeconds int64     `json:"age_seconds"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	ModTime    time.Time `json:"mod_time,omitempty"`
	Removed    bool      `json:"removed,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type GCReport struct {
	DryRun           bool      `json:"dry_run"`
	OlderThan        string    `json:"older_than"`
	Cutoff           time.Time `json:"cutoff"`
	StaleKeepAlive   []GCEntry `json:"stale_keep_alive"`
	DeadSessions     []GCEntry `json:"dead_sessions"`
	DeadProfiles     []GCEntry `json:"dead_profiles"`
	RemovedSessions  int       `json:"removed_sessions"`
	RemovedProfiles  int       `json:"removed_profiles"`
	ReclaimableBytes int64     `json:"reclaimable_bytes"`
	RemovedBytes     int64     `json:"removed_bytes"`
}

func GC(opts GCOptions) (*GCReport, error) {
	if opts.OlderThan <= 0 {
		opts.OlderThan = 24 * time.Hour
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	cutoff := now().Add(-opts.OlderThan)
	report := &GCReport{
		DryRun:         !opts.Apply,
		OlderThan:      opts.OlderThan.String(),
		Cutoff:         cutoff,
		StaleKeepAlive: []GCEntry{},
		DeadSessions:   []GCEntry{},
		DeadProfiles:   []GCEntry{},
	}

	protectedProfiles := map[string]bool{}
	sessions, err := ListSessions()
	if err != nil {
		return nil, err
	}
	for _, listed := range sessions {
		unlock, err := acquireSessionLifecycleLock(listed.Name, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("lock session %q for gc: %w", listed.Name, err)
		}
		s, err := LoadSession(listed.Name)
		if err != nil {
			unlock()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reload session %q for gc: %w", listed.Name, err)
		}
		if s.IsAlive() {
			protectedProfiles[s.Name] = true
			if s.EffectiveLifecyclePolicy() == LifecycleKeepAlive && !s.StartedAt.IsZero() && !s.StartedAt.After(cutoff) {
				path, pathErr := SessionPath(s.Name)
				if pathErr != nil {
					unlock()
					return nil, pathErr
				}
				report.StaleKeepAlive = append(report.StaleKeepAlive, GCEntry{
					Kind:       "keep_alive",
					Name:       s.Name,
					Path:       path,
					StartedAt:  s.StartedAt,
					AgeSeconds: int64(now().Sub(s.StartedAt).Seconds()),
				})
			}
			unlock()
			continue
		}
		path, pathErr := SessionPath(s.Name)
		if pathErr != nil {
			unlock()
			return nil, pathErr
		}
		modTime := time.Time{}
		if info, statErr := os.Stat(path); statErr == nil {
			modTime = info.ModTime()
		}
		refTime := s.StartedAt
		if refTime.IsZero() {
			refTime = modTime
		}
		if !refTime.IsZero() && refTime.After(cutoff) {
			protectedProfiles[s.Name] = true
			unlock()
			continue
		}
		entry := GCEntry{
			Kind:       "session",
			Name:       s.Name,
			Path:       path,
			StartedAt:  s.StartedAt,
			ModTime:    modTime,
			AgeSeconds: int64(now().Sub(refTime).Seconds()),
		}
		if opts.Apply {
			if err := RemoveSession(s.Name); err != nil && !errors.Is(err, os.ErrNotExist) {
				entry.Error = err.Error()
				protectedProfiles[s.Name] = true
			} else {
				entry.Removed = true
				report.RemovedSessions++
			}
		}
		report.DeadSessions = append(report.DeadSessions, entry)
		unlock()
	}

	profiles, err := profilesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(profiles)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if ValidateSessionName(name) != nil || protectedProfiles[name] {
			continue
		}
		path := filepath.Join(profiles, name)
		var unlock func()
		if opts.Apply {
			unlock, err = acquireSessionLifecycleLock(name, 5*time.Second)
			if err != nil {
				return nil, fmt.Errorf("lock profile %q for gc: %w", name, err)
			}
			current, loadErr := LoadSession(name)
			if loadErr == nil {
				// Any newly registered generation protects its profile. Old dead
				// eligible records were removed while holding this same lock above.
				_ = current
				unlock()
				continue
			}
			if !errors.Is(loadErr, os.ErrNotExist) {
				unlock()
				return nil, fmt.Errorf("inspect profile %q session before gc: %w", name, loadErr)
			}
		}
		info, err := e.Info()
		if err != nil {
			if unlock != nil {
				unlock()
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			if unlock != nil {
				unlock()
			}
			continue
		}
		entry := GCEntry{
			Kind:       "profile",
			Name:       name,
			Path:       path,
			ModTime:    info.ModTime(),
			AgeSeconds: int64(now().Sub(info.ModTime()).Seconds()),
		}
		sizeBytes, sizeErr := directorySize(path)
		if sizeErr != nil {
			entry.Error = fmt.Sprintf("measure profile: %v", sizeErr)
			report.DeadProfiles = append(report.DeadProfiles, entry)
			if unlock != nil {
				unlock()
			}
			continue
		}
		entry.SizeBytes = sizeBytes
		report.ReclaimableBytes += sizeBytes
		if opts.Apply {
			if err := os.RemoveAll(path); err != nil {
				entry.Error = err.Error()
			} else {
				entry.Removed = true
				report.RemovedProfiles++
				report.RemovedBytes += sizeBytes
			}
		}
		report.DeadProfiles = append(report.DeadProfiles, entry)
		if unlock != nil {
			unlock()
		}
	}
	return report, nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}
