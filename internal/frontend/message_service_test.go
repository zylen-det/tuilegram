package frontend

import (
	"testing"

	"github.com/zylen-det/tuilegram/internal/domain"
	"github.com/zylen-det/tuilegram/internal/telegram"
)

func TestMessageContentUpdateSynchronizesServiceFlag(t *testing.T) {
	state := InitialState()
	state.Messages[9] = []domain.Message{{ID: 1, ChatID: 9, Kind: domain.MessageUnsupported}}
	for _, kind := range []domain.MessageKind{domain.MessageService, domain.MessageText} {
		updateState(&state, TelegramEvent{Value: telegram.MessageContentUpdated{ChatID: 9, MessageID: 1, Kind: kind, Text: "updated"}})
		message := state.Messages[9][0]
		if message.Service != (kind == domain.MessageService) || message.Kind != kind || message.Text != "updated" {
			t.Fatalf("content update left inconsistent message: %+v", message)
		}
	}
}
