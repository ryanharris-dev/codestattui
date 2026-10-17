package task

import "testing"

func TestParsePR(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/cli/cli/pull/12/files": "cli/cli#12",
		"cli/cli#12": "cli/cli#12",
	} {
		got, err := ParsePR(in)
		if err != nil || got != want {
			t.Errorf("ParsePR(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	if _, err := ParsePR("nope"); err == nil {
		t.Error("expected error")
	}
}

func TestFindJiraKey(t *testing.T) {
	if got := FindJiraKey("feature/abc-123-do-thing"); got != "ABC-123" {
		t.Errorf("got %q", got)
	}
}
