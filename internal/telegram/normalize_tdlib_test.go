//go:build tdlib

package telegram

import (
	"reflect"
	"strings"
	"testing"
	"time"

	td "github.com/zelenin/go-tdlib/client"
	"github.com/zylen-det/tuilegram/internal/domain"
)

func TestNormalizerMembershipServiceMessages(t *testing.T) {
	n := newNormalizer()
	n.user(&td.User{Id: 41, FirstName: "Alice"})
	n.user(&td.User{Id: 42, FirstName: "Bob"})
	n.user(&td.User{Id: 43, FirstName: "Carol"})
	tests := []struct {
		name    string
		sender  td.MessageSender
		content td.MessageContent
		want    string
	}{
		{"self join", &td.MessageSenderUser{UserId: 41}, &td.MessageChatAddMembers{MemberUserIds: []int64{41}}, "Alice joined the group"},
		{"invited member", &td.MessageSenderUser{UserId: 41}, &td.MessageChatAddMembers{MemberUserIds: []int64{42}}, "Alice added Bob"},
		{"multiple members", &td.MessageSenderUser{UserId: 41}, &td.MessageChatAddMembers{MemberUserIds: []int64{43, 42}}, "Alice added Carol, Bob"},
		{"invite link", &td.MessageSenderUser{UserId: 41}, &td.MessageChatJoinByLink{}, "Alice joined the group via an invite link"},
		{"approved request", &td.MessageSenderUser{UserId: 41}, &td.MessageChatJoinByRequest{}, "Alice joined the group after approval"},
		{"self leave", &td.MessageSenderUser{UserId: 41}, &td.MessageChatDeleteMember{UserId: 41}, "Alice left the group"},
		{"removed member", &td.MessageSenderUser{UserId: 41}, &td.MessageChatDeleteMember{UserId: 42}, "Alice removed Bob"},
		{"uncached member", &td.MessageSenderUser{UserId: 41}, &td.MessageChatAddMembers{MemberUserIds: []int64{99}}, "Alice added User 99"},
		{"uncached actor", &td.MessageSenderUser{UserId: 99}, &td.MessageChatAddMembers{MemberUserIds: []int64{99}}, "User 99 joined the group"},
		{"empty members", &td.MessageSenderUser{UserId: 41}, &td.MessageChatAddMembers{}, "Alice added members"},
		{"chat actor", &td.MessageSenderChat{ChatId: 41}, &td.MessageChatDeleteMember{UserId: 41}, "Unknown removed Alice"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := n.message(&td.Message{Id: 50, ChatId: 10, SenderId: test.sender, Content: test.content})
			if message.Kind != domain.MessageService || !message.Service || message.DisplayText() != test.want {
				t.Fatalf("service message = %+v, want %q", message, test.want)
			}
		})
	}
}

func TestNormalizerMembershipServiceUpdateAndPreview(t *testing.T) {
	n := newNormalizer()
	n.user(&td.User{Id: 41, FirstName: "Alice"})
	message := &td.Message{Id: 50, ChatId: 10, SenderId: &td.MessageSenderUser{UserId: 41}, Content: &td.MessageChatJoinByLink{}}
	got := singleUpdate(t, n.update(&td.UpdateNewMessage{Message: message})).(MessageUpserted).Message
	if !got.Service || got.Text != "Alice joined the group via an invite link" {
		t.Fatalf("live service message = %+v", got)
	}
	chat := n.chat(&td.Chat{Id: 10, Type: &td.ChatTypeBasicGroup{BasicGroupId: 1}, LastMessage: message})
	if chat.LastMessage != got.Text {
		t.Fatalf("chat preview = %q, want %q", chat.LastMessage, got.Text)
	}
	updated := singleUpdate(t, n.update(&td.UpdateMessageContent{ChatId: 10, MessageId: 50, NewContent: &td.MessageChatAddMembers{MemberUserIds: []int64{41}}})).(MessageContentUpdated)
	if updated.Kind != domain.MessageService || updated.Text != "Unknown added Alice" {
		t.Fatalf("service content update = %+v", updated)
	}
}

func TestNormalizerMapsConnectionAndClosedUpdates(t *testing.T) {
	tests := []struct {
		state td.ConnectionState
		want  domain.ConnectionState
	}{
		{state: &td.ConnectionStateReady{}, want: domain.ConnectionOnline},
		{state: &td.ConnectionStateWaitingForNetwork{}, want: domain.ConnectionOffline},
		{state: &td.ConnectionStateConnecting{}, want: domain.ConnectionReconnecting},
		{state: &td.ConnectionStateConnectingToProxy{}, want: domain.ConnectionReconnecting},
		{state: &td.ConnectionStateUpdating{}, want: domain.ConnectionReconnecting},
	}

	n := newNormalizer()
	for _, test := range tests {
		updates := n.update(&td.UpdateConnectionState{State: test.state})
		got, ok := singleUpdate(t, updates).(ConnectionChanged)
		if !ok || got.State != test.want {
			t.Fatalf("connection update did not map to state %d", test.want)
		}
	}
	if _, ok := singleUpdate(t, n.update(&td.UpdateAuthorizationState{AuthorizationState: &td.AuthorizationStateClosed{}})).(Closed); !ok {
		t.Fatal("closed authorization state did not normalize to Closed")
	}
}

func TestNormalizerSenderAccentColors(t *testing.T) {
	n := newNormalizer()
	user := singleUpdate(t, n.update(&td.UpdateUser{User: &td.User{Id: 41, FirstName: "Ada", AccentColorId: 3}})).(UserUpserted)
	if user.User.AccentColorID != 3 {
		t.Fatalf("user accent ID = %d", user.User.AccentColorID)
	}
	message := n.message(tdTextMessage(1, 90, 41, 100, "Hi"))
	if !message.SenderAccentKnown || message.SenderAccentID != 3 {
		t.Fatalf("user message accent = %d, known %v", message.SenderAccentID, message.SenderAccentKnown)
	}
	chat := singleChat(t, n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 90, Type: &td.ChatTypeSupergroup{SupergroupId: 9}, Title: "Channel", AccentColorId: 6}}))
	if chat.AccentColorID != 6 {
		t.Fatalf("chat accent ID = %d", chat.AccentColorID)
	}
	chat = singleChat(t, n.update(&td.UpdateChatAccentColors{ChatId: 90, AccentColorId: 8}))
	if chat.AccentColorID != 8 {
		t.Fatalf("changed chat accent ID = %d", chat.AccentColorID)
	}
	chatMessage := tdTextMessage(2, 90, 41, 100, "Channel post")
	chatMessage.SenderId = &td.MessageSenderChat{ChatId: 90}
	message = n.message(chatMessage)
	if !message.SenderAccentKnown || message.SenderAccentID != 8 {
		t.Fatalf("chat message accent = %d, known %v", message.SenderAccentID, message.SenderAccentKnown)
	}
	colors := singleUpdate(t, n.update(&td.UpdateAccentColors{Colors: []*td.AccentColor{{Id: 8, DarkThemeColors: []int32{0x123456}}, {Id: 9}}})).(AccentColorsChanged)
	if !reflect.DeepEqual(colors.DarkRGB, map[int32]uint32{8: 0x123456}) {
		t.Fatalf("custom colors = %v", colors.DarkRGB)
	}
}

func TestNormalizerCachesFullUserIdentity(t *testing.T) {
	n := newNormalizer()
	updates := n.update(&td.UpdateUser{User: &td.User{
		Id:        41,
		FirstName: "Ada",
		LastName:  "Lovelace",
		Usernames: &td.Usernames{ActiveUsernames: []string{"ada"}},
		ProfilePhoto: &td.ProfilePhoto{
			Small: tdFile(11, "avatar-small"),
			Big:   tdFile(12, "avatar-original"),
		},
	}})
	got, ok := singleUpdate(t, updates).(UserUpserted)
	if !ok {
		t.Fatal("user update did not normalize to UserUpserted")
	}
	if got.User.ID != 41 || got.User.Name != "Ada Lovelace" || got.User.Username != "ada" {
		t.Fatal("user names were not normalized")
	}
	if got.User.Avatar.FileID != 11 || got.User.Avatar.UniqueID != "avatar-small" || got.User.Avatar.OriginalFileID != 12 || got.User.Avatar.OriginalUniqueID != "avatar-original" {
		t.Fatal("user avatar identities were not normalized")
	}
}

func TestNormalizerUserUpdateReemitsPrivateChatsInIDOrder(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 5, FirstName: "Before", Usernames: &td.Usernames{ActiveUsernames: []string{"before"}}}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 20, Type: &td.ChatTypePrivate{UserId: 5}, Title: "Private 20"}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 10, Type: &td.ChatTypePrivate{UserId: 5}, Title: "Private 10"}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 15, Type: &td.ChatTypeBasicGroup{BasicGroupId: 5}, Title: "Unrelated group"}})

	updates := n.update(&td.UpdateUser{User: &td.User{Id: 5, FirstName: "After", Usernames: &td.Usernames{ActiveUsernames: []string{"after"}}}})
	if len(updates) != 3 {
		t.Fatalf("user update emitted %d updates, want user plus 2 private chats", len(updates))
	}
	user, userOK := updates[0].(UserUpserted)
	first, firstOK := updates[1].(ChatUpserted)
	second, secondOK := updates[2].(ChatUpserted)
	if !userOK || user.User.Username != "after" || !firstOK || !secondOK {
		t.Fatal("user update did not emit typed user and chat updates")
	}
	if first.Chat.ID != 10 || second.Chat.ID != 20 || first.Chat.Username != "after" || second.Chat.Username != "after" {
		t.Fatal("private chat username updates were not emitted in deterministic chat ID order")
	}
}

