package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// LinkedInResult is the typed envelope for a LinkedIn profile or
// company page. The stable typed field set lets downstream lead-capture
// ingestion consume SnapRef output without depending on page-local JS shape.
//
// Person/Company are populated by running the corresponding JS
// extractor under ~/.snapref/skills/linkedin/. URL/Type/Identifier
// are filled by ExtractLinkedIn from the page URL — not the JS — so
// routing logic stays Go-side.
type LinkedInResult struct {
	Type       string           `json:"type"` // "person" | "company" | "unknown"
	Identifier string           `json:"identifier,omitempty"`
	URL        string           `json:"url"`
	Person     *LinkedInPerson  `json:"person,omitempty"`
	Company    *LinkedInCompany `json:"company,omitempty"`
}

// LinkedInPerson is the field set extracted from a profile page by
// linkedin/extract-person.js.
type LinkedInPerson struct {
	FirstName  string `json:"first_name,omitempty"`
	LastName   string `json:"last_name,omitempty"`
	FullName   string `json:"full_name,omitempty"`
	Headline   string `json:"headline,omitempty"`
	JobTitle   string `json:"job_title,omitempty"`
	Company    string `json:"company_name,omitempty"`
	Location   string `json:"location,omitempty"`
	About      string `json:"about,omitempty"`
	ProfileURL string `json:"profile_url"`
}

// LinkedInCompany is the field set extracted from a company page by
// linkedin/extract-company.js.
type LinkedInCompany struct {
	Name        string   `json:"company_name,omitempty"`
	Tagline     string   `json:"tagline,omitempty"`
	Description string   `json:"description,omitempty"`
	Industry    string   `json:"industry,omitempty"`
	Size        string   `json:"company_size,omitempty"`
	Location    string   `json:"location,omitempty"`
	Founded     string   `json:"founded,omitempty"`
	Specialties []string `json:"specialties,omitempty"`
	Website     string   `json:"website,omitempty"`
	Domain      string   `json:"company_domain,omitempty"`
}

// ExtractLinkedIn detects the page type from the URL path and runs the
// matching JS extractor loaded from root. Returns Type=="unknown" with
// no person/company for pages that aren't /in/ or /company/ (or their
// /sales/lead /sales/company variants).
func ExtractLinkedIn(ctx context.Context, page Page, root SkillsRoot, pageURL string) (LinkedInResult, error) {
	out := LinkedInResult{URL: pageURL}
	u, err := url.Parse(pageURL)
	if err != nil {
		return out, fmt.Errorf("parse url: %w", err)
	}
	path := u.Path
	switch {
	case strings.HasPrefix(path, "/in/"):
		out.Type = "person"
		out.Identifier = trimSlash(strings.TrimPrefix(path, "/in/"))
		raw, err := root.RunExtractor(ctx, page, DomainLinkedIn, "person")
		if err != nil {
			return out, fmt.Errorf("person extract: %w", err)
		}
		var p LinkedInPerson
		if err := json.Unmarshal(raw, &p); err != nil {
			return out, fmt.Errorf("person decode: %w", err)
		}
		p.ProfileURL = pageURL
		if p.FullName != "" {
			parts := strings.Fields(p.FullName)
			if len(parts) > 0 {
				p.FirstName = parts[0]
			}
			if len(parts) > 1 {
				p.LastName = strings.Join(parts[1:], " ")
			}
		}
		out.Person = &p
	case strings.HasPrefix(path, "/company/"):
		out.Type = "company"
		out.Identifier = trimSlash(strings.TrimPrefix(path, "/company/"))
		raw, err := root.RunExtractor(ctx, page, DomainLinkedIn, "company")
		if err != nil {
			return out, fmt.Errorf("company extract: %w", err)
		}
		var c LinkedInCompany
		if err := json.Unmarshal(raw, &c); err != nil {
			return out, fmt.Errorf("company decode: %w", err)
		}
		out.Company = &c
	case strings.HasPrefix(path, "/sales/lead/"):
		out.Type = "person"
		out.Identifier = trimSlash(strings.TrimPrefix(path, "/sales/lead/"))
		raw, _ := root.RunExtractor(ctx, page, DomainLinkedIn, "person")
		var p LinkedInPerson
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &p)
		}
		p.ProfileURL = pageURL
		out.Person = &p
	case strings.HasPrefix(path, "/sales/company/"):
		out.Type = "company"
		out.Identifier = trimSlash(strings.TrimPrefix(path, "/sales/company/"))
		raw, _ := root.RunExtractor(ctx, page, DomainLinkedIn, "company")
		var c LinkedInCompany
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &c)
		}
		out.Company = &c
	default:
		out.Type = "unknown"
	}
	return out, nil
}

func trimSlash(s string) string {
	return strings.Trim(s, "/")
}
