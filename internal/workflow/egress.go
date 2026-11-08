package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AcSunday/cocogem/internal/tools"
)

// externalPublish forwards one already-authorized external operation. The
// approval is bound by the tool's action ("external"), its destination scope and
// the exact argument digest, so a different body or target needs its own
// decision. A hostless egress fails closed rather than pretending success.
func (x *stepRuntime) externalPublish(ctx context.Context, raw json.RawMessage) (string, error) {
	if x.engine.Egress == nil {
		return "", tools.ErrEgressUnavailable
	}
	request, err := tools.DecodeExternalRequest(raw)
	if err != nil {
		return "", err
	}
	status, err := x.engine.Egress.Forward(ctx, request)
	record, _ := json.Marshal(map[string]any{
		"kind": request.Kind, "method": request.Method, "url": request.URL,
		"digest": request.Digest, "status": status, "forwarded": err == nil,
	})
	if err != nil {
		return string(record), fmt.Errorf("external %s failed before/while forwarding: %w", request.Kind, err)
	}
	x.mutated.Store(true)
	return string(record), nil
}
