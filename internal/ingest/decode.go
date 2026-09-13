package ingest

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var jsonUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}

// idFields are OTLP/JSON fields whose bytes are hex-encoded (not base64) per
// the OTLP specification. Both lowerCamelCase and snake_case spellings are accepted.
var idFields = map[string]bool{
	"traceId": true, "spanId": true, "parentSpanId": true,
	"trace_id": true, "span_id": true, "parent_span_id": true,
}

// unmarshalOTLPJSON decodes OTLP/JSON into msg. OTLP/JSON differs from the
// canonical protobuf JSON mapping in that trace and span ids are hex strings;
// they are rewritten to base64 before handing the document to protojson.
func unmarshalOTLPJSON(body []byte, msg proto.Message) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // keep int64 precision
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	if err := rewriteIDs(doc); err != nil {
		return err
	}
	normalized, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return jsonUnmarshal.Unmarshal(normalized, msg)
}

func rewriteIDs(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if s, ok := child.(string); ok && idFields[k] {
				if s == "" {
					continue
				}
				b, err := hex.DecodeString(s)
				if err != nil {
					return fmt.Errorf("field %s: invalid hex id %q", k, s)
				}
				x[k] = base64.StdEncoding.EncodeToString(b)
				continue
			}
			if err := rewriteIDs(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range x {
			if err := rewriteIDs(child); err != nil {
				return err
			}
		}
	}
	return nil
}
