package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/onuragtas/openlog/agents/infra/internal/phpaccess"
)

// PHP socket access results (PHPAccessStatus).
const (
	PHPCreated  = "created"
	PHPExists   = "exists"
	PHPAdded    = "added"
	PHPMember   = "member"
	PHPOptOut   = "opt_out"
	PHPFailed   = "error"
	PHPGrantsOn = "auto"
)

// PHPAccessStatus is what reconcile did for php.sock access (php-agent.md §1, D-103).
type PHPAccessStatus struct {
	// Group: PHPCreated, PHPExists or PHPFailed (openlog-php).
	Group string `json:"group"`
	// Agent: PHPAdded, PHPMember or PHPFailed (the agent user in openlog-php).
	Agent string `json:"agent"`
	// Grants: PHPGrantsOn or PHPOptOut.
	Grants string `json:"grants"`
	// Added are the pool/web server users added to openlog-php by this run; Failed could not be added.
	Added  []string `json:"added,omitempty"`
	Failed []string `json:"failed,omitempty"`
	// Reloaded are the services gracefully reloaded for Added; NotActive were skipped because they are not running.
	Reloaded  []string `json:"reloaded,omitempty"`
	NotActive []string `json:"not_active,omitempty"`
}

// errNoGroupTool: none of usermod, gpasswd, adduser exists.
var errNoGroupTool = errors.New("no usermod, gpasswd or adduser")

// addToGroup adds user to an existing group with the first available tool.
func (s *Sys) addToGroup(ctx context.Context, group, user string) error {
	for _, cmd := range [][]string{
		{"usermod", "-aG", group, user},
		{"gpasswd", "-a", user, group},
		{"adduser", user, group},
	} {
		if _, err := s.LookPath(cmd[0]); err != nil {
			continue
		}
		return s.Run(ctx, cmd[0], cmd[1:]...)
	}
	return errNoGroupTool
}

// webServerUnits are reloaded when the Apache user (mod_php) got access. nginx runs no PHP: no reload.
var webServerReloadUnits = map[string][]string{"www-data": {"apache2.service"}, "apache": {"httpd.service"}}

