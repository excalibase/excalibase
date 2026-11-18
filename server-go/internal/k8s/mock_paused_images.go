package k8s

import "context"

// defaultPausedImage is what a resume runs unless a test says otherwise.
const defaultPausedImage = "ghcr.io/acme/app:1"

// PausedImagesStub answers PausedAppImages for any app. PausedImages nil means
// the one ordinary image; an empty non-nil slice means the Deployment named none.
type PausedImagesStub struct {
	PausedImages    []string
	PausedImagesErr error
}

func (m *MockClient) PausedAppImages(ctx context.Context, namespace, appID, appName string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, "PausedAppImages:"+namespace+"/"+appID)
	if m.PausedImagesErr != nil {
		return nil, m.PausedImagesErr
	}
	if m.PausedImages == nil {
		return []string{defaultPausedImage}, nil
	}
	return append([]string{}, m.PausedImages...), nil
}
