package main

import (
	"slices"
	"testing"
)

func TestParseKeyModels(t *testing.T) {
	got, err := parseKeyModels(" deepseek/*, ,deepseek-* ")
	if err != nil || !slices.Equal(got, []string{"deepseek/*", "deepseek-*"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err = parseKeyModels("all"); err != nil || got != nil {
		t.Fatalf("all: got %q, %v", got, err)
	}
	for _, bad := range []string{"", " , ", "deepseek/["} {
		if _, err := parseKeyModels(bad); err == nil {
			t.Errorf("%q: want an error, an empty list would lift the limit", bad)
		}
	}
}
