package config

import (
	"strings"
	"testing"
	"time"
)

func TestPrivacyDefaults(t *testing.T) {
	c, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.Privacy.OrgDeletionGrace != 7*24*time.Hour || !c.Privacy.Export.Enabled || c.Privacy.Export.ExportStorage() != "local" ||
		c.Privacy.Export.S3Region != "us-east-1" || c.StatusPage.Enabled {
		t.Fatalf("defaults: %+v %+v", c.Privacy, c.StatusPage)
	}
	saas, err := Load(func(n string) string {
		return map[string]string{"OPENLOG_SAAS_MODE": "true"}[n]
	})
	if err != nil {
		t.Fatal(err)
	}
	if !saas.StatusPage.Enabled {
		t.Fatal("status page must default to OPENLOG_SAAS_MODE")
	}
}

func TestPrivacyS3Credentials(t *testing.T) {
	load := func(env map[string]string) (DataExport, error) {
		c, err := Load(func(n string) string { return env[n] })
		return c.Privacy.Export, err
	}
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{nil, "auto"},
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID": "ak", "OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY": "sk"}, "static"},
		{map[string]string{"OPENLOG_S3_ACCESS_KEY_ID": "ak", "OPENLOG_S3_SECRET_ACCESS_KEY": "sk"}, "static"},
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_CREDENTIALS": "AUTO", "OPENLOG_S3_ACCESS_KEY_ID": "ak", "OPENLOG_S3_SECRET_ACCESS_KEY": "sk"}, "auto"},
	} {
		e, err := load(tc.env)
		if err != nil || e.S3Credentials != tc.want {
			t.Errorf("%v: %q %v, want %q", tc.env, e.S3Credentials, err, tc.want)
		}
		if tc.want == "auto" && e.S3AccessKeyID != "" {
			t.Errorf("%v: auto must not borrow static keys", tc.env)
		}
	}
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_CREDENTIALS": "static"}, "CREDENTIALS=static requires"},
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_CREDENTIALS": "auto", "OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID": "ak", "OPENLOG_DATA_EXPORT_S3_SECRET_ACCESS_KEY": "sk"}, "CREDENTIALS=auto uses"},
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_CREDENTIALS": "iam"}, "must be static or auto"},
	} {
		if _, err := load(tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error %v, want %q", tc.env, err, tc.want)
		}
	}
}

func TestPrivacyTieredS3Fallback(t *testing.T) {
	env := map[string]string{
		"OPENLOG_STORAGE_TIERING_ENABLED": "true",
		"OPENLOG_S3_ENDPOINT":             "http://minio:9000/openlog-cold/ch-1/",
		"OPENLOG_S3_ACCESS_KEY_ID":        "ak",
		"OPENLOG_S3_SECRET_ACCESS_KEY":    "sk",
	}
	c, err := Load(func(n string) string { return env[n] })
	if err != nil {
		t.Fatal(err)
	}
	e := c.Privacy.Export
	if e.S3URL != "http://minio:9000/openlog-cold/openlog-exports/" || e.S3AccessKeyID != "ak" || e.S3SecretAccessKey != "sk" || e.ExportStorage() != "s3" {
		t.Fatalf("fallback: %+v", e)
	}
	for in, want := range map[string]string{
		"https://my-bucket.s3.eu-west-1.amazonaws.com/ch/{replica}/": "https://my-bucket.s3.eu-west-1.amazonaws.com/openlog-exports/",
		"https://s3.eu-west-1.amazonaws.com/my-bucket/cold/":         "https://s3.eu-west-1.amazonaws.com/my-bucket/openlog-exports/",
		"http://minio:9000/": "",
		"":                   "",
	} {
		if got := ExportURLFromTieredEndpoint(in); got != want {
			t.Errorf("ExportURLFromTieredEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrivacyValidation(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"OPENLOG_ORG_DELETION_GRACE": "3000h"}, "OPENLOG_ORG_DELETION_GRACE"},
		{map[string]string{"OPENLOG_DATA_EXPORT_STORAGE": "s3"}, "requires OPENLOG_DATA_EXPORT_S3_URL"},
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_URL": "https://s3.example.com/bucket"}, "ending with /"},
		{map[string]string{"OPENLOG_DATA_EXPORT_S3_ACCESS_KEY_ID": "x"}, "must be set together"},
		{map[string]string{"OPENLOG_DATA_EXPORT_MAX_BYTES": "6000000000"}, "OPENLOG_DATA_EXPORT_MAX_BYTES"},
		{map[string]string{"OPENLOG_DATA_EXPORT_LOCAL_PATH": "exports"}, "absolute path"},
		{map[string]string{"OPENLOG_DATA_EXPORT_TTL": "10m"}, "OPENLOG_DATA_EXPORT_TTL"},
	} {
		_, err := Load(func(n string) string { return tc.env[n] })
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error %v, want %q", tc.env, err, tc.want)
		}
	}
}
