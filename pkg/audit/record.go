// Package audit writes Snellius audit records as JSON lines and tags each
// request with an ID.
package audit

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Schema is the record schema version.
const Schema = "1"

// Audit levels.
const (
	LevelSecurity   = "security"
	LevelAgent      = "agent"
	LevelReads      = "reads"
	LevelSummaries  = "summaries"
	LevelDisclosure = "disclosure"
	LevelRequests   = "requests"
)

// DefaultLevels is the level list used when none is configured.
const DefaultLevels = "security,agent,reads,summaries"

var allLevels = []string{LevelSecurity, LevelAgent, LevelReads, LevelSummaries, LevelDisclosure, LevelRequests}

// Levels is the set of enabled audit levels.
type Levels map[string]bool

// ParseLevels parses a comma list of level names; empty means DefaultLevels.
// An unknown name is an error.
func ParseLevels(s string) (Levels, error) {
	if strings.TrimSpace(s) == "" {
		s = DefaultLevels
	}
	ls := Levels{}
	for _, n := range strings.Split(s, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		known := false
		for _, k := range allLevels {
			known = known || k == n
		}
		if !known {
			return nil, fmt.Errorf("unknown audit level %q: want a list of %s", n, strings.Join(allLevels, ", "))
		}
		ls[n] = true
	}
	return ls, nil
}

// Identity names a person by issuer and subject.
type Identity struct{ Issuer, Subject string }

// Kube is the identity Kubernetes sees.
type Kube struct {
	Username string
	Groups   []string
}

// Request describes the HTTP request behind a record. Filters hold only
// allow-listed names and values, never a raw query string.
type Request struct {
	ID      string
	Method  string
	Route   string
	Filters map[string]string
}

// Record is one audit record (schema 1). Empty fields are left out of the output.
type Record struct {
	Time       time.Time
	ID         string
	Level      string
	Action     string
	Outcome    string
	Reason     string
	User       Identity
	Kube       Kube
	Session    string
	Agent      string
	OnBehalfOf string
	Request    Request
	Install    string
	Server     string
	Refs       []string
	AuthzRev   string
	Source     string
	UserAgent  string
	// Count is the number of events a collapsed record stands for.
	Count int
	// Mode and Listen describe the process; the startup record uses them.
	Mode   string
	Listen string
}

const maxUserAgent = 256

// FromRequest fills the request ID, method, route template, source IP and
// truncated user agent from r. The source is the peer, or the forwarded client
// that RequestIDTrusting accepted. It copies no header, cookie, query or body.
func FromRequest(r *http.Request) Record {
	rec := Record{Request: Request{ID: RequestIDFrom(r.Context()), Method: r.Method, Route: r.Pattern}}
	if src := sourceFrom(r.Context()); src != "" {
		rec.Source = src
	} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		rec.Source = host
	}
	ua := r.UserAgent()
	if len(ua) > maxUserAgent {
		ua = strings.ToValidUTF8(ua[:maxUserAgent], "")
	}
	rec.UserAgent = ua
	return rec
}
