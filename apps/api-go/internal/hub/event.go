package hub

import "encoding/json"

type HubEvent struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Level    string                 `json:"level"`
	Title    string                 `json:"title"`
	Content  string                 `json:"content"`
	Symbol   string                 `json:"symbol,omitempty"`
	Market   string                 `json:"market,omitempty"`
	Ts       int64                  `json:"ts"`
	Meta     map[string]interface{} `json:"meta,omitempty"`
	DedupeKey string               `json:"dedupe_key,omitempty"`
}

// Wire messages (server → client)

type ConnectedMsg struct {
	Kind         string `json:"kind"`
	ConnectionID string `json:"connectionId"`
	Ts           int64  `json:"ts"`
}

type SubscribedMsg struct {
	Kind    string   `json:"kind"`
	Markets []string `json:"markets"`
	Symbols []string `json:"symbols"`
	Types   []string `json:"types"`
	Ts      int64    `json:"ts"`
}

type EventMsg struct {
	Kind string   `json:"kind"`
	Data HubEvent `json:"data"`
}

type PingMsg struct {
	Kind string `json:"kind"`
	Ts   int64  `json:"ts"`
}

type ErrorMsg struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// Wire messages (client → server)

type ClientOp struct {
	Op      string   `json:"op"`
	Markets []string `json:"markets,omitempty"`
	Symbols []string `json:"symbols,omitempty"`
	Types   []string `json:"types,omitempty"`
}

func ParseClientOp(data []byte) (*ClientOp, error) {
	var op ClientOp
	if err := json.Unmarshal(data, &op); err != nil {
		return nil, err
	}
	return &op, nil
}
