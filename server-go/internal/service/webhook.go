package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"
)

type WebhookService struct {
	client *http.Client
}

func NewWebhookService() *WebhookService {
	return &WebhookService{
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (s *WebhookService) Send(url, projectID, event, message string) {
	payload, _ := json.Marshal(map[string]string{
		"projectId": projectID,
		"event":     event,
		"message":   message,
		"timestamp": time.Now().Format(time.RFC3339),
	})

	resp, err := s.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return // fire-and-forget
	}
	resp.Body.Close()
}
