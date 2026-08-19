# Instagram — meta-driven post extraction

`https://www.instagram.com` — meta-tag extractor. Pulls content id from
`al:ios:url`, parses likes/comments out of the OG description, falls
back to URL path for handle.

## Trap: logged-out walls

Instagram aggressively gates desktop browsers behind a login modal.
Meta tags are still served pre-modal, so this extractor mostly works
without auth — but the `og:description` text is missing for some
content types. Re-run `snapref skill <url>` after a snapshot to confirm
the extractor still sees the expected fields before scaling a flow.
