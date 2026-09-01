package model

import (
	"fmt"
	"net/url"
	"strings"
)

// StatusIsSpec asks whether a documented link is still alive: the Grafana board,
// the status page, the wiki page the runbook tells you to open.
//
// The runner issues a single read-only request and follows redirects; it never
// sends a body and never uses a verb other than GET or HEAD.
type StatusIsSpec struct {
	URL string `json:"url"`
	// Expect is the acceptable status code. Absent means 200.
	Expect int `json:"expect,omitempty"`
	// Method is GET (default) or HEAD. Nothing else: checks are read-only.
	Method string `json:"method,omitempty"`
}

// Validate implements Spec.
func (s *StatusIsSpec) Validate() error {
	if s.URL == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("parse url %q: %w", s.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q must be http or https", s.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", s.URL)
	}
	if s.Expect != 0 && (s.Expect < 100 || s.Expect > 599) {
		return fmt.Errorf("expect %d is not an HTTP status code", s.Expect)
	}
	switch strings.ToUpper(s.Method) {
	case "", "GET", "HEAD":
	default:
		return fmt.Errorf("method %q is not allowed: checks are read-only, use GET or HEAD", s.Method)
	}
	return nil
}

// ExpectedStatus is Expect, defaulted to 200.
func (s *StatusIsSpec) ExpectedStatus() int {
	if s.Expect == 0 {
		return 200
	}
	return s.Expect
}

// DNSRecordTypes are the record types a check may assert on. Read-only by
// nature; the list is short because a runbook's DNS claims are usually about
// where a name points during a failover.
var DNSRecordTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT", "SRV"}

// RecordExistsSpec asks whether a name still resolves, and optionally whether it
// resolves to what the runbook says: the failover CNAME that was repointed six
// months ago and never documented.
type RecordExistsSpec struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Expect optionally asserts the record's value. Absent asserts existence
	// only.
	Expect string `json:"expect,omitempty"`
}

// Validate implements Spec.
func (s *RecordExistsSpec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if s.Type == "" {
		return fmt.Errorf("type is required")
	}
	if !s.knownType() {
		return fmt.Errorf("type %q is not one of %s", s.Type, strings.Join(DNSRecordTypes, ", "))
	}
	return nil
}

func (s *RecordExistsSpec) knownType() bool {
	for _, t := range DNSRecordTypes {
		if strings.EqualFold(s.Type, t) {
			return true
		}
	}
	return false
}
