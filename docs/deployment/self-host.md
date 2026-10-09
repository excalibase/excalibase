# Self-hosting without cloud accounts

A self-hosted install needs no AWS, Resend or Cloudflare account. This page
lists the exact settings for each piece a cloud install gets from a provider.
Values are given for the Kubernetes chart (`charts/platform-aio` in
`excalibase-service`), the `rke2/install-platform.sh` environment, and the
provisioning server's own environment (single host).

## Email through your own SMTP relay

Sign-up verification, password reset and invitation emails go through any
SMTP relay (Postfix, a mail provider's SMTP endpoint, Mailpit for testing).
Without an email provider only the first admin can sign up; every later
sign-up answers 503.

| Server env | Chart value | `install-platform.sh` env | Default | Notes |
|---|---|---|---|---|
| `EMAIL_PROVIDER=smtp` | `email.provider: smtp` | `EMAIL_PROVIDER=smtp` | `ses` | |
| `SMTP_HOST` | `email.smtp.host` | `SMTP_HOST` | — | Required. |
| `SMTP_PORT` | `email.smtp.port` | `SMTP_PORT` | by TLS mode | 587 for `starttls`, 465 for `tls`, 25 for `none`. |
| `SMTP_TLS` | `email.smtp.tls` | `SMTP_TLS` | `starttls` | `starttls`, `tls` (implicit, port 465) or `none`. |
| `SMTP_USERNAME` / `SMTP_PASSWORD` | `email.smtp.credentialsSecret` | `SMTP_CREDENTIALS_SECRET` | — | Secret keys `username` and `password`. Leave unset for a relay that needs no login. |
| `SMTP_CA_FILE` | `email.smtp.caSecret` | `SMTP_CA_SECRET` | — | Secret key `ca.crt`: trusts a relay whose certificate a private CA issued. System roots are always trusted. |
| `EMAIL_FROM_ADDRESS` / `EMAIL_FROM_NAME` | `email.fromAddress` / `email.fromName` | `EMAIL_FROM_ADDRESS` | `noreply@excalibase.io` | Use an address your relay may send as. |

Security:

- `starttls` refuses a relay that does not offer STARTTLS, and both TLS modes
  verify the relay's certificate. There is no "skip verify" switch; give a
  private CA with `caSecret` instead.
- `none` sends in plain text. It is meant for a relay on the same private
  network and refuses credentials: a login is never sent without TLS.
- The password is read only from the Secret and never logged. The boot log
  names the host, TLS mode, whether a login is used and the from-address.
- A bad SMTP setting stops provisioning at boot instead of failing the first
  sign-up.

### Kubernetes (chart)

```bash
kubectl -n excalibase-platform create secret generic smtp-creds \
  --from-literal=username=excalibase@example.com --from-literal=password='...'
# Only for a relay with a private CA:
kubectl -n excalibase-platform create secret generic smtp-ca --from-file=ca.crt=./relay-ca.pem

helm upgrade --install platform-aio charts/platform-aio -n excalibase-platform \
  --set email.provider=smtp \
  --set email.smtp.host=smtp.example.com \
  --set email.smtp.credentialsSecret=smtp-creds \
  --set email.fromAddress=noreply@example.com \
  ...
```

With `rke2/install-platform.sh`, export the same settings instead:

```bash
EMAIL_PROVIDER=smtp SMTP_HOST=smtp.example.com SMTP_CREDENTIALS_SECRET=smtp-creds \
EMAIL_FROM_ADDRESS=noreply@example.com rke2/install-platform.sh
```

### Single host (provisioning env)

```bash
EMAIL_PROVIDER=smtp
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_TLS=starttls
SMTP_USERNAME=excalibase@example.com
SMTP_PASSWORD=...            # from your secret store, not a committed file
EMAIL_FROM_ADDRESS=noreply@example.com
```

### Check it

Sign up a second user in Studio: the verification email arrives and its link
opens Studio. The provisioning log shows
`SMTP sender configured (host=... tls=starttls auth=true ...)` at start; a
relay that refuses STARTTLS, an untrusted certificate or a wrong password
appears there as the sign-up's error.
