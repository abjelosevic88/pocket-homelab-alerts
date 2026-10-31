package server

import "testing"

func TestDueText(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-31T09:30:00": "Due Sat 31 Oct, 09:30",
		"2026-11-02":          "Due Mon 2 Nov",
		"":                    "",
		"soon":                "",
	} {
		if got := dueText(in); got != want {
			t.Errorf("dueText(%q) = %q, want %q", in, got, want)
		}
	}
	if a, tp, ok := topicOf("tasks.reminder"); !ok || a.slug != "tasks" || tp.title != "Todo due" {
		t.Fatalf("tasks.reminder is not an alert topic: %v %v %v", a.slug, tp.title, ok)
	}
}
