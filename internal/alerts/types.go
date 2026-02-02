package alerts

import "time"

type RuleID string

const (
	RuleNodeDowntime                   RuleID = "node_downtime"
	RuleWsDowntime                     RuleID = "ws_downtime"
	RuleNodeHeightStalled              RuleID = "node_height_stalled"
	RuleValidatorDowntime              RuleID = "validator_downtime"
	RuleValidatorUptime                RuleID = "validator_uptime"
	RuleValidatorLastSignHeightHalting RuleID = "validator_last_sign_height_halting"
)

type SubjectType string

const (
	SubjectNode      SubjectType = "node"
	SubjectValidator SubjectType = "validator"
)

type AlertStatus string

const (
	AlertFiring   AlertStatus = "firing"
	AlertResolved AlertStatus = "resolved"
)

type AlertEvent struct {
	Key         string
	RuleID      RuleID
	SubjectType SubjectType
	SubjectID   string
	SubjectName string
	ChainID     string
	Status      AlertStatus
	Severity    string
	Title       string
	Message     string
	Details     []AlertDetail
	Timestamp   time.Time
}

type AlertDetail struct {
	Label string
	Value string
}
