package command

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		tool      string
		verb      string
		namespace string
		refs      []Ref
	}{
		{
			name:    "type and name",
			command: "kubectl -n payments get deploy payments-api",
			tool:    "kubectl", verb: "get", namespace: "payments",
			refs: []Ref{{Type: "deploy", Name: "payments-api"}},
		},
		{
			name:    "slash form",
			command: "kubectl get deployment/payments-api",
			tool:    "kubectl", verb: "get",
			refs: []Ref{{Type: "deployment", Name: "payments-api"}},
		},
		{
			name:    "everything after -- belongs to the container",
			command: "kubectl -n data exec statefulset/postgres-primary -- pg_ctl promote",
			tool:    "kubectl", verb: "exec", namespace: "data",
			refs: []Ref{{Type: "statefulset", Name: "postgres-primary"}},
		},
		{
			name:    "namespace as one token",
			command: "kubectl --namespace=payments get deploy/payments-api",
			tool:    "kubectl", verb: "get", namespace: "payments",
			refs: []Ref{{Type: "deploy", Name: "payments-api"}},
		},
		{
			name:    "flag values are not resources",
			command: "kubectl get pods -l app=payments -o json",
			tool:    "kubectl", verb: "get",
			refs: []Ref{},
		},
		{
			name:    "rollout keeps its subcommand",
			command: "kubectl rollout restart deployment/payments-api",
			tool:    "kubectl", verb: "rollout restart",
			refs: []Ref{{Type: "deployment", Name: "payments-api"}},
		},
		{
			name:    "a verb that names no resource",
			command: "kubectl config use-context prod-eu-1",
			tool:    "kubectl", verb: "config",
			refs: []Ref{},
		},
		{
			name:    "a path is stripped from the tool",
			command: "/usr/local/bin/kubectl get deployment/x",
			tool:    "kubectl", verb: "get",
			refs: []Ref{{Type: "deployment", Name: "x"}},
		},
		{
			name:    "quotes group",
			command: `aws s3 ls "my bucket"`,
			tool:    "aws", verb: "s3",
			refs: []Ref{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.command)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Tool != tt.tool || got.Verb != tt.verb || got.Namespace != tt.namespace {
				t.Errorf("got tool=%q verb=%q namespace=%q", got.Tool, got.Verb, got.Namespace)
			}
			if len(got.Resources) != len(tt.refs) {
				t.Fatalf("resources = %+v, want %+v", got.Resources, tt.refs)
			}
			for i, ref := range tt.refs {
				if got.Resources[i] != ref {
					t.Errorf("resource %d = %+v, want %+v", i, got.Resources[i], ref)
				}
			}
		})
	}
}

func TestParseRejectsUnclosedQuotes(t *testing.T) {
	if _, err := Parse(`kubectl get deploy "payments`); err == nil {
		t.Fatal("expected an error")
	}
}

// TestParseNeverRuns is a reminder in test form: parsing a destructive command
// is safe, because parsing is all that happens.
func TestParseNeverRuns(t *testing.T) {
	got, err := Parse("kubectl -n payments delete deployment payments-api --force")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Verb != "delete" || len(got.Resources) != 1 {
		t.Fatalf("got %+v", got)
	}
}