func TestNormalizerAppliesChatMetadataAndFieldUpdates(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateSupergroup{Supergroup: &td.Supergroup{
		Id:        77,
		Usernames: &td.Usernames{ActiveUsernames: []string{"news"}},
		IsChannel: true,
	}})
	chat := &td.Chat{
		Id:                   900,
		Type:                 &td.ChatTypeSupergroup{SupergroupId: 77, IsChannel: true},
		Title:                "Original",
		Photo:                &td.ChatPhotoInfo{Small: tdFile(21, "chat-small"), Big: tdFile(22, "chat-original")},
		Permissions:          &td.ChatPermissions{CanSendBasicMessages: true},
		NotificationSettings: &td.ChatNotificationSettings{MuteFor: 60},
		UnreadCount:          4,
		UnreadMentionCount:   2,
		LastMessage:          tdTextMessage(8, 900, 41, 123, "preview"),
		Positions: []*td.ChatPosition{
			{List: &td.ChatListArchive{}, Order: 999},
			{List: &td.ChatListMain{}, Order: 0},
			{List: &td.ChatListMain{}, Order: 700},
		},
	}
	got := singleChat(t, n.update(&td.UpdateNewChat{Chat: chat}))
	if got.ID != 900 || got.Kind != domain.ChatChannel || got.Title != "Original" || got.Username != "news" || got.Order != 700 {
		t.Fatal("new chat identity or main-list metadata was not normalized")
	}
	if got.Avatar != (domain.AvatarRef{FileID: 21, UniqueID: "chat-small", OriginalFileID: 22, OriginalUniqueID: "chat-original"}) || got.LastMessage != "preview" || got.LastMessageAt != 123 || got.UnreadCount != 4 || got.UnreadMentionCount != 2 || !got.Muted || !got.CanSend {
		t.Fatal("new chat fields were not normalized")
	}

	got = singleChat(t, n.update(&td.UpdateChatTitle{ChatId: 900, Title: "Renamed"}))
	if got.Title != "Renamed" {
		t.Fatal("chat title update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatPhoto{ChatId: 900, Photo: &td.ChatPhotoInfo{Small: tdFile(31, "new-small"), Big: tdFile(32, "new-original")}}))
	if got.Avatar != (domain.AvatarRef{FileID: 31, UniqueID: "new-small", OriginalFileID: 32, OriginalUniqueID: "new-original"}) {
		t.Fatal("chat photo update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatReadInbox{ChatId: 900, UnreadCount: 2}))
	if got.UnreadCount != 2 {
		t.Fatal("chat read update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatUnreadMentionCount{ChatId: 900, UnreadMentionCount: 1}))
	if got.UnreadMentionCount != 1 {
		t.Fatal("chat unread mention update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatNotificationSettings{ChatId: 900, NotificationSettings: &td.ChatNotificationSettings{MuteFor: 0}}))
	if got.Muted {
		t.Fatal("chat notification update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatPermissions{ChatId: 900, Permissions: &td.ChatPermissions{CanSendBasicMessages: false}}))
	if got.CanSend {
		t.Fatal("chat permissions update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatLastMessage{
		ChatId:      900,
		LastMessage: tdMediaMessage(9, 900, 41, 456, &td.MessagePhoto{}),
		Positions:   []*td.ChatPosition{{List: &td.ChatListMain{}, Order: 800}},
	}))
	if got.LastMessage != "[Photo]" || got.LastMessageAt != 456 || got.Order != 800 {
		t.Fatal("last-message update was not applied")
	}
	got = singleChat(t, n.update(&td.UpdateChatPosition{ChatId: 900, Position: &td.ChatPosition{List: &td.ChatListMain{}, Order: 0}}))
	if got.Order != 0 {
		t.Fatal("zero main-list position did not remove chat from visible ordering")
	}
}

func TestNormalizerTracksChatChangeInfoRightAcrossRoleAndPermissionUpdates(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 7, Status: &td.ChatMemberStatusMember{}}})
	chat := singleChat(t, n.update(&td.UpdateNewChat{Chat: &td.Chat{
		Id: 70, Type: &td.ChatTypeBasicGroup{BasicGroupId: 7}, Permissions: &td.ChatPermissions{CanChangeInfo: true},
	}}))
	if !chat.CanChangeInfo {
		t.Fatal("member right from default permissions missing")
	}
	chat = singleChat(t, n.update(&td.UpdateChatPermissions{ChatId: 70, Permissions: &td.ChatPermissions{}}))
	if chat.CanChangeInfo {
		t.Fatal("revoked default right remained enabled")
	}
	chat = singleChat(t, n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 7, Status: &td.ChatMemberStatusAdministrator{Rights: &td.ChatAdministratorRights{CanChangeInfo: true}}}}))
	if !chat.CanChangeInfo {
		t.Fatal("administrator change-info right missing")
	}
	chat = singleChat(t, n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 7, Status: &td.ChatMemberStatusMember{}}}))
	if chat.CanChangeInfo {
		t.Fatal("demoted administrator retained old right")
	}
}

func TestNormalizerMapsTextDraftSnapshotsAndUpdates(t *testing.T) {
	n := newNormalizer()
	chat := &td.Chat{
		Id:   900,
		Type: &td.ChatTypePrivate{UserId: 9},
		DraftMessage: &td.DraftMessage{
			Date:             123,
			ReplyTo:          &td.InputMessageReplyToMessage{MessageId: 44},
			InputMessageText: &td.InputMessageText{Text: &td.FormattedText{Text: "exact draft"}},
		},
		Positions: []*td.ChatPosition{{List: &td.ChatListMain{}, Order: 700}},
	}
	got := singleChat(t, n.update(&td.UpdateNewChat{Chat: chat}))
	want := domain.Draft{Text: "exact draft", ReplyToMessageID: 44, Date: 123}
	if got.Draft != want {
		t.Fatalf("snapshot draft = %#v, want %#v", got.Draft, want)
	}

	updates := n.update(&td.UpdateChatDraftMessage{
		ChatId: 900,
		DraftMessage: &td.DraftMessage{
			Date:             456,
			InputMessageText: &td.InputMessageText{Text: &td.FormattedText{Text: "remote draft"}},
		},
		Positions: []*td.ChatPosition{{List: &td.ChatListMain{}, Order: 800}},
	})
	if len(updates) != 2 {
		t.Fatalf("draft update count = %d, want 2", len(updates))
	}
	chatUpdate, chatOK := updates[0].(ChatUpserted)
	draftUpdate, draftOK := updates[1].(DraftChanged)
	want = domain.Draft{Text: "remote draft", Date: 456}
	if !chatOK || chatUpdate.Chat.Order != 800 || chatUpdate.Chat.Draft != want {
		t.Fatalf("chat draft update = %#v", updates[0])
	}
	if !draftOK || draftUpdate.ChatID != 900 || draftUpdate.Draft != want {
		t.Fatalf("domain draft update = %#v", updates[1])
	}

	updates = n.update(&td.UpdateChatDraftMessage{ChatId: 900})
	if changed, ok := updates[len(updates)-1].(DraftChanged); !ok || changed.Draft != (domain.Draft{}) {
		t.Fatalf("cleared draft update = %#v", updates)
	}
}

