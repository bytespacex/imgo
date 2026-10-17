//go:build gui

package main

import (
	"reflect"
	"testing"
)

// TestListMatches pins the file-list filter: an empty filter keeps every row in
// order; otherwise base names are matched case-insensitively as a substring, so
// a query finds a name wherever the text appears.
func TestListMatches(t *testing.T) {
	v := &viewer{files: []string{
		"/d/Cat.png", "/d/dog.JPG", "/d/beach-cathedral.webp", "/d/notes.txt",
	}}
	cases := []struct {
		filter string
		want   []int
	}{
		{"", []int{0, 1, 2, 3}},
		{"cat", []int{0, 2}}, // substring, case-insensitive: Cat and cathedral
		{"DOG", []int{1}},
		{"jpg", []int{1}},
		{".png", []int{0}},
		{"zzz", nil},
	}
	for _, c := range cases {
		v.listFilter = c.filter
		got := v.listMatches()
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("listMatches(%q) = %v, want %v", c.filter, got, c.want)
		}
	}
}
