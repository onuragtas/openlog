// Package packaging embeds the installation files the agent reconciles itself: the systemd unit
// in systemd/ is the single copy used by the deb/rpm packages (nfpm), install.sh and
// "openlog-infra-agent -reconcile". launchd/ holds the reference copy of the macOS LaunchDaemon that
// "-reconcile" renders (update.RenderLaunchdPlist); config.example.yaml seeds "-configure" on Linux.
package packaging

import _ "embed"

// SystemdUnit is systemd/openlog-infra-agent.service.
//
//go:embed systemd/openlog-infra-agent.service
var SystemdUnit []byte

// ConfigExample is config.example.yaml.
//
//go:embed config.example.yaml
var ConfigExample []byte

// LaunchdPlist is launchd/org.openlog.infra-agent.plist (default paths).
//
//go:embed launchd/org.openlog.infra-agent.plist
var LaunchdPlist []byte