func TestNormalizerIgnoresUnsupportedAndInvalidDraftContent(t *testing.T) {
	for _, test := range []struct {
		name  string
		draft *td.DraftMessage
	}{
		{name: "nil"},
		{name: "unsupported", draft: &td.DraftMessage{Date: 1, InputMessageText: &td.InputMessageVideoNote{}}},
		{name: "invalid reply", draft: &td.DraftMessage{Date: 2, ReplyTo: &td.InputMessageReplyToMessage{MessageId: -1}, InputMessageText: &td.InputMessageText{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := draftMessage(test.draft)
			if got != (domain.Draft{}) {
				t.Fatalf("unsupported draft = %#v", got)
			}
		})
	}
}

func TestNormalizerUsesEffectiveScopeMuteAndReemitsDeterministically(t *testing.T) {
	n := newNormalizer()
	privateScope := &td.ScopeNotificationSettings{MuteFor: 30}
	groupScope := &td.ScopeNotificationSettings{MuteFor: 60}
	channelScope := &td.ScopeNotificationSettings{MuteFor: 0}
	n.update(&td.UpdateScopeNotificationSettings{Scope: &td.NotificationSettingsScopePrivateChats{}, NotificationSettings: privateScope})
	n.update(&td.UpdateScopeNotificationSettings{Scope: &td.NotificationSettingsScopeGroupChats{}, NotificationSettings: groupScope})
	n.update(&td.UpdateScopeNotificationSettings{Scope: &td.NotificationSettingsScopeChannelChats{}, NotificationSettings: channelScope})
	privateScope.MuteFor = 0
	groupScope.MuteFor = 0

	privateSettings := &td.ChatNotificationSettings{UseDefaultMuteFor: true}
	groupSettings20 := &td.ChatNotificationSettings{UseDefaultMuteFor: true}
	groupSettings10 := &td.ChatNotificationSettings{UseDefaultMuteFor: true}
	privateChat := singleChat(t, n.update(&td.UpdateNewChat{Chat: notificationChat(30, &td.ChatTypePrivate{UserId: 1}, privateSettings)}))
	groupChat20 := singleChat(t, n.update(&td.UpdateNewChat{Chat: notificationChat(20, &td.ChatTypeBasicGroup{BasicGroupId: 2}, groupSettings20)}))
	groupChat10 := singleChat(t, n.update(&td.UpdateNewChat{Chat: notificationChat(10, &td.ChatTypeSupergroup{SupergroupId: 3}, groupSettings10)}))
	channelChat := singleChat(t, n.update(&td.UpdateNewChat{Chat: notificationChat(40, &td.ChatTypeSupergroup{SupergroupId: 4, IsChannel: true}, &td.ChatNotificationSettings{UseDefaultMuteFor: true})}))
	if !privateChat.Muted || !groupChat20.Muted || !groupChat10.Muted || channelChat.Muted {
		t.Fatal("chats did not inherit mute from their matching notification scope")
	}

	groupSettings20.UseDefaultMuteFor = false
	groupSettings20.MuteFor = 999
	groupSettings10.UseDefaultMuteFor = false
	groupSettings10.MuteFor = 999
	unmutedScope := &td.ScopeNotificationSettings{MuteFor: 0}
	updates := n.update(&td.UpdateScopeNotificationSettings{Scope: &td.NotificationSettingsScopeGroupChats{}, NotificationSettings: unmutedScope})
	unmutedScope.MuteFor = 999
	if len(updates) != 2 {
		t.Fatalf("group scope update emitted %d chat updates, want 2", len(updates))
	}
	first, firstOK := updates[0].(ChatUpserted)
	second, secondOK := updates[1].(ChatUpserted)
	if !firstOK || !secondOK || first.Chat.ID != 10 || second.Chat.ID != 20 || first.Chat.Muted || second.Chat.Muted {
		t.Fatal("scope update did not deterministically refresh inherited chat mute")
	}

	newGroup := singleChat(t, n.update(&td.UpdateNewChat{Chat: notificationChat(50, &td.ChatTypeBasicGroup{BasicGroupId: 5}, &td.ChatNotificationSettings{UseDefaultMuteFor: true})}))
	if newGroup.Muted {
		t.Fatal("scope notification settings retained caller-owned pointer data")
	}
}

func TestNormalizerRecomputesInheritedMuteWhenSupergroupBecomesChannel(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateScopeNotificationSettings{Scope: &td.NotificationSettingsScopeGroupChats{}, NotificationSettings: &td.ScopeNotificationSettings{MuteFor: 0}})
	n.update(&td.UpdateScopeNotificationSettings{Scope: &td.NotificationSettingsScopeChannelChats{}, NotificationSettings: &td.ScopeNotificationSettings{MuteFor: 300}})
	chat := singleChat(t, n.update(&td.UpdateNewChat{Chat: notificationChat(70, &td.ChatTypeSupergroup{SupergroupId: 7}, &td.ChatNotificationSettings{UseDefaultMuteFor: true})}))
	if chat.Kind != domain.ChatSupergroup || chat.Muted {
		t.Fatal("supergroup did not initially use group notification settings")
	}

	chat = singleChat(t, n.update(&td.UpdateSupergroup{Supergroup: &td.Supergroup{Id: 7, IsChannel: true}}))
	if chat.Kind != domain.ChatChannel || !chat.Muted {
		t.Fatal("channel metadata change did not recompute inherited notification settings")
	}
}

func TestNormalizerCachesNewChatBeforeNormalizingItsChatSender(t *testing.T) {
	n := newNormalizer()
	chat := &td.Chat{
		Id:    71,
		Type:  &td.ChatTypeSupergroup{SupergroupId: 9, IsChannel: true},
		Title: "Release channel",
		Photo: &td.ChatPhotoInfo{Small: tdFile(41, "release-small"), Big: tdFile(42, "release-original")},
		LastMessage: &td.Message{
			Id:       700,
			ChatId:   71,
			SenderId: &td.MessageSenderChat{ChatId: 71},
			Date:     456,
			Content:  &td.MessageText{Text: &td.FormattedText{Text: "announcement"}},
		},
	}

	n.mu.Lock()
	normalizedChat, message := n.chatWithLastMessageLocked(chat)
	n.mu.Unlock()
	if normalizedChat.ID != 71 || n.chats[71] != normalizedChat {
		t.Fatal("new chat was not cached by the normalization path")
	}
	if message.SenderName != "Release channel" || message.SenderAvatar != (domain.AvatarRef{FileID: 41, UniqueID: "release-small", OriginalFileID: 42, OriginalUniqueID: "release-original"}) {
		t.Fatal("new chat was not cached before normalizing its chat sender")
	}
}

func TestNormalizerRefreshesCacheFromNewerSnapshotUntilAnUpdateIsAuthoritative(t *testing.T) {
	n := newNormalizer()
	first := n.chat(&td.Chat{Id: 80, Type: &td.ChatTypePrivate{UserId: 8}, Title: "first snapshot"})
	second := n.chat(&td.Chat{Id: 80, Type: &td.ChatTypePrivate{UserId: 8}, Title: "second snapshot"})
	if first.Title != "first snapshot" || second.Title != "second snapshot" {
		t.Fatal("non-authoritative GetChat snapshots did not refresh the cache")
	}
}

func TestNormalizerCachesBasicAndSupergroupMetadata(t *testing.T) {
	n := newNormalizer()
	basicGroup := &td.BasicGroup{Id: 5, IsActive: true}
	if updates := n.update(&td.UpdateBasicGroup{BasicGroup: basicGroup}); len(updates) != 0 {
		t.Fatal("metadata without a cached chat emitted an update")
	}
	basicGroup.IsActive = false
	if n.basicGroups[5] == nil || !n.basicGroups[5].IsActive {
		t.Fatal("basic-group metadata was not cached with owned storage")
	}

	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 501, Type: &td.ChatTypeSupergroup{SupergroupId: 6}, Permissions: &td.ChatPermissions{CanSendBasicMessages: true}}})
	supergroup := &td.Supergroup{Id: 6, Usernames: &td.Usernames{ActiveUsernames: []string{"active"}}, IsChannel: true}
	got := singleChat(t, n.update(&td.UpdateSupergroup{Supergroup: supergroup}))
	supergroup.IsChannel = false
	supergroup.Usernames.ActiveUsernames[0] = "mutated"
	if got.Kind != domain.ChatChannel || got.Username != "active" {
		t.Fatal("supergroup metadata did not refresh its cached chat")
	}
	if !n.supergroups[6].IsChannel || firstUsername(n.supergroups[6].Usernames) != "active" {
		t.Fatal("supergroup metadata retained caller-owned pointer data")
	}
}

func TestNormalizerInviteLinkCapabilityTracksCurrentRole(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 501, Type: &td.ChatTypeBasicGroup{BasicGroupId: 5}}})
	admin := &td.ChatMemberStatusAdministrator{Rights: &td.ChatAdministratorRights{CanInviteUsers: true, CanRestrictMembers: true, CanPromoteMembers: true}}
	chat := singleChat(t, n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 5, Status: admin}}))
	if !chat.CanManageInviteLinks || !chat.CanRestrictMembers || !chat.CanPromoteMembers {
		t.Fatal("admin with invite right should manage links")
	}
	chat = singleChat(t, n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 5, Status: &td.ChatMemberStatusAdministrator{Rights: &td.ChatAdministratorRights{}}}}))
	if chat.CanManageInviteLinks || chat.CanRestrictMembers || chat.CanPromoteMembers {
		t.Fatal("admin without invite right should not manage links")
	}
	chat = singleChat(t, n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 5, Status: &td.ChatMemberStatusCreator{IsMember: true}}}))
	if !chat.CanManageInviteLinks || !chat.CanRestrictMembers || !chat.CanPromoteMembers {
		t.Fatal("owner should manage links")
	}
	chat = singleChat(t, n.update(&td.UpdateBasicGroup{BasicGroup: &td.BasicGroup{Id: 5, Status: &td.ChatMemberStatusMember{}}}))
	if chat.CanManageInviteLinks || chat.CanRestrictMembers || chat.CanPromoteMembers {
		t.Fatal("member should not manage links")
	}
}

func TestNormalizerMapsMessagesAndSenderKinds(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 7, FirstName: "User", ProfilePhoto: &td.ProfilePhoto{Small: tdFile(1, "user-avatar")}}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 7, Type: &td.ChatTypeBasicGroup{BasicGroupId: 1}, Title: "Same numeric ID", Photo: &td.ChatPhotoInfo{Small: tdFile(2, "chat-avatar")}}})

	userMessage := n.message(tdTextMessage(1, 100, 7, 1700000000, "hello"))
	if userMessage.Sender != (domain.SenderRef{Kind: domain.SenderUser, ID: 7}) || userMessage.SenderName != "User" || userMessage.SenderAvatar.UniqueID != "user-avatar" {
		t.Fatal("user sender was not normalized")
	}
	chatMessage := n.message(&td.Message{Id: 2, ChatId: 100, SenderId: &td.MessageSenderChat{ChatId: 7}, Date: 1700000001, Content: &td.MessageText{Text: &td.FormattedText{Text: "as chat"}}})
	if chatMessage.Sender != (domain.SenderRef{Kind: domain.SenderChat, ID: 7}) || chatMessage.SenderName != "Same numeric ID" || chatMessage.SenderAvatar.UniqueID != "chat-avatar" {
		t.Fatal("chat sender collided with same-numbered user sender")
	}
	unknown := n.message(tdTextMessage(3, 100, 999, 1700000002, "unknown"))
	if unknown.SenderName != "Unknown" || unknown.SenderAvatar != (domain.AvatarRef{}) {
		t.Fatal("unknown sender was not retained with safe fallback metadata")
	}
}

