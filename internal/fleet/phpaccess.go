package fleet

import "encoding/json"

// PHP socket access of a host (docs/contracts/php-agent.md §1, releases-updates.md §3 php_access, D-103): which
// PHP-FPM pools may send spans to the infra agent's php.sock.

// maxPHPPools bounds the stored pools of one host (the agent reports at most 512).
const maxPHPPools = 512

// PHPAccessReport is the php_access section of the agent sync request.
type PHPAccessReport struct {
	// SocketGroup is the group applied to php.sock ("" when none).
	SocketGroup string `json:"socket_group"`
	Group       string `json:"group"`
	GroupExists bool   `json:"group_exists"`
	AgentMember bool   `json:"agent_member"`
	// Grants is auto or opted_out.
	Grants string          `json:"grants"`
	Pools  []PHPPoolAccess `json:"pools"`
}

// PHPPoolAccess is one PHP-FPM pool; Access is ok, missing, opted_out or unsupported_user.
type PHPPoolAccess struct {
	Pool       string `json:"pool"`
	PHPVersion string `json:"php_version"`
	User       string `json:"user"`
	Unit       string `json:"unit"`
	Access     string `json:"access"`
}

// ParsePHPAccessReport decodes and bounds an untrusted php_access section (nil when absent or invalid).
func ParsePHPAccessReport(raw []byte) *PHPAccessReport {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var r PHPAccessReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	r.SocketGroup, r.Group, r.Grants = clip(r.SocketGroup, 64), clip(r.Group, 64), clip(r.Grants, 16)
	if len(r.Pools) > maxPHPPools {
		r.Pools = r.Pools[:maxPHPPools]
	}
	if r.Pools == nil {
		r.Pools = []PHPPoolAccess{}
	}
	for i := range r.Pools {
		p := &r.Pools[i]
		p.Pool, p.PHPVersion, p.User = clip(p.Pool, 256), clip(p.PHPVersion, 16), clip(p.User, 64)
		p.Unit, p.Access = clip(p.Unit, 128), clip(p.Access, 32)
	}
	return &r
}
