package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/internal/api/query"
	"github.com/onuragtas/openlog/internal/oql"
)

// OQL endpoints (docs/contracts/api.md "Query language (OQL)", oql.md). They read telemetry through wrap, which
// binds the query scope to the principal's tenant and requires telemetry.read (every role, API keys too).

const (
	maxOQLVariables     = 20
	maxOQLVariableVals  = 500
	maxOQLVariableBytes = 4096
)

func (s *Server) oqlRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/query", s.wrap("POST /api/v1/query", s.runOQL))
	mux.Handle("POST /api/v1/query/validate", s.wrap("POST /api/v1/query/validate", s.validateOQL))
	mux.Handle("GET /api/v1/query/schema", s.wrap("GET /api/v1/query/schema", s.oqlSchema))
}

// oqlVariables accepts {"name": "v" | ["v1", "v2"]}.
type oqlVariables map[string][]string

func (v *oqlVariables) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return errors.New("variables must be an object")
	}
	if len(raw) > maxOQLVariables {
		return fmt.Errorf("at most %d variables", maxOQLVariables)
	}
	out := oqlVariables{}
	for name, r := range raw {
		var one string
		if err := json.Unmarshal(r, &one); err == nil {
			out[name] = []string{one}
			continue
		}
		var many []string
		if err := json.Unmarshal(r, &many); err != nil {
			return fmt.Errorf("variables.%s must be a string or an array of strings", truncate(name, 64))
		}
		if len(many) > maxOQLVariableVals {
			return fmt.Errorf("variables.%s: at most %d values", truncate(name, 64), maxOQLVariableVals)
		}
		out[name] = many
	}
	for name, vals := range out {
		for _, s := range vals {
			if len(s) > maxOQLVariableBytes {
				return fmt.Errorf("variables.%s: values must be at most %d bytes", truncate(name, 64), maxOQLVariableBytes)
			}
		}
	}
	*v = out
	return nil
}

// flexTime accepts RFC3339 strings and unix milliseconds (number or string).
type flexTime string

func (t *flexTime) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = flexTime(s)
		return nil
	}
	if string(b) == "null" {
		return nil
	}
	if _, err := strconv.ParseInt(string(b), 10, 64); err != nil {
		return errors.New("time must be RFC3339 or unix milliseconds")
	}
	*t = flexTime(b)
	return nil
}

type oqlRequest struct {
	Query     string       `json:"query"`
	From      flexTime     `json:"from"`
	To        flexTime     `json:"to"`
	Variables oqlVariables `json:"variables"`
}

func (s *Server) decodeOQL(r *http.Request) (oqlRequest, oql.Options, error) {
	var in oqlRequest
	if err := decodeJSON(r, &in); err != nil {
		return in, oql.Options{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return in, oql.Options{}, badRequest("query is required")
	}
	if len(in.Query) > oql.MaxQueryBytes {
		return in, oql.Options{}, badRequest("query must be at most %d bytes", oql.MaxQueryBytes)
	}
	opt := oql.Options{Now: s.now(), Variables: in.Variables, MaxLimit: min(oql.MaxTableLimit, max(s.cfg.MaxRows, 1))}
	if (in.From == "") != (in.To == "") {
		return in, opt, badRequest("from and to must be given together")
	}
	if in.From != "" {
		var err error
		if opt.From, err = parseTime(string(in.From)); err != nil {
			return in, opt, badRequest("from: %v", err)
		}
		if opt.To, err = parseTime(string(in.To)); err != nil {
			return in, opt, badRequest("to: %v", err)
		}
		if !opt.From.Before(opt.To) {
			return in, opt, badRequest("from must be before to")
		}
	}
	return in, opt, nil
}

func oqlError(src string, err error) error {
	return &apiError{http.StatusBadRequest, "invalid_argument", oql.Describe(src, err)}
}

type oqlMetadataJSON struct {
	oql.Metadata
	From string `json:"from"`
	To   string `json:"to"`
}

type oqlResultJSON struct {
	*oql.Result
	Metadata oqlMetadataJSON `json:"metadata"`
}

func (s *Server) runOQL(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	in, opt, err := s.decodeOQL(r)
	if err != nil {
		return err
	}
	p, err := oql.Compile(in.Query, opt)
	if err != nil {
		return oqlError(in.Query, err)
	}
	res, err := oql.Execute(r.Context(), sc, p)
	if err != nil {
		var oe *oql.Error
		if errors.As(err, &oe) {
			return oqlError(in.Query, oe)
		}
		return err
	}
	writeJSON(w, http.StatusOK, oqlResultJSON{Result: res, Metadata: oqlMetadataJSON{Metadata: res.Metadata,
		From: formatTime(res.Metadata.From), To: formatTime(res.Metadata.To)}})
	return nil
}

func (s *Server) validateOQL(w http.ResponseWriter, r *http.Request, _ *query.Scope) error {
	noStore(w)
	var in oqlRequest
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if len(in.Query) > oql.MaxQueryBytes*2 {
		return badRequest("query must be at most %d bytes", oql.MaxQueryBytes)
	}
	writeJSON(w, http.StatusOK, oql.Validate(in.Query, oql.Options{Now: s.now(), Variables: in.Variables,
		MaxLimit: min(oql.MaxTableLimit, max(s.cfg.MaxRows, 1))}))
	return nil
}

func (s *Server) oqlSchema(w http.ResponseWriter, r *http.Request, sc *query.Scope) error {
	noStore(w)
	out := map[string]any{
		"event_types": oql.EventTypes(), "functions": oql.Functions, "keywords": oql.Keywords,
		"attribute_keys": []string{}, "resource_keys": []string{}, "metric_names": []string{},
	}
	if et := r.URL.Query().Get("event_type"); et != "" {
		valid := false
		for _, n := range oql.EventTypeNames() {
			valid = valid || n == et
		}
		if !valid {
			return badRequest("event_type must be one of %s", strings.Join(oql.EventTypeNames(), ", "))
		}
		smp, err := oql.SampleKeys(r.Context(), sc, et, s.now().UTC().Truncate(time.Minute))
		if err != nil {
			return err
		}
		out["attribute_keys"], out["resource_keys"], out["metric_names"] = smp.AttributeKeys, smp.ResourceKeys, smp.MetricNames
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
