# Examples

These playbooks are synthetic. They do not need a personal browser profile.

```sh
snapref runtime install --runtime chrome-for-testing --channel stable
snapref open demo
snapref script run examples/open-and-snapshot.yaml --session demo --allow-drive
snapref close demo --purge-profile
```

`open-and-snapshot.yaml` navigates `https://example.com`, waits for `body`,
takes a snapshot, and asserts the URL contains `example.com`.
Replace the URL with your own target only in a throwaway checkout.
