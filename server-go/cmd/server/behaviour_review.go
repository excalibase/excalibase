package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// reviewTables is what the detector needs from the platform store.
type reviewTables interface {
	behaviour.History
	behaviour.Directory
	behaviour.Reviews
}

func behaviourReviewOn(cfg config.AppConfig) bool {
	return features.Enabled(context.Background(), cfg.Features(), features.BehaviourReview)
}

// behaviourSampler builds the behaviour detector for the metrics loop (EXC-844),
// or nil while its flag is off or the store keeps no review tables. It reads
// the meter's history and writes reviews; it can stop or limit nothing.
func behaviourSampler(cfg config.AppConfig, store any) service.LoopSampler {
	if !behaviourReviewOn(cfg) {
		return nil
	}
	tables, ok := store.(reviewTables)
	if !ok {
		log.Printf("behaviour review: the platform store keeps no review tables; off")
		return nil
	}
	log.Printf("behaviour review: on (%s)", cfg.ProvisionerMode)
	return behaviour.NewDetector(behaviour.DetectorDeps{History: tables, Directory: tables, Reviews: tables}, cfg.Behaviour)
}
