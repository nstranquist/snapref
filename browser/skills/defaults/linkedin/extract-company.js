(()=>{
  const out = {
    company_name: firstText([
      'h1.org-top-card-summary__title',
      'h1[data-test-id="org-name"]',
      '.org-top-card-summary__title',
      'h1.org-page-header__title',
    ]),
    tagline: firstText(['.org-top-card-summary__tagline']),
    description: '',
    industry: '',
    company_size: '',
    location: '',
    founded: '',
    specialties: [],
    website: '',
    company_domain: '',
  };
  for (const s of [
    'p.break-words.white-space-pre-wrap.t-black--light.text-body-medium',
    'p.break-words.white-space-pre-wrap',
    '.org-about-us-organization-description__text',
    '[data-test-id="about-us__description"]',
  ]) {
    const el = document.querySelector(s);
    if (el && el.textContent.trim().length > 50) { out.description = el.textContent.trim(); break; }
  }
  const dls = [
    document.querySelector('.org-page-details__definition-list'),
    document.querySelector('dl.overflow-hidden'),
    document.querySelector('.org-about-module__margin-bottom dl'),
  ];
  for (const dl of dls) {
    if (!dl) continue;
    let label = null;
    for (const item of dl.querySelectorAll('dt, dd')) {
      if (item.tagName === 'DT') {
        const h3 = item.querySelector('h3');
        label = (h3 ? h3.textContent : item.textContent).trim().toLowerCase();
      } else if (item.tagName === 'DD' && label) {
        const value = item.textContent.trim();
        if (label.includes('website')) {
          const link = item.querySelector('a');
          if (link && link.href && !link.href.includes('linkedin.com')) {
            out.website = link.href;
            try { out.company_domain = new URL(link.href).hostname.replace(/^www\./, ''); } catch (e) {}
          }
        } else if (label.includes('industry')) out.industry = value;
        else if (label.includes('company size')) {
          const m = value.match(/^([\d,]+-[\d,]+|[\d,]+\+?) employees/);
          out.company_size = m ? m[1] + ' employees' : value.split('\n')[0].trim();
        } else if (label.includes('headquarters')) out.location = value;
        else if (label.includes('founded')) out.founded = value;
        else if (label.includes('specialties')) out.specialties = value.split(',').map(s => s.trim()).filter(Boolean);
        label = null;
      }
    }
    if (out.website || out.industry) break;
  }
  return out;
})()
