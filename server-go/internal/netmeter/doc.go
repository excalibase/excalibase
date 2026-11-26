// Package netmeter measures how much each customer app sends and receives
// (EXC-844): cumulative network counters read from the kubelet on Kubernetes
// or from the engine on a single host, turned into per-app rates once a
// minute and stored, so abuse can be told from behaviour instead of from
// what an app is called. It only measures; nothing here acts on an app.
package netmeter
