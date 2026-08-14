# GitHub

`https://github.com` — public data. **Prefer the GitHub REST API for
everything except the trending page.** See `scraping.md` for the full
recipe set (repo metadata, user/org profiles, search, commits,
releases, issues, file contents, parallel fetching, rate-limit
gotchas). See `repo-actions.md` for star/unstar/watch flows.

## Files in this skill

- `scraping.md` — comprehensive guide; "Do this first" leads with
  REST-API recipes, then explains when (and only when) to fall back to
  the browser. Lifted from browser-use/browser-harness 2026-05-04.
- `repo-actions.md` — star/unstar/watch via form submission. Lifted
  from browser-use/browser-harness 2026-05-04.
- `extract.js` — default extractor: trending-page repo cards. The
  scraping.md doc explains why you must `goto_url() → wait_for_load()
  → wait(2)` in the same eval scope before running this.
- `selectors.yaml` — drift probes for trending + repo-action flows.

## Why ship this default

GitHub is the most common reference target in our refs/ directory and
agents constantly need to pull repo metadata or trending data. Shipping
the upstream skills as defaults means the agent already knows the
"REST API first, browser only for trending" rule on day one.
