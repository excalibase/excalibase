# Behaviour review (FEATURE_BEHAVIOURREVIEW)

EXC-844. The platform looks at how much each customer app, and each account as
a whole, sends and receives, and writes a review when the shape or the volume
looks like a proxy, a VPN node or a relay. A person decides what to do. Nothing
here stops, limits or suspends an app or an account, and the detector has no
way to: it can read the meter's history and write reviews, and that is all.

Off by default. It needs network metering ([net-metering.md](net-metering.md),
`FEATURE_NETMETER`) for data. In this stage the reviews are only written;
nothing shows them yet, so they can be collected and the thresholds tuned
before anyone sees a list.

## What it looks for

Each signal is judged against a reference rate for the account's tier, so a
paid tier is not held to the Free allowance. Every number below is a setting;
these are the defaults.

| Signal | Raised when |
|---|---|
| `sustained_egress` | sent bytes per second are at least 80 % of the reference in at least 45 of the last 60 minutes |
| `relay_symmetry` | in at least 45 of the last 60 minutes the app sent at least 25 % of the reference, and over those minutes received between 0.6 and 1.6 times what it sent (a relay hands on what it takes in; a website is send-heavy, an uploader or downloader is receive-heavy) |
| `volume_daily` | bytes sent in 24 hours are at least 30 % of what the reference rate would send in a day |
| `volume_30d` | bytes sent in 30 days are at least 20 % of what the reference rate would send in 30 days |

Each signal is evaluated for every app, and again for the owner of the apps as
a whole: the owner's minutes are added together, and the owner is judged
against its highest tier's reference, not the sum, so splitting a workload over
several apps does not buy more allowance. The owner total also counts deleted
apps of projects that still exist, so deleting and recreating an app does not
reset the evidence. A project that was deleted can no longer be attributed to
an owner.

A quiet 1:1 connection such as a chat WebSocket is below the volume floor, a
batch job's short burst is below the minutes needed, and send-heavy or
receive-heavy apps are outside the symmetry band. A busy site that sends most
of the reference rate all hour is flagged by `sustained_egress` by design: that
is what a person is there to look at.

Reference rates (Mbit/s) default to Free 10, Standard 100, Enterprise 1000, and
100 for any other tier.

## Settings

| Variable | Default |
|---|---|
| `FEATURE_BEHAVIOURREVIEW` | `false` |
| `BEHAVIOUR_WINDOW_MINUTES` | 60 |
| `BEHAVIOUR_SUSTAINED_PERCENT` | 80 |
| `BEHAVIOUR_SUSTAINED_MINUTES` | 45 |
| `BEHAVIOUR_SYMMETRY_LOW`, `BEHAVIOUR_SYMMETRY_HIGH` | 0.6, 1.6 |
| `BEHAVIOUR_SYMMETRY_MIN_PERCENT` | 25 |
| `BEHAVIOUR_DAILY_VOLUME_PERCENT` | 30 |
| `BEHAVIOUR_MONTHLY_VOLUME_PERCENT` | 20 |
| `BEHAVIOUR_TIER_MBPS` | `free=10,standard=100,enterprise=1000` |
| `BEHAVIOUR_DEFAULT_MBPS` | 100 |
| `BEHAVIOUR_EVAL_MINUTES` | 5 |
| `BEHAVIOUR_MUTE_DAYS` | 14 |
| `BEHAVIOUR_GROWTH_FACTOR` | 2 |
| `BEHAVIOUR_DISMISSED_RETENTION_DAYS` | 180 |

A value that is not a positive number, a symmetry band that is empty, or a
sustained-minutes count larger than the window is logged and the default is
kept.

## What a review holds

One row per owner, app and signal in `abuse_review`:

- the snapshot taken when it was made: owner, org, project and app ids and
  names, and the tier. Nothing references an app, project or account, so the
  review outlives all of them;
- first and last seen, and the largest magnitude (a percentage of the
  reference) while it was open;
- evidence: rates, ratios, counts and windows only, numbers under a fixed set
  of keys. No address, DNS name or content is read or stored, and the store
  refuses any other key.

A finding that repeats updates the open row. Dismissing a review mutes its
owner, app and signal for 14 days; a finding in that time opens a new review
only if it has grown to twice the peak the dismissed one had. After the mute
any finding opens a new review. Dismissed reviews are deleted after 180 days;
open ones are kept, and so are ones marked actioned.

## Privacy and policy

Metering stores byte counts per app, not what the bytes were. The Acceptable
Use Policy and the privacy text should say that traffic volume is measured and
reviewed to detect abuse; that wording is a website change for the owner to
merge.

## Not in this stage

The review list in the admin console and the dismiss action (next), the
HAProxy session signal, the Hubble connection signals, and the egress cap.
