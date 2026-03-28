package domain

import "time"

type ParameterGroup struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Parameters     map[string]string `json:"parameters"`
	ParametersJSON string            `json:"parametersJson,omitempty"` // raw JSON string for compat
	CreatedAt      *time.Time        `json:"createdAt,omitempty"`
	UpdatedAt      *time.Time        `json:"updatedAt,omitempty"`
}
