package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const statusFile = "status.json"

type HistoryWriter struct {
	basePath string
}

func NewHistoryWriter(basePath string) *HistoryWriter {
	return &HistoryWriter{basePath: basePath}
}

func (w *HistoryWriter) attemptDir(projectID, attemptID string) string {
	return filepath.Join(w.basePath, "projects", projectID, "provisioning-history", attemptID)
}

func (w *HistoryWriter) StartAttempt(projectID, attemptID string) error {
	dir := w.attemptDir(projectID, attemptID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create attempt dir: %w", err)
	}
	status := map[string]string{
		"attemptId": attemptID,
		"status":    "IN_PROGRESS",
		"startedAt": time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal attempt status: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, statusFile), data, 0644)
}

func (w *HistoryWriter) LogStage(projectID, attemptID string, stage domain.ProvisioningStage, message string) {
	dir := w.attemptDir(projectID, attemptID)
	f, err := os.OpenFile(filepath.Join(dir, "stages.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	entry := map[string]string{
		"stage":     string(stage),
		"message":   message,
		"timestamp": time.Now().Format(time.RFC3339),
	}
	line, _ := json.Marshal(entry)
	f.Write(append(line, '\n'))
}

func (w *HistoryWriter) FinalizeAttempt(projectID, attemptID, status string) {
	dir := w.attemptDir(projectID, attemptID)
	finalStatus := map[string]string{
		"attemptId":  attemptID,
		"status":     status,
		"finishedAt": time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(finalStatus, "", "  ")
	if err != nil {
		log.Printf("WARN: marshal attempt status %s: %v", attemptID, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, statusFile), data, 0644); err != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, statusFile), err)
	}
}

func (w *HistoryWriter) ListAttempts(projectID string) []string {
	dir := filepath.Join(w.basePath, "projects", projectID, "provisioning-history")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []string
	for _, e := range entries {
		if e.IsDir() {
			result = append(result, e.Name())
		}
	}
	return result
}
