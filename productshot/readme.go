package productshot

import (
	"fmt"
	"path"
	"strings"
)

// ReadmeSnippet returns drop-in markdown for a GitHub README or a Product
// Hunt upload checklist.
func ReadmeSnippet(man Manifest) string {
	var b strings.Builder
	title := strings.TrimSpace(man.Title)
	if title == "" {
		title = "Product"
	}
	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString(" — screenshot pack\n\n")
	if man.SourceURL != "" {
		b.WriteString("Source: ")
		b.WriteString(man.SourceURL)
		b.WriteString("\n\n")
	}

	var hero, gif, mp4 string
	var gallery []Artifact
	var thumb string
	for _, a := range man.Artifacts {
		switch {
		case strings.HasSuffix(a.Role, ".hero") && strings.HasSuffix(a.Path, ".png"):
			if hero == "" {
				hero = a.Path
			}
		case strings.HasSuffix(a.Path, ".gif"):
			if gif == "" {
				gif = a.Path
			}
		case strings.HasSuffix(a.Path, ".mp4"):
			if mp4 == "" {
				mp4 = a.Path
			}
		case strings.Contains(a.Role, "gallery") && strings.HasSuffix(a.Path, ".png"):
			gallery = append(gallery, a)
		case strings.Contains(a.Role, "thumbnail"):
			thumb = a.Path
		case strings.HasSuffix(a.Role, ".card") && hero == "":
			hero = a.Path
		}
	}

	b.WriteString("## GitHub README\n\n")
	if gif != "" {
		b.WriteString(fmt.Sprintf("![%s](%s)\n\n", title, slash(gif)))
		b.WriteString("GitHub renders GIFs inline. Keep the file under ~8 MB.\n\n")
	} else if hero != "" {
		b.WriteString(fmt.Sprintf("![%s](%s)\n\n", title, slash(hero)))
	}
	if mp4 != "" {
		b.WriteString("MP4 (upload via a GitHub issue comment, then paste the resulting URL on its own line):\n\n")
		b.WriteString(fmt.Sprintf("`%s`\n\n", slash(mp4)))
	}

	if thumb != "" || len(gallery) > 0 || mp4 != "" {
		b.WriteString("## Product Hunt upload\n\n")
		if thumb != "" {
			b.WriteString(fmt.Sprintf("- Thumbnail (240×240): `%s`\n", slash(thumb)))
		}
		for i, g := range gallery {
			b.WriteString(fmt.Sprintf("- Gallery %d (1270×760): `%s`\n", i+1, slash(g.Path)))
		}
		if mp4 != "" {
			b.WriteString(fmt.Sprintf("- Video (16:9 MP4, ≤30s): `%s`\n", slash(mp4)))
		}
		if gif != "" {
			b.WriteString(fmt.Sprintf("- Optional GIF: `%s`\n", slash(gif)))
		}
		if len(gallery) > 0 {
			b.WriteString("\nGallery needs two or more images. Every still is under the usual 5 MB gallery cap when captured at these sizes.\n")
		} else {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func slash(p string) string {
	return path.Clean(strings.ReplaceAll(p, "\\", "/"))
}
