package api

import (
	"net/http"
	"time"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/auth"
)

// Holiday calendars and mute schedule previews (api.md "Alerting", alerting.md §5.2).

type alertCalendarJSON struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Dates          []string `json:"dates"`
	MuteCount      int      `json:"mute_count"`
	CreatedByEmail string   `json:"created_by_email"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

type muteOccurrenceJSON struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

// upcomingMuteOccurrences is the number of occurrences listed in mute responses and schedule previews.
const upcomingMuteOccurrences = 5

func alertCalendarResponse(c *alert.HolidayCalendar) alertCalendarJSON {
	dates := c.Dates
	if dates == nil {
		dates = []string{}
	}
	return alertCalendarJSON{ID: c.ID, Name: c.Name, Description: c.Description, Dates: dates, MuteCount: c.MuteCount,
		CreatedByEmail: c.CreatedByEmail, CreatedAt: formatTime(c.CreatedAt), UpdatedAt: formatTime(c.UpdatedAt)}
}

func occurrencesJSON(list [][2]time.Time) []muteOccurrenceJSON {
	out := make([]muteOccurrenceJSON, 0, len(list))
	for _, o := range list {
		out = append(out, muteOccurrenceJSON{StartsAt: formatTime(o[0]), EndsAt: formatTime(o[1])})
	}
	return out
}

func (s *Server) alertCalendarRoutes(route func(pattern string, access alertAccess, h alertFunc)) {
	route("GET /api/v1/alerts/holiday-calendars", alertRead, s.listAlertCalendars)
	route("POST /api/v1/alerts/holiday-calendars", alertManage, s.createAlertCalendar)
	route("GET /api/v1/alerts/holiday-calendars/{id}", alertRead, s.getAlertCalendar)
	route("PUT /api/v1/alerts/holiday-calendars/{id}", alertManage, s.updateAlertCalendar)
	route("DELETE /api/v1/alerts/holiday-calendars/{id}", alertManage, s.deleteAlertCalendar)
	route("POST /api/v1/alerts/mutes/preview", alertRead, s.previewAlertMuteSchedule)
}

func (s *Server) listAlertCalendars(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	cs, err := s.alerts.ListHolidayCalendars(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]alertCalendarJSON, 0, len(cs))
	for i := range cs {
		out = append(out, alertCalendarResponse(&cs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendars": out})
	return nil
}

func (s *Server) getAlertCalendar(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	c, err := s.alerts.GetHolidayCalendar(r.Context(), p.OrgID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertCalendarResponse(c))
	return nil
}

func (s *Server) createAlertCalendar(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.HolidayCalendarInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.alerts.CreateHolidayCalendar(r.Context(), p.OrgID, in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, alertCalendarResponse(c))
	return nil
}

func (s *Server) updateAlertCalendar(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in alert.HolidayCalendarInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := s.alerts.UpdateHolidayCalendar(r.Context(), p.OrgID, r.PathValue("id"), in, s.alertActor(r, p))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, alertCalendarResponse(c))
	return nil
}

func (s *Server) deleteAlertCalendar(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	if err := s.alerts.DeleteHolidayCalendar(r.Context(), p.OrgID, r.PathValue("id"), s.alertActor(r, p)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// previewAlertMuteSchedule validates an unsaved schedule and returns its next occurrences.
func (s *Server) previewAlertMuteSchedule(w http.ResponseWriter, r *http.Request, p *auth.Principal) error {
	var in struct {
		Schedule *alert.MuteScheduleInput `json:"schedule"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Schedule == nil {
		return badRequest("schedule: required")
	}
	list, err := s.alerts.PreviewMuteSchedule(r.Context(), p.OrgID, *in.Schedule, parseTime, upcomingMuteOccurrences)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"occurrences": occurrencesJSON(list)})
	return nil
}
