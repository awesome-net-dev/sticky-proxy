package proxy

import (
	"encoding/json"
	"time"
)

// Assignment represents a user-to-backend mapping in the assignment table.
type Assignment struct {
	Backend    string    `json:"backend"`
	AssignedAt time.Time `json:"assigned_at"`
	Source     string    `json:"source"`           // "hash", "assignment", "discovery"
	Weight     int       `json:"weight,omitempty"` // resource weight; 0 or absent means 1
}

// EffectiveWeight returns the assignment's weight, defaulting to 1.
func (a *Assignment) EffectiveWeight() int {
	if a.Weight <= 0 {
		return 1
	}
	return a.Weight
}

// BulkAssignEntry carries a backend and weight for bulk assignment operations.
type BulkAssignEntry struct {
	Backend string
	Weight  int
}

func unmarshalAssignment(data string) (*Assignment, error) {
	var a Assignment
	err := json.Unmarshal([]byte(data), &a)
	return &a, err
}
