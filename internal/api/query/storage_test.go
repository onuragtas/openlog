package query

import (
	"context"
	"errors"
	"fmt"
	"testing"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

func TestAsStorageError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"s3 error", &ch.Exception{Code: 499, Message: "Message: Access Denied, bucket openlog-cold"}, true},
		{"wrapped s3 error", fmt.Errorf("query: %w", &ch.Exception{Code: 499}), true},
		{"remote io server", &ch.Exception{Code: 86, Message: "Received error from remote io server: HTTP status code: 503"}, true},
		{"socket timeout on s3", &ch.Exception{Code: 209, Message: "Timeout: while reading from ReadBufferFromS3"}, true},
		{"poco exception from aws sdk", &ch.Exception{Code: 1000, Message: "Poco::Exception. Code: 1000, e.code() = 0, Timeout (AWS S3 client)"}, true},
		{"network error to a shard", &ch.Exception{Code: 210, Message: "Connection refused (chi-openlog-0-1:9000)"}, false},
		{"memory limit", &ch.Exception{Code: 241, Message: "Memory limit (for query) exceeded"}, false},
		{"not clickhouse", errors.New("S3 is down"), false},
		{"context", context.DeadlineExceeded, false},
	} {
		se, ok := AsStorageError(tc.err)
		if ok != tc.want {
			t.Errorf("%s: AsStorageError = %v, want %v", tc.name, ok, tc.want)
			continue
		}
		if ok && !errors.Is(se, tc.err) && !errors.Is(tc.err, se.Err) {
			t.Errorf("%s: does not wrap the original error", tc.name)
		}
	}
	// classify prefers limits and passes the storage error through Unwrap.
	se := classify(&ch.Exception{Code: 499, Message: "S3 exception"})
	var got *StorageError
	if !errors.As(se, &got) || got.Code != 499 {
		t.Fatalf("classify = %v", se)
	}
	var ex *ch.Exception
	if !errors.As(se, &ex) {
		t.Error("classified storage error does not unwrap to the exception")
	}
	if _, ok := AsStorageError(classify(&ch.Exception{Code: 241})); ok {
		t.Error("limit error classified as storage error")
	}
}
