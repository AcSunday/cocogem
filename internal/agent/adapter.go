package agent

import "context"

// AdapterInfo separates installation, adapter support and isolation evidence.
type AdapterInfo struct {
	ID           string   `json:"id"`
	Installed    bool     `json:"installed"`
	Version      string   `json:"version"`
	Program      string   `json:"program"`
	Identity     string   `json:"identity"`
	Adapter      string   `json:"adapter"`
	Isolation    string   `json:"isolation"`
	Supported    bool     `json:"supported"`
	Reason       string   `json:"reason"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// AdapterChecker rechecks the installed CLI before dispatch and recovery.
type AdapterChecker interface {
	Check(context.Context, string) (AdapterInfo, error)
}

// ConnectionChecker validates local connection configuration without a model call.
type ConnectionChecker interface {
	CheckConnection(context.Context, string, string) error
}

// ConnectionInfo is a non-secret snapshot of the exact model and upstream.
type ConnectionInfo struct {
	ResolvedModel string `json:"resolved_model"`
	Identity      string `json:"identity"`
}

// ConnectionInspector additionally exposes the resolved connection identity.
type ConnectionInspector interface {
	InspectConnection(context.Context, string, string) (ConnectionInfo, error)
}
