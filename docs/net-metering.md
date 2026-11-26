# Network metering (FEATURE_NETMETER)

EXC-844. The platform measures how many bytes each customer app receives and
sends, once a minute, so abuse can be told from behaviour instead of from what
an app is called. This stage only measures and stores. It never stops, limits
or flags an app, and nothing reads the numbers except the admin console's
"top egress" card.

Off by default. Turn it on with `FEATURE_NETMETER=true`.

## What is stored

- `app_net_samples`: one row per app per minute (instances, rx and tx bytes
  per second, the busiest single instance), kept 48 hours.
- `app_net_hourly`: one row per app per finished hour (averages and the
  maximum), kept 30 days.

Rows are removed by whole hours: minutes go once the hour that holds them is
more than 48 hours old, hourly rows after 30 days. A finished hour is rolled up
once and never rebuilt, so the hour at the edge of the window keeps its full
figures.

Both tables also carry the owner id and tier the project had when the minute was
taken (added with behaviour review, empty on older rows), so [behaviour
review](behaviour-review.md) can attribute the history after the project is gone.

No IP address, DNS name or payload is read or stored; only byte counts. The
tables reference no app, project or user, so the history outlives a deleted app.

## Kubernetes

Provisioning reads each node's kubelet at `https://<node IP>:10250/stats/summary`
with its service account token and keeps only pods labelled as customer apps
(`excalibase.io/component=app`, managed by provisioning). Databases, build
jobs and function runtimes are not metered.

Enable it in the chart with `--set netmeter.enabled=true`. That:

- grants the role `list` on `nodes` and `get` on `nodes/stats` (never
  `nodes/proxy`);
- mounts the CA that signed the kubelets' serving certificates from the
  ConfigMap `netmeter.kubeletCAConfigMap` (key `ca.crt`; default
  `kube-root-ca.crt`, which Kubernetes publishes to every namespace and which
  on RKE2 and k3s is the kubelet's CA). The certificate is checked, there is no
  option to skip that. Where the kubelet is signed by another CA (kind,
  kubeadm), name your own ConfigMap;
- lets the pod's egress policy reach TCP 10250, and on a cluster with Cilium
  adds a CiliumNetworkPolicy naming the node by entity (`host`,
  `remote-node`), which an address rule does not reliably match.

The ConfigMap volume is optional. If it is missing the pod still starts, and
the meter logs why and stays off; creating the ConfigMap later needs a restart
of provisioning. Once running, the CA file is read again whenever it changes,
so a CA rotation needs no restart, and a replacement that cannot be read keeps
the last good one. Redirects are never followed, so the token goes only to the
kubelet.

Counter handling: baselines are kept per pod, a counter that went down counts
its new value, a pod first seen later is judged by its start time, and rates
use the kubelet's own timestamps. After a restart or a leader change one round
only records baselines. Bytes sent between a pod's last reading and its
deletion are not counted. A pod whose counters the kubelet did not report is
skipped, never stored as zero.

A failed read does not disturb the baselines: a round where nothing could be
read is skipped, and when one node fails the pods on it keep their baselines
for the next round (a long outage, more than three rounds, starts over with one
baseline-only round).

## Single host (Docker or Podman)

The same readings come from the engine's container stats through the engine
proxy; no other flag is needed (`FEATURE_HOSTMETRICS` is separate). A container
whose engine reports no network counters, or zero in both directions, is
recorded as not measured, never as zero: a rootless Podman network may report
nothing, and a running container that has really moved no byte at all is not a
case worth a number. Real Podman is still to be confirmed on a node. While the
meter is running, the admin console's top-egress card reads these samples
instead of `container_metrics`; if the flag is on but the meter did not start,
the card keeps reading `container_metrics`.

## Not done yet

Signals, the review list and the egress cap are later steps of EXC-844.
