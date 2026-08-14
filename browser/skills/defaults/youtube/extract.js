(()=>{
  function meta(name) {
    const el = document.querySelector('meta[' + name + ']');
    return el ? el.getAttribute('content') : null;
  }
  const data = { title: null, views: null, channel: null };
  const v = meta('itemprop="interactionCount"');
  if (v) data.views = parseInt(v, 10);
  data.title = meta('name="title"') || meta('property="og:title"');
  const link = document.querySelector('ytd-channel-name a, #owner-name a');
  if (link) {
    const href = link.getAttribute('href') || '';
    const m = href.match(/\/@([^\/]+)/) || href.match(/\/channel\/([^\/]+)/) || href.match(/\/c\/([^\/]+)/);
    if (m) data.channel = m[1];
    else if (link.textContent) data.channel = link.textContent.trim();
  }
  return data;
})()
