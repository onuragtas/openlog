// Package packaging embeds the installation files the agent reconciles itself: the systemd unit
// in systemd/ is the single copy used by the deb/rpm packages (nfpm), install.sh and
// "openlog-infra-agent -reconcile".
package packaging

import _ "embed"

// SystemdUnit is systemd/openlog-infra-agent.service.
//
//go:embed systemd/openlog-infra-agent.service
var SystemdUnit []byte
