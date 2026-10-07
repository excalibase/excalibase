package k8s

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DenoRolloutState is where the project's function runtime stands.
type DenoRolloutState int

const (
	// DenoRuntimeAbsent: no runtime yet; the first deploy creates it.
	DenoRuntimeAbsent DenoRolloutState = iota
	// DenoRuntimeRollingOut: a new pod is starting, or the controller has
	// not yet acted on a change. The pod serving now may be the old one,
	// and whatever is deployed to it is lost when it goes.
	DenoRuntimeRollingOut
	// DenoRuntimeReady: every pod runs the current spec and is available.
	DenoRuntimeReady
)

func (s DenoRolloutState) String() string {
	switch s {
	case DenoRuntimeRollingOut:
		return "rolling out"
	case DenoRuntimeReady:
		return "ready"
	default:
		return "absent"
	}
}

// DenoRuntimeRollout reports whether the runtime has finished rolling out
// its current spec (EXC-569).
func (c *Client) DenoRuntimeRollout(ctx context.Context, namespace string) (DenoRolloutState, error) {
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, denoRuntimeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return DenoRuntimeAbsent, nil
	}
	if err != nil {
		return DenoRuntimeAbsent, fmt.Errorf("read deno deployment: %w", err)
	}
	if converged(dep) {
		return DenoRuntimeReady, nil
	}
	return DenoRuntimeRollingOut, nil
}
