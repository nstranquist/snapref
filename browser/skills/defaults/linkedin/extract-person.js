(()=>{
  const out = {
    full_name: firstText([
      'h1.text-heading-xlarge',
      '.pv-text-details__left-panel h1',
      'h1[data-test-id="profile-name"]',
    ]),
    headline: firstText(['.text-body-medium.break-words']),
    job_title: '',
    company_name: '',
    location: firstText(['.text-body-small.inline.t-black--light.break-words']),
    about: '',
  };
  const firstJob = document.querySelector('.pvs-list__paged-list-item');
  if (firstJob) {
    const titleEl = firstJob.querySelector('.t-bold span[aria-hidden="true"]');
    if (titleEl) out.job_title = titleEl.textContent.trim();
    const companyEl = firstJob.querySelector('.t-14.t-normal span[aria-hidden="true"]');
    if (companyEl) out.company_name = companyEl.textContent.trim().split(' · ')[0];
  }
  const aboutAnchor = document.querySelector('#about');
  if (aboutAnchor && aboutAnchor.parentElement) {
    const t = aboutAnchor.parentElement.querySelector('.inline-show-more-text');
    if (t) out.about = t.textContent.trim();
  }
  return out;
})()
