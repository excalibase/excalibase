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
| `relay_symmetry` | in at least 45 of the last 60 minutes the app sent at least 25 % of the reference, and over those minutes received between 0.5 and 2 times what it sent (a relay hands on what it takes in; a website is send-heavy, an uploader or downloader is receive-heavy) |
| `relay_symmetry_day` | in at least 6 of the last 24 hours the app sent at least 10 KB/s on average, and over those hours received between 0.5 and 2 times what it sent. The rate is absolute, it does not scale with the reference, and only Free has one by default |
| `volume_daily` | bytes sent in 24 hours are at least 30 % of what the reference rate would send in a day, or at least the tier's daily floor (3 GB for Free, no floor for the other tiers) if that is lower |
| `volume_30d` | bytes sent in 30 days are at least 20 % of what the reference rate would send in 30 days |

Each signal is evaluated for every app, and again for the owner of the apps as
a whole, once per tier: an owner's Free apps are added together and judged
against the Free reference, its Standard apps against the Standard one, so a
single Standard app does not raise the allowance of a dozen Free ones. Splitting
a workload over several apps does not buy more allowance either.

The meter writes the owner and tier on every minute when it takes it, and the
hourly rollups keep them, so the owner's evidence does not depend on the
project or app still existing: deleting and recreating a project or an app does
not reset it. Rows written before this snapshot existed have no owner and are
attributed through the live project list while they last; traffic of a deleted
project that has neither is not attributed.

## What the absolute floors are for

The reference-scaled signals only see what is large next to the tier's line. On
Free (10 Mbit/s) that means nothing under about 2 Mbit/s is flagged, and the
disguised proxies seen in production moved far less: under 1.5 GB in about
three days, a few KB/s. The two absolute floors do not scale with the reference
and are set per tier:

- `relay_symmetry_day` looks at the last 24 hours as hourly averages. An hour
  counts when the app sent at least the floor (10 KB/s for Free); the signal
  needs 6 such hours, and judges the receive/send ratio over those hours alone,
  so idle hours do not hide a relay that only works part of the day.
- the daily floor flags a few GB sent in 24 hours however far under the
  reference line that is. 3 GB a day is about 2.8 % of what Free's reference
  would carry.

A steady 2 Mbit/s each way is caught by both. A chat app (about 1 KB/s each way)
or a small API (a few KB/s out, less in) is below the rate floor, and a busy
hour of an API is not a relay.

What this does not do: a relay quieter than the floor, or one that stays under
the daily floor and spreads over fewer than 6 hours, is not seen by traffic
volume at all. Low-volume relays are mainly caught by the connection signals,
which look at what the traffic does rather than how much there is: long-lived
sessions at the edge (HAProxy session duration, phase P2b) and one app talking
to many distinct peers (Hubble fan-out, phase P5). Volume review is the net;
those are the tools for the small fish.

Padding: a relay can add dummy traffic in one direction to push its ratio out
of the band. The band is wide (0.5 to 2), the all-day signal judges it over
hours, and padding costs the operator bandwidth that makes `sustained_egress`
and the volume signals more likely. It can still be evaded by someone who
shapes both directions on purpose; that is what the connection signals and a
person's review are for.

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
| `BEHAVIOUR_SYMMETRY_LOW`, `BEHAVIOUR_SYMMETRY_HIGH` | 0.5, 2 |
| `BEHAVIOUR_SYMMETRY_FLOOR_KBPS` | `free=10` (a tier not named has no all-day relay signal; `=0` clears one) |
| `BEHAVIOUR_SYMMETRY_DAY_HOURS` | 6 |
| `BEHAVIOUR_DAILY_FLOOR_GB` | `free=3` (a tier not named has no floor) |
| `BEHAVIOUR_SYMMETRY_MIN_PERCENT` | 25 |
| `BEHAVIOUR_DAILY_VOLUME_PERCENT` | 30 |
| `BEHAVIOUR_MONTHLY_VOLUME_PERCENT` | 20 |
| `BEHAVIOUR_TIER_MBPS` | `free=10,standard=100,enterprise=1000` |
| `BEHAVIOUR_DEFAULT_MBPS` | 100 |
| `BEHAVIOUR_EVAL_MINUTES` | 5 |
| `BEHAVIOUR_MUTE_DAYS` | 14 |
| `BEHAVIOUR_GROWTH_FACTOR` | 2 |
| `BEHAVIOUR_DISMISSED_RETENTION_DAYS` | 180 |

A setting that is set but unusable (not a positive number, a tier pair that is
malformed, an empty symmetry band, a sustained-minutes count larger than the
window, more than 24 day-hours) stops the boot with an error naming the
variable, while `FEATURE_BEHAVIOURREVIEW` is on. There is no fall-back to the
default: a threshold the operator did not mean is worse than a stopped boot.
With the flag off the settings are not checked.

## What a review holds

One row per owner, app, signal and, for an owner-level finding, tier in
`abuse_review`:

- the snapshot taken when it was made: owner, org, project and app ids and
  names, and the tier. Nothing references an app, project or account, so the
  review outlives all of them;
- first and last seen, and the largest magnitude (a percentage of the
  reference) while it was open;
- evidence: rates, ratios, counts and windows only, numbers under a fixed set
  of keys. No address, DNS name or content is read or stored, and the store
  refuses any other key.

A finding that repeats updates the open row. Dismissing a review mutes its
owner, app, tier bucket and signal for 14 days; a finding in that time opens a new review
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
