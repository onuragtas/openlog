package metrics

import (
	"testing"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs/hostfstest"

	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// A coretemp chip and a motherboard chip, as the kernel lays them out: millidegrees, rpm, millivolts, and
// the thresholds next to each reading.
func hwmonFiles() map[string]string {
	return map[string]string{
		"/sys/class/hwmon/hwmon0/name":         "coretemp\n",
		"/sys/class/hwmon/hwmon0/temp1_input":  "45000\n",
		"/sys/class/hwmon/hwmon0/temp1_label":  "Package id 0\n",
		"/sys/class/hwmon/hwmon0/temp1_max":    "80000\n",
		"/sys/class/hwmon/hwmon0/temp1_crit":   "100000\n",
		"/sys/class/hwmon/hwmon0/temp2_input":  "43000\n",
		"/sys/class/hwmon/hwmon0/temp2_label":  "Core 0\n",
		"/sys/class/hwmon/hwmon1/name":         "nct6798\n",
		"/sys/class/hwmon/hwmon1/fan1_input":   "1200\n",
		"/sys/class/hwmon/hwmon1/fan2_input":   "0\n",
		"/sys/class/hwmon/hwmon1/in0_input":    "1120\n",
		"/sys/class/hwmon/hwmon1/power1_input": "45000000\n",
		// A disconnected sensor: the kernel reports a temperature no machine has.
		"/sys/class/hwmon/hwmon1/temp5_input": "-274000\n",
		// Not a reading, and not a number: neither may become a metric.
		"/sys/class/hwmon/hwmon1/temp1_type": "4\n",
		"/sys/class/hwmon/hwmon1/fan3_input": "not-a-number\n",
	}
}

func pointsOf(t *testing.T, metrics []*metricspb.Metric, name string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, m := range metrics {
		if m.Name != name {
			continue
		}
		for _, dp := range m.GetGauge().DataPoints {
			key := ""
			for _, a := range dp.Attributes {
				key += a.Key + "=" + a.Value.GetStringValue() + " "
			}
			out[key] = dp.GetAsDouble()
		}
	}
	return out
}

func TestSensors(t *testing.T) {
	fs := hostfstest.Build(t, hwmonFiles())
	s := &Sensors{FS: fs}
	metrics, err := s.Collect(time.Now())
	if err != nil {
		t.Fatal(err)
	}

	temps := pointsOf(t, metrics, "system.hardware.temperature")
	// Millidegrees become degrees: the number a person reads off a sensor.
	if got := temps["hw.chip=coretemp hw.sensor=temp1 hw.label=Package id 0 "]; got != 45 {
		t.Errorf("package temperature = %v, want 45", got)
	}
	if got := temps["hw.chip=coretemp hw.sensor=temp2 hw.label=Core 0 "]; got != 43 {
		t.Errorf("core temperature = %v, want 43", got)
	}
	// −274 °C is not a temperature; the sensor is disconnected.
	if len(temps) != 2 {
		t.Errorf("temperatures = %v, want only the two real ones", temps)
	}

	// The thresholds are their own series, because "how hot is it" and "how hot may it get" are two facts.
	limits := pointsOf(t, metrics, "system.hardware.temperature.limit")
	if got := limits["hw.chip=coretemp hw.sensor=temp1 hw.label=Package id 0 hw.limit=high "]; got != 80 {
		t.Errorf("high limit = %v, want 80", got)
	}
	if got := limits["hw.chip=coretemp hw.sensor=temp1 hw.label=Package id 0 hw.limit=critical "]; got != 100 {
		t.Errorf("critical limit = %v, want 100", got)
	}

	fans := pointsOf(t, metrics, "system.hardware.fan.speed")
	if got := fans["hw.chip=nct6798 hw.sensor=fan1 "]; got != 1200 {
		t.Errorf("fan1 = %v, want 1200", got)
	}
	// A fan at 0 rpm is a real and important reading — it is not dropped as "no data".
	if got, ok := fans["hw.chip=nct6798 hw.sensor=fan2 "]; !ok || got != 0 {
		t.Errorf("fan2 = %v, %v; a stopped fan must be reported", got, ok)
	}
	// A file that is not a number contributes nothing rather than a zero.
	if _, ok := fans["hw.chip=nct6798 hw.sensor=fan3 "]; ok {
		t.Error("an unreadable value must not become a metric")
	}

	volts := pointsOf(t, metrics, "system.hardware.voltage")
	if got := volts["hw.chip=nct6798 hw.sensor=in0 "]; got != 1.12 {
		t.Errorf("voltage = %v, want 1.12", got)
	}
	watts := pointsOf(t, metrics, "system.hardware.power")
	if got := watts["hw.chip=nct6798 hw.sensor=power1 "]; got != 45 {
		t.Errorf("power = %v, want 45", got)
	}
}

// A machine without hwmon is not a failure to report every interval; there is simply nothing to read.
func TestSensorsWithoutHwmon(t *testing.T) {
	fs := hostfstest.Build(t, map[string]string{"/proc/uptime": "1 1\n"})
	metrics, err := (&Sensors{FS: fs}).Collect(time.Now())
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if len(metrics) != 0 {
		t.Fatalf("metrics = %d, want none", len(metrics))
	}
}

// The cap is what keeps a chassis with hundreds of sensors from becoming a cardinality problem.
func TestSensorsLimit(t *testing.T) {
	files := map[string]string{"/sys/class/hwmon/hwmon0/name": "big\n"}
	for i := 1; i <= 50; i++ {
		files["/sys/class/hwmon/hwmon0/temp"+itoa(i)+"_input"] = "40000\n"
	}
	fs := hostfstest.Build(t, files)
	metrics, err := (&Sensors{FS: fs, MaxSensors: 10}).Collect(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range metrics {
		if m.Name == "system.hardware.temperature" {
			count += len(m.GetGauge().DataPoints)
		}
	}
	if count != 10 {
		t.Fatalf("readings = %d, want the cap of 10", count)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestMatchKind(t *testing.T) {
	cases := map[string]string{"temp1_input": "temp", "fan10_input": "fan", "in0_input": "in",
		"curr1_input": "curr", "power1_input": "power"}
	for file, want := range cases {
		k, _, ok := matchKind(file)
		if !ok || k.prefix != want {
			t.Errorf("matchKind(%q) = %q, %v; want %q", file, k.prefix, ok, want)
		}
	}
	for _, file := range []string{"temp1_label", "name", "temp_input", "device", "fan1_min"} {
		if _, _, ok := matchKind(file); ok {
			t.Errorf("matchKind(%q) must not match a reading", file)
		}
	}
}