func TestNormalizerMapsMessageContentAndGroupingBoundaries(t *testing.T) {
	tests := []struct {
		content  td.MessageContent
		kind     domain.MessageKind
		text     string
		fileName string
		display  string
	}{
		{content: &td.MessageText{Text: &td.FormattedText{Text: "original"}}, kind: domain.MessageText, text: "original", display: "original"},
		{content: &td.MessagePhoto{}, kind: domain.MessagePhoto, display: "[Photo]"},
		{content: &td.MessageVideo{}, kind: domain.MessageVideo, display: "[Video]"},
		{content: &td.MessageSticker{}, kind: domain.MessageSticker, display: "[Sticker]"},
		{content: &td.MessageDocument{Document: &td.Document{FileName: "report.pdf"}}, kind: domain.MessageDocument, fileName: "report.pdf", display: "[File: report.pdf]"},
	}
	n := newNormalizer()
	for index, test := range tests {
		message := n.message(&td.Message{Id: int64(index + 1), ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10, Content: test.content})
		if message.Kind != test.kind || message.Text != test.text || message.FileName != test.fileName || message.DisplayText() != test.display {
			t.Fatalf("content case %d was not normalized", index)
		}
	}

	message := n.message(&td.Message{
		Id:          10,
		ChatId:      3,
		SenderId:    &td.MessageSenderUser{UserId: 1},
		Date:        20,
		Content:     &td.MessageText{Text: &td.FormattedText{Text: "boundary"}},
		ReplyTo:     &td.MessageReplyToMessage{MessageId: 9},
		ForwardInfo: &td.MessageForwardInfo{},
	})
	if !message.HasReply || !message.HasForward || !message.SentAt.Equal(time.Unix(20, 0)) {
		t.Fatal("reply, forward, or timestamp metadata was not normalized")
	}
}

func TestNormalizerReplyIdentityDistinguishesSameAndCrossChat(t *testing.T) {
	n := newNormalizer()
	for _, test := range []struct {
		name      string
		replyChat int64
		wantID    domain.MessageID
	}{
		{name: "omitted same chat", replyChat: 0, wantID: 9},
		{name: "explicit same chat", replyChat: 3, wantID: 9},
		{name: "cross chat", replyChat: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := n.message(&td.Message{Id: 10, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Content: &td.MessageText{Text: &td.FormattedText{}}, ReplyTo: &td.MessageReplyToMessage{ChatId: test.replyChat, MessageId: 9}})
			if !message.HasReply || message.ReplyToMessageID != test.wantID {
				t.Fatal("reply identity normalization mismatch")
			}
		})
	}
}

func TestNormalizerMapsMessageUpdatesAndSendStates(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 4, FirstName: "Sender", ProfilePhoto: &td.ProfilePhoto{Small: tdFile(61, "sender-small")}}})
	message := tdTextMessage(50, 10, 4, 100, "message")
	if got, ok := singleUpdate(t, n.update(&td.UpdateNewMessage{Message: message})).(MessageUpserted); !ok || got.Message.ID != 50 {
		t.Fatal("new message did not normalize to MessageUpserted")
	}
	if n.message(message).Edited() {
		t.Fatal("zero edit date marked an unedited message")
	}
	content, ok := singleUpdate(t, n.update(&td.UpdateMessageContent{ChatId: 10, MessageId: 50, NewContent: &td.MessageText{Text: &td.FormattedText{Text: "opaque-updated"}}})).(MessageContentUpdated)
	if !ok || content.ChatID != 10 || content.MessageID != 50 || content.Kind != domain.MessageText || content.Text != "opaque-updated" {
		t.Fatal("message content update normalization mismatch")
	}
	edited, ok := singleUpdate(t, n.update(&td.UpdateMessageEdited{ChatId: 10, MessageId: 50, EditDate: 120})).(MessageEdited)
	if !ok || edited.ChatID != 10 || edited.MessageID != 50 || !edited.EditedAt.Equal(time.Unix(120, 0)) {
		t.Fatal("message edited update normalization mismatch")
	}
	deleted, ok := singleUpdate(t, n.update(&td.UpdateDeleteMessages{ChatId: 10, MessageIds: []int64{50, 51}, FromCache: true})).(MessagesDeleted)
	if !ok || deleted.ChatID != 10 || len(deleted.MessageIDs) != 2 || deleted.MessageIDs[0] != 50 || deleted.MessageIDs[1] != 51 || !deleted.FromCache {
		t.Fatal("message deletion update normalization mismatch")
	}
	permanent, ok := singleUpdate(t, n.update(&td.UpdateDeleteMessages{ChatId: 10, MessageIds: []int64{52}, IsPermanent: true})).(MessagesDeleted)
	if !ok || permanent.FromCache {
		t.Fatal("permanent deletion carried cache flag")
	}
	pinned, ok := singleUpdate(t, n.update(&td.UpdateMessageIsPinned{ChatId: 10, MessageId: 50, IsPinned: true})).(MessagePinnedUpdated)
	if !ok || pinned.ChatID != 10 || pinned.MessageID != 50 || !pinned.Pinned {
		t.Fatal("message pin update normalization mismatch")
	}
	unpinned, ok := singleUpdate(t, n.update(&td.UpdateMessageIsPinned{ChatId: 10, MessageId: 50, IsPinned: false})).(MessagePinnedUpdated)
	if !ok || unpinned.Pinned {
		t.Fatal("message unpin update normalization mismatch")
	}
	if got, ok := singleUpdate(t, n.update(&td.UpdateMessageSendSucceeded{OldMessageId: -5, Message: message})).(MessageSendSucceeded); !ok || got.OldID != -5 || !normalizedSentMessageMatches(got.Message, domain.SendSucceeded) {
		t.Fatal("send success did not normalize its old ID and state")
	}
	failure := &td.Error{Code: 429, Message: "FLOOD_WAIT_6"}
	got, ok := singleUpdate(t, n.update(&td.UpdateMessageSendFailed{OldMessageId: -6, Message: message, Error: failure})).(MessageSendFailed)
	if !ok || got.OldID != -6 || !normalizedSentMessageMatches(got.Message, domain.SendFailed) || got.Error.Kind != domain.ErrorRateLimit || got.Error.RetryAfter != 6*time.Second || got.Message.Failure == nil {
		t.Fatal("send failure did not normalize its old ID, state, and error")
	}
}

func normalizedSentMessageMatches(message domain.Message, state domain.SendState) bool {
	return message.ID == 50 &&
		message.ChatID == 10 &&
		message.Text == "message" &&
		message.SentAt.Equal(time.Unix(100, 0)) &&
		message.Sender == (domain.SenderRef{Kind: domain.SenderUser, ID: 4}) &&
		message.SenderName == "Sender" &&
		message.SenderAvatar.UniqueID == "sender-small" &&
		message.SendState == state
}

func TestNormalizerMapsChatCanReactFromPermissions(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateSupergroup{Supergroup: &td.Supergroup{Id: 77, IsChannel: true}})
	chat := &td.Chat{
		Id:          900,
		Type:        &td.ChatTypeSupergroup{SupergroupId: 77, IsChannel: true},
		Title:       "Original",
		Permissions: &td.ChatPermissions{CanSendBasicMessages: true, CanReactToMessages: true},
	}
	got := singleChat(t, n.update(&td.UpdateNewChat{Chat: chat}))
	if !got.CanSend || !got.CanReact {
		t.Fatalf("new chat permissions = %#v", got)
	}
	got = singleChat(t, n.update(&td.UpdateChatPermissions{ChatId: 900, Permissions: &td.ChatPermissions{CanReactToMessages: false}}))
	if got.CanReact {
		t.Fatal("chat permissions update did not clear CanReact")
	}
	got = singleChat(t, n.update(&td.UpdateChatPermissions{ChatId: 900, Permissions: &td.ChatPermissions{CanReactToMessages: true}}))
	if !got.CanReact {
		t.Fatal("chat permissions update did not grant CanReact")
	}
}

func TestNormalizerMapsUpdateMessageReactionsEmojiOnly(t *testing.T) {
	n := newNormalizer()
	updates := n.update(&td.UpdateMessageReactions{
		ChatId:    900,
		MessageId: 7,
		Date:      100,
		Reactions: []*td.MessageReaction{
			{Type: &td.ReactionTypeEmoji{Emoji: "👍"}, TotalCount: 3, IsChosen: true},
			{Type: &td.ReactionTypeEmoji{Emoji: "❤️"}, TotalCount: 1, IsChosen: false},
			{Type: &td.ReactionTypeCustomEmoji{CustomEmojiId: 5}, TotalCount: 2, IsChosen: false},
			{Type: &td.ReactionTypePaid{}, TotalCount: 9, IsChosen: false},
			nil,
		},
	})
	update, ok := singleUpdate(t, updates).(MessageReactionsUpdated)
	if !ok {
		t.Fatalf("update type = %T", updates[0])
	}
	if update.ChatID != 900 || update.MessageID != 7 {
		t.Fatalf("reaction identity = (%d, %d)", update.ChatID, update.MessageID)
	}
	want := []domain.MessageReaction{
		{Emoji: "👍", Count: 3, Chosen: true},
		{Emoji: "❤️", Count: 1, Chosen: false},
	}
	if !reflect.DeepEqual(update.Reactions, want) {
		t.Fatalf("reactions = %#v, want %#v", update.Reactions, want)
	}
}

