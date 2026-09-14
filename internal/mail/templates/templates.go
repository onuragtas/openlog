// Package templates renders the transactional e-mails (invitation, address verification, domain verification, usage
// notification) in the supported languages, as plain text and HTML (docs/contracts/api.md "E-mail language").
//
// Templates live in text/<name>.<lang>.tmpl (blocks "subject" and "text") and html/<name>.<lang>.tmpl (blocks "content"
// and "linkLabel", wrapped by html/layout.tmpl). Adding a language means adding both files for every name and the
// language to Supported and the label maps below; TestAllTemplates fails until every combination renders.
package templates

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
)

//go:embed text/*.tmpl html/*.tmpl
var files embed.FS

// Languages.
const (
	English = "en"
	Turkish = "tr"
)

// Supported lists the e-mail languages; the first is the fallback.
var Supported = []string{English, Turkish}

// Template names.
const (
	NameInvitation         = "invitation"
	NameVerification       = "verification"
	NameDomainVerification = "domain_verification"
	NameUsage              = "usage"
)

var names = []string{NameInvitation, NameVerification, NameDomainVerification, NameUsage, NameTrial}

// Normalize maps a stored or requested locale ("tr", "tr-TR", "EN") to a supported language, English otherwise.
func Normalize(locale string) string {
	base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(locale)), "-")
	base, _, _ = strings.Cut(base, "_")
	for _, l := range Supported {
		if base == l {
			return l
		}
	}
	return English
}

// Resolve returns the first candidate that names a supported language ("tr", "tr-TR"), English when none does. Callers
// pass the sources in precedence order: user preference, organization default, stored request language (D-095).
func Resolve(candidates ...string) string {
	for _, c := range candidates {
		base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(c)), "-")
		base, _, _ = strings.Cut(base, "_")
		for _, l := range Supported {
			if base == l {
				return l
			}
		}
	}
	return English
}

// FromAcceptLanguage returns the supported language the client prefers most (RFC 9110 Accept-Language with q-values),
// or "" when it names none of them (the caller then falls back, e.g. to English).
func FromAcceptLanguage(header string) string {
	type pref struct {
		lang  string
		q     float64
		order int
	}
	var prefs []pref
	for i, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			q = f
		}
		for _, l := range Supported {
			if base == l && q > 0 {
				prefs = append(prefs, pref{l, q, i})
			}
		}
	}
	if len(prefs) == 0 {
		return ""
	}
	sort.SliceStable(prefs, func(a, b int) bool { return prefs[a].q > prefs[b].q })
	return prefs[0].lang
}

// Message is a rendered e-mail.
type Message struct {
	Subject string
	Text    string
	HTML    string
}

type set struct {
	text *texttemplate.Template
	html *htmltemplate.Template
}

var sets = map[string]set{}

func init() {
	for _, name := range names {
		for _, lang := range Supported {
			funcs := funcMap(lang)
			t, err := texttemplate.New(name).Funcs(funcs).ParseFS(files, "text/"+name+"."+lang+".tmpl")
			if err != nil {
				panic(fmt.Sprintf("mail template text/%s.%s: %v", name, lang, err))
			}
			h, err := htmltemplate.New(name).Funcs(htmltemplate.FuncMap(funcs)).ParseFS(files, "html/layout.tmpl", "html/"+name+"."+lang+".tmpl")
			if err != nil {
				panic(fmt.Sprintf("mail template html/%s.%s: %v", name, lang, err))
			}
			sets[name+"."+lang] = set{t, h}
		}
	}
}

func funcMap(lang string) texttemplate.FuncMap {
	return texttemplate.FuncMap{
		"lang":  func() string { return lang },
		"date":  func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 MST") },
		"lower": strings.ToLower,
	}
}

