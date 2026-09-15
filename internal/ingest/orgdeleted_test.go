package ingest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/onuragtas/openlog/internal/config"
	"github.com/onuragtas/openlog/internal/tenant"
)

// deletedResolver resolves goodkey, reports deletedkey as a key of a deleted organization and everything else as unknown.
type deletedResolver struct{}

func (deletedResolver) Resolve(_ context.Context, key string) (string, error) {
	switch key {
	case "goodkey":
		return "tenant-a", nil
	case "deletedkey":
		return "", tenant.ErrOrgDeleted
	}
	return "", tenant.ErrUnknownKey
}

func newDeletedService(t *testing.T, p Producer) *Service {
	t.Helper()
	cfg := config.Ingest{MaxBodyBytes: 1 << 20, ProduceTimeout: time.Second, HTTPAddr: "127.0.0.1:0", GRPCAddr: "127.0.0.1:0"}
	return New(cfg, "openlog", deletedResolver{}, p, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
}

func TestOrgDeletedKeyHTTP403(t *testing.T) {
	prod := &fakeProducer{}
	s := newDeletedService(t, prod)
	h := s.HTTPHandler()
	body, _ := proto.Marshal(hostLogs("h1"))
	rec := post(h, "/v1/logs", ctProtobuf, "deletedkey", body, nil)
	var st rpcstatus.Status
	_ = proto.Unmarshal(rec.Body.Bytes(), &st)
	if rec.Code != 403 || codes.Code(st.Code) != codes.PermissionDenied || errorReason(t, &st) != ReasonOrgDeleted || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("deleted org key: status %d body %+v", rec.Code, &st)
	}
	for _, key := range []string{"", "wrong"} {
		if rec := post(h, "/v1/logs", ctProtobuf, key, body, nil); rec.Code != 401 {
			t.Fatalf("key %q: status %d, want 401", key, rec.Code)
		}
	}
	if rec := post(h, "/v1/logs", ctProtobuf, "goodkey", body, nil); rec.Code != 200 {
		t.Fatalf("good key: %d", rec.Code)
	}
	if len(prod.msgs) != 1 {
		t.Fatalf("produced %d messages, want 1 (only the good key)", len(prod.msgs))
	}
}

func TestOrgDeletedKeyGRPC(t *testing.T) {
	s := newDeletedService(t, &fakeProducer{})
	export := func(key string) error {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("openlog-license-key", key))
		_, err := logsServer{s: s}.Export(ctx, hostLogs("h1"))
		return err
	}
	st, _ := status.FromError(export("deletedkey"))
	reason := ""
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			reason = info.Reason
		}
	}
	if st.Code() != codes.PermissionDenied || reason != ReasonOrgDeleted {
		t.Fatalf("deleted org key: %v", st.Err())
	}
	if err := export("wrong"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unknown key: %v", err)
	}
}
