(()=>{
  // Default extractor: trending-page repo cards. Run only after
  // navigating to /trending (or /trending/<lang>?since=...) and waiting
  // for React hydration; `document.readyState=='complete'` fires before
  // the cards render. See scraping.md for the full gotchas list.
  //
  // For everything that ISN'T the trending page (repo metadata, user
  // profiles, search, releases, issues, file contents) prefer the GitHub
  // REST API at https://api.github.com: one HTTP call, fully parsed
  // JSON, no browser. scraping.md has the full recipe set.
  const rows = Array.from(document.querySelectorAll('article.Box-row'));
  return rows.map(el => {
    const h2link = el.querySelector('h2 a');
    const starLink = el.querySelector('a[href*="/stargazers"]');
    const forkLink = el.querySelector('a[href*="/forks"]');
    const langEl = el.querySelector('[itemprop="programmingLanguage"]');
    const todayEl = el.querySelector('.d-inline-block.float-sm-right');
    const descEl = el.querySelector('p');
    return {
      name: h2link ? h2link.innerText.trim().replace(/\s+/g, ' ') : null,
      url: h2link ? 'https://github.com' + h2link.getAttribute('href') : null,
      stars_total: starLink ? starLink.innerText.trim() : null,
      stars_period: todayEl ? todayEl.innerText.trim() : null,
      forks: forkLink ? forkLink.innerText.trim() : null,
      language: langEl ? langEl.innerText.trim() : null,
      desc: descEl ? descEl.innerText.trim() : null,
    };
  });
})()
