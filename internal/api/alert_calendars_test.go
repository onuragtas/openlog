package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/onuragtas/openlog/internal/alert"
	"github.com/onuragtas/openlog/internal/auth"
)

// ---- fakeAlertStore: holiday calendars ----

func (f *fakeAlertStore) ListHolidayCalendars(_ context.Context, orgID string) ([]alert.HolidayCalendar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.HolidayCalendar{}
	for _, c := range f.calendars {
		if c.OrgID == orgID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *fakeAlertStore) GetHolidayCalendar(_ context.Context, orgID, id string) (*alert.HolidayCalendar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calendars[id]
	if !ok || c.OrgID != orgID {
		return nil, alert.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *fakeAlertStore) CreateHolidayCalendar(_ context.Context, orgID string, v *alert.ValidHolidayCalendar, actor alert.Actor) (*alert.HolidayCalendar, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calendars == nil {
		f.calendars = map[string]*alert.HolidayCalendar{}
	}
	c := &alert.HolidayCalendar{ID: uuid.NewString(), OrgID: orgID, Name: v.Name, Description: v.Description, Dates: v.Dates,
		CreatedBy: actor.UserID, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	f.calendars[c.ID] = c
	return c, nil
}

func (f *fakeAlertStore) UpdateHolidayCalendar(ctx context.Context, orgID, id string, v *alert.ValidHolidayCalendar, _ alert.Actor) (*alert.HolidayCalendar, error) {
	if _, err := f.GetHolidayCalendar(ctx, orgID, id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calendars[id]
	c.Name, c.Description, c.Dates = v.Name, v.Description, v.Dates
	return c, nil
}

func (f *fakeAlertStore) DeleteHolidayCalendar(ctx context.Context, orgID, id string, _ alert.Actor) error {
	if _, err := f.GetHolidayCalendar(ctx, orgID, id); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.mutes {
		if m.Schedule != nil {
			for _, x := range m.Schedule.HolidayCalendarIDs {
				if x == id {
					return &alert.PreconditionError{Msg: "the holiday calendar is used by recurring mutes"}
				}
			}
		}
	}
	delete(f.calendars, id)
	return nil
}

func (f *fakeAlertStore) HolidayCalendarDates(_ context.Context, orgID string, ids []string) (map[string][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]string{}
	for _, id := range ids {
		if c, ok := f.calendars[id]; ok && c.OrgID == orgID {
			out[id] = c.Dates
		}
	}
	return out, nil
}

func TestAlertHolidayCalendarsAPI(t *testing.T) {
	e := newAlertEnv(t)
	admin := e.member(t, "cal-admin@example.com", auth.RoleAdmin)
	member := e.member(t, "cal-member@example.com", auth.RoleMember)

	cal := map[string]any{"name": "TR holidays", "dates": []string{"01-01", "2026-04-23", "--05-19"}}
	if rec := member.do(http.MethodPost, "/api/v1/alerts/holiday-calendars", cal); rec.Code != http.StatusForbidden {
		t.Errorf("member create calendar: %d", rec.Code)
	}
	if rec := admin.do(http.MethodPost, "/api/v1/alerts/holiday-calendars", map[string]any{"name": "bad", "dates": []string{"2026-02-30"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid date: %d %s", rec.Code, rec.Body)
	}
	rec := admin.do(http.MethodPost, "/api/v1/alerts/holiday-calendars", cal)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create calendar: %d %s", rec.Code, rec.Body)
	}
	created := decode[map[string]any](t, rec)
	calID := created["id"].(string)
	if got := created["dates"].([]any); len(got) != 3 || got[0] != "01-01" || got[2] != "2026-04-23" {
		t.Errorf("dates not normalized: %v", got)
	}
	list := decode[map[string]any](t, member.do(http.MethodGet, "/api/v1/alerts/holiday-calendars", nil))
	if n := len(list["calendars"].([]any)); n != 1 {
		t.Errorf("member lists %d calendars", n)
	}
	if rec := e.otherB.do(http.MethodGet, "/api/v1/alerts/holiday-calendars/"+calID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("other org reads calendar: %d", rec.Code)
	}

	// A recurring monthly mute with an exdate and the calendar; upcoming occurrences skip both.
	schedule := map[string]any{"timezone": "UTC", "rrule": "FREQ=MONTHLY;BYMONTHDAY=1", "start_time": "00:00", "end_time": "06:00",
		"exdates": []string{"2026-11-01"}, "holiday_calendar_ids": []string{calID}, "from": "2026-10-15T00:00:00Z"}
	prev := member.do(http.MethodPost, "/api/v1/alerts/mutes/preview", map[string]any{"schedule": schedule})
	if prev.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", prev.Code, prev.Body)
	}
	occ := decode[map[string]any](t, prev)["occurrences"].([]any)
	var starts []string
	for _, o := range occ {
		starts = append(starts, o.(map[string]any)["starts_at"].(string)[:10])
	}
	// 2026-11-01 is an exdate, 2027-01-01 a holiday (01-01).
	if strings.Join(starts, ",") != "2026-12-01,2027-02-01,2027-03-01,2027-04-01,2027-05-01" {
		t.Errorf("preview occurrences: %v", starts)
	}
	if rec := member.do(http.MethodPost, "/api/v1/alerts/mutes/preview", map[string]any{"schedule": map[string]any{"rrule": "FREQ=MONTHLY", "start_time": "00:00", "end_time": "06:00"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid preview: %d", rec.Code)
	}
	unknown := map[string]any{"name": "x", "schedule": map[string]any{"rrule": "FREQ=DAILY", "start_time": "00:00", "end_time": "06:00",
		"holiday_calendar_ids": []string{uuid.NewString()}}}
	if rec := member.do(http.MethodPost, "/api/v1/alerts/mutes", unknown); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown calendar: %d %s", rec.Code, rec.Body)
	}
	rec = member.do(http.MethodPost, "/api/v1/alerts/mutes", map[string]any{"name": "month start", "schedule": schedule})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create monthly mute: %d %s", rec.Code, rec.Body)
	}
	mute := decode[map[string]any](t, rec)
	sc := mute["schedule"].(map[string]any)
	if sc["rrule"] != "FREQ=MONTHLY;BYMONTHDAY=1" || len(sc["days"].([]any)) != 0 || len(sc["exdates"].([]any)) != 1 || len(sc["holiday_calendar_ids"].([]any)) != 1 {
		t.Errorf("stored schedule: %v", sc)
	}
	if up := mute["upcoming"].([]any); len(up) != 5 {
		t.Errorf("upcoming: %v", up)
	}

	// A referenced calendar cannot be deleted.
	if rec := admin.do(http.MethodDelete, "/api/v1/alerts/holiday-calendars/"+calID, nil); rec.Code != http.StatusConflict {
		t.Errorf("delete referenced calendar: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do(http.MethodPut, "/api/v1/alerts/holiday-calendars/"+calID, map[string]any{"name": "TR", "dates": []string{"01-01"}}); rec.Code != http.StatusOK {
		t.Errorf("update calendar: %d %s", rec.Code, rec.Body)
	}
}
