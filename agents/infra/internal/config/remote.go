package config

import (
	"fmt"
	"slices"
)

// RevisionDisabled is reported as integrations_config_revision when
// integrations.remote_config is false (releases-updates.md §3).
const RevisionDisabled = "disabled"

// RemoteIntegrations is the integrations_config object of an agent sync response:
// integration settings configured in the openlog UI for this host.
type RemoteIntegrations struct {
	Revision string       `json:"revision"`
	Items    []RemoteItem `json:"items"`
}

// RemoteItem is one setting row. Items are applied in order; later items win
// per field (all-hosts rows come before host rows).
type RemoteItem struct {
	Integration string       `json:"integration"`
	Match       *RemoteMatch `json:"match,omitempty"`
	Enabled     bool         `json:"enabled"`
	Endpoint    string       `json:"endpoint,omitempty"`
	Username    string       `json:"username,omitempty"`
	// Password is plaintext; it becomes a LiteralSecret and is never logged.
	Password  string   `json:"password,omitempty"`
	Database  string   `json:"database,omitempty"`
	Databases []string `json:"databases,omitempty"`
}

// RemoteMatch selects discovered services like InstanceMatch.
type RemoteMatch struct {
	Port      int    `json:"port,omitempty"`
	Container string `json:"container,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Instance  string `json:"instance,omitempty"`
}

func (m *RemoteMatch) instanceMatch() InstanceMatch {
	if m == nil {
		return InstanceMatch{}
	}
	return InstanceMatch{Port: m.Port, Container: m.Container, Endpoint: m.Endpoint, Instance: m.Instance}
}

// Clone returns a copy whose instance lists can be modified independently.
func (c IntegrationsConfig) Clone() IntegrationsConfig {
	for _, id := range IntegrationIDs {
		ic := c.Integration(id)
		ic.Instances = slices.Clone(ic.Instances)
	}
	return c
}

// ApplyRemote overlays remote items on a copy of c: remote values win per field
// over config.yaml. Items without a match change the integration defaults;
// items with a match update the config.yaml instance with the same match or are
// added before the configured instances. Invalid items are skipped and
// reported; the errors never contain a password.
func (c IntegrationsConfig) ApplyRemote(items []RemoteItem) (IntegrationsConfig, []error) {
	out := c.Clone()
	var errs []error
	added := map[string][]InstanceConfig{}
	for i, it := range items {
		ic := out.Integration(it.Integration)
		prefix := fmt.Sprintf("remote integration config item %d (%s): ", i, it.Integration)
		if ic == nil {
			errs = append(errs, fmt.Errorf("%sunknown integration", prefix))
			continue
		}
		s := InstanceSettings{Endpoint: it.Endpoint, Username: it.Username, Password: LiteralSecret(it.Password), Database: it.Database}
		if len(it.Databases) > 0 {
			s.Databases = slices.Clone(it.Databases)
		}
		m := it.Match.instanceMatch()
		verrs := s.validate(it.Integration, prefix)
		if m.Port < 0 || m.Port > 65535 {
			verrs = append(verrs, fmt.Errorf("%smatch.port must be 1..65535", prefix))
		}
		if len(verrs) > 0 {
			errs = append(errs, verrs...)
			continue
		}
		if m.IsZero() {
			ic.InstanceSettings = ic.InstanceSettings.Merge(s)
			ic.Enabled = it.Enabled
			continue
		}
		enabled := it.Enabled
		found := false
		for j := range ic.Instances {
			if ic.Instances[j].Match == m {
				ic.Instances[j].InstanceSettings = ic.Instances[j].InstanceSettings.Merge(s)
				ic.Instances[j].Enabled = &enabled
				found = true
				break
			}
		}
		if !found {
			list := added[it.Integration]
			if k := slices.IndexFunc(list, func(in InstanceConfig) bool { return in.Match == m }); k >= 0 {
				list[k].InstanceSettings = list[k].InstanceSettings.Merge(s)
				list[k].Enabled = &enabled
			} else {
				added[it.Integration] = append(list, InstanceConfig{Match: m, Enabled: &enabled, InstanceSettings: s})
			}
		}
	}
	for id, list := range added {
		ic := out.Integration(id)
		ic.Instances = append(list, ic.Instances...)
	}
	return out, errs
}
