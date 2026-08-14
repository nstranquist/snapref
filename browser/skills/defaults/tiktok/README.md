# TikTok — like/comment/share extraction

`https://www.tiktok.com` — drift-focused skill. The extractor returns
parsed counts and the handle from the URL; for stable scraping prefer
the public oembed/profile JSON endpoints.

## Trap: counts use suffix encoding

TikTok renders likes as `12.3K`, `1.2M`. `extract.js` parses these via
the `parseCount` helper. Add new suffix variants to `mult` if you spot
locales using `B` (billion) etc.
