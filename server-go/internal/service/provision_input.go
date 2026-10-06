package service

import (
	"fmt"
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"k8s.io/apimachinery/pkg/util/validation"
)

// reservedLabelDomains are label prefixes Kubernetes, CNPG and the platform
// select on; a tag there could pull the cluster into someone else's selector.
var reservedLabelDomains = []string{"kubernetes.io", "k8s.io", "cnpg.io", "excalibase.io", "excalibase.com"}

// validateTags refuses a tag that is not a valid Kubernetes label, since tags
// are written onto the cluster as labels.
func validateTags(tags map[string]string) error {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if msgs := validation.IsQualifiedName(key); len(msgs) > 0 {
			return fmt.Errorf("tag key %q is not a valid label key: %s", key, msgs[0])
		}
		if reservedLabelPrefix(key) {
			return fmt.Errorf("tag key %q uses a reserved prefix", key)
		}
		if msgs := validation.IsValidLabelValue(tags[key]); len(msgs) > 0 {
			return fmt.Errorf("tag %q value is not a valid label value: %s", key, msgs[0])
		}
	}
	return nil
}

func reservedLabelPrefix(key string) bool {
	prefix, _, found := strings.Cut(key, "/")
	if !found {
		return false
	}
	for _, reserved := range reservedLabelDomains {
		if prefix == reserved || strings.HasSuffix(prefix, "."+reserved) {
			return true
		}
	}
	return false
}

// validateOwnerAndTags checks the request fields that are written verbatim
// into the cluster: the owner role (pg_hba) and the tags (labels).
func validateOwnerAndTags(req domain.ProvisioningRequest) error {
	if req.MasterUsername != "" {
		if err := domain.ValidateMasterUsername(req.MasterUsername); err != nil {
			return err
		}
	}
	return validateTags(req.Tags)
}
