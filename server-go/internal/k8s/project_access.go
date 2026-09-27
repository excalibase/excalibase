package k8s

import (
	"context"
	"errors"
	"fmt"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProjectRoleBindingName is the RoleBinding in every project namespace that
// grants provisioning its tenant role there.
const ProjectRoleBindingName = "excalibase-provisioning"

const (
	defaultAccessPoll    = 250 * time.Millisecond
	defaultAccessTimeout = 30 * time.Second
)

var (
	// ErrProjectAccessUnset refuses to create a project namespace provisioning
	// could not then manage.
	ErrProjectAccessUnset = errors.New("project namespace access is not configured: PROJECT_NAMESPACE_ROLE, PROVISIONING_SERVICE_ACCOUNT and POD_NAMESPACE are required")
	// ErrProjectAccessNotInForce: the RoleBinding exists but the API server
	// still refuses provisioning in the namespace.
	ErrProjectAccessNotInForce = errors.New("provisioning's role binding did not take effect in the project namespace")
	// ErrForeignProjectBinding: a binding of that name grants something else.
	ErrForeignProjectBinding = errors.New("the project namespace already holds a different provisioning role binding")
)

// ProjectAccess is the identity provisioning binds, in each project namespace,
// to the tenant ClusterRole it holds nowhere else.
type ProjectAccess struct {
	ClusterRole    string
	ServiceAccount string
	Namespace      string
}

func (a ProjectAccess) validate() error {
	if a.ClusterRole == "" || a.ServiceAccount == "" || a.Namespace == "" {
		return ErrProjectAccessUnset
	}
	return nil
}

// WithProjectAccess returns a copy of the client that binds access in every
// project namespace it creates.
func (c *Client) WithProjectAccess(access ProjectAccess) *Client {
	configured := *c
	configured.projectAccess = access
	return &configured
}

func (a ProjectAccess) binding(namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ProjectRoleBindingName,
			Namespace: namespace,
			Labels:    map[string]string{componentLabelKey: "provisioning-access"},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: a.ClusterRole},
		Subjects: []rbacv1.Subject{{
			Kind: rbacv1.ServiceAccountKind, Name: a.ServiceAccount, Namespace: a.Namespace,
		}},
	}
}

// bindProjectAccess grants provisioning its tenant role in namespace and waits
// until the API server enforces it, since every call after this relies on it.
func (c *Client) bindProjectAccess(ctx context.Context, namespace string) error {
	desired := c.projectAccess.binding(namespace)
	bindings := c.clientset.RbacV1().RoleBindings(namespace)
	_, err := bindings.Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		err = c.checkExistingBinding(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("bind provisioning in %s: %w", namespace, err)
	}
	return c.waitForProjectAccess(ctx, namespace)
}

// NamespaceDeleting reports whether name is gone or terminating. Reading the
// namespace object is a cluster-wide grant, so it answers after the binding
// in the namespace has been removed.
func (c *Client) NamespaceDeleting(ctx context.Context, name string) (bool, error) {
	ns, err := c.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get namespace %s: %w", name, err)
	}
	return ns.DeletionTimestamp != nil, nil
}

func (c *Client) checkExistingBinding(ctx context.Context, desired *rbacv1.RoleBinding) error {
	current, err := c.clientset.RbacV1().RoleBindings(desired.Namespace).Get(ctx, desired.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.RoleRef != desired.RoleRef || len(current.Subjects) != 1 || current.Subjects[0] != desired.Subjects[0] {
		return ErrForeignProjectBinding
	}
	return nil
}

func (c *Client) waitForProjectAccess(ctx context.Context, namespace string) error {
	poll, timeout := c.accessPoll, c.accessTimeout
	if poll == 0 {
		poll = defaultAccessPoll
	}
	if timeout == 0 {
		timeout = defaultAccessTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		allowed, err := c.canManagePolicies(ctx, namespace)
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s", ErrProjectAccessNotInForce, namespace)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (c *Client) canManagePolicies(ctx context.Context, namespace string) (bool, error) {
	review := &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace, Verb: "create", Group: "networking.k8s.io", Resource: "networkpolicies",
			},
		},
	}
	result, err := c.clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("check provisioning access in %s: %w", namespace, err)
	}
	return result.Status.Allowed, nil
}
