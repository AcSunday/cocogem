package nativecli

import (
	"encoding/json"
	"sync"
)

// gateAliases maps the exact native call (tool name + canonical input) of one
// attempt to the identity the CLI's stream reported for it, plus equivalences
// between ids the CLI exposes on different channels.
//
// Gemini's hook payload has no tool-call id while its stream frame does, so the
// approval and the terminal event would otherwise use two different keys and the
// host could not settle one admission.
//
// Only an exact tool name + canonical input match is aliased. A different call
// with the same tool or the same arguments in another order never reuses an id.
type gateAliases struct {
	mu         sync.Mutex
	byCall     map[string]string
	equivalent map[string]string
}

func newGateAliases() *gateAliases {
	return &gateAliases{byCall: map[string]string{}, equivalent: map[string]string{}}
}

// aliasKey canonicalizes a native call for identity comparison.
func aliasKey(name string, input json.RawMessage) string {
	return name + "\x00" + canonicalToolInput(input)
}

// record stores the stream identity of one call.
func (a *gateAliases) record(name string, input json.RawMessage, id string) {
	if a == nil || id == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byCall[aliasKey(name, input)] = id
}

// resolve returns the stream identity already observed for this exact call.
func (a *gateAliases) resolve(name string, input json.RawMessage) string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byCall[aliasKey(name, input)]
}

// link records that two identities denote the same call (the hook's derived id
// and the stream's own tool_id).
func (a *gateAliases) link(first, second string) {
	if a == nil || first == "" || second == "" || first == second {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.equivalent[first] = second
	a.equivalent[second] = first
}

// canonical maps any equivalent identity to the one that was approved.
func (a *gateAliases) canonical(id string) string {
	if a == nil || id == "" {
		return id
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if other, ok := a.equivalent[id]; ok {
		return other
	}
	return id
}

// forget drops one call binding after its call settled.
func (a *gateAliases) forget(name string, input json.RawMessage) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.byCall, aliasKey(name, input))
}

// gateAliasRegistry attaches the per-attempt alias map to a bridge without
// changing the shared bridge type. Entries live exactly as long as the attempt:
// the gate broker registers on construction and removes on close.
var gateAliasRegistry = struct {
	sync.Mutex
	byBridge map[*bridge]*gateAliases
}{byBridge: map[*bridge]*gateAliases{}}

func attachGateAliases(target *bridge, aliases *gateAliases) {
	if target == nil || aliases == nil {
		return
	}
	gateAliasRegistry.Lock()
	defer gateAliasRegistry.Unlock()
	gateAliasRegistry.byBridge[target] = aliases
}

func detachGateAliases(target *bridge) {
	if target == nil {
		return
	}
	gateAliasRegistry.Lock()
	defer gateAliasRegistry.Unlock()
	delete(gateAliasRegistry.byBridge, target)
}

// gateAliasesFor returns the alias map of one attempt, if any.
func gateAliasesFor(target *bridge) *gateAliases {
	if target == nil {
		return nil
	}
	gateAliasRegistry.Lock()
	defer gateAliasRegistry.Unlock()
	return gateAliasRegistry.byBridge[target]
}