func TestNormalizerMapsReactionsOnMessageSnapshot(t *testing.T) {
	n := newNormalizer()
	message := n.message(&td.Message{
		Id:      50,
		ChatId:  10,
		Date:    100,
		Content: &td.MessageText{Text: &td.FormattedText{Text: "opaque-body"}},
		InteractionInfo: &td.MessageInteractionInfo{Reactions: &td.MessageReactions{Reactions: []*td.MessageReaction{
			{Type: &td.ReactionTypeEmoji{Emoji: "👍"}, TotalCount: 3, IsChosen: true},
			{Type: &td.ReactionTypeEmoji{Emoji: "❤️"}, TotalCount: 1, IsChosen: false},
			{Type: &td.ReactionTypeCustomEmoji{CustomEmojiId: 5}, TotalCount: 2, IsChosen: false},
		}}},
	})
	want := []domain.MessageReaction{
		{Emoji: "👍", Count: 3, Chosen: true},
		{Emoji: "❤️", Count: 1, Chosen: false},
	}
	if !reflect.DeepEqual(message.Reactions, want) {
		t.Fatalf("message reactions = %#v, want %#v", message.Reactions, want)
	}
}

func TestNormalizerMapsUpdateMessageInteractionInfoReactions(t *testing.T) {
	n := newNormalizer()
	updates := n.update(&td.UpdateMessageInteractionInfo{
		ChatId:    901,
		MessageId: 8,
		InteractionInfo: &td.MessageInteractionInfo{Reactions: &td.MessageReactions{Reactions: []*td.MessageReaction{
			{Type: &td.ReactionTypeEmoji{Emoji: "👍"}, TotalCount: 2, IsChosen: true},
			{Type: &td.ReactionTypeEmoji{Emoji: "😮"}, TotalCount: 4, IsChosen: false},
			{Type: &td.ReactionTypeCustomEmoji{CustomEmojiId: 7}, TotalCount: 1, IsChosen: false},
		}}},
	})
	update, ok := singleUpdate(t, updates).(MessageReactionsUpdated)
	if !ok {
		t.Fatalf("update type = %T", updates[0])
	}
	if update.ChatID != 901 || update.MessageID != 8 {
		t.Fatalf("reaction identity = (%d, %d)", update.ChatID, update.MessageID)
	}
	want := []domain.MessageReaction{
		{Emoji: "👍", Count: 2, Chosen: true},
		{Emoji: "😮", Count: 4, Chosen: false},
	}
	if !reflect.DeepEqual(update.Reactions, want) {
		t.Fatalf("reactions = %#v, want %#v", update.Reactions, want)
	}
}

func tdFile(id int32, uniqueID string) *td.File {
	return &td.File{Id: id, Remote: &td.RemoteFile{UniqueId: uniqueID}}
}

func tdTextMessage(id, chatID, userID int64, date int32, text string) *td.Message {
	return &td.Message{Id: id, ChatId: chatID, SenderId: &td.MessageSenderUser{UserId: userID}, Date: date, Content: &td.MessageText{Text: &td.FormattedText{Text: text}}}
}

func tdMediaMessage(id, chatID, userID int64, date int32, content td.MessageContent) *td.Message {
	return &td.Message{Id: id, ChatId: chatID, SenderId: &td.MessageSenderUser{UserId: userID}, Date: date, Content: content}
}

func notificationChat(id int64, chatType td.ChatType, settings *td.ChatNotificationSettings) *td.Chat {
	return &td.Chat{Id: id, Type: chatType, Title: "notification chat", NotificationSettings: settings}
}

func singleUpdate(t *testing.T, updates []Update) Update {
	t.Helper()
	if len(updates) != 1 {
		t.Fatalf("update count = %d, want 1", len(updates))
	}
	return updates[0]
}

func singleChat(t *testing.T, updates []Update) domain.Chat {
	t.Helper()
	update, ok := singleUpdate(t, updates).(ChatUpserted)
	if !ok {
		t.Fatal("update is not ChatUpserted")
	}
	return update.Chat
}

