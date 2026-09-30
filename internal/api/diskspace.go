package api

import (
	"context"
	"net/http"
	"time"

	"github.com/onuragtas/openlog/internal/auth"
	"github.com/onuragtas/openlog/internal/diskspace"
)

// How full the ClickHouse disks are, and the levels they are reported at (docs/operations/disk-space.md):
//
//	GET    /api/v1/storage/disk            the last measurement and the levels in force (admin)
//	PUT    /api/v1/storage/disk/settings   change the levels (owner; see canManageDiskSpace)
//	DELETE /api/v1/storage/disk/settings   go back to the built-in levels
//
// The measurement comes from the stored snapshot rather than from ClickHouse: only the leader measures, and an
// api pod answering a page must not turn into a second source of the same query.

// DiskSpaceStore persists the operator's levels (diskspace.PGStore).
type DiskSpaceStore interface {
	Get(ctx context.Context) (diskspace.Stored, bool, error)
	Put(ctx context.Context, s diskspace.Settings, actor diskspace.Actor) error
}

// DiskSpaceDeps enables the disk space endpoints.
type DiskSpaceDeps struct {
	// Snapshot reads the last measurement the leader stored; nil disables the endpoints.
	Snapshot func(ctx context.Context) (diskspace.Snapshot, bool, error)
	// Settings is nil in static auth mode: the levels are then the built-in ones and cannot be changed.
	Settings DiskSpaceStore
	SaaS     bool
	// Interval is how often the leader measures, so the UI can say how old the number may be.
	Interval time.Duration
}

// SetDiskSpace enables the disk space endpoints. Must be called before Run.
func (s *Server) SetDiskSpace(d DiskSpaceDeps) {
	s.diskSpace = &d
	s.srv.Handler = s.Handler()
}

func (s *Server) diskSpaceRoutes(mux *http.ServeMux) {
	if s.diskSpace == nil || s.diskSpace.Snapshot == nil {
		return
	}
	// Reading takes an admin: these are the operator's disks, and in a multi-tenant install their free space is
	// not a tenant's business. Writing is checked again in the handler, which can tell an owner from a SaaS
	// tenant who may not change them at all.
	route := func(pattern string, h usageFunc) {
		mux.Handle(pattern, s.instrument(pattern, func(rec *statusRecorder, r *http.Request) {
			noStore(rec)
			p, r := s.authenticate(rec, r)
			if p == nil {
				return
			}
			if !s.isSuperadmin(p) && !allowed(p, auth.ActReadDiskSpace) {
				writeError(rec, &apiError{http.StatusForbidden, "permission_denied",
					"your role does not allow reading the storage status"})
				return
			}
			if err := h(rec, r, p); err != nil {
				s.writeDiskSpaceError(rec, pattern, err)
			}
		}))
	}
	route("GET /api/v1/storage/disk", s.getDiskSpace)
	if s.diskSpace.Settings != nil {
		route("PUT /api/v1/storage/disk/settings", s.putDiskSpaceSettings)
		route("DELETE /api/v1/storage/disk/settings", s.deleteDiskSpaceSettings)
	}
}

func (s *Server) writeDiskSpaceError(w http.ResponseWriter, route string, err error) {
	ae := s.toAPIError(err)
	if ae.status >= 500 {
		s.log.Error("api request failed", "route", route, "err", err)
	}
	writeError(w, ae)
}

// canManageDiskSpace: superadmins always; owners (session users) unless OPENLOG_SAAS_MODE=true, where the disks
// belong to the operator and not to the tenant looking at the page.
func (s *Server) canManageDiskSpace(p *auth.Principal) bool {
	if s.isSuperadmin(p) {
		return true
	}
	return !s.diskSpace.SaaS && allowed(p, auth.ActManageDiskSpace)
}

type diskJSON struct {
	Host        string  `json:"host"`
	Disk        string  `json:"disk"`
	FreeBytes   uint64  `json:"free_bytes"`
	TotalBytes  uint64  `json:"total_bytes"`
	UsedPercent float64 `json:"used_percent"`
	Broken      bool    `json:"broken"`
	// Level is the threshold this disk was reported at in the last check, 0 when it is under all of them.
	Level int `json:"level"`
}

