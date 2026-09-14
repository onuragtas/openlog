package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/onuragtas/openlog/internal/updatereq"
)

// update_requests.message is text only: fixed updater messages get message_code/message_params in
// the response, free text does not.
func TestUpdateRequestResponseMessageCode(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	r := &updatereq.Request{ID: "r1", Action: updatereq.ActionApply, TargetVersion: "0.9.1", State: updatereq.StateFailed, RequestedAt: now,
		Message: "update to 0.9.1 failed and was rolled back to 0.9.0: health: container exited"}
	b, _ := json.Marshal(updateRequestResponse(r, false))
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	params, _ := got["message_params"].(map[string]any)
	if got["message_code"] != "rolled_back" || params["to"] != "0.9.1" || params["from"] != "0.9.0" || params["error"] != "health: container exited" {
		t.Fatalf("response = %s", b)
	}

	r.Message = "current version: dial tcp: lookup openlog: no such host"
	b, _ = json.Marshal(updateRequestResponse(r, false))
	if strings.Contains(string(b), "message_code") || strings.Contains(string(b), "message_params") {
		t.Fatalf("free text must not get a code: %s", b)
	}
}
