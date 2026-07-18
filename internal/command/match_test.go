package command

import (
	"github.com/go-telegram/bot/models"
	"testing"
)

func TestMatches(t *testing.T) {
	tests := []struct {
		text, name, username string
		want                 bool
	}{
		{"/help", "help", "SupportBot", true},
		{"/help@supportbot", "help", "SupportBot", true},
		{"/HELP@SupportBot arg", "help", "supportbot", true},
		{"/help@OtherBot", "help", "SupportBot", false},
		{"help", "help", "SupportBot", false},
	}
	for _, test := range tests {
		update := &models.Update{Message: &models.Message{Text: test.text}}
		if got := Matches(update, test.name, test.username); got != test.want {
			t.Fatalf("Matches(%q)=%v want %v", test.text, got, test.want)
		}
	}
}
