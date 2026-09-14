package query

import (
	"errors"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

// StorageError reports a query that failed because ClickHouse could not read table data from its storage, typically
// parts moved to S3 by tiered storage (D-066: object storage unreachable, throttled or timing out). Retrying later
// usually succeeds; the query itself is fine.
type StorageError struct {
	// Code is the ClickHouse error code (499 S3_ERROR, 86 RECEIVED_ERROR_FROM_REMOTE_IO_SERVER, …).
	Code int32
	Err  error
}

func (e *StorageError) Error() string { return "cold storage unavailable: " + e.Err.Error() }
func (e *StorageError) Unwrap() error { return e.Err }

// storageCodes are always storage errors.
var storageCodes = map[int32]bool{
	499: true, // S3_ERROR
	86:  true, // RECEIVED_ERROR_FROM_REMOTE_IO_SERVER (HTTP error of the object storage)
}

// storageHintCodes are storage errors only when the message names object storage: generic network, timeout and
// exception codes are also raised for other reasons (e.g. an unreachable shard).
var storageHintCodes = map[int32]bool{
	209:  true, // SOCKET_TIMEOUT
	210:  true, // NETWORK_ERROR
	1000: true, // POCO_EXCEPTION
	1001: true, // STD_EXCEPTION
	1002: true, // UNKNOWN_EXCEPTION
}

// storageHints mark messages of reads from object storage disks and their filesystem cache.
var storageHints = []string{"s3", "aws", "object storage", "objectstorage", "readbufferfromremotefs", "cachedondiskreadbuffer"}

// AsStorageError reports whether err is (or wraps) a ClickHouse error of reading data from (cold) storage.
func AsStorageError(err error) (*StorageError, bool) {
	var se *StorageError
	if errors.As(err, &se) {
		return se, true
	}
	var ex *ch.Exception
	if !errors.As(err, &ex) {
		return nil, false
	}
	if storageCodes[ex.Code] {
		return &StorageError{Code: ex.Code, Err: err}, true
	}
	if storageHintCodes[ex.Code] {
		msg := strings.ToLower(ex.Message)
		for _, h := range storageHints {
			if strings.Contains(msg, h) {
				return &StorageError{Code: ex.Code, Err: err}, true
			}
		}
	}
	return nil, false
}