// Render renders template name in locale (normalized; unknown languages use English) with data. The data must provide
// the fields the templates use, including Link.
func Render(name, locale string, data any) (Message, error) {
	s, ok := sets[name+"."+Normalize(locale)]
	if !ok {
		return Message{}, fmt.Errorf("unknown mail template %q", name)
	}
	var subject, text, htmlBody bytes.Buffer
	if err := s.text.ExecuteTemplate(&subject, "subject", data); err != nil {
		return Message{}, err
	}
	if err := s.text.ExecuteTemplate(&text, "text", data); err != nil {
		return Message{}, err
	}
	if err := s.html.ExecuteTemplate(&htmlBody, "html", data); err != nil {
		return Message{}, err
	}
	body := strings.TrimSpace(strings.ReplaceAll(text.String(), "\r\n", "\n"))
	return Message{
		Subject: strings.Join(strings.Fields(subject.String()), " "),
		Text:    strings.ReplaceAll(body, "\n", "\r\n") + "\r\n",
		HTML:    htmlBody.String(),
	}, nil
}

func must(m Message, err error) Message {
	if err != nil {
		// Templates are embedded and covered by TestAllTemplates; a failure here is a programming error.
		panic(err)
	}
	return m
}

var roleLabels = map[string]map[string]string{
	English: {"owner": "owner", "admin": "admin", "member": "member", "viewer": "viewer"},
	Turkish: {"owner": "sahip", "admin": "yönetici", "member": "üye", "viewer": "izleyici"},
}

var metricLabels = map[string]map[string]string{
	English: {"ingest_bytes": "Data ingest", "hosts": "Hosts", "users": "Users"},
	Turkish: {"ingest_bytes": "Veri alımı", "hosts": "Host sayısı", "users": "Kullanıcı sayısı"},
}

func label(m map[string]map[string]string, locale, key string) string {
	if v, ok := m[Normalize(locale)][key]; ok {
		return v
	}
	return key
}

// InvitationData is the invitation e-mail.
type InvitationData struct {
	Inviter   string // e-mail of the inviter; empty = "an administrator"
	OrgName   string
	Role      string // owner, admin, member, viewer
	RoleLabel string // set by Invitation
	ExpiresAt time.Time
	Link      string
}

// Invitation renders the invitation e-mail.
func Invitation(locale string, d InvitationData) Message {
	d.RoleLabel = label(roleLabels, locale, d.Role)
	return must(Render(NameInvitation, locale, d))
}

// VerificationData is the e-mail address verification of a sign-up.
type VerificationData struct {
	ExpiresAt time.Time
	Link      string
}

// Verification renders the address verification e-mail.
func Verification(locale string, d VerificationData) Message {
	return must(Render(NameVerification, locale, d))
}

// DomainVerificationData is the SSO domain ownership e-mail.
type DomainVerificationData struct {
	Requester string
	Domain    string
	OrgName   string
	Link      string
}

// DomainVerification renders the domain verification e-mail.
func DomainVerification(locale string, d DomainVerificationData) Message {
	return must(Render(NameDomainVerification, locale, d))
}

// UsageData is the owner notification of a quota threshold.
type UsageData struct {
	OrgName     string
	TenantID    string
	Metric      string // ingest_bytes, hosts, users
	MetricLabel string // set by Usage
	Used        string // formatted
	Limit       string // formatted
	Percent     float64
	Threshold   int
	PlanName    string
	Period      string // YYYY-MM
	PeriodEnd   string // YYYY-MM-DD
	Exceeded    bool   // threshold >= 100
	// HardBlock: ingest is rejected above BlockPercent (SaaS mode, hard_ingest_limit).
	HardBlock    bool
	BlockPercent float64
	GracePercent float64
	Link         string // usage page; empty without OPENLOG_PUBLIC_URL
}

// Usage renders the usage notification.
func Usage(locale string, d UsageData) Message {
	d.MetricLabel = label(metricLabels, locale, d.Metric)
	return must(Render(NameUsage, locale, d))
}
