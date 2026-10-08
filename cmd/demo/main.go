// Command demo runs the production frontend against an in-memory Telegram fake.
// It never resolves app credentials, opens a TDLib client, or reads the normal
// application's config, database, or session.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"image/color"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/blacktop/go-termimg"
	"github.com/zylen-det/tuilegram/internal/auth"
	"github.com/zylen-det/tuilegram/internal/config"
	"github.com/zylen-det/tuilegram/internal/domain"
	"github.com/zylen-det/tuilegram/internal/frontend"
	"github.com/zylen-det/tuilegram/internal/media/avatar"
	"github.com/zylen-det/tuilegram/internal/media/kitty"
	"github.com/zylen-det/tuilegram/internal/media/pixel"
	"github.com/zylen-det/tuilegram/internal/media/thumbnail"
	"github.com/zylen-det/tuilegram/internal/platform"
	"github.com/zylen-det/tuilegram/internal/telegram"
)

// A separate command keeps the installed client and its CLI unchanged.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo could not run:", err)
		os.Exit(1)
	}
}

type noAccountResolver struct{}

func (noAccountResolver) Resolve(ctx context.Context) (config.Runtime, error) {
	return config.Runtime{}, ctx.Err()
}

func run() (resultErr error) {
	root, err := os.MkdirTemp("", "tuilegram-demo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	avatarPath, err := copyDemoAsset(root, "demo-avatar.png")
	if err != nil {
		return err
	}
	photo, err := copyDemoAsset(root, "demo-photo.png")
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newDemoClient(ctx, demoData(photo, avatarPath))
	factory := func(config.Runtime, auth.Prompter) (telegram.Client, error) { return client, nil }
	cache := pixel.NewCache(filepath.Join(root, "pixels"))
	avatars := avatar.Renderer{Cache: cache, Theme: "dark", Background: color.NRGBA{R: 15, G: 17, B: 20, A: 255}}
	handler := frontend.NewHandler(ctx, noAccountResolver{}, factory, nil, avatars, platform.NewProductionClipboard())
	handler.SetNotifier(platform.NewProductionNotifier())
	protocol := termimg.Halfblocks
	if termimg.DetectKittyFromEnvironment() {
		protocol = termimg.Kitty
	}
	if protocol == termimg.Kitty {
		os.Setenv("TERMIMG_BYPASS_DETECTION", "kitty")
	} else {
		os.Setenv("TERMIMG_BYPASS_DETECTION", "halfblocks")
	}
	handler.SetThumbnailRenderer(thumbnail.NewRenderer(protocol))
	model, err := frontend.NewAppModel(frontend.InitialState(), handler)
	if err != nil {
		return err
	}
	images := kitty.NewManager(os.Stdout)
	cellPixels := frontend.LinuxCellPixelsForFD(os.Stdout.Fd())
	output := frontend.NewOutputOverlay(os.Stdout, images, cellPixels)
	model.SetOutputOverlay(output)
	program := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(os.Stdin), tea.WithOutput(output), tea.WithoutSignalHandler())

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	stopped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-stopped:
				return
			case <-signals:
				program.Send(frontend.ProcessQuitMsg{})
			}
		}
	}()
	defer func() {
		close(stopped)
		signal.Stop(signals)
		<-finished
		cancel()
		resultErr = errors.Join(resultErr, output.Clear())
	}()
	_, resultErr = program.Run()
	if errors.Is(resultErr, tea.ErrProgramKilled) && ctx.Err() != nil {
		resultErr = nil
	}
	return resultErr
}

//go:embed assets/demo-avatar.png assets/demo-photo.png
var demoAssets embed.FS

