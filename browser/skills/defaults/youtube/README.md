# YouTube — title/views/channel extraction

`https://www.youtube.com` — meta-driven extractor. Reads page metadata
plus DOM channel link. youtu.be aliases to youtube via the host alias
map in `skills.go`.

## Trap: pre-hydration meta tags

YouTube's SSR injects most meta tags before React hydration, so this
extractor is reliable on cold loads. If channel handle parsing fails
intermittently, the renderer is mid-hydration; wait for
`ytd-channel-name a` to appear before extracting.
