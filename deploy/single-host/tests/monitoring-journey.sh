# Sourced by boot-smoke.sh with HOST_METRICS=1 (EXC-670): the host and every
# container are sampled each minute, the project's database shows its figures,
# its members see only its own containers, and a stopped database raises an
# alert that its restart resolves. Needs T, PID, TC, PORT, DOCKER, c, ok, ko.
echo "== host monitoring"
api() { c -H "Authorization: Bearer $T" "https://studio.localhost:$PORT/api$1"; }
field() { python3 -c "import json,sys
d=json.load(sys.stdin)
for k in sys.argv[1].split('.'):
    d = d.get(k) if isinstance(d, dict) else None
print('' if d is None else d)" "$1" 2>/dev/null; }

H=
for i in $(seq 60); do H=$(api /admin/monitoring/host); [ -n "$(echo "$H" | field current.cpuPercent)" ] && break; sleep 5; done
cpu=$(echo "$H" | field current.cpuPercent); mem=$(echo "$H" | field current.memoryUsedBytes); disk=$(echo "$H" | field current.diskTotalBytes)
load=$(echo "$H" | field current.load5); netsrc=$(echo "$H" | field current.networkSource)
[ -n "$cpu" ] && [ -n "$mem" ] && [ -n "$disk" ] && [ -n "$load" ] && ok "host sampled: cpu ${cpu}% memory ${mem}B disk ${disk}B load5 $load" || ko "host sample: ${H:0:400}"
[ "$netsrc" = host ] && ok "host network read from the host's interfaces" || ko "host network source '$netsrc'"
echo "info: temperatures: $(echo "$H" | field current.temperatureMaxCelsius) C (empty where the host has no sensors)"

CS=$(api /admin/monitoring/containers)
echo "$CS" | python3 -c "import json,sys
d={c['name']:c for c in json.load(sys.stdin)['containers']}
p=d.get('excalibase-postgres',{}); t=d.get('$TC',{})
assert p.get('component')=='platform' and p.get('state')=='running' and p.get('memoryBytes'), p
assert t.get('component')=='database' and t.get('projectId')=='$PID' and t.get('cpuCores') is not None, t
assert t.get('memoryLimitBytes'), t" && ok "platform and project containers sampled with figures" || ko "containers: ${CS:0:600}"

M=$(api /provision/$PID/metrics/current)
[ "$(echo "$M" | field metricsAvailable)" = True ] && [ -n "$(echo "$M" | field activeConnections)" ] && [ -n "$(echo "$M" | field cpuLimitCores)" ] \
  && ok "the project's Metrics page has its database figures" || ko "database metrics: ${M:0:400}"
PC=$(api "/projects/$PID/containers/metrics?range=1h")
case "$PC" in *"\"$TC\""*) case "$PC" in *'"excalibase-postgres"'*) ko "a project sees the platform's containers" ;; *) ok "the project sees its own containers only" ;; esac ;; *) ko "project containers: ${PC:0:400}" ;; esac

st=$(c -o /dev/null -w '%{http_code}' -X PUT -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "https://studio.localhost:$PORT/api/admin/monitoring/alert-settings" \
  -d '{"enabled":true,"cpuPercent":90,"memoryPercent":90,"diskPercent":99,"temperatureCelsius":95,"windowMinutes":5,"downMinutes":0,"emails":[]}')
[ "$st" = 200 ] && ok "alert rules saved" || ko "alert rules: $st"
$DOCKER stop "$TC" >/dev/null 2>&1
A=
for i in $(seq 40); do A=$(api /alerts/project/$PID); case "$A" in *"container.down:$TC"*) break ;; esac; sleep 5; done
case "$A" in *"container.down:$TC"*) ok "a stopped database raises its project's alert" ;; *) ko "no down alert: ${A:0:400}" ;; esac
$DOCKER start "$TC" >/dev/null 2>&1
for i in $(seq 40); do A=$(api /alerts/project/$PID); case "$A" in *"container.down:$TC"*) sleep 5 ;; *) break ;; esac; done
case "$A" in *"container.down:$TC"*) ko "the alert stayed open after the restart" ;; *) ok "the restart resolves the alert" ;; esac
for i in $(seq 30); do api /provision/$PID/metrics/current | grep -q '"metricsAvailable":true' && break; sleep 5; done

echo "info: provisioning after sampling: $($DOCKER stats --no-stream --format '{{.CPUPerc}} cpu, {{.MemUsage}}' excalibase-provisioning 2>/dev/null)"
refused=$($DOCKER logs excalibase-engine-proxy 2>&1 | grep -c '/stats.* 40[34] \| 403 GET .*/stats')
echo "info: proxy refusals of stats calls: $refused"
