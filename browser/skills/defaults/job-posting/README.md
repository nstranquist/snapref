# Job Posting

Generic extractor for public job posting pages, especially Greenhouse, Lever,
and Ashby hosted boards.

Run:

```sh
snapref job-posting <url> --json
ndev browser job-posting <url> --json
ndev browser job-posting <url> --jobkit-apply
```

The extractor returns title, company, location, apply URL, and readable
description text. Standalone SnapRef emits the typed extraction contract;
the `ndev` adapter can hand it to JobKit with `--jobkit-apply`.

Known traps:

- Some boards hydrate late; retry the extraction if the description is empty
  (`ndev browser job-posting --patient` enables the bounded patient lane).
- Company names are often inferred from the board slug when the page omits a
  visible company element.
