package nativecli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AcSunday/cocogem/internal/model"
)

func TestAgyFullAccessAdmitsUnlistedToolsAndRestrictsReadOnly(t *testing.T) {
	root := t.TempDir()
	req := model.Request{
		Executor:         "agy",
		ExecutionMode:    "full_access",
		TrustedExecution: true,
		NativeTools:      []string{"read_file"},
		AuthorizeNative: func(_ context.Context, r model.NativePermissionRequest) (model.NativePermissionDecision, error) {
			return model.NativePermissionDecision{Allow: true, Scope: model.NativeScopeOnce}, nil
		},
	}
	approver := newNativeApprover(nil, nil, req, "agy", nil)
	broker, err := newGateBroker(approver, nil, t.TempDir(), root, "agy")
	if err != nil {
		t.Fatal(err)
	}
	defer broker.close()
	broker.fullAccess = true
	broker.agyPolicy = newAgyGatePolicy(nil, nil, []string{root}, "", false, true)

	send := func() map[string]any {
		t.Helper()
		payload := []byte(`{"hookEventName":"before_tool","conversationId":"c-1","cwd":"` + root + `","toolCall":{"id":"call-1","name":"run_command","args":{"CommandLine":"echo ok"}}}`)
		rec := httptest.NewRecorder()
		httpReq := httptest.NewRequest(http.MethodPost, broker.url()+"/gate/agy/authorize", bytes.NewReader(payload))
		httpReq.Host = broker.listener.Addr().String()
		httpReq.Header.Set("Authorization", "Bearer "+broker.token)
		broker.serveAgyAuthorize(rec, httpReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("unexpected status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := send(); got["decision"] != "allow" {
		t.Fatalf("expected full_access agy authorize to allow run_command, got %+v", got)
	}

	// ReadOnly mode still denies run_command even with fullAccess flag.
	broker.agyPolicy = newAgyGatePolicy(nil, nil, []string{root}, "", true, true)
	if got := send(); got["decision"] != "deny" {
		t.Fatalf("expected readOnly agy authorize to deny run_command even under fullAccess, got %+v", got)
	}
}
