package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type JobPostingResult struct {
	Provider    string `json:"provider,omitempty"`
	Title       string `json:"title,omitempty"`
	Company     string `json:"company,omitempty"`
	Location    string `json:"location,omitempty"`
	URL         string `json:"url"`
	ApplyURL    string `json:"apply_url,omitempty"`
	Description string `json:"description,omitempty"`
	Text        string `json:"text,omitempty"`
	JDText      string `json:"jd_text"`
}

func ExtractJobPosting(ctx context.Context, page Page, root SkillsRoot, pageURL string) (JobPostingResult, error) {
	out := JobPostingResult{URL: pageURL, Provider: JobPostingProvider(pageURL)}
	if root.DomainOf(pageURL) != DomainJobPosting {
		return out, ErrNoSkillForDomain
	}
	raw, err := root.RunExtractor(ctx, page, DomainJobPosting, "extract")
	if err != nil {
		return out, fmt.Errorf("job-posting extract: %w", err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return out, fmt.Errorf("job-posting decode: %w", err)
		}
	}
	if out.URL == "" {
		out.URL = pageURL
	}
	if out.Provider == "" {
		out.Provider = JobPostingProvider(pageURL)
	}
	out.Title = cleanOneLine(out.Title)
	out.Company = cleanOneLine(out.Company)
	out.Location = cleanOneLine(out.Location)
	out.ApplyURL = strings.TrimSpace(out.ApplyURL)
	out.Description = cleanBlock(out.Description)
	out.Text = cleanBlock(out.Text)
	out.JDText = BuildJobPostingJDText(out)
	return out, nil
}

func JobPostingProvider(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "unknown"
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(host, "greenhouse.io"):
		return "greenhouse"
	case strings.Contains(host, "lever.co"):
		return "lever"
	case strings.Contains(host, "ashbyhq.com"):
		return "ashby"
	default:
		return "unknown"
	}
}

func BuildJobPostingJDText(result JobPostingResult) string {
	var b strings.Builder
	if result.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", result.Title)
	}
	if result.Company != "" {
		fmt.Fprintf(&b, "Company: %s\n", result.Company)
	}
	if result.Location != "" {
		fmt.Fprintf(&b, "Location: %s\n", result.Location)
	}
	if result.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", result.URL)
	}
	if result.ApplyURL != "" && result.ApplyURL != result.URL {
		fmt.Fprintf(&b, "Apply URL: %s\n", result.ApplyURL)
	}
	body := result.Description
	if body == "" {
		body = result.Text
	}
	if strings.TrimSpace(body) != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}
	return b.String()
}

func cleanOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cleanBlock(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	var out []string
	blank := false
	for _, line := range lines {
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
