package proxy

import (
	"encoding/json"
	"time"
)

// Assignment represents a user-to-backend mapping in the assignment table.
type Assignment struct {
	Backend    string    `json:"backend"`
	AssignedAt time.Time `json:"assigned_at"`
	Source     string    `json:"source"` // "hash", "assignment", "discovery"
}

func (a *Assignment) marshal() (string, error) {
	b, err := json.Marshal(a)
	return string(b), err
}

func unmarshalAssignment(data string) (*Assignment, error) {
	var a Assignment
	err := json.Unmarshal([]byte(data), &a)
	return &a, err
}
