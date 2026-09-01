package model

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestCatalogueIsWellFormed guards the properties everything else assumes: no
// duplicate pairs, no empty names, and a distinct spec per entry.
func TestCatalogueIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range Catalogue() {
		key := string(def.Kind) + "/" + def.Check
		t.Run(key, func(t *testing.T) {
			if def.Kind == "" || def.Check == "" {
				t.Fatal("kind and check are required")
			}
			if def.Summary == "" {
				t.Error("summary is required: it is what an error message offers the user")
			}
			if seen[key] {
				t.Fatalf("duplicate catalogue entry %s", key)
			}
			seen[key] = true
			if def.New == nil {
				t.Fatal("New is required")
			}
			if def.New() == def.New() {
				t.Error("New must return a fresh spec each call, or two checks share one spec")
			}
		})
	}
}

// TestLookupRejectsUnknownPairs pins the message a user sees for a typo. Never
// silently ignoring an unknown pair is what stops a mistyped check from looking
// like a passing one.
func TestLookupRejectsUnknownPairs(t *testing.T) {
	if _, ok := Lookup("terraform", "resource_exists"); ok {
		t.Fatal("terraform is not a kind")
	}
	unknownKind := (&UnknownCheckError{Kind: "terraform", Check: "resource_exists"}).Error()
	if !strings.Contains(unknownKind, "valid kinds are") {
		t.Errorf("unknown kind error does not list the valid kinds: %q", unknownKind)
	}
	unknownCheck := (&UnknownCheckError{Kind: KindKubernetes, Check: "resource_is_healthy"}).Error()
	if !strings.Contains(unknownCheck, "resource_exists") {
		t.Errorf("unknown check error does not list the valid checks: %q", unknownCheck)
	}
}

// TestSpecFieldsWalksEmbeddedStructs covers the inline KubernetesTarget: if
// reflection missed it, every kubernetes check would warn that resource and
// namespace are unknown fields.
func TestSpecFieldsWalksEmbeddedStructs(t *testing.T) {
	fields := SpecFields(&ResourceExistsSpec{})
	for _, want := range []string{"resource", "namespace", "cluster"} {
		if !slices.Contains(fields, want) {
			t.Errorf("SpecFields is missing %q: got %v", want, fields)
		}
	}
	accepted := AcceptedFields(&ResourceExistsSpec{})
	for _, want := range CommonFields {
		if !slices.Contains(accepted, want) {
			t.Errorf("AcceptedFields is missing the common field %q", want)
		}
	}
}

func TestDurationRoundTrips(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"90s"`), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Duration != 90*time.Second {
		t.Errorf("got %v, want 90s", d.Duration)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `"1m30s"` {
		t.Errorf("got %s, want \"1m30s\"", out)
	}
	for _, bad := range []string{`"soon"`, `"-5s"`, `"0"`, `30`} {
		if err := json.Unmarshal([]byte(bad), &d); err == nil {
			t.Errorf("unmarshal %s: expected an error", bad)
		}
	}
}

func TestParseResourceRef(t *testing.T) {
	ref, err := ParseResourceRef(" deployment/payments-api ")
	if err != nil {
		t.Fatalf("ParseResourceRef: %v", err)
	}
	if ref.Type != "deployment" || ref.Name != "payments-api" {
		t.Errorf("got %+v", ref)
	}
	for _, bad := range []string{"payments-api", "deployment/", "/payments-api", "a/b/c", ""} {
		if _, err := ParseResourceRef(bad); err == nil {
			t.Errorf("ParseResourceRef(%q): expected an error", bad)
		}
	}
}

// TestCanISubjectReview pins which spelling of a can_i check needs the
// privileged SubjectAccessReview grant, since docs/security.md promises a crisp
// answer to exactly this question.
func TestCanISubjectReview(t *testing.T) {
	self := &CanISpec{Verb: "get", Resource: "pods"}
	if self.SubjectReview() {
		t.Error("a can_i with no subject asks about the probe itself")
	}
	for _, spec := range []*CanISpec{
		{Verb: "get", Resource: "pods", AsUser: "alice"},
		{Verb: "get", Resource: "pods", AsGroup: "sre"},
	} {
		if !spec.SubjectReview() {
			t.Errorf("%+v asks about another identity", spec)
		}
	}
}

// TestCheckRoundTrips covers the path a check takes to the probe: parsed here,
// marshalled into a job payload, decoded back into the same spec type.
func TestCheckRoundTrips(t *testing.T) {
	for _, def := range Catalogue() {
		t.Run(string(def.Kind)+"/"+def.Check, func(t *testing.T) {
			original := Check{
				ID:      "c",
				Kind:    def.Kind,
				Check:   def.Check,
				Spec:    def.New(),
				Line:    7,
				Enabled: true,
				Timeout: &Duration{Duration: 15 * time.Second},
			}
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded Check
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if decoded.Kind != original.Kind || decoded.Check != original.Check {
				t.Fatalf("decoded %s/%s", decoded.Kind, decoded.Check)
			}
			if decoded.Timeout == nil || decoded.Timeout.Duration != 15*time.Second {
				t.Errorf("timeout did not survive: %+v", decoded.Timeout)
			}
			reencoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}
			if string(reencoded) != string(encoded) {
				t.Errorf("round trip changed the check:\n%s\n%s", encoded, reencoded)
			}
		})
	}
}

// TestUnmarshalRejectsUnknownPairs stops a job payload naming a check this
// build does not have from decoding into something that looks runnable.
func TestUnmarshalRejectsUnknownPairs(t *testing.T) {
	var c Check
	err := json.Unmarshal([]byte(`{"id":"c","kind":"kubernetes","check":"resource_is_healthy","spec":{}}`), &c)
	var unknown *UnknownCheckError
	if !errors.As(err, &unknown) {
		t.Fatalf("expected UnknownCheckError, got %v", err)
	}
}
