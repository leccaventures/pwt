package validators

import (
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"lecca.io/pharos-watchtower/internal/window"
)

type ValidatorMeta struct {
	ValidatorID string
	Description string // moniker
	BlsKeyHex   string // normalized lowercase without 0x
	Staking     *big.Int
	Status      uint8
	Endpoint    string
	Owner       common.Address
}

type ValidatorState struct {
	Meta       ValidatorMeta
	Window     *window.RollingWindow
	LastHeight uint64
	LastSeenAt time.Time
	Down       bool
	Mu         sync.RWMutex
}
