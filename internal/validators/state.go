package validators

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"lecca.io/pharos-watchtower/internal/window"
)

type ValidatorStateFile struct {
	Version        int                          `json:"version"`
	ChainID        string                       `json:"chain_id"`
	Mode           string                       `json:"mode"`
	ValidatorsHash string                       `json:"validators_hash"`
	UpdatedAt      time.Time                    `json:"updated_at"`
	Validators     map[string]ValidatorSnapshot `json:"validators"`
}

type ValidatorSnapshot struct {
	Meta       ValidatorMetaSnapshot `json:"meta"`
	Window     window.WindowSnapshot `json:"window"`
	LastHeight uint64                `json:"last_height"`
	LastSeenAt time.Time             `json:"last_seen_at"`
	Down       bool                  `json:"down"`
}

type ValidatorMetaSnapshot struct {
	ValidatorID string `json:"validator_id"`
	Description string `json:"description"`
	BlsKeyHex   string `json:"bls_key_hex"`
	Staking     string `json:"staking"`
	Status      uint8  `json:"status"`
	Endpoint    string `json:"endpoint"`
	Owner       string `json:"owner"`
}

type StateStore struct {
	path string
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

func (s *StateStore) Load(chainID string) (ValidatorStateFile, error) {
	if s.path == "" {
		return ValidatorStateFile{Validators: make(map[string]ValidatorSnapshot)}, nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return ValidatorStateFile{Validators: make(map[string]ValidatorSnapshot)}, nil
		}
		return ValidatorStateFile{}, err
	}

	var state ValidatorStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return ValidatorStateFile{}, err
	}

	if state.Validators == nil {
		state.Validators = make(map[string]ValidatorSnapshot)
	}

	return state, nil
}

func (s *StateStore) Save(chainID string, mode string, validatorsHash string, validators map[string]*ValidatorState) error {
	if s.path == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	snapshots := make(map[string]ValidatorSnapshot)
	for blsKey, v := range validators {
		v.Mu.RLock()

		stakingStr := "0"
		if v.Meta.Staking != nil {
			stakingStr = v.Meta.Staking.String()
		}

		snapshots[blsKey] = ValidatorSnapshot{
			Meta: ValidatorMetaSnapshot{
				ValidatorID: v.Meta.ValidatorID,
				Description: v.Meta.Description,
				BlsKeyHex:   v.Meta.BlsKeyHex,
				Staking:     stakingStr,
				Status:      v.Meta.Status,
				Endpoint:    v.Meta.Endpoint,
				Owner:       v.Meta.Owner.Hex(),
			},
			Window:     v.Window.Export(),
			LastHeight: v.LastHeight,
			LastSeenAt: v.LastSeenAt,
			Down:       v.Down,
		}
		v.Mu.RUnlock()
	}

	state := ValidatorStateFile{
		Version:        1,
		ChainID:        chainID,
		Mode:           mode,
		ValidatorsHash: validatorsHash,
		UpdatedAt:      time.Now().UTC(),
		Validators:     snapshots,
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

func SnapshotToState(snapshot ValidatorSnapshot, windowDuration time.Duration) *ValidatorState {
	staking := new(big.Int)
	staking.SetString(snapshot.Meta.Staking, 10)

	state := &ValidatorState{
		Meta: ValidatorMeta{
			ValidatorID: snapshot.Meta.ValidatorID,
			Description: snapshot.Meta.Description,
			BlsKeyHex:   snapshot.Meta.BlsKeyHex,
			Staking:     staking,
			Status:      snapshot.Meta.Status,
			Endpoint:    snapshot.Meta.Endpoint,
			Owner:       common.HexToAddress(snapshot.Meta.Owner),
		},
		Window:     window.NewRollingWindow(windowDuration),
		LastHeight: snapshot.LastHeight,
		LastSeenAt: snapshot.LastSeenAt,
		Down:       snapshot.Down,
	}

	state.Window.Restore(snapshot.Window)
	return state
}
