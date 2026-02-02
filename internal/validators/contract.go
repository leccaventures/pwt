package validators

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ContractService handles contract interactions for validator information
type ContractService struct {
	client   *ethclient.Client
	contract *abi.ABI
	address  common.Address
}

// Contract ABI JSON for validator functions
const contractABI = `[
	{
		"inputs": [],
		"name": "getActiveValidators",
		"outputs": [
			{
				"internalType": "bytes32[]",
				"name": "",
				"type": "bytes32[]"
			}
		],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [
			{
				"internalType": "bytes32",
				"name": "",
				"type": "bytes32"
			}
		],
		"name": "validators",
		"outputs": [
			{"internalType":"string","name":"description","type":"string"},
			{"internalType":"string","name":"publicKey","type":"string"},
			{"internalType":"string","name":"publicKeyPop","type":"string"},
			{"internalType":"string","name":"blsPublicKey","type":"string"},
			{"internalType":"string","name":"blsPublicKeyPop","type":"string"},
			{"internalType":"string","name":"endpoint","type":"string"},
			{"internalType":"uint8","name":"status","type":"uint8"},
			{"internalType":"bytes32","name":"poolId","type":"bytes32"},
			{"internalType":"uint256","name":"totalStake","type":"uint256"},
			{"internalType":"address","name":"owner","type":"address"},
			{"internalType":"uint256","name":"stakeSnapshot","type":"uint256"},
			{"internalType":"uint256","name":"pendingWithdrawStake","type":"uint256"},
			{"internalType":"uint8","name":"pendingWithdrawWindow","type":"uint8"}
		],
		"stateMutability": "view",
		"type": "function"
	}
]`

// NewContractService creates a new contract service instance
func NewContractService(rpcURL, address string) (*ContractService, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RPC: %w", err)
	}

	parsedABI, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to parse ABI: %w", err)
	}

	addr := common.HexToAddress(address)

	return &ContractService{
		client:   client,
		contract: &parsedABI,
		address:  addr,
	}, nil
}

// Close closes the client connection
func (s *ContractService) Close() {
	if s.client != nil {
		s.client.Close()
	}
}

// GetActiveValidators retrieves the list of active validator IDs
func (s *ContractService) GetActiveValidators(ctx context.Context) ([][32]byte, error) {
	data, err := s.contract.Pack("getActiveValidators")
	if err != nil {
		return nil, fmt.Errorf("failed to pack method: %w", err)
	}

	msg := ethereum.CallMsg{
		To:   &s.address,
		Data: data,
	}

	result, err := s.client.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call contract: %w", err)
	}

	method := s.contract.Methods["getActiveValidators"]
	values, err := method.Outputs.UnpackValues(result)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack result: %w", err)
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("unexpected number of return values: 0")
	}

	validatorIDsSlice, ok := values[0].([][32]byte)
	if !ok {
		return nil, fmt.Errorf("failed to convert result to [][32]byte")
	}

	return validatorIDsSlice, nil
}

// ContractValidatorInfo represents validator information from contract
type ContractValidatorInfo struct {
	Description          string
	PublicKey            string
	PublicKeyPop         string
	BlsPublicKey         string
	BlsPublicKeyPop      string
	Endpoint             string
	Status               uint8
	PoolID               [32]byte
	TotalStake           *big.Int
	Owner                common.Address
	StakeSnapshot        *big.Int
	PendingWithdrawStake *big.Int
	PendingWithdrawWindow uint8
}

// GetValidatorInfo retrieves detailed information for a specific validator
func (s *ContractService) GetValidatorInfo(ctx context.Context, validatorID [32]byte) (*ContractValidatorInfo, error) {
	data, err := s.contract.Pack("validators", validatorID)
	if err != nil {
		return nil, fmt.Errorf("failed to pack method: %w", err)
	}

	msg := ethereum.CallMsg{
		To:   &s.address,
		Data: data,
	}

	result, err := s.client.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to call contract: %w", err)
	}

	method := s.contract.Methods["validators"]
	values, err := method.Outputs.UnpackValues(result)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack result: %w", err)
	}

	if len(values) < 13 {
		return nil, fmt.Errorf("unexpected number of return values: %d", len(values))
	}

	validator := &ContractValidatorInfo{}

	if v, ok := values[0].(string); ok {
		validator.Description = v
	}
	if v, ok := values[1].(string); ok {
		validator.PublicKey = v
	}
	if v, ok := values[2].(string); ok {
		validator.PublicKeyPop = v
	}
	if v, ok := values[3].(string); ok {
		validator.BlsPublicKey = v
	}
	if v, ok := values[4].(string); ok {
		validator.BlsPublicKeyPop = v
	}
	if v, ok := values[5].(string); ok {
		validator.Endpoint = v
	}
	if v, ok := values[6].(uint8); ok {
		validator.Status = v
	}
	if v, ok := values[7].([32]byte); ok {
		validator.PoolID = v
	}
	if v, ok := values[8].(*big.Int); ok {
		validator.TotalStake = v
	}
	if v, ok := values[9].(common.Address); ok {
		validator.Owner = v
	}
	if v, ok := values[10].(*big.Int); ok {
		validator.StakeSnapshot = v
	}
	if v, ok := values[11].(*big.Int); ok {
		validator.PendingWithdrawStake = v
	}
	if v, ok := values[12].(uint8); ok {
		validator.PendingWithdrawWindow = v
	}

	return validator, nil
}

// bytes32ToHex converts [32]byte to hex string
func bytes32ToHex(b [32]byte) string {
	return "0x" + hex.EncodeToString(b[:])
}
