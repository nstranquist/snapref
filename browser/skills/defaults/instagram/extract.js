(()=>{
  function meta(prop) {
    const el = document.querySelector('meta[property="' + prop + '"]');
    return el ? el.getAttribute('content') : null;
  }
  const data = { content_id: null, title: null, handle: null, likes: null, comments: null };
  const ios = meta('al:ios:url');
  if (ios) {
    const m = ios.match(/media\?id=(\d+)/);
    if (m) data.content_id = m[1];
  }
  data.title = meta('og:title');
  const desc = meta('og:description') || '';
  const lm = desc.match(/([\d,]+)\s*likes/i);
  if (lm) data.likes = parseInt(lm[1].replace(/,/g, ''), 10);
  const cm = desc.match(/([\d,]+)\s*comments/i);
  if (cm) data.comments = parseInt(cm[1].replace(/,/g, ''), 10);
  const um = desc.match(/- (@?\w+):/);
  if (um) data.handle = um[1].replace('@', '');
  if (!data.handle) {
    const path = location.pathname.match(/^\/([^\/]+)\/(p|reel)\//);
    if (path) data.handle = path[1];
  }
  return data;
})()
