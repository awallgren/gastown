package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

func TestBuildVarMap_DefaultsOnly(t *testing.T) {
	f := &formula.Formula{
		Vars: map[string]formula.Var{
			"test_command":  {Default: "go test ./..."},
			"target_branch": {Default: "main"},
			"empty_var":     {Default: ""},
		},
	}
	got := buildVarMap(f, nil)

	if got["test_command"] != "go test ./..." {
		t.Errorf("test_command = %q, want %q", got["test_command"], "go test ./...")
	}
	if got["target_branch"] != "main" {
		t.Errorf("target_branch = %q, want %q", got["target_branch"], "main")
	}
	if _, ok := got["empty_var"]; ok {
		t.Errorf("empty_var should not be in map, but got %q", got["empty_var"])
	}
}

func TestBuildVarMap_OverridesWin(t *testing.T) {
	f := &formula.Formula{
		Vars: map[string]formula.Var{
			"test_command":  {Default: "go test ./..."},
			"target_branch": {Default: "main"},
		},
	}
	overrides := []string{
		"test_command=./gradlew test",
		"target_branch=master",
	}
	got := buildVarMap(f, overrides)

	if got["test_command"] != "./gradlew test" {
		t.Errorf("test_command = %q, want %q", got["test_command"], "./gradlew test")
	}
	if got["target_branch"] != "master" {
		t.Errorf("target_branch = %q, want %q", got["target_branch"], "master")
	}
}

func TestBuildVarMap_OverrideAddsNew(t *testing.T) {
	f := &formula.Formula{
		Vars: map[string]formula.Var{},
	}
	overrides := []string{"custom_var=hello"}
	got := buildVarMap(f, overrides)

	if got["custom_var"] != "hello" {
		t.Errorf("custom_var = %q, want %q", got["custom_var"], "hello")
	}
}

func TestBuildVarMap_OverrideWithEqualsInValue(t *testing.T) {
	f := &formula.Formula{
		Vars: map[string]formula.Var{},
	}
	overrides := []string{"cmd=VAR=1 make test"}
	got := buildVarMap(f, overrides)

	if got["cmd"] != "VAR=1 make test" {
		t.Errorf("cmd = %q, want %q", got["cmd"], "VAR=1 make test")
	}
}

func TestSubstituteVars_Basic(t *testing.T) {
	vars := map[string]string{
		"test_command":  "./gradlew test",
		"target_branch": "master",
	}
	input := "Run {{test_command}} on branch {{target_branch}}"
	got := substituteVars(input, vars)
	want := "Run ./gradlew test on branch master"
	if got != want {
		t.Errorf("substituteVars = %q, want %q", got, want)
	}
}

func TestSubstituteVars_UnresolvedLeftAlone(t *testing.T) {
	vars := map[string]string{
		"test_command": "./gradlew test",
	}
	input := "Run {{test_command}} then {{unknown_var}}"
	got := substituteVars(input, vars)
	want := "Run ./gradlew test then {{unknown_var}}"
	if got != want {
		t.Errorf("substituteVars = %q, want %q", got, want)
	}
}

func TestSubstituteVars_EmptyMap(t *testing.T) {
	input := "No {{substitution}} here"
	got := substituteVars(input, nil)
	if got != input {
		t.Errorf("substituteVars = %q, want %q", got, input)
	}
}

func TestSubstituteVars_MultipleOccurrences(t *testing.T) {
	vars := map[string]string{"cmd": "make"}
	input := "First {{cmd}} then {{cmd}} again"
	got := substituteVars(input, vars)
	want := "First make then make again"
	if got != want {
		t.Errorf("substituteVars = %q, want %q", got, want)
	}
}
