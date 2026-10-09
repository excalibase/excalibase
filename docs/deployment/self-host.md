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

## Vault unseal without a cloud KMS (manual unseal)

The platform vault holds every project's database credentials and the JWT
signing key. It is encrypted at rest and must be unsealed with its key before
anything can use it. There are three ways to hold that key:

| Provider | Key kept | After a restart | Use |
|---|---|---|---|
| `awskms` (default) | Only a ciphertext under your AWS KMS key | Unseals itself | Production with AWS |
| `manual` | **Nowhere on the server**; the admin keeps it | **Sealed until an admin unseals it** | Self-hosting without a cloud KMS |
| `plaintext` | In the `platform-bootstrap` Secret | Unseals itself | Development only (`devPlaintext: true`) |

The trade-off: `manual` means a stolen disk, backup or Secret dump cannot open
the vault, but every restart of provisioning (upgrade, node reboot, eviction)
leaves projects unservable until someone unseals it. While sealed, Studio
redirects to the unseal screen, auth stays unready and logs
`the vault is sealed: waiting for a platform admin to unseal it`, and project
APIs that need credentials fail. Lose every copy of the key and the vault (and
the project credentials in it) cannot be recovered.

| Server env | Chart value | `install-platform.sh` / `install-all.sh` env |
|---|---|---|
| `VAULT_UNSEAL_PROVIDER=manual` | `vault.unseal.provider: manual` | `VAULT_UNSEAL_MANUAL=true` |

`manual` refuses `VAULT_UNSEAL_KEY`, `VAULT_UNSEAL_KEY_CIPHERTEXT`,
`VAULT_KMS_KEY_ID` and a remote `VAULT_URL`, so no key can be handed to the
server by configuration. On the docker provisioner it also turns off the
`STORAGE_PATH/unseal.key` file.

### First start

1. Install (Kubernetes):

   ```bash
   helm upgrade --install platform-aio charts/platform-aio -n excalibase-platform \
     --set vault.unseal.provider=manual ...
   # or: VAULT_UNSEAL_MANUAL=true rke2/install-platform.sh
   ```

   The bootstrap Job neither initializes nor unseals the vault; auth and
   graphql become ready only after step 3.
2. Read the one-time setup token and open Studio at `https://<studio>/setup`:

   ```bash
   kubectl -n excalibase-platform get secret platform-setup-token -o jsonpath='{.data.token}' | base64 -d; echo
   ```

   Create the platform admin with it.
3. Studio asks to initialize the vault: choose the number of key shares and
   how many are needed to unseal (Shamir; 1 of 1 is fine for one admin). The
   keys are shown **once**: store them off the server (password manager,
   printed copy in a safe). Then submit them to unseal.

### After every restart

Unseal in Studio (any page redirects to `/setup`; sign in as a platform admin
and paste the key), or with the CLI inside the provisioning pod — the key and
token are prompted for without echo and never go on the command line:

```bash
kubectl -n excalibase-platform exec -it deploy/provisioning -- excalibase-provisioning vault status
kubectl -n excalibase-platform exec -it deploy/provisioning -- excalibase-provisioning vault unseal
# Platform admin token: (a PAT, or set EXCALIBASE_TOKEN inside the pod)
# Unseal key (1 of 1):
```

Single host (docker provisioner): `docker exec -it <provisioning container>
excalibase-provisioning vault unseal`.

Unseal, init, seal and rekey are allowed to platform admins only, limited to
10 calls per minute per address, and written to the platform audit log
(`audit_log`, actions `vault.*`) with the outcome and progress, never the key.
