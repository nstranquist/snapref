package skills

import (
	"context"
	"encoding/json"
	"fmt"
)

// SocialResult is the typed envelope for non-LinkedIn social skills.
// The Fields map carries whatever the loaded extract.js returned —
// shape is the script's contract, not Go's.
type SocialResult struct {
	Domain Domain         `json:"domain"`
	URL    string         `json:"url"`
	Fields map[string]any `json:"fields"`
}

// ExtractSocial runs the default extractor (extract.js) under
// <root>/<domain>/. Returns ErrNoSkillForDomain for domains without a
// skill dir. LinkedIn is rejected — use ExtractLinkedIn for it because
// the URL→extractor routing matters.
func ExtractSocial(ctx context.Context, page Page, root SkillsRoot, domain Domain, pageURL string) (SocialResult, error) {
	out := SocialResult{Domain: domain, URL: pageURL, Fields: map[string]any{}}
	if domain == DomainLinkedIn {
		return out, fmt.Errorf("ExtractSocial does not handle linkedin; use ExtractLinkedIn")
	}
	if domain == "" {
		return out, ErrNoSkillForDomain
	}
	raw, err := root.RunExtractor(ctx, page, domain, "extract")
	if err != nil {
		return out, fmt.Errorf("eval %s: %w", domain, err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Fields); err != nil {
			return out, fmt.Errorf("decode %s: %w", domain, err)
		}
	}
	return out, nil
}