// phpAccess gives PHP workers write access to php.sock: the system group openlog-php (never the openlog-agent group,
// which can read the license key in config.yaml) with the agent user in it, and, unless opted out, every PHP-FPM pool
// user and the Apache/nginx user as members.
//
// Group changes reach a process only through initgroups(3) at its start:
//   - the agent: with context apply this step runs in ExecStartPre, before systemd execs ExecStart, which resolves the
//     supplementary groups of User= again for that exec; other contexts need one service restart (RestartRequired);
//   - PHP-FPM: a graceful reload (SIGUSR2) re-executes the root master, which forks new workers that call
//     setgid/initgroups/setuid for the pool user, so a reload is enough; a restart is never done. Only services whose
//     users were added and that are active are reloaded. Apache mod_php children likewise drop privileges with
//     initgroups after a graceful reload.
//
// Users are only ever added (never removed): memberships an operator added stay.
func (r *reconciler) phpAccess(ctx context.Context) {
	s, agentUser := r.o.Sys, r.o.AgentUser
	st := &PHPAccessStatus{Grants: PHPGrantsOn}
	r.st.PHPAccess = st
	group := phpaccess.Group

	if _, ok := s.groupMembers(group); ok {
		st.Group = PHPExists
	} else {
		var err error
		if _, lerr := s.LookPath("groupadd"); lerr == nil {
			err = s.Run(ctx, "groupadd", "--system", group)
		} else {
			err = s.Run(ctx, "addgroup", "-S", group)
		}
		if err != nil {
			st.Group, st.Agent = PHPFailed, PHPFailed
			r.fail("php access group", err)
			return
		}
		st.Group = PHPCreated
		r.note("created the group " + group + " for the PHP agent socket " + "(write access to php.sock only)")
	}

	acc := phpaccess.ReadAccounts(s.Root)
	if acc.InGroup(agentUser, group) {
		st.Agent = PHPMember
	} else if err := s.addToGroup(ctx, group, agentUser); err != nil {
		st.Agent = PHPFailed
		r.fail("php access group", err)
	} else {
		st.Agent = PHPAdded
		if r.o.Context != ReconcileApply {
			r.st.RestartRequired = true // supplementary groups apply at process start
		}
		r.note("added " + agentUser + " to " + group + " (sets the group of php.sock)")
	}

	if r.phpOptedOut() {
		st.Grants = PHPOptOut
		return
	}
	pools := phpaccess.DiscoverPools(s.Root)
	web := phpaccess.WebServerPresent(s.Root)
	units := map[string][]string{} // user -> services to reload
	for _, p := range pools {
		if p.Unit != "" && !slices.Contains(units[p.User], p.Unit) {
			units[p.User] = append(units[p.User], p.Unit)
		}
	}
	var reload []string
	for _, u := range phpaccess.Candidates(pools, web, acc, agentUser) {
		if acc.InGroup(u, group) {
			continue
		}
		if err := s.addToGroup(ctx, group, u); err != nil {
			st.Failed = append(st.Failed, u)
			r.fail("php access "+u, err)
			continue
		}
		st.Added = append(st.Added, u)
		userUnits := slices.Clone(units[u])
		for _, unit := range webServerReloadUnits[u] {
			if phpaccess.UnitInstalled(s.Root, unit) {
				userUnits = append(userUnits, unit)
			}
		}
		for _, unit := range userUnits {
			if !slices.Contains(reload, unit) {
				reload = append(reload, unit)
			}
		}
	}
	if len(st.Added) == 0 {
		return
	}
	r.note("added PHP users to "+group+" so their workers can send spans to php.sock: "+strings.Join(st.Added, ", "),
		"revert", "gpasswd -d <user> "+group+" && touch "+filepath.Join(r.confDir(), phpaccess.OptOutFile))
	if !r.exists(s.path("/run/systemd/system")) {
		if len(reload) > 0 {
			r.note("no systemd: reload PHP-FPM/Apache yourself so workers get the new group: " + strings.Join(reload, ", "))
		}
		return
	}
	for _, unit := range reload {
		if err := s.Run(ctx, "systemctl", "is-active", "--quiet", unit); err != nil {
			st.NotActive = append(st.NotActive, unit) // not installed or stopped: its next start picks up the groups
			continue
		}
		if err := s.Run(ctx, "systemctl", "reload", unit); err != nil {
			r.fail("reload "+unit, err)
			continue
		}
		st.Reloaded = append(st.Reloaded, unit)
	}
	if len(st.Reloaded) > 0 {
		r.note("gracefully reloaded " + strings.Join(st.Reloaded, ", ") + " (new workers start with the new group)")
	}
}

func (r *reconciler) confDir() string {
	if r.o.ConfigPath != "" {
		return filepath.Dir(r.o.ConfigPath)
	}
	return "/etc/openlog-infra-agent"
}

// phpOptedOut: config php_forwarder.grant_pool_users: false, OPENLOG_AGENT_PHP_ACCESS=0 (recorded in the no-php-access
// file) or that file.
func (r *reconciler) phpOptedOut() bool {
	s := r.o.Sys
	marker := filepath.Join(r.confDir(), phpaccess.OptOutFile)
	switch strings.ToLower(s.Getenv(phpaccess.AccessEnv)) {
	case "0", "false", "no", "off":
		if err := os.MkdirAll(r.confDir(), 0o755); err != nil {
			r.fail("php access opt-out", err)
		} else if f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0o644); err != nil {
			r.fail("php access opt-out", err)
		} else {
			f.Close()
		}
		r.log.Info("not adding PHP-FPM pool users to "+phpaccess.Group, "env", phpaccess.AccessEnv+"=0", "recorded_in", marker)
		return true
	}
	if r.exists(marker) {
		return true
	}
	return r.o.PHPGrantsDisabled
}

// phpReport summarizes PHPAccessStatus for ReconcileReport.PHPAccess.
func (st *PHPAccessStatus) summary() string {
	switch {
	case st == nil:
		return ""
	case st.Group == PHPFailed || st.Agent == PHPFailed || len(st.Failed) > 0:
		return PHPFailed
	case st.Grants == PHPOptOut:
		return PHPOptOut
	case len(st.Added) > 0 || st.Agent == PHPAdded:
		return PHPAdded
	}
	return PHPMember
}
