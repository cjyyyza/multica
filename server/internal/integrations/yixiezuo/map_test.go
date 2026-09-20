package yixiezuo

import "testing"

func TestIncomingPriority(t *testing.T) {
	if MapIncomingPriority("高", "medium") != "high" {
		t.Fatal("priority mapping")
	}
	if MapIncomingPriority("P5", "medium") != "medium" {
		t.Fatal("unknown source priority must use the local default")
	}
}
