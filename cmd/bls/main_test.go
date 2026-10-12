package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLanguageFromArgs(t *testing.T) {
	tests := []struct {
		args []string
		want string
		rest int
	}{
		{[]string{"/usr/bin/bls", "yaml", "--stdio"}, "yaml", 1},
		{[]string{"/home/me/.local/bin/yaml-ls", "--stdio"}, "yaml", 1},
		{[]string{"bls-bash"}, "bash", 0},
		{[]string{"C:/Users/me/.local/bin/bls.exe", "yaml"}, "yaml", 0},
		{[]string{"C:/Users/me/bin/bls-yaml.exe"}, "yaml", 0},
		{[]string{"C:/Users/me/bin/yaml-ls.exe"}, "yaml", 0},
		{[]string{"bls"}, "", 0},
	}
	for _, tt := range tests {
		got, rest := languageFromArgs(tt.args)
		if got != tt.want || len(rest) != tt.rest {
			t.Errorf("languageFromArgs(%v) = %q, %v", tt.args, got, rest)
		}
	}
}

func TestLanguageFromName(t *testing.T) {
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{"bls", "", false},
		{"bls.exe", "", false},
		{"bls-yaml", "yaml", true},
		{"bls-yaml.exe", "yaml", true},
		{"yaml-ls", "yaml", true},
		{"yaml-ls.exe", "yaml", true},
		{"yaml-language-server", "", false},
	}
	for _, tt := range tests {
		got, ok := languageFromName(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("languageFromName(%q) = %q, %v", tt.name, got, ok)
		}
	}
}

func TestVersionAndUnknown(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bls", "version"}, nil, &out, &errOut); code != 0 ||
		!strings.HasPrefix(out.String(), "bls ") {
		t.Fatalf("version: code %d out %q", code, out.String())
	}
	if code := run([]string{"bls", "nope"}, nil, &out, &errOut); code != 2 ||
		!strings.Contains(errOut.String(), "unknown language") {
		t.Fatalf("unknown: code %d err %q", code, errOut.String())
	}
}
