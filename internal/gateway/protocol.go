package gateway

import "regexp"

// Wire format of tally protocol v1, documented in docs/protocol.md.

const protocolVersion = 1

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type clientMessage struct {
	Version  int    `json:"version"`
	Type     string `json:"type"`
	DeviceID string `json:"deviceId"`
	SourceID string `json:"sourceId"`
	Firmware string `json:"firmware"`
}

type registeredMessage struct {
	Version             int    `json:"version"`
	Type                string `json:"type"`
	DeviceID            string `json:"deviceId"`
	SourceID            string `json:"sourceId"`
	Session             string `json:"session"`
	HeartbeatIntervalMs int64  `json:"heartbeatIntervalMs"`
	TimeoutMs           int64  `json:"timeoutMs"`
}

type tallyMessage struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	SourceID  string `json:"sourceId"`
	Program   bool   `json:"program"`
	Preview   bool   `json:"preview"`
	State     string `json:"state"`
	Valid     bool   `json:"valid"`
	Session   string `json:"session"`
	Sequence  uint64 `json:"sequence"`
	Timestamp string `json:"timestamp"`
}

type heartbeatMessage struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	Session   string `json:"session"`
	Sequence  uint64 `json:"sequence"`
	Timestamp string `json:"timestamp"`
}

type errorMessage struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

const (
	codeInvalidMessage     = "invalid_message"
	codeInvalidRegister    = "invalid_register"
	codeUnsupportedVersion = "unsupported_version"
	codeUnknownType        = "unknown_type"
)
