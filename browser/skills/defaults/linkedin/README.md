# LinkedIn — profile + company extraction

`https://www.linkedin.com` — agent-edited skill. Drift commands read
`selectors.yaml`; extraction reads `extract-person.js` and `extract-company.js`.

## URL routing

| Path prefix | Extractor |
|---|---|
| `/in/<slug>/` | `extract-person.js` |
| `/sales/lead/<slug>/` | `extract-person.js` |
| `/company/<slug>/` | `extract-company.js` |
| `/sales/company/<slug>/` | `extract-company.js` |

The Go-side `ExtractLinkedIn` helper does the URL→extractor routing
and assembles the typed `LinkedInResult` envelope. Direct callers can
use `RunExtractor("linkedin", "person")` / `…("linkedin", "company")`
to skip the envelope and get raw JS output.

## Trap: signed-out pages

LinkedIn redirects logged-out browsers to `/login`, so selectors return
zero matches. If drift is suddenly 100% across the board, check whether
the harness session is logged in.

## Trap: profile-name selectors drift on logged-out CDN edges

`h1.text-heading-xlarge` is the live A/B; the alt selectors in
`extract-person.js` cover older render paths. Add more alts as you find
them — agents are encouraged to extend `firstText([...])` lists.
