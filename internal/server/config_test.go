package server

import (
	"slices"
	"testing"
)

func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":                       nil,
		"  ":                     nil,
		"one":                    {"one"},
		"one,two":                {"one", "two"},
		" one , two ,, three , ": {"one", "two", "three"},
	}
	for in, want := range cases {
		if got := splitList(in); !slices.Equal(got, want) {
			t.Errorf("splitList(%q) = %v, want %v", in, got, want)
		}
	}
}
