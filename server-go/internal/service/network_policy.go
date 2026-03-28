package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var networkPolicyGVR = schema.GroupVersionResource{
	Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies",
}

type NetworkPolicyService struct {
	store     storage.InstanceStore
	k8sClient k8s.KubeClient
}

func NewNetworkPolicyService(store storage.InstanceStore, client k8s.KubeClient) *NetworkPolicyService {
	return &NetworkPolicyService{store: store, k8sClient: client}
}

func (s *NetworkPolicyService) UpdateNetworkPolicy(ctx context.Context, projectID string, cfg domain.NetworkConfig) error {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	var ingress []interface{}
	for _, cidr := range cfg.AllowedCIDRs {
		ingress = append(ingress, map[string]interface{}{
			"from": []interface{}{
				map[string]interface{}{
					"ipBlock": map[string]interface{}{"cidr": cidr},
				},
			},
		})
	}

	np := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "networking.k8s.io/v1",
			"kind":       "NetworkPolicy",
			"metadata": map[string]interface{}{
				"name":      projectID + "-network-policy",
				"namespace": inst.Namespace,
			},
			"spec": map[string]interface{}{
				"podSelector": map[string]interface{}{},
				"policyTypes": []interface{}{"Ingress"},
				"ingress":     ingress,
			},
		},
	}

	return s.k8sClient.ApplyCRD(ctx, networkPolicyGVR, inst.Namespace, np)
}
