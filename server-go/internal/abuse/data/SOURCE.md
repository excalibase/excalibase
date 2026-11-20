# Vendored lists (EXC-755)

disposable_email_blocklist.conf
- Source: https://github.com/disposable-email-domains/disposable-email-domains (file `disposable_email_blocklist.conf`)
- Commit: 3cb930c9fdbbc271d8dd84709bb6bc9364f629d8 (fetched 2026-11-16)
- Licence: CC0 1.0 Universal (public domain dedication, stated in the project's README)
- Refreshed weekly by `.github/workflows/disposable-domains.yml`, which opens a PR.

disposable_extra.txt
- Ours. Domains seen in abuse that the upstream list lacked, and `name*` label-prefix rules.

temp_mx_hosts.txt
- Ours. Mail hosts (matched as a suffix) that throwaway domains share, taken from the MX records of the upstream list.
  A domain whose MX points at one of these is refused, which catches new domains the lists do not know yet.