func TestNormalizerMapsMessageMediaIdentity(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 1, FirstName: "user"}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 3, Type: &td.ChatTypePrivate{UserId: 1}}})

	type args struct {
		content td.MessageContent
	}
	type want struct {
		kind     domain.MessageKind
		text     string
		fileName string
		media    domain.MessageMedia
		display  string
	}

	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "photo",
			args: args{
				content: &td.MessagePhoto{
					Caption: &td.FormattedText{Text: "photo caption"},
					Photo: &td.Photo{Sizes: []*td.PhotoSize{
						{
							Type:   "small",
							Photo:  &td.File{Id: 11, Size: 100, ExpectedSize: 200, Remote: &td.RemoteFile{UniqueId: "photo-small"}, Local: &td.LocalFile{Path: "/tmp/small.jpg", CanBeDownloaded: true, IsDownloadingCompleted: true}},
							Width:  40,
							Height: 30,
						},
						{
							Type:   "large",
							Photo:  &td.File{Id: 12, Size: 500, ExpectedSize: 1000, Remote: &td.RemoteFile{UniqueId: "photo-large"}, Local: &td.LocalFile{Path: "/tmp/large.jpg", CanBeDownloaded: true, IsDownloadingCompleted: false}},
							Width:  400,
							Height: 300,
						},
						{
							Type:   "thumb",
							Photo:  nil,
							Width:  10,
							Height: 10,
						},
					}},
				},
			},
			want: want{
				kind: domain.MessagePhoto,
				text: "photo caption",
				media: domain.MessageMedia{
					File: domain.MediaFileRef{
						ID: 12, UniqueID: "photo-large", Size: 500, ExpectedSize: 1000,
						LocalPath: "/tmp/large.jpg", CanDownload: true, Downloaded: false,
					},
					Thumbnail: domain.MediaFileRef{
						ID: 11, UniqueID: "photo-small", Size: 100, ExpectedSize: 200,
						LocalPath: "/tmp/small.jpg", CanDownload: true, Downloaded: true,
					},
					Width:  400,
					Height: 300,
				},
				display: "[Photo]",
			},
		},
		{
			name: "video",
			args: args{
				content: &td.MessageVideo{
					Video: &td.Video{
						Video:     &td.File{Id: 21, Size: 10000, Remote: &td.RemoteFile{UniqueId: "video-main"}},
						Thumbnail: &td.Thumbnail{File: &td.File{Id: 22, Remote: &td.RemoteFile{UniqueId: "video-thumb"}}, Width: 320, Height: 180},
						Width:     640,
						Height:    360,
						MimeType:  "video/mp4",
						FileName:  "clip.mp4",
						Duration:  12,
					},
				},
			},
			want: want{
				kind:     domain.MessageVideo,
				fileName: "clip.mp4",
				media: domain.MessageMedia{
					File: domain.MediaFileRef{
						ID: 21, UniqueID: "video-main", Size: 10000,
					},
					Thumbnail: domain.MediaFileRef{
						ID: 22, UniqueID: "video-thumb",
					},
					MIMEType: "video/mp4",
					Width:    640,
					Height:   360,
					Duration: 12 * time.Second,
				},
				display: "[Video]",
			},
		},
		{
			name: "sticker",
			args: args{
				content: &td.MessageSticker{
					Sticker: &td.Sticker{
						Id:        100,
						Sticker:   &td.File{Id: 31, Remote: &td.RemoteFile{UniqueId: "sticker-main"}},
						Thumbnail: &td.Thumbnail{File: &td.File{Id: 32, Remote: &td.RemoteFile{UniqueId: "sticker-thumb"}}, Width: 64, Height: 64},
						Width:     512,
						Height:    512,
					},
				},
			},
			want: want{
				kind: domain.MessageSticker,
				media: domain.MessageMedia{
					File:      domain.MediaFileRef{ID: 31, UniqueID: "sticker-main"},
					Thumbnail: domain.MediaFileRef{ID: 32, UniqueID: "sticker-thumb"},
					Width:     512,
					Height:    512,
				},
				display: "[Sticker]",
			},
		},
		{
			name: "document",
			args: args{
				content: &td.MessageDocument{
					Caption: &td.FormattedText{Text: "report caption"},
					Document: &td.Document{
						FileName:  "report.pdf",
						MimeType:  "application/pdf",
						Document:  &td.File{Id: 41, Remote: &td.RemoteFile{UniqueId: "document-main"}},
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatJpeg{}, File: &td.File{Id: 42, Remote: &td.RemoteFile{UniqueId: "document-thumb"}}},
					},
				},
			},
			want: want{
				kind:     domain.MessageDocument,
				text:     "report caption",
				fileName: "report.pdf",
				media: domain.MessageMedia{
					File:      domain.MediaFileRef{ID: 41, UniqueID: "document-main"},
					Thumbnail: domain.MediaFileRef{ID: 42, UniqueID: "document-thumb"},
					MIMEType:  "application/pdf",
				},
				display: "[File: report.pdf]",
			},
		},
		{
			name: "documentImage",
			args: args{
				content: &td.MessageDocument{
					Document: &td.Document{
						FileName:  "scan.jpg",
						MimeType:  "image/jpeg",
						Document:  &td.File{Id: 43, Remote: &td.RemoteFile{UniqueId: "doc-image-main"}},
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatPng{}, File: &td.File{Id: 44, Remote: &td.RemoteFile{UniqueId: "doc-image-thumb"}}},
					},
				},
			},
			want: want{
				kind:     domain.MessageDocument,
				fileName: "scan.jpg",
				media: domain.MessageMedia{
					File:      domain.MediaFileRef{ID: 43, UniqueID: "doc-image-main"},
					Thumbnail: domain.MediaFileRef{ID: 44, UniqueID: "doc-image-thumb"},
					MIMEType:  "image/jpeg",
				},
				display: "[File: scan.jpg]",
			},
		},
		{
			name: "animation",
			args: args{
				content: &td.MessageAnimation{
					Caption: &td.FormattedText{Text: "look"},
					Animation: &td.Animation{
						Duration:  5,
						Width:     256,
						Height:    256,
						FileName:  "cat.gif",
						MimeType:  "image/gif",
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatJpeg{}, File: &td.File{Id: 52, Remote: &td.RemoteFile{UniqueId: "animation-thumb"}}},
						Animation: &td.File{Id: 51, Remote: &td.RemoteFile{UniqueId: "animation-main"}},
					},
				},
			},
			want: want{
				kind:     domain.MessageAnimation,
				text:     "look",
				fileName: "cat.gif",
				media: domain.MessageMedia{
					File:      domain.MediaFileRef{ID: 51, UniqueID: "animation-main"},
					Thumbnail: domain.MediaFileRef{ID: 52, UniqueID: "animation-thumb"},
					MIMEType:  "image/gif",
					Width:     256,
					Height:    256,
					Duration:  5 * time.Second,
				},
				display: "[Animation]",
			},
		},
		{
			name: "animationMPEG4NoThumbnail",
			args: args{
				content: &td.MessageAnimation{
					Animation: &td.Animation{
						Duration:  12,
						Width:     640,
						Height:    360,
						FileName:  "clip.mp4",
						MimeType:  "video/mp4",
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatMpeg4{}, File: &td.File{Id: 54, Remote: &td.RemoteFile{UniqueId: "animation-mp4-thumb"}}},
						Animation: &td.File{Id: 53, Remote: &td.RemoteFile{UniqueId: "animation-mp4-main"}},
					},
				},
			},
			want: want{
				kind:     domain.MessageAnimation,
				fileName: "clip.mp4",
				media: domain.MessageMedia{
					File:     domain.MediaFileRef{ID: 53, UniqueID: "animation-mp4-main"},
					MIMEType: "video/mp4",
					Width:    640,
					Height:   360,
					Duration: 12 * time.Second,
				},
				display: "[Animation]",
			},
		},
		{
			name: "animationSpoilerNoThumbnail",
			args: args{
				content: &td.MessageAnimation{
					HasSpoiler: true,
					Animation: &td.Animation{
						Duration:  5,
						Width:     256,
						Height:    256,
						FileName:  "secret.gif",
						MimeType:  "image/gif",
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatJpeg{}, File: &td.File{Id: 56, Remote: &td.RemoteFile{UniqueId: "animation-spoiler-thumb"}}},
						Animation: &td.File{Id: 55, Remote: &td.RemoteFile{UniqueId: "animation-spoiler-main"}},
					},
				},
			},
			want: want{
				kind:     domain.MessageAnimation,
				fileName: "secret.gif",
				media: domain.MessageMedia{
					File:     domain.MediaFileRef{ID: 55, UniqueID: "animation-spoiler-main"},
					MIMEType: "image/gif",
					Width:    256,
					Height:   256,
					Duration: 5 * time.Second,
				},
				display: "[Animation]",
			},
		},
		{
			name: "voiceNote",
			args: args{
				content: &td.MessageVoiceNote{
					Caption: &td.FormattedText{Text: "note"},
					VoiceNote: &td.VoiceNote{
						Duration: 42,
						MimeType: "audio/ogg",
						Voice:    &td.File{Id: 61, Remote: &td.RemoteFile{UniqueId: "voice-main"}},
					},
				},
			},
			want: want{
				kind: domain.MessageVoiceNote,
				text: "note",
				media: domain.MessageMedia{
					File:     domain.MediaFileRef{ID: 61, UniqueID: "voice-main"},
					MIMEType: "audio/ogg",
					Duration: 42 * time.Second,
				},
				display: "[Voice note]",
			},
		},
		{
			name: "videoNote",
			args: args{
				content: &td.MessageVideoNote{
					VideoNote: &td.VideoNote{
						Duration:  15,
						Length:    480,
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatJpeg{}, File: &td.File{Id: 72, Remote: &td.RemoteFile{UniqueId: "videonote-thumb"}}},
						Video:     &td.File{Id: 71, Remote: &td.RemoteFile{UniqueId: "videonote-main"}},
					},
				},
			},
			want: want{
				kind: domain.MessageVideoNote,
				media: domain.MessageMedia{
					File:      domain.MediaFileRef{ID: 71, UniqueID: "videonote-main"},
					Thumbnail: domain.MediaFileRef{ID: 72, UniqueID: "videonote-thumb"},
					Width:     480,
					Height:    480,
					Duration:  15 * time.Second,
				},
				display: "[Video note]",
			},
		},
		{
			name: "videoNoteSecretNoThumbnail",
			args: args{
				content: &td.MessageVideoNote{
					IsSecret: true,
					VideoNote: &td.VideoNote{
						Duration:  15,
						Length:    480,
						Thumbnail: &td.Thumbnail{Format: &td.ThumbnailFormatJpeg{}, File: &td.File{Id: 74, Remote: &td.RemoteFile{UniqueId: "videonote-secret-thumb"}}},
						Video:     &td.File{Id: 73, Remote: &td.RemoteFile{UniqueId: "videonote-secret-main"}},
					},
				},
			},
			want: want{
				kind: domain.MessageVideoNote,
				media: domain.MessageMedia{
					File:     domain.MediaFileRef{ID: 73, UniqueID: "videonote-secret-main"},
					Width:    480,
					Height:   480,
					Duration: 15 * time.Second,
				},
				display: "[Video note]",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message := n.message(&td.Message{
				Id:       1,
				ChatId:   3,
				SenderId: &td.MessageSenderUser{UserId: 1},
				Date:     10,
				Content:  tc.args.content,
			})
			if message.Kind != tc.want.kind {
				t.Fatalf("kind = %v, want %v", message.Kind, tc.want.kind)
			}
			if message.Text != tc.want.text {
				t.Fatalf("text = %q, want %q", message.Text, tc.want.text)
			}
			if message.FileName != tc.want.fileName {
				t.Fatalf("fileName = %q, want %q", message.FileName, tc.want.fileName)
			}
			if message.Media != tc.want.media {
				t.Fatalf("Media = %#v, want %#v", message.Media, tc.want.media)
			}
			if message.DisplayText() != tc.want.display {
				t.Fatalf("DisplayText = %q, want %q", message.DisplayText(), tc.want.display)
			}
		})
	}

	// Equal area primary chooses greater file ID.
	t.Run("equalArea", func(t *testing.T) {
		msg := n.message(&td.Message{
			Id: 5, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessagePhoto{Photo: &td.Photo{Sizes: []*td.PhotoSize{
				{Type: "a", Photo: &td.File{Id: 10, Remote: &td.RemoteFile{UniqueId: "a"}}, Width: 100, Height: 100},
				{Type: "b", Photo: &td.File{Id: 20, Remote: &td.RemoteFile{UniqueId: "b"}}, Width: 100, Height: 100},
			}}},
		})
		if msg.Media.File.ID != 20 {
			t.Fatalf("primary ID = %d, want 20", msg.Media.File.ID)
		}
		if msg.Media.Thumbnail.ID != 10 {
			t.Fatalf("thumbnail ID = %d, want 10", msg.Media.Thumbnail.ID)
		}
	})

	// One valid size maps same file to primary and thumbnail.
	t.Run("singleSize", func(t *testing.T) {
		msg := n.message(&td.Message{
			Id: 6, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessagePhoto{Photo: &td.Photo{Sizes: []*td.PhotoSize{
				{Type: "only", Photo: &td.File{Id: 99, Remote: &td.RemoteFile{UniqueId: "single"}}, Width: 200, Height: 200},
			}}},
		})
		if msg.Media.File.ID != 99 || msg.Media.Thumbnail.ID != 99 {
			t.Fatalf("single-size: file=%d thumbnail=%d", msg.Media.File.ID, msg.Media.Thumbnail.ID)
		}
	})
}

