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

No IP address, DNS name or payload is read or stored; only byte counts. The
tables reference no app, project or user, so the history outlives a deleted app.

## Kubernetes

Provisioning reads each node's kubelet at `https://<node IP>:10250/stats/summary`
with its service account token and keeps only pods labelled as customer apps
(`excalibase.io/component=app`, managed by provisioning). Databases, build
jobs and function runtimes are not metered.

Enable it in the chart with `--set netmeter.enabled=true`. That:

- grants the role `get` on `nodes/stats` and `get`, `list` on `nodes` (never
  `nodes/proxy`);
- mounts the CA that signed the kubelets' serving certificates from the
  ConfigMap `kubelet-serving-ca` (key `ca.crt`). The node install creates it;
  the certificate is checked, there is no option to skip that;
- on a cluster with Cilium, adds a CiliumNetworkPolicy so provisioning can
  reach TCP 10250 on the node (the kubelet is the `host` or `remote-node`
  entity, which an IP block rule does not reliably match).

Without the ConfigMap the meter logs why and stays off.

Counter handling: baselines are kept per pod, a counter that went down counts
its new value, a pod first seen later is judged by its start time, and rates
use the kubelet's own timestamps. After a restart or a leader change one round
only records baselines. Bytes sent between a pod's last reading and its
deletion are not counted.

## Single host (Docker or Podman)

The same readings come from the engine's container stats through the engine
proxy; no other flag is needed (`FEATURE_HOSTMETRICS` is separate). A container
whose engine reports no network counters is recorded as not measured, never as
zero. With the meter on, the admin console's top-egress card reads these
samples instead of `container_metrics`.

## Not done yet

Signals, the review list and the egress cap are later steps of EXC-844.
