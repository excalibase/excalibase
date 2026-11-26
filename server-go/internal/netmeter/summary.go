package netmeter

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// maxSummaryBytes bounds a kubelet answer; a node's summary is a few megabytes at most.
const maxSummaryBytes = 32 << 20

// PodNet is one pod's network counters from a kubelet summary.
type PodNet struct {
	Namespace, Name, UID string
	Started, At          time.Time
	Rx, Tx               uint64
}

type summaryDoc struct {
	Pods []struct {
		PodRef struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			UID       string `json:"uid"`
		} `json:"podRef"`
		StartTime time.Time `json:"startTime"`
		Network   *struct {
			Time    time.Time `json:"time"`
			RxBytes *uint64   `json:"rxBytes"`
			TxBytes *uint64   `json:"txBytes"`
		} `json:"network"`
	} `json:"pods"`
}

// ParseSummary reads a kubelet /stats/summary answer. A pod with no network
// counters, or with only one of the two, is left out.
func ParseSummary(reader io.Reader) ([]PodNet, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxSummaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read kubelet summary: %w", err)
	}
	if len(body) > maxSummaryBytes {
		return nil, fmt.Errorf("kubelet summary is larger than %d bytes", maxSummaryBytes)
	}
	var doc summaryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse kubelet summary: %w", err)
	}
	pods := make([]PodNet, 0, len(doc.Pods))
	for _, pod := range doc.Pods {
		// A counter the kubelet did not report is unmeasured, never zero.
		if pod.Network == nil || pod.Network.RxBytes == nil || pod.Network.TxBytes == nil {
			continue
		}
		pods = append(pods, PodNet{
			Namespace: pod.PodRef.Namespace, Name: pod.PodRef.Name, UID: pod.PodRef.UID,
			Started: pod.StartTime, At: pod.Network.Time, Rx: *pod.Network.RxBytes, Tx: *pod.Network.TxBytes,
		})
	}
	return pods, nil
}