func TestNormalizerMessageMediaNilSafety(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 1, FirstName: "u"}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 3, Type: &td.ChatTypePrivate{UserId: 1}}})

	// Nil photo descriptor.
	t.Run("nilPhotoDescriptor", func(t *testing.T) {
		nilPhoto := n.message(&td.Message{
			Id: 1, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessagePhoto{Photo: nil},
		})
		if nilPhoto.Kind != domain.MessagePhoto {
			t.Fatal("nil photo kind wrong")
		}
		if nilPhoto.Media != (domain.MessageMedia{}) {
			t.Fatal("nil photo should yield zero Media")
		}
	})

	// Nil thumbnail in video.
	t.Run("nilVideoThumbnail", func(t *testing.T) {
		noThumb := n.message(&td.Message{
			Id: 2, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessageVideo{
				Video: &td.Video{
					Video:     &td.File{Id: 21, Remote: &td.RemoteFile{UniqueId: "main"}},
					Thumbnail: nil,
					Width:     640, Height: 360, MimeType: "video/mp4", Duration: 10,
				},
			},
		})
		if noThumb.Media.File.ID != 21 {
			t.Fatal("video primary nil")
		}
		if noThumb.Media.Thumbnail != (domain.MediaFileRef{}) {
			t.Fatal("nil thumbnail should be zero MediaFileRef")
		}
	})

	// Nonnil thumbnail descriptor whose File is nil — assert exact zero MediaFileRef and no panic.
	t.Run("nilThumbnailFile", func(t *testing.T) {
		nilThumbFile := n.message(&td.Message{
			Id: 3, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessageVideo{
				Video: &td.Video{
					Video:     &td.File{Id: 23, Remote: &td.RemoteFile{UniqueId: "main"}},
					Thumbnail: &td.Thumbnail{File: nil, Width: 100, Height: 100},
					Width:     640, Height: 360, MimeType: "video/mp4", Duration: 10,
				},
			},
		})
		if nilThumbFile.Media.File.ID != 23 {
			t.Fatalf("primary ID = %d, want 23", nilThumbFile.Media.File.ID)
		}
		if nilThumbFile.Media.Thumbnail != (domain.MediaFileRef{}) {
			t.Fatal("nil thumbnail file should yield zero MediaFileRef")
		}
	})

	// Nil main file for video returns zero Media.
	t.Run("nilMainVideo", func(t *testing.T) {
		nilMain := n.message(&td.Message{
			Id: 4, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessageVideo{Video: nil},
		})
		if nilMain.Media != (domain.MessageMedia{}) {
			t.Fatalf("nil main video should yield zero Media, got %#v", nilMain.Media)
		}
	})

	// Video with nil main file but other descriptor metadata: Media.File is zero (not downloadable),
	// but permitted non-file metadata remains exact.
	t.Run("nilMainWithDescriptor", func(t *testing.T) {
		nilMain := n.message(&td.Message{
			Id: 5, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessageVideo{
				Video: &td.Video{
					Video:     nil,
					Thumbnail: &td.Thumbnail{File: &td.File{Id: 22, Remote: &td.RemoteFile{UniqueId: "thumb"}}, Width: 100, Height: 100},
					Width:     640, Height: 360, MimeType: "video/mp4", Duration: 10,
				},
			},
		})
		if nilMain.Media.File != (domain.MediaFileRef{}) {
			t.Fatalf("nil main file should yield zero MediaFileRef, got %#v", nilMain.Media.File)
		}
		if nilMain.Media.Thumbnail.ID != 22 || nilMain.Media.Thumbnail.UniqueID != "thumb" {
			t.Fatalf("thumbnail should survive nil main: %#v", nilMain.Media.Thumbnail)
		}
		if nilMain.Media.MIMEType != "video/mp4" || nilMain.Media.Width != 640 || nilMain.Media.Height != 360 {
			t.Fatalf("non-file metadata should survive nil main: %#v", nilMain.Media)
		}
		if nilMain.Media.Duration != 10*time.Second {
			t.Fatalf("duration should survive nil main: got %v", nilMain.Media.Duration)
		}
	})

	// Nil sticker.
	t.Run("nilSticker", func(t *testing.T) {
		nilSticker := n.message(&td.Message{
			Id: 6, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessageSticker{Sticker: nil},
		})
		if nilSticker.Kind != domain.MessageSticker || nilSticker.Media != (domain.MessageMedia{}) {
			t.Fatal("nil sticker should yield zero Media")
		}
	})

	// Nil document.
	t.Run("nilDocument", func(t *testing.T) {
		nilDoc := n.message(&td.Message{
			Id: 7, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessageDocument{Document: nil},
		})
		if nilDoc.Kind != domain.MessageDocument || nilDoc.FileName != "" || nilDoc.Media != (domain.MessageMedia{}) {
			t.Fatal("nil document should yield empty kind/filename/zero Media")
		}
	})

	// Photo with invalid size entry (nil Photo).
	t.Run("invalidPhotoSize", func(t *testing.T) {
		invalidSize := n.message(&td.Message{
			Id: 8, ChatId: 3, SenderId: &td.MessageSenderUser{UserId: 1}, Date: 10,
			Content: &td.MessagePhoto{Photo: &td.Photo{Sizes: []*td.PhotoSize{
				{Type: "bad", Photo: nil, Width: 100, Height: 100},
				{Type: "bad2", Photo: &td.File{}, Width: 100, Height: 100},
			}}},
		})
		if invalidSize.Media != (domain.MessageMedia{}) {
			t.Fatal("invalid photo sizes should yield zero Media")
		}
	})
}

func TestNormalizerMapsMessageContentMediaUpdate(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 1, FirstName: "u"}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 3, Type: &td.ChatTypePrivate{UserId: 1}}})

	// Feed UpdateMessageContent with a single-size photo.
	updates := n.update(&td.UpdateMessageContent{
		ChatId:    3,
		MessageId: 50,
		NewContent: &td.MessagePhoto{
			Caption: &td.FormattedText{Text: "updated caption"},
			Photo: &td.Photo{
				Sizes: []*td.PhotoSize{
					{Type: "s", Photo: &td.File{Id: 55, Remote: &td.RemoteFile{UniqueId: "upd-photo"}}, Width: 200, Height: 200},
				},
			},
		},
	})
	content, ok := singleUpdate(t, updates).(MessageContentUpdated)
	if !ok {
		t.Fatal("update is not MessageContentUpdated")
	}
	// Exact full payload assertion.
	if content.ChatID != 3 || content.MessageID != 50 {
		t.Fatalf("identity = (%d, %d), want (3, 50)", content.ChatID, content.MessageID)
	}
	if content.Kind != domain.MessagePhoto {
		t.Fatalf("kind = %v, want %v", content.Kind, domain.MessagePhoto)
	}
	if content.Text != "updated caption" {
		t.Fatalf("text = %q, want updated caption", content.Text)
	}
	if content.FileName != "" {
		t.Fatalf("fileName = %q, want empty", content.FileName)
	}
	wantMedia := domain.MessageMedia{
		File:      domain.MediaFileRef{ID: 55, UniqueID: "upd-photo"},
		Thumbnail: domain.MediaFileRef{ID: 55, UniqueID: "upd-photo"},
		Width:     200,
		Height:    200,
	}
	if content.Media != wantMedia {
		t.Fatalf("media = %#v, want %#v", content.Media, wantMedia)
	}

	// Feed text replacement - should clear FileName and Media.
	updates2 := n.update(&td.UpdateMessageContent{
		ChatId:     3,
		MessageId:  50,
		NewContent: &td.MessageText{Text: &td.FormattedText{Text: "plain text now"}},
	})
	content2, ok := singleUpdate(t, updates2).(MessageContentUpdated)
	if !ok {
		t.Fatal("update is not MessageContentUpdated")
	}
	// Exact full payload assertion for text replacement.
	if content2.ChatID != 3 || content2.MessageID != 50 {
		t.Fatalf("identity = (%d, %d), want (3, 50)", content2.ChatID, content2.MessageID)
	}
	if content2.Kind != domain.MessageText || content2.Text != "plain text now" {
		t.Fatalf("kind=%v text=%q, want %v %q", content2.Kind, content2.Text, domain.MessageText, "plain text now")
	}
	if content2.FileName != "" {
		t.Fatalf("fileName = %q, want empty", content2.FileName)
	}
	if content2.Media != (domain.MessageMedia{}) {
		t.Fatalf("media should be zero: %#v", content2.Media)
	}
}

func TestNormalizerPhotoSendFailureUsesPhotoOperation(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 41, FirstName: "Sender", ProfilePhoto: &td.ProfilePhoto{Small: tdFile(61, "sender-small")}}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 10, Type: &td.ChatTypePrivate{UserId: 41}}})

	msg := &td.Message{
		Id:       100,
		ChatId:   10,
		SenderId: &td.MessageSenderUser{UserId: 41},
		Date:     100,
		Content:  &td.MessagePhoto{},
	}

	failure := &td.Error{Code: 429, Message: "FLOOD_WAIT_6"}
	updates := n.update(&td.UpdateMessageSendFailed{OldMessageId: -999, Message: msg, Error: failure})

	got, ok := singleUpdate(t, updates).(MessageSendFailed)
	if !ok {
		t.Fatal("update is not MessageSendFailed")
	}
	if got.OldID != -999 {
		t.Fatalf("OldID = %d, want -999", got.OldID)
	}
	if got.Message.Kind != domain.MessagePhoto {
		t.Fatalf("Message.Kind = %v, want %v", got.Message.Kind, domain.MessagePhoto)
	}
	if got.Message.SendState != domain.SendFailed {
		t.Fatalf("SendState = %v, want %v", got.Message.SendState, domain.SendFailed)
	}
	if got.Message.Failure == nil {
		t.Fatal("Message.Failure is nil")
	}
	if got.Error.Op != "send photo" {
		t.Fatalf("Error.Op = %q, want %q", got.Error.Op, "send photo")
	}
	if got.Error.Kind != domain.ErrorRateLimit {
		t.Fatalf("Error.Kind = %v, want %v", got.Error.Kind, domain.ErrorRateLimit)
	}
	if got.Error.RetryAfter != 6*time.Second {
		t.Fatalf("Error.RetryAfter = %v, want %v", got.Error.RetryAfter, 6*time.Second)
	}
	if got.Message.Failure.Op != "send photo" {
		t.Fatalf("Failure.Op = %q, want %q", got.Message.Failure.Op, "send photo")
	}
	if got.Error.Kind != got.Message.Failure.Kind {
		t.Fatalf("Error.Kind %v != Failure.Kind %v", got.Error.Kind, got.Message.Failure.Kind)
	}
	if got.Error.RetryAfter != got.Message.Failure.RetryAfter {
		t.Fatalf("Error.RetryAfter %v != Failure.RetryAfter %v", got.Error.RetryAfter, got.Message.Failure.RetryAfter)
	}
}