// The renderers need file paths; bundled images are copied to a temporary
// directory, which is removed when the demo exits.
func copyDemoAsset(root, name string) (string, error) {
	contents, err := demoAssets.ReadFile("assets/" + name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Chat identities and message text are synthetic. The two bundled images are
// used once each: one chat avatar and one photo message.
func demoData(photo, avatarPath string) telegram.FakeData {
	at := time.Date(2026, time.July, 20, 14, 35, 0, 0, time.Local)
	chats := []domain.Chat{
		{ID: 1, Kind: domain.ChatSupergroup, Title: "Terminal Makers", LastMessage: "The colors look great! See tuilegram on GitHub.", LastMessageAt: at.Unix(), UnreadCount: 3, UnreadMentionCount: 1, CanSend: true, CanReact: true, IsMember: true, IsPinned: true, Order: 5},
		{ID: 2, Kind: domain.ChatPrivate, Title: "Mina Chen", Avatar: domain.AvatarRef{FileID: 102, UniqueID: "demo-user-avatar"}, LastMessage: "See you tomorrow", LastMessageAt: at.Add(-time.Hour).Unix(), CanSend: true, CanReact: true, IsMember: true, Order: 4, Draft: domain.Draft{Text: "Sounds good!", Date: at.Unix()}},
		{ID: 3, Kind: domain.ChatChannel, Title: "Release Notes", LastMessage: "This channel is read-only", LastMessageAt: at.Add(-100 * time.Minute).Unix(), UnreadCount: 1, IsMember: true, Order: 3},
		{ID: 4, Kind: domain.ChatSupergroup, IsForum: true, Title: "Project Forum", LastMessage: "Let's polish the colors", LastMessageAt: at.Add(-150 * time.Minute).Unix(), CanSend: true, CanReact: true, IsMember: true, Order: 2},
		{ID: 5, Kind: domain.ChatPrivate, Title: "Demo Bot", LastMessage: "Try /help", LastMessageAt: at.Add(-4 * time.Hour).Unix(), CanSend: true, IsMember: true, Muted: true, Order: 1},
	}
	photoRef := domain.MediaFileRef{ID: 101, UniqueID: "demo-photo", LocalPath: photo, Downloaded: true}
	messages := map[domain.ChatID][]domain.Message{
		1: {
			{ID: 11, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 10}, SenderName: "Iris", SenderAccentKnown: true, SenderAccentID: 3, SentAt: at.Add(-55 * time.Minute), Kind: domain.MessageText, Text: "Welcome to Terminal Makers!"},
			{ID: 12, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 11}, SenderName: "Noah", SenderAccentKnown: true, SenderAccentID: 5, SentAt: at.Add(-44 * time.Minute), Kind: domain.MessageText, Text: "Navigate with j/k, or click a chat."},
			{ID: 13, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 13}, SenderName: "Kai", SenderAccentKnown: true, SenderAccentID: 1, SentAt: at.Add(-37 * time.Minute), Kind: domain.MessageText, Text: "I like having the chat list and conversation side by side."},
			{ID: 14, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 10}, SenderName: "Iris", SenderAccentKnown: true, SenderAccentID: 3, SentAt: at.Add(-25 * time.Minute), Kind: domain.MessageText, Text: "Try replying, reacting, or opening an image."},
			{ID: 15, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 11}, SenderName: "Noah", SenderAccentKnown: true, SenderAccentID: 5, SentAt: at.Add(-19 * time.Minute), Kind: domain.MessageText, Text: "Drafts work in this offline demo too.", HasReply: true, ReplyToMessageID: 14, Reactions: []domain.MessageReaction{{Emoji: "👍", Count: 3}}},
			{ID: 16, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 13}, SenderName: "Kai", SenderAccentKnown: true, SenderAccentID: 1, SentAt: at.Add(-13 * time.Minute), Kind: domain.MessageText, Text: "Here's a sample image:", Pinned: true},
			{ID: 17, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 12}, SenderName: "Mina", SentAt: at.Add(-12 * time.Minute), Kind: domain.MessageService, Service: true, Text: "Mina joined the group via an invite link"},
			{ID: 18, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 10}, SenderName: "Iris", SentAt: at.Add(-11 * time.Minute), Kind: domain.MessageService, Service: true, Text: "Iris added Noah, Kai"},
			{ID: 19, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 13}, SenderName: "Kai", SentAt: at.Add(-10 * time.Minute), Kind: domain.MessageService, Service: true, Text: "Kai left the group"},
			{ID: 20, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 10}, SenderName: "Iris", SentAt: at.Add(-9 * time.Minute), Kind: domain.MessageService, Service: true, Text: "Iris removed Noah"},
			{ID: 21, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 10}, SenderName: "Iris", SenderAccentKnown: true, SenderAccentID: 3, SentAt: at.Add(-6 * time.Minute), Kind: domain.MessagePhoto, Text: "A sample image", Media: domain.MessageMedia{File: photoRef, Thumbnail: photoRef, Width: 674, Height: 414, MIMEType: "image/png"}, Reactions: []domain.MessageReaction{{Emoji: "❤️", Count: 2}}},
			{ID: 22, ChatID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 11}, SenderName: "Noah", SenderAccentKnown: true, SenderAccentID: 5, SentAt: at, Kind: domain.MessageText,
				Text:     "The colors look great! See tuilegram on GitHub.",
				Entities: []domain.TextEntity{{Offset: 27, Length: 9, Kind: domain.EntityLink, Link: domain.LinkTextURL, URL: "https://github.com/zylen-det/tuilegram"}}},
		},
		2: {
			{ID: 21, ChatID: 2, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 12}, SenderName: "Mina", SenderAccentKnown: true, SenderAccentID: 2, SentAt: at.Add(-3 * time.Hour), Kind: domain.MessageText, Text: "Are we still on for tomorrow?"},
			{ID: 22, ChatID: 2, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 1}, SenderName: "You", SenderAccentKnown: true, SenderAccentID: 0, SentAt: at.Add(-2 * time.Hour), Kind: domain.MessageText, Text: "Definitely!", Outgoing: true, SendState: domain.SendSucceeded, HasReply: true, ReplyToMessageID: 21},
			{ID: 23, ChatID: 2, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 12}, SenderName: "Mina", SenderAccentKnown: true, SenderAccentID: 2, SentAt: at.Add(-time.Hour), Kind: domain.MessageText, Text: "See you tomorrow"},
		},
		3: {
			{ID: 31, ChatID: 3, Sender: domain.SenderRef{Kind: domain.SenderChat, ID: 3}, SenderName: "Release Notes", SenderAccentKnown: true, SenderAccentID: 4, SentAt: at.Add(-2 * time.Hour), Kind: domain.MessageText, Text: "New build is out. Try the demo!"},
			{ID: 32, ChatID: 3, Sender: domain.SenderRef{Kind: domain.SenderChat, ID: 3}, SenderName: "Release Notes", SenderAccentKnown: true, SenderAccentID: 4, SentAt: at.Add(-100 * time.Minute), Kind: domain.MessageText, Text: "This channel is read-only, just like a real announcement channel."},
		},
		4: {
			{ID: 41, ChatID: 4, TopicID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 10}, SenderName: "Iris", SenderAccentKnown: true, SenderAccentID: 3, SentAt: at.Add(-3 * time.Hour), Kind: domain.MessageText, Text: "What should we make next?"},
			{ID: 42, ChatID: 4, TopicID: 1, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 13}, SenderName: "Kai", SenderAccentKnown: true, SenderAccentID: 1, SentAt: at.Add(-160 * time.Minute), Kind: domain.MessageText, Text: "An even better message search."},
			{ID: 43, ChatID: 4, TopicID: 2, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 11}, SenderName: "Noah", SenderAccentKnown: true, SenderAccentID: 5, SentAt: at.Add(-150 * time.Minute), Kind: domain.MessageText, Text: "Let's polish the colors."},
		},
		5: {
			{ID: 51, ChatID: 5, Sender: domain.SenderRef{Kind: domain.SenderUser, ID: 14}, SenderName: "Demo Bot", SenderAccentKnown: true, SenderAccentID: 6, SentAt: at.Add(-4 * time.Hour), Kind: domain.MessageText, Text: "Hi! Try /help to see a bot-command suggestion."},
		},
	}
	properties := make(map[telegram.MessageIdentity]domain.MessageCapabilities)
	for id, entries := range messages {
		for _, message := range entries {
			if message.Service {
				properties[telegram.MessageIdentity{ChatID: id, MessageID: message.ID}] = message.Capabilities()
				continue
			}
			properties[telegram.MessageIdentity{ChatID: id, MessageID: message.ID}] = domain.MessageCapabilities{Copy: true, Reply: id != 3, Forward: true, Edit: message.Outgoing, Pin: id == 1, DeleteForSelf: id != 3, DeleteForAll: message.Outgoing}
		}
	}
	return telegram.FakeData{
		Chats: chats, Messages: messages, MessageProperties: properties,
		Users: map[domain.UserID]domain.User{
			1:  {ID: 1, Name: "You", IsCurrent: true, AccentColorID: 0},
			10: {ID: 10, Name: "Iris", AccentColorID: 3}, 11: {ID: 11, Name: "Noah", AccentColorID: 5},
			12: {ID: 12, Name: "Mina Chen", Username: "mina_demo", AccentColorID: 2},
			13: {ID: 13, Name: "Kai", AccentColorID: 1}, 14: {ID: 14, Name: "Demo Bot", AccentColorID: 6},
		},
		Members: map[domain.ChatID][]domain.ChatMember{
			1: {{User: domain.User{ID: 10, Name: "Iris", AccentColorID: 3}}, {User: domain.User{ID: 11, Name: "Noah", AccentColorID: 5}}, {User: domain.User{ID: 13, Name: "Kai", AccentColorID: 1}}},
		},
		Topics: map[domain.ChatID][]domain.ForumTopic{
			4: {
				{ID: 1, ChatID: 4, Name: "General", IsGeneral: true, LastMessage: "An even better message search", LastMessageAt: at.Add(-160 * time.Minute).Unix(), Order: 2},
				{ID: 2, ChatID: 4, Name: "Design", LastMessage: "Let's polish the colors", LastMessageAt: at.Add(-150 * time.Minute).Unix(), Order: 1},
			},
		},
		Avatars:    map[string]string{"demo-user-avatar": avatarPath},
		MediaFiles: map[int32]string{101: photo},
		BotCommands: map[domain.ChatID][]domain.BotCommand{
			5: {{Name: "help", Description: "Show available commands"}, {Name: "status", Description: "Show demo status"}},
		},
	}
}
