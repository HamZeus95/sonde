package command

import (
	"fmt"
	"strings"
)

// Invocation is what a documented command line addresses.
type Invocation struct {
	Tool      string
	Verb      string
	Namespace string
	Resources []Ref
}

// Ref is a type/name pair named on a command line.
type Ref struct {
	Type string
	Name string
}

// verbsWithResources are the kubectl verbs whose arguments name objects. A verb
// outside this set is parsed but its arguments are not treated as objects,
// because `kubectl config use-context prod` names no resource and guessing
// otherwise would produce a check that fails for no reason.
var verbsWithResources = map[string]bool{
	"get": true, "describe": true, "exec": true, "logs": true, "scale": true,
	"delete": true, "edit": true, "patch": true, "annotate": true, "label": true,
	"rollout": true, "port-forward": true, "cp": true, "wait": true, "top": true,
}

// flagsTakingValue are the kubectl flags whose value is the next argument, so
// that the value is not mistaken for a resource.
var flagsTakingValue = map[string]bool{
	"-n": true, "--namespace": true, "--context": true, "--kubeconfig": true,
	"-o": true, "--output": true, "-l": true, "--selector": true, "-c": true,
	"--container": true, "--replicas": true, "--timeout": true, "--field-selector": true,
	"--server": true, "--token": true, "--user": true, "--cluster": true,
	"--type": true, "--patch": true, "--from": true, "--to": true,
}

// Parse reads a documented command line.
//
// Tokenising is deliberately simple: quotes group, everything after -- is the
// command being handed to a container and is not ours to interpret. It never
// executes, expands, or shells out.
func Parse(command string) (Invocation, error) {
	tokens, err := tokenise(command)
	if err != nil {
		return Invocation{}, err
	}
	if len(tokens) == 0 {
		return Invocation{}, fmt.Errorf("command is empty")
	}

	inv := Invocation{Tool: toolName(tokens[0])}
	args := tokens[1:]

	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if name, value, found := strings.Cut(arg, "="); found && strings.HasPrefix(arg, "-") {
			if name == "-n" || name == "--namespace" {
				inv.Namespace = value
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if flagsTakingValue[arg] {
				if i+1 >= len(args) {
					return Invocation{}, fmt.Errorf("flag %s has no value", arg)
				}
				if arg == "-n" || arg == "--namespace" {
					inv.Namespace = args[i+1]
				}
				i++
			}
			continue
		}
		positional = append(positional, arg)
	}

	if len(positional) == 0 {
		return inv, nil
	}
	inv.Verb = positional[0]
	rest := positional[1:]
	// rollout, and anything else with a subcommand, puts the object after it.
	if inv.Verb == "rollout" && len(rest) > 0 {
		inv.Verb += " " + rest[0]
		rest = rest[1:]
	}
	if !verbsWithResources[positional[0]] {
		return inv, nil
	}
	inv.Resources = refs(rest)
	return inv, nil
}

// refs reads the two spellings kubectl accepts: type/name, and a type followed
// by one or more names.
func refs(args []string) []Ref {
	var out []Ref
	var pendingType string
	for _, arg := range args {
		if typ, name, found := strings.Cut(arg, "/"); found {
			if typ != "" && name != "" {
				out = append(out, Ref{Type: typ, Name: name})
			}
			pendingType = ""
			continue
		}
		if pendingType == "" {
			pendingType = arg
			continue
		}
		out = append(out, Ref{Type: pendingType, Name: arg})
	}
	return out
}

// toolName strips a path so that /usr/local/bin/kubectl is kubectl.
func toolName(arg string) string {
	if index := strings.LastIndex(arg, "/"); index >= 0 {
		arg = arg[index+1:]
	}
	return arg
}

// tokenise splits a command line on whitespace, honouring single and double
// quotes. It does not expand variables, globs, or anything else: a runbook's
// $NAMESPACE is a token, not a lookup.
func tokenise(command string) ([]string, error) {
	var (
		tokens  []string
		current strings.Builder
		quote   rune
		started bool
	)
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n':
			if started {
				tokens = append(tokens, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("command has an unclosed %c quote", quote)
	}
	if started {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}