func TestNormalizerTextSendFailureKeepsTextOperation(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 41, FirstName: "Sender", ProfilePhoto: &td.ProfilePhoto{Small: tdFile(61, "sender-small")}}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 10, Type: &td.ChatTypePrivate{UserId: 41}}})

	msg := &td.Message{
		Id:       100,
		ChatId:   10,
		SenderId: &td.MessageSenderUser{UserId: 41},
		Date:     100,
		Content:  &td.MessageText{Text: &td.FormattedText{Text: "hello"}},
	}

	failure := &td.Error{Code: 429, Message: "FLOOD_WAIT_6"}
	updates := n.update(&td.UpdateMessageSendFailed{OldMessageId: -888, Message: msg, Error: failure})

	got, ok := singleUpdate(t, updates).(MessageSendFailed)
	if !ok {
		t.Fatal("update is not MessageSendFailed")
	}
	if got.OldID != -888 {
		t.Fatalf("OldID = %d, want -888", got.OldID)
	}
	if got.Error.Op != "send text" {
		t.Fatalf("Error.Op = %q, want %q", got.Error.Op, "send text")
	}
	if got.Error.Kind != domain.ErrorRateLimit {
		t.Fatalf("Error.Kind = %v, want %v", got.Error.Kind, domain.ErrorRateLimit)
	}
	if got.Error.RetryAfter != 6*time.Second {
		t.Fatalf("Error.RetryAfter = %v, want %v", got.Error.RetryAfter, 6*time.Second)
	}
	if got.Message.Failure == nil {
		t.Fatal("Message.Failure is nil")
	}
	if got.Message.Failure.Op != "send text" {
		t.Fatalf("Failure.Op = %q, want %q", got.Message.Failure.Op, "send text")
	}
	if got.Message.SendState != domain.SendFailed {
		t.Fatalf("SendState = %v, want %v", got.Message.SendState, domain.SendFailed)
	}
	if got.Message.Kind != domain.MessageText {
		t.Fatalf("Message.Kind = %v, want %v", got.Message.Kind, domain.MessageText)
	}
}

func TestNormalizerPhotoSendFailureKeepsPrivateTDDetailOutOfPublicError(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateUser{User: &td.User{Id: 41, FirstName: "Sender", ProfilePhoto: &td.ProfilePhoto{Small: tdFile(61, "sender-small")}}})
	n.update(&td.UpdateNewChat{Chat: &td.Chat{Id: 10, Type: &td.ChatTypePrivate{UserId: 41}}})

	msg := &td.Message{
		Id:       100,
		ChatId:   10,
		SenderId: &td.MessageSenderUser{UserId: 41},
		Date:     100,
		Content:  &td.MessagePhoto{},
	}

	privateMessage := "/private/photo.jpg SECRET_CAPTION"
	failure := &td.Error{Code: 400, Message: privateMessage}
	updates := n.update(&td.UpdateMessageSendFailed{OldMessageId: -777, Message: msg, Error: failure})

	got, ok := singleUpdate(t, updates).(MessageSendFailed)
	if !ok {
		t.Fatal("update is not MessageSendFailed")
	}
	if got.Error.Op != "send photo" {
		t.Fatalf("Error.Op = %q, want %q", got.Error.Op, "send photo")
	}
	if got.Error.Kind != domain.ErrorInternal {
		t.Fatalf("Error.Kind = %v, want %v", got.Error.Kind, domain.ErrorInternal)
	}
	if strings.Contains(got.Error.Message, "/private/photo.jpg") {
		t.Fatal("Error.Message leaked private path")
	}
	if strings.Contains(got.Error.Error(), "/private/photo.jpg") {
		t.Fatal("Error.Error() leaked private path")
	}
	if strings.Contains(got.Error.Message, "SECRET_CAPTION") {
		t.Fatal("Error.Message leaked caption")
	}
	if strings.Contains(got.Error.Error(), "SECRET_CAPTION") {
		t.Fatal("Error.Error() leaked caption")
	}
	if got.Message.Failure == nil {
		t.Fatal("Message.Failure is nil")
	}
	if strings.Contains(got.Message.Failure.Message, "/private/photo.jpg") {
		t.Fatal("Failure.Message leaked private path")
	}
	if strings.Contains(got.Message.Failure.Error(), "/private/photo.jpg") {
		t.Fatal("Failure.Error() leaked private path")
	}
	if strings.Contains(got.Message.Failure.Message, "SECRET_CAPTION") {
		t.Fatal("Failure.Message leaked caption")
	}
	if strings.Contains(got.Message.Failure.Error(), "SECRET_CAPTION") {
		t.Fatal("Failure.Error() leaked caption")
	}
}

func TestNormalizerMapsForumChatFlag(t *testing.T) {
	n := newNormalizer()
	n.update(&td.UpdateSupergroup{Supergroup: &td.Supergroup{Id: 91, IsForum: true}})
	chat := &td.Chat{Id: 21, Type: &td.ChatTypeSupergroup{SupergroupId: 91}, Title: "forum"}
	got := singleChat(t, n.update(&td.UpdateNewChat{Chat: chat}))
	if got.ID != 21 || !got.IsForum {
		t.Fatalf("forum flag = (%d, %v), want (21, true)", got.ID, got.IsForum)
	}
}

func TestNormalizerMapsMessageTopicID(t *testing.T) {
	n := newNormalizer()
	forumMessage := tdTextMessage(77, 21, 4, 100, "topic message")
	forumMessage.TopicId = &td.MessageTopicForum{ForumTopicId: 5}
	if got := n.message(forumMessage); got.TopicID != 5 {
		t.Fatalf("forum topic message TopicID = %d, want 5", got.TopicID)
	}
	generalMessage := tdTextMessage(78, 21, 4, 101, "general message")
	generalMessage.TopicId = &td.MessageTopicThread{}
	if got := n.message(generalMessage); got.TopicID != 0 {
		t.Fatalf("general message TopicID = %d, want 0", got.TopicID)
	}
	plainMessage := tdTextMessage(79, 21, 4, 102, "plain message")
	if got := n.message(plainMessage); got.TopicID != 0 {
		t.Fatalf("nil topic message TopicID = %d, want 0", got.TopicID)
	}
}

func TestNormalizerMapsForumTopicUpdates(t *testing.T) {
	n := newNormalizer()
	info := &td.ForumTopicInfo{
		ChatId:       21,
		ForumTopicId: 5,
		Name:         "Announcements",
		Icon:         &td.ForumTopicIcon{Color: 0x33b5e5},
		IsClosed:     true,
	}
	infoUpdate, ok := singleUpdate(t, n.update(&td.UpdateForumTopicInfo{Info: info})).(ForumTopicInfoChanged)
	if !ok {
		t.Fatal("UpdateForumTopicInfo did not normalize to ForumTopicInfoChanged")
	}
	if infoUpdate.Topic.ID != 5 || infoUpdate.Topic.ChatID != 21 || infoUpdate.Topic.Name != "Announcements" {
		t.Fatalf("topic info = %#v", infoUpdate.Topic)
	}
	if infoUpdate.Topic.IconColor != 0x33b5e5 || !infoUpdate.Topic.IsClosed {
		t.Fatalf("topic icon and flags = %#v", infoUpdate.Topic)
	}
	if infoUpdate.Topic.IsGeneral {
		t.Fatal("non-general topic reported as general")
	}
	stateUpdate, ok := singleUpdate(t, n.update(&td.UpdateForumTopic{
		ChatId:             21,
		ForumTopicId:       5,
		IsPinned:           true,
		UnreadMentionCount: 2,
		DraftMessage:       &td.DraftMessage{Date: 5, InputMessageText: &td.InputMessageText{Text: &td.FormattedText{Text: "topic draft"}}},
	})).(ForumTopicStateChanged)
	if !ok {
		t.Fatal("UpdateForumTopic did not normalize to ForumTopicStateChanged")
	}
	if stateUpdate.ChatID != 21 || stateUpdate.TopicID != 5 || !stateUpdate.IsPinned || stateUpdate.UnreadMentionCount != 2 {
		t.Fatalf("topic state = %#v", stateUpdate)
	}
	if stateUpdate.Draft.Text != "topic draft" {
		t.Fatalf("topic draft = %#v", stateUpdate.Draft)
	}
}
