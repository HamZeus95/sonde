package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HamZeus95/sonde/internal/checks"
	"github.com/HamZeus95/sonde/internal/model"
)

// keycloakServer stands in for Keycloak's admin API: a token endpoint and a
// groups endpoint, and nothing else, because nothing else is used.
func keycloakServer(t *testing.T, groups map[string][]group) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			if err := r.ParseForm(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.PostForm.Get("grant_type") != "client_credentials" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": 60})
		case strings.Contains(r.URL.Path, "/admin/realms/"):
			if r.Header.Get("Authorization") != "Bearer token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			realm := strings.Split(strings.TrimPrefix(r.URL.Path, "/admin/realms/"), "/")[0]
			found, ok := groups[realm]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			search := r.URL.Query().Get("search")
			var matched []group
			for _, g := range found {
				if search == "" || matches([]group{g}, search) {
					matched = append(matched, g)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(matched)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func run(t *testing.T, server *httptest.Server, meta model.Meta, spec *model.GroupExistsSpec) (checks.Outcome, error) {
	t.Helper()
	keycloak, err := NewKeycloak(KeycloakConfig{
		BaseURL: server.URL, ClientID: "sonde", ClientSecret: "secret", Client: server.Client(),
	})
	if err != nil {
		t.Fatalf("new keycloak: %v", err)
	}
	reg := checks.NewRegistry()
	if err := New(map[string]Provider{"keycloak": keycloak}).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	runner, ok := reg.Runner(model.KindIdentity, "group_exists")
	if !ok {
		t.Fatal("identity/group_exists is not registered")
	}
	return runner.Run(context.Background(), meta, model.Check{
		ID: "c", Kind: model.KindIdentity, Check: "group_exists", Spec: spec, Enabled: true,
	})
}

func TestGroupExists(t *testing.T) {
	server := keycloakServer(t, map[string][]group{
		"prod-eu-1": {
			{Name: "sre-oncall", Path: "/sre-oncall"},
			{Name: "platform", Path: "/platform", SubGroups: []group{{Name: "db-operators", Path: "/platform/db-operators"}}},
		},
	})
	defer server.Close()
	meta := model.Meta{Version: 1, ID: "rb", Environment: "prod-eu-1"}

	tests := []struct {
		name string
		spec *model.GroupExistsSpec
		want model.Status
	}{
		{"top level group", &model.GroupExistsSpec{Provider: "keycloak", Group: "sre-oncall"}, model.StatusPass},
		{"subgroup by name", &model.GroupExistsSpec{Provider: "keycloak", Group: "db-operators"}, model.StatusPass},
		{"subgroup by path", &model.GroupExistsSpec{Provider: "keycloak", Group: "/platform/db-operators"}, model.StatusPass},
		{"renamed group", &model.GroupExistsSpec{Provider: "keycloak", Group: "sre-oncall-old"}, model.StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := run(t, server, meta, tt.spec)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if outcome.Status != tt.want {
				t.Errorf("status = %q, want %q (%s)", outcome.Status, tt.want, outcome.Observed.Summary)
			}
		})
	}
}

// TestMissingRealmIsAnError separates a misconfigured check from a deleted
// group: a realm that does not exist tells us nothing about the group.
func TestMissingRealmIsAnError(t *testing.T) {
	server := keycloakServer(t, map[string][]group{"prod-eu-1": nil})
	defer server.Close()
	meta := model.Meta{Version: 1, ID: "rb", Environment: "staging"}
	if _, err := run(t, server, meta, &model.GroupExistsSpec{Provider: "keycloak", Group: "sre-oncall"}); err == nil {
		t.Fatal("an unknown realm must be an error, not a missing group")
	}
}

// TestUnconfiguredProviderRegistersNothing keeps a build with no identity
// credentials from reporting anything but error for identity checks.
func TestUnconfiguredProviderRegistersNothing(t *testing.T) {
	reg := checks.NewRegistry()
	if err := New(nil).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Runner(model.KindIdentity, "group_exists"); ok {
		t.Error("with no provider configured, identity checks must have no runner")
	}
}

// TestTokenFailureNeverEchoesCredentials guards the one place a client secret
// could escape into a result.
func TestTokenFailureNeverEchoesCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","client_secret":"super-secret"}`))
	}))
	defer server.Close()

	_, err := run(t, server, model.Meta{Version: 1, ID: "rb", Environment: "prod"},
		&model.GroupExistsSpec{Provider: "keycloak", Group: "sre-oncall"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("the error echoed the response body: %v", err)
	}
}
