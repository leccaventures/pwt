package alerts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type AlertStateFile struct {
	Version   int                       `json:"version"`
	ChainID   string                    `json:"chain_id"`
	UpdatedAt time.Time                 `json:"updated_at"`
	Alerts    map[string]AlertStateItem `json:"alerts"`
}

type AlertStateItem struct {
	Key          string      `json:"key"`
	RuleID       RuleID      `json:"rule_id"`
	SubjectType  SubjectType `json:"subject_type"`
	SubjectID    string      `json:"subject_id"`
	Status       AlertStatus `json:"status"`
	FiringSince  time.Time   `json:"firing_since"`
	LastObserved time.Time   `json:"last_observed_at"`
	LastEventAt  time.Time   `json:"last_event_at"`
}

type StateStore struct {
	path string
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

func (s *StateStore) Load(chainID string) (map[string]AlertStateItem, error) {
	if s.path == "" {
		return make(map[string]AlertStateItem), nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]AlertStateItem), nil
		}
		return nil, err
	}

	var state AlertStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Alerts == nil {
		state.Alerts = make(map[string]AlertStateItem)
	}
	return state.Alerts, nil
}

func (s *StateStore) Save(chainID string, alerts map[string]AlertStateItem) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	state := AlertStateFile{
		Version:   1,
		ChainID:   chainID,
		UpdatedAt: time.Now().UTC(),
		Alerts:    alerts,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tempPath := fmt.Sprintf("%s.tmp", s.path)
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, s.path)
}