type diskSpaceSettingJSON struct {
	diskspace.Settings
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

type diskSpaceJSON struct {
	// CheckedAt is empty until the leader has measured once.
	CheckedAt string     `json:"checked_at"`
	Disks     []diskJSON `json:"disks"`
	// Worst is the disk under the most pressure, which is what a banner shows. A cluster is as full as its
	// fullest replica.
	Worst      *diskJSON             `json:"worst"`
	Defaults   diskspace.Effective   `json:"defaults"`
	Configured *diskSpaceSettingJSON `json:"configured"`
	Effective  diskspace.Effective   `json:"effective"`
	CanManage  bool                  `json:"can_manage"`
	// IntervalSeconds is how often the disks are measured, so the UI can say when a change takes effect.
	IntervalSeconds int `json:"interval_seconds"`
}

func toDiskJSON(d diskspace.Disk, level int) diskJSON {
	return diskJSON{Host: d.Host, Disk: d.Name, FreeBytes: d.Free, TotalBytes: d.Total,
		UsedPercent: d.UsedPercent(), Broken: d.Broken, Level: level}
}

func (s *Server) diskSpaceResponse(ctx context.Context, p *auth.Principal) (diskSpaceJSON, error) {
	d := s.diskSpace
	resp := diskSpaceJSON{
		Disks:           []diskJSON{},
		Defaults:        diskspace.Defaults(),
		Effective:       diskspace.Defaults(),
		CanManage:       s.canManageDiskSpace(p),
		IntervalSeconds: int(d.Interval.Seconds()),
	}
	if d.Settings != nil {
		stored, found, err := d.Settings.Get(ctx)
		if err != nil {
			return resp, err
		}
		if found {
			resp.Configured = &diskSpaceSettingJSON{Settings: stored.Settings,
				UpdatedAt: formatTime(stored.UpdatedAt), UpdatedBy: stored.UpdatedByEmail}
			resp.Effective = stored.Settings.Resolve()
		}
	}
	snap, ok, err := d.Snapshot(ctx)
	if err != nil {
		return resp, err
	}
	if !ok {
		return resp, nil
	}
	resp.CheckedAt = formatTime(snap.CheckedAt)
	for _, disk := range snap.Disks {
		resp.Disks = append(resp.Disks, toDiskJSON(disk, snap.Reported[disk.Key()]))
	}
	if worst, found := snap.Fullest(); found {
		w := toDiskJSON(worst, snap.Reported[worst.Key()])
		resp.Worst = &w
	}
	return resp, nil
}

func (s *Server) getDiskSpace(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	resp, err := s.diskSpaceResponse(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) putDiskSpaceSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var req diskspace.Settings
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	return s.storeDiskSpaceSettings(w, r, p, req)
}

func (s *Server) deleteDiskSpaceSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	return s.storeDiskSpaceSettings(w, r, p, diskspace.Settings{})
}

func (s *Server) storeDiskSpaceSettings(w http.ResponseWriter, r *http.Request, p *auth.Principal, set diskspace.Settings) error {
	if !s.canManageDiskSpace(p) {
		msg := "only organization owners can change the storage levels"
		if s.diskSpace.SaaS {
			msg = "the storage levels are managed by the openlog operators (OPENLOG_SUPERADMIN_EMAILS)"
		}
		return &apiError{http.StatusForbidden, "permission_denied", msg}
	}
	// Validated here as well as in the store, so an impossible pair of levels comes back as a message rather
	// than as a constraint violation.
	if err := set.Validate(); err != nil {
		return badRequest("%v", err)
	}
	actor := diskspace.Actor{UserID: p.UserID, Email: p.Email, IP: auth.ClientIP(r, nil)}
	if err := s.diskSpace.Settings.Put(r.Context(), set, actor); err != nil {
		return err
	}
	s.log.Info("storage levels changed", "by", p.Email, "cleared", set.Empty())
	resp, err := s.diskSpaceResponse(r.Context(), p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}
