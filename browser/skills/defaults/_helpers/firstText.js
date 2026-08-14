// firstText returns the trimmed textContent of the first selector that
// matches and has non-empty text. Used by domain extractors that try a
// list of selector fallbacks (LinkedIn render variants, etc.). Auto-
// prepended into every extractor's eval scope by RunExtractor; also
// callable directly as `firstText([...])` from any extract-*.js.
function firstText(selectors) {
  for (const s of selectors) {
    const el = document.querySelector(s);
    if (el && el.textContent && el.textContent.trim()) return el.textContent.trim();
  }
  return '';
}
