(()=>{
  // job-posting extractor: generic ATS posting capture for Greenhouse, Lever,
  // Ashby, and similar hosted job pages.
  const pickText = (selectors) => {
    for (const selector of selectors) {
      const el = document.querySelector(selector);
      const text = el && (el.innerText || el.textContent || '').trim();
      if (text) return text.replace(/\s+/g, ' ');
    }
    return '';
  };
  const meta = (names) => {
    for (const name of names) {
      const el = document.querySelector(`meta[property="${name}"], meta[name="${name}"]`);
      const value = el && (el.getAttribute('content') || '').trim();
      if (value) return value;
    }
    return '';
  };
  const absURL = (href) => {
    try { return href ? new URL(href, location.href).toString() : ''; } catch (_) { return ''; }
  };
  const host = location.hostname.toLowerCase();
  const pathParts = location.pathname.split('/').filter(Boolean);
  const provider =
    host.includes('greenhouse.io') ? 'greenhouse' :
    host.includes('lever.co') ? 'lever' :
    host.includes('ashbyhq.com') ? 'ashby' : 'unknown';
  const boardCompany =
    provider === 'greenhouse' ? pathParts[0] :
    provider === 'lever' ? pathParts[0] :
    provider === 'ashby' ? pathParts[0] : '';
  const title = pickText([
    '[data-automation-id="jobPostingHeader"] h1',
    '[data-qa="job-title"]',
    '.posting-headline h2',
    '.job__title',
    'h1',
  ]) || meta(['og:title', 'twitter:title']) || document.title.replace(/\s*\|.*$/, '');
  const company = pickText([
    '[data-qa="company-name"]',
    '.company-name',
    '.posting-company',
    '.app-title',
  ]) || meta(['og:site_name']) || boardCompany;
  const locationText = pickText([
    '[data-automation-id="locations"]',
    '[data-automation-id="location"]',
    '[data-qa="job-location"]',
    '.location',
    '.posting-categories .location',
    '.job__location',
  ]);
  const descriptionEl = document.querySelector([
    '[data-automation-id="jobPostingDescription"]',
    '[data-qa="job-description"]',
    '.posting-page',
    '.posting',
    '.job__description',
    'main',
    'article',
  ].join(', '));
  const description = descriptionEl ? (descriptionEl.innerText || descriptionEl.textContent || '').trim() : (document.body ? document.body.innerText.trim() : '');
  const applyLink = Array.from(document.querySelectorAll('a[href], button[href]')).find((el) => {
    const text = (el.innerText || el.textContent || '').trim();
    const href = el.getAttribute('href') || '';
    return /apply/i.test(text) || /\/apply|application/i.test(href);
  });
  return {
    provider,
    title,
    company,
    location: locationText,
    url: location.href,
    apply_url: applyLink ? absURL(applyLink.getAttribute('href')) : '',
    description,
    text: document.body ? document.body.innerText.trim() : '',
  };
})()
