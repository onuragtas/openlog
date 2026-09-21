package config

import (
	"strconv"
	"strings"
)

// RUM configures real user monitoring (docs/contracts/rum.md, D-136).
//
// What bounds a browser application — which origins a key serves, how many events a minute it may send,
// what share of sessions it keeps — deliberately lives on the browser key rather than here, because one
// openlog serves many applications with different traffic. What is left for the installation is the switch
// below and where uploaded source maps are kept, which is an operator's decision about storage, not an
// application's about telemetry.
type RUM struct {
	// Enabled serves POST /v1/rum on ingest and the /api/v1/rum/* reads (OPENLOG_RUM_ENABLED). Turning it
	// off leaves existing browser keys in place but stops accepting their data, which is the switch an
	// operator wants when a page is flooding them and revoking one key at a time is not fast enough.
	Enabled bool
	// GeoHeader names the request header a trusted proxy or CDN writes the visitor's ISO 3166-1 alpha-2
	// country into (OPENLOG_RUM_GEO_HEADER; e.g. CF-IPCountry behind Cloudflare). Empty — the default —
	// means no country is recorded at all.
	//
	// It is a header name rather than a switch because openlog does not resolve addresses itself: there is
	// no GeoIP database to ship, license and refresh, and **no visitor address is ever stored**. The cost
	// of that choice is stated rather than hidden: without a proxy that writes the header, the field stays
	// empty. Anything openlog is fronted by can be trusted to write it exactly as far as it can be trusted
	// to forward the request at all.
	GeoHeader string
	// SourceMaps configures where the maps that un-minify browser stacks are stored (rum.md §8).
	SourceMaps SourceMaps
}

// SourceMaps configures the object storage of uploaded source maps. The fields mirror DataExport: a map is
// the same kind of object — user-supplied, occasionally large, read rarely — and an operator who has already
// pointed exports at a bucket should not have to learn a second scheme for this one.
type SourceMaps struct {
	// Enabled offers the /api/v1/source-maps endpoints and symbolication (OPENLOG_SOURCE_MAPS_ENABLED;
	// default OPENLOG_RUM_ENABLED, postgres auth mode only).
	Enabled bool
	// Storage is auto (s3 when an S3 URL is known, else local), local or s3 (OPENLOG_SOURCE_MAPS_STORAGE).
	Storage string
	// LocalPath is the directory of locally stored maps (OPENLOG_SOURCE_MAPS_LOCAL_PATH).
	LocalPath string
	// S3URL is the object base URL, bucket and prefix included (OPENLOG_SOURCE_MAPS_S3_URL). Empty with
	// tiered storage enabled: derived from OPENLOG_S3_ENDPOINT (same bucket, prefix openlog-sourcemaps/).
	S3URL             string
	S3Region          string // OPENLOG_SOURCE_MAPS_S3_REGION, else OPENLOG_S3_REGION, else us-east-1
	S3AccessKeyID     string // OPENLOG_SOURCE_MAPS_S3_ACCESS_KEY_ID, else OPENLOG_S3_ACCESS_KEY_ID
	S3SecretAccessKey string // OPENLOG_SOURCE_MAPS_S3_SECRET_ACCESS_KEY, else OPENLOG_S3_SECRET_ACCESS_KEY
	// S3Credentials is static (the keys above) or auto (AWS credential chain; D-116)
	// (OPENLOG_SOURCE_MAPS_S3_CREDENTIALS; default static when keys are set, else auto).
	S3Credentials string
}

// MapStorage returns the effective storage: local or s3.
func (s SourceMaps) MapStorage() string {
	if s.Storage == "auto" {
		if s.S3URL != "" {
			return "s3"
		}
		return "local"
	}
	return s.Storage
}

func loadRUM(p *parser) RUM {
	enabled := p.bool("OPENLOG_RUM_ENABLED", true)
	geoHeader := p.str("OPENLOG_RUM_GEO_HEADER", "")
	m := SourceMaps{
		Enabled:           p.bool("OPENLOG_SOURCE_MAPS_ENABLED", enabled),
		Storage:           p.str("OPENLOG_SOURCE_MAPS_STORAGE", "auto"),
		LocalPath:         p.str("OPENLOG_SOURCE_MAPS_LOCAL_PATH", "/tmp/openlog-sourcemaps"),
		S3URL:             p.str("OPENLOG_SOURCE_MAPS_S3_URL", ""),
		S3Region:          p.str("OPENLOG_SOURCE_MAPS_S3_REGION", ""),
		S3AccessKeyID:     p.str("OPENLOG_SOURCE_MAPS_S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: p.str("OPENLOG_SOURCE_MAPS_S3_SECRET_ACCESS_KEY", ""),
	}
	// Tiered storage settings (the ClickHouse S3 disk) are reused when maps have none of their own, exactly
	// as the data export does — same bucket, its own prefix beside the disk prefixes.
	tiering, _ := strconv.ParseBool(strings.TrimSpace(p.getenv("OPENLOG_STORAGE_TIERING_ENABLED")))
	if m.S3URL == "" && tiering {
		if u := ExportURLFromTieredEndpoint(p.str("OPENLOG_S3_ENDPOINT", "")); u != "" {
			m.S3URL = strings.TrimSuffix(u, "openlog-exports/") + "openlog-sourcemaps/"
		}
	}
	if m.S3Region == "" {
		m.S3Region = p.str("OPENLOG_S3_REGION", "us-east-1")
	}
	m.S3Credentials = strings.ToLower(strings.TrimSpace(p.str("OPENLOG_SOURCE_MAPS_S3_CREDENTIALS", "")))
	// With explicit auto the tiered storage keys (ClickHouse's) are not borrowed: maps use the IAM role.
	if m.S3AccessKeyID == "" && m.S3SecretAccessKey == "" && m.S3Credentials != "auto" {
		m.S3AccessKeyID, m.S3SecretAccessKey = p.str("OPENLOG_S3_ACCESS_KEY_ID", ""), p.str("OPENLOG_S3_SECRET_ACCESS_KEY", "")
	}
	if m.S3Credentials == "" {
		m.S3Credentials = "auto"
		if m.S3AccessKeyID != "" || m.S3SecretAccessKey != "" {
			m.S3Credentials = "static"
		}
	}
	return RUM{Enabled: enabled, GeoHeader: geoHeader, SourceMaps: m}
}
