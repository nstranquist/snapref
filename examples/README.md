# Examples

These playbooks are synthetic. They do not need a personal browser profile.

```sh
make init
snapref open demo
snapref script run examples/open-and-snapshot.yaml --session demo --allow-drive
snapref close demo --purge-profile
```

`open-and-snapshot.yaml` navigates a local fixture URL, waits for `body`,
takes a snapshot, and asserts the URL still matches the fixture host.
Replace the URL with your own target only in a throwaway checkout.
