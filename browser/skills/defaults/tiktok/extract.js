(()=>{
  function pick(sels) {
    for (const s of sels) {
      const el = document.querySelector(s);
      if (el) return el;
    }
    return null;
  }
  function parseCount(s) {
    if (!s) return null;
    const c = s.replace(/[^0-9.KMBkmb]/g, '').trim();
    if (!c) return null;
    const mult = { k: 1e3, m: 1e6, b: 1e9 };
    const last = c.slice(-1).toLowerCase();
    if (mult[last]) return Math.round(parseFloat(c.slice(0, -1)) * mult[last]);
    return parseInt(c.replace(/,/g, ''), 10) || null;
  }
  const data = { handle: null, likes: null, comments: null, shares: null };
  const h = location.href.match(/@([^\/]+)/);
  if (h) data.handle = h[1];
  const likesEl = pick(['[data-e2e="like-count"]', '[data-e2e="browse-like-count"]']);
  if (likesEl) data.likes = parseCount(likesEl.textContent);
  const cEl = pick(['[data-e2e="comment-count"]', '[data-e2e="browse-comment-count"]']);
  if (cEl) data.comments = parseCount(cEl.textContent);
  const sEl = document.querySelector('[data-e2e="share-count"]');
  if (sEl) data.shares = parseCount(sEl.textContent);
  return data;
})()
