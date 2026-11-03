package service

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// hostLogProjectID is every project id provisioning generates; anything else
// never reaches a Loki selector.
var hostLogProjectID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// SetHostLogStore makes a single host search its bundled Loki (EXC-672), which
// provisioning fills from the project containers' output.
func (s *ProvisioningService) SetHostLogStore(lokiURL string) {
	s.hostLogStore = strings.TrimRight(lokiURL, "/")
}

// hostLogSelector selects a project's database lines; the labels are the ones
// the single-host shipper sets, never anything a log line says.
func hostLogSelector(projectID string) (string, bool) {
	if !hostLogProjectID.MatchString(projectID) {
		return "", false
	}
	return fmt.Sprintf("{project_id=%s,component=%s}", strconv.Quote(projectID), strconv.Quote(domain.ContainerDatabase)), true
}

// searchHostLogStore reads the stored lines with the Kubernetes query code,
// no further back than the plan keeps them, and reads the engine log when
// Loki has none yet or does not answer, as Kubernetes reads the pod log.
func (s *ProvisioningService) searchHostLogStore(ctx context.Context, inst *domain.DatabaseInstance, search LogSearch) (LogPage, error) {
	keptFor := s.logsKeptFor(ctx, inst)
	if keptFor > 0 && search.Since > keptFor {
		search.Since = keptFor
	}
	selector, ok := hostLogSelector(inst.ProjectID)
	if !ok {
		log.Printf("logs of a project with an unexpected id: engine log only")
		return s.readEngineLog(ctx, inst, search)
	}
	out, err := s.getLogsFromLoki(ctx, s.hostLogStore, selector, inst.ProjectID, search)
	if err == nil && out != "" {
		return LogPage{Lines: out, KeptFor: keptFor}, nil
	}
	page, engineErr := s.readEngineLog(ctx, inst, search)
	if engineErr != nil && err == nil {
		return LogPage{KeptFor: keptFor}, nil
	}
	page.KeptFor = keptFor
	return page, engineErr
}
