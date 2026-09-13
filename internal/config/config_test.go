package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminAddr != ":9464" || c.Ingest.HTTPAddr != ":4318" || c.Ingest.GRPCAddr != ":4317" || c.API.HTTPAddr != ":8080" {
		t.Errorf("unexpected default addrs: %+v", c)
	}
	if !reflect.DeepEqual(c.KafkaBrokers, []string{"localhost:9092"}) {
		t.Errorf("brokers = %v", c.KafkaBrokers)
	}
	if c.Ingest.MaxBodyBytes != 10485760 || c.Ingest.ProduceTimeout != 10*time.Second {
		t.Errorf("ingest defaults = %+v", c.Ingest)
	}
	if c.Processor.Group != "openlog-processor" || c.Processor.BatchRows != 50000 || c.Processor.FlushInterval != 2*time.Second || c.Processor.InsertTimeout != 30*time.Second {
		t.Errorf("processor defaults = %+v", c.Processor)
	}
	if c.API.MaxRows != 10000 || c.API.QueryTimeout != 30*time.Second {
		t.Errorf("api defaults = %+v", c.API)
	}
	if c.Migrate.KafkaPartitions != 6 || c.Migrate.KafkaReplicationFactor != 1 || c.Migrate.SkipKafka {
		t.Errorf("migrate defaults = %+v", c.Migrate)
	}
	if c.Migrate.KafkaMinInsyncReplicas != 1 || c.Migrate.KafkaRetentionMs != 86400000 || c.Migrate.KafkaMaxMessageBytes != 12582912 {
		t.Errorf("topic config defaults = %+v", c.Migrate)
	}
	if !c.MigrateOnStart || c.ClickHouseCluster != "openlog" || c.ClickHouseDatabase != "openlog" || c.ClickHouseUser != "default" {
		t.Errorf("common defaults = %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"OPENLOG_KAFKA_BROKERS":            " k1:9092, k2:9092 ,",
		"OPENLOG_PROCESSOR_FLUSH_INTERVAL": "500ms",
		"OPENLOG_MIGRATE_SKIP_KAFKA":       "true",
		"OPENLOG_MIGRATE_ON_START":         "false",
		"OPENLOG_API_MAX_ROWS":             "42",
		"OPENLOG_LOG_LEVEL":                "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.KafkaBrokers, []string{"k1:9092", "k2:9092"}) {
		t.Errorf("brokers = %v", c.KafkaBrokers)
	}
	if c.Processor.FlushInterval != 500*time.Millisecond || !c.Migrate.SkipKafka || c.MigrateOnStart || c.API.MaxRows != 42 || c.LogLevel != "debug" {
		t.Errorf("overrides not applied: %+v", c)
	}
}

func TestTopicConfigOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"OPENLOG_KAFKA_REPLICATION_FACTOR":  "3",
		"OPENLOG_KAFKA_MIN_INSYNC_REPLICAS": "2",
		"OPENLOG_KAFKA_RETENTION_MS":        "-1",
		"OPENLOG_KAFKA_MAX_MESSAGE_BYTES":   "1048576",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Migrate.KafkaReplicationFactor != 3 || c.Migrate.KafkaMinInsyncReplicas != 2 || c.Migrate.KafkaRetentionMs != -1 || c.Migrate.KafkaMaxMessageBytes != 1048576 {
		t.Errorf("topic config overrides not applied: %+v", c.Migrate)
	}
}

func TestTopicConfigInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"min isr not a number": {"OPENLOG_KAFKA_MIN_INSYNC_REPLICAS": "two"},
		"min isr zero":         {"OPENLOG_KAFKA_MIN_INSYNC_REPLICAS": "0"},
		"min isr above rf":     {"OPENLOG_KAFKA_MIN_INSYNC_REPLICAS": "2"}, // default RF is 1
		"retention zero":       {"OPENLOG_KAFKA_RETENTION_MS": "0"},
		"retention below -1":   {"OPENLOG_KAFKA_RETENTION_MS": "-5"},
		"retention not number": {"OPENLOG_KAFKA_RETENTION_MS": "1d"},
		"max bytes zero":       {"OPENLOG_KAFKA_MAX_MESSAGE_BYTES": "0"},
		"max bytes overflow":   {"OPENLOG_KAFKA_MAX_MESSAGE_BYTES": "3000000000"},
	}
	for name, vars := range cases {
		_, err := Load(env(vars))
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		for k := range vars {
			if !strings.Contains(err.Error(), k) {
				t.Errorf("%s: error %q does not mention %s", name, err, k)
			}
		}
	}
}

func TestUpdateCheckInterval(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.UpdateCheck.Interval != 24*time.Hour {
		t.Errorf("default interval = %s", c.UpdateCheck.Interval)
	}
	if c, err = Load(env(map[string]string{"OPENLOG_UPDATE_CHECK_INTERVAL": "2m"})); err != nil || c.UpdateCheck.Interval != 2*time.Minute {
		t.Errorf("interval 2m: %v %s", err, c.UpdateCheck.Interval)
	}
	for _, bad := range []string{"30s", "soon"} {
		if _, err := Load(env(map[string]string{"OPENLOG_UPDATE_CHECK_INTERVAL": bad})); err == nil || !strings.Contains(err.Error(), "OPENLOG_UPDATE_CHECK_INTERVAL") {
			t.Errorf("interval %q: err = %v", bad, err)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	_, err := Load(env(map[string]string{
		"OPENLOG_INGEST_PRODUCE_TIMEOUT": "ten",
		"OPENLOG_API_MAX_ROWS":           "x",
		"OPENLOG_MIGRATE_SKIP_KAFKA":     "maybe",
		"OPENLOG_LOG_LEVEL":              "loud",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"OPENLOG_INGEST_PRODUCE_TIMEOUT", "OPENLOG_API_MAX_ROWS", "OPENLOG_MIGRATE_SKIP_KAFKA"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
