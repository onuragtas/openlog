package metrics

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/hostfs"
	"github.com/onuragtas/openlog/agents/infra/internal/otlputil"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Hardware sensors (D-145): the temperatures, fans, voltages and power draw the kernel exposes under
// /sys/class/hwmon, which is where `sensors` reads them from too.
//
// This is the layer below everything else openlog collects. A host whose CPU is thermally throttled shows
// up in every metric as "slower", and nothing in the stack says why; a fan that stopped shows up as
// nothing at all until the machine does. The kernel already knows both, and reading them costs a few file
// reads per collection.
//
// Linux only: hwmon is a Linux interface. On macOS and Windows the collector is simply not built into the
// set (nativeCollectors), rather than reporting an error every interval.

// Sensors emits system.hardware.* from /sys/class/hwmon.
type Sensors struct {
	FS *hostfs.FS
	// MaxSensors bounds the readings of one collection; a server with many drives and power supplies can
	// expose hundreds, and each is a series.
	MaxSensors int
}

// DefaultMaxSensors is the cap when none is configured.
const DefaultMaxSensors = 200

func (s *Sensors) Name() string { return "sensors" }

// sensorKind is one family of hwmon readings: its file prefix, the metric it becomes, its unit and the
// divisor that turns the kernel's integer into the unit's own scale.
type sensorKind struct {
	prefix  string
	metric  string
	unit    string
	divisor float64
}

// kinds are the hwmon families worth collecting. The kernel reports temperatures in millidegrees, voltages
// in millivolts, currents in milliamperes and power in microwatts; each is scaled to its SI unit here, so
// the stored number is the one a person would read off a sensor.
var kinds = []sensorKind{
	{"temp", "system.hardware.temperature", "Cel", 1000},
	{"fan", "system.hardware.fan.speed", "rpm", 1},
	{"in", "system.hardware.voltage", "V", 1000},
	{"curr", "system.hardware.current", "A", 1000},
	{"power", "system.hardware.power", "W", 1_000_000},
}

// limitSuffixes are the thresholds the kernel publishes next to a reading. They are emitted as their own
// series rather than folded into the reading, because "how hot is it" and "how hot may it get" are two
// facts, and an alert needs both.
var limitSuffixes = map[string]string{"max": "high", "crit": "critical"}

// Collect implements Collector.
func (s *Sensors) Collect(now time.Time) ([]*metricspb.Metric, error) {
	entries, err := s.FS.ReadDir("/sys/class/hwmon")
	if err != nil {
		// A machine without hwmon (a container, a VM with no sensors passed through) is not a failure to
		// report every interval: there is simply nothing to read.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	limit := s.MaxSensors
	if limit <= 0 {
		limit = DefaultMaxSensors
	}
	byMetric := map[string][]otlputil.Point{}
	read := 0
	for _, dir := range names {
		if read >= limit {
			break
		}
		base := path.Join("/sys/class/hwmon", dir)
		chip := s.chipName(base, dir)
		read += s.readChip(base, chip, limit-read, byMetric)
	}
	if len(byMetric) == 0 {
		return nil, nil
	}
	out := make([]*metricspb.Metric, 0, len(byMetric))
	for _, k := range kinds {
		for _, metric := range []string{k.metric, k.metric + ".limit"} {
			pts := byMetric[metric]
			if len(pts) == 0 {
				continue
			}
			// A limit carries the unit of the reading it bounds: 90 °C is a temperature either way.
			out = append(out, otlputil.Gauge(metric, k.unit, now, pts...))
		}
	}
	return out, nil
}

// chipName is what `sensors` shows as the chip: its name file, falling back to the directory.
func (s *Sensors) chipName(base, dir string) string {
	if name, err := s.FS.ReadString(path.Join(base, "name")); err == nil {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return dir
}

// readChip reads one hwmon directory and returns how many readings it produced.
func (s *Sensors) readChip(base, chip string, budget int, byMetric map[string][]otlputil.Point) int {
	entries, err := s.FS.ReadDir(base)
	if err != nil {
		return 0
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}
	sort.Strings(files)

	read := 0
	for _, file := range files {
		if read >= budget {
			return read
		}
		k, index, ok := matchKind(file)
		if !ok {
			continue
		}
		value, err := s.readValue(path.Join(base, file))
		if err != nil {
			continue
		}
		// A disconnected sensor reads as 0 for a fan and as a nonsense temperature for others; a fan at 0
		// rpm is a real and important reading, so only the obviously invalid ones are dropped.
		if !plausible(k.prefix, value/k.divisor) {
			continue
		}
		label := s.label(base, k.prefix, index)
		attrs := []*commonpb.KeyValue{
			otlputil.Str("hw.chip", chip),
			otlputil.Str("hw.sensor", k.prefix+index),
		}
		if label != "" {
			attrs = append(attrs, otlputil.Str("hw.label", label))
		}
		byMetric[k.metric] = append(byMetric[k.metric], otlputil.DoublePoint(value/k.divisor, attrs...))
		read++

		for suffix, kind := range limitSuffixes {
			v, err := s.readValue(path.Join(base, k.prefix+index+"_"+suffix))
			if err != nil {
				continue
			}
			limitAttrs := append(append([]*commonpb.KeyValue{}, attrs...), otlputil.Str("hw.limit", kind))
			byMetric[k.metric+".limit"] = append(byMetric[k.metric+".limit"],
				otlputil.DoublePoint(v/k.divisor, limitAttrs...))
		}
	}
	return read
}

// matchKind recognizes a reading file ("temp1_input") and returns its family and index.
func matchKind(file string) (sensorKind, string, bool) {
	name, ok := strings.CutSuffix(file, "_input")
	if !ok {
		return sensorKind{}, "", false
	}
	for _, k := range kinds {
		index, ok := strings.CutPrefix(name, k.prefix)
		if !ok || index == "" {
			continue
		}
		if _, err := strconv.Atoi(index); err != nil {
			continue
		}
		return k, index, true
	}
	return sensorKind{}, "", false
}

// label is the sensor's own name ("Package id 0", "Core 0"), which is what makes a reading legible.
func (s *Sensors) label(base, prefix, index string) string {
	v, err := s.FS.ReadString(path.Join(base, prefix+index+"_label"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func (s *Sensors) readValue(p string) (float64, error) {
	raw, err := s.FS.ReadString(p)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", p, err)
	}
	return v, nil
}

// plausible rejects readings a sensor produces when nothing is connected to it. The bounds are wide on
// purpose: the point is to drop −274 °C, not to judge an unusual machine.
func plausible(prefix string, v float64) bool {
	switch prefix {
	case "temp":
		return v > -273 && v < 500
	case "fan":
		return v >= 0 && v < 100_000
	case "in":
		return v >= -100 && v < 1000
	case "curr":
		return v >= -1000 && v < 10_000
	case "power":
		return v >= 0 && v < 100_000
	}
	return true
}
