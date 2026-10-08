//go:build tdlib

package telegram

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	td "github.com/zelenin/go-tdlib/client"
	"github.com/zylen-det/tuilegram/internal/domain"
)

type chatPeer struct {
	kind         domain.ChatKind
	userID       int64
	basicGroupID int64
	supergroupID int64
}

type notificationScope uint8

const (
	notificationScopePrivate notificationScope = iota
	notificationScopeGroup
	notificationScopeChannel
)

type normalizer struct {
	mu sync.RWMutex

	users              map[int64]domain.User
	chats              map[int64]domain.Chat
	basicGroups        map[int64]*td.BasicGroup
	supergroups        map[int64]*td.Supergroup
	chatPeers          map[int64]chatPeer
	chatChangeInfo     map[int64]bool
	authoritativeChats map[int64]bool

	chatNotifications  map[int64]*td.ChatNotificationSettings
	scopeNotifications map[notificationScope]*td.ScopeNotificationSettings
}

func newNormalizer() *normalizer {
	return &normalizer{
		users:              make(map[int64]domain.User),
		chats:              make(map[int64]domain.Chat),
		basicGroups:        make(map[int64]*td.BasicGroup),
		supergroups:        make(map[int64]*td.Supergroup),
		chatPeers:          make(map[int64]chatPeer),
		chatChangeInfo:     make(map[int64]bool),
		authoritativeChats: make(map[int64]bool),
		chatNotifications:  make(map[int64]*td.ChatNotificationSettings),
		scopeNotifications: make(map[notificationScope]*td.ScopeNotificationSettings),
	}
}

func (n *normalizer) update(value td.Type) []Update {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch update := value.(type) {
	case *td.UpdateConnectionState:
		return []Update{ConnectionChanged{State: connectionState(update.State)}}
	case *td.UpdateAuthorizationState:
		if update.AuthorizationState != nil && update.AuthorizationState.AuthorizationStateConstructor() == td.ConstructorAuthorizationStateClosed {
			return []Update{Closed{}}
		}
	case *td.UpdateAccentColors:
		colors := make(map[int32]uint32, len(update.Colors))
		for _, entry := range update.Colors {
			if entry != nil && len(entry.DarkThemeColors) > 0 {
				colors[entry.Id] = uint32(entry.DarkThemeColors[0]) & 0xffffff
			}
		}
		return []Update{AccentColorsChanged{DarkRGB: colors}}
	case *td.UpdateUser:
		if update.User == nil {
			return nil
		}
		user := n.userLocked(update.User)
		chatIDs := make([]int64, 0)
		for chatID, peer := range n.chatPeers {
			if peer.userID == update.User.Id {
				chatIDs = append(chatIDs, chatID)
			}
		}
		sort.Slice(chatIDs, func(i, j int) bool { return chatIDs[i] < chatIDs[j] })
		updates := make([]Update, 0, len(chatIDs)+1)
		updates = append(updates, UserUpserted{User: user})
		for _, chatID := range chatIDs {
			chat := n.chats[chatID]
			chat.Username = user.Username
			n.chats[chatID] = chat
			n.authoritativeChats[chatID] = true
			updates = append(updates, ChatUpserted{Chat: chat})
		}
		return updates
	case *td.UpdateBasicGroup:
		if update.BasicGroup == nil {
			return nil
		}
		group := cloneBasicGroup(update.BasicGroup)
		n.basicGroups[group.Id] = group
		return n.refreshBasicGroupChatsLocked(group.Id)
	case *td.UpdateSupergroup:
		if update.Supergroup == nil {
			return nil
		}
		group := cloneSupergroup(update.Supergroup)
		n.supergroups[group.Id] = group
		return n.refreshSupergroupChatsLocked(group.Id)
	case *td.UpdateNewChat:
		if update.Chat == nil {
			return nil
		}
		chat := n.chatLocked(update.Chat)
		n.authoritativeChats[update.Chat.Id] = true
		return []Update{ChatUpserted{Chat: chat}}
	case *td.UpdateChatTitle:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) { chat.Title = update.Title })
	case *td.UpdateChatPhoto:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) { chat.Avatar = avatarFromChatPhoto(update.Photo) })
	case *td.UpdateChatAccentColors:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) { chat.AccentColorID = update.AccentColorId })
	case *td.UpdateChatReadInbox:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) { chat.UnreadCount = int(update.UnreadCount) })
	case *td.UpdateChatUnreadMentionCount:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) { chat.UnreadMentionCount = int(update.UnreadMentionCount) })
	case *td.UpdateChatNotificationSettings:
		n.chatNotifications[update.ChatId] = cloneChatNotificationSettings(update.NotificationSettings)
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) {
			chat.Muted = n.effectiveMutedLocked(update.ChatId, chat.Kind)
		})
	case *td.UpdateScopeNotificationSettings:
		scope, ok := notificationScopeOf(update.Scope)
		if !ok {
			return nil
		}
		if update.NotificationSettings == nil {
			delete(n.scopeNotifications, scope)
		} else {
			settings := *update.NotificationSettings
			n.scopeNotifications[scope] = &settings
		}
		return n.refreshNotificationScopeLocked(scope)
	case *td.UpdateChatPermissions:
		n.chatChangeInfo[update.ChatId] = update.Permissions != nil && update.Permissions.CanChangeInfo
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) {
			chat.CanSend = canSend(update.Permissions)
			chat.CanReact = canReact(update.Permissions)
			chat.CanChangeInfo = n.peerCanChangeInfoLocked(n.chatPeers[update.ChatId], n.chatChangeInfo[update.ChatId])
		})
	case *td.UpdateChatLastMessage:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) {
			setLastMessage(chat, n.messageLocked(update.LastMessage))
			chat.Order = mainOrder(update.Positions)
		})
	case *td.UpdateChatDraftMessage:
		draft := draftMessage(update.DraftMessage)
		updates := n.changeChatLocked(update.ChatId, func(chat *domain.Chat) {
			chat.Draft = draft
			chat.Order = mainOrder(update.Positions)
		})
		return append(updates, DraftChanged{ChatID: domain.ChatID(update.ChatId), Draft: draft})
	case *td.UpdateChatPosition:
		if update.Position == nil {
			return nil
		}
		switch update.Position.List.(type) {
		case *td.ChatListMain:
			return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) {
				chat.Order = int64(update.Position.Order)
				chat.IsPinned = update.Position.Order != 0 && update.Position.IsPinned
				if update.Position.Order != 0 {
					chat.IsArchived = false
				}
			})
		case *td.ChatListArchive:
			return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) {
				chat.IsArchived = update.Position.Order != 0
			})
		default:
			return nil
		}
	case *td.UpdateChatIsMarkedAsUnread:
		return n.changeChatLocked(update.ChatId, func(chat *domain.Chat) { chat.IsMarkedUnread = update.IsMarkedAsUnread })
	case *td.UpdateForumTopicInfo:
		if update.Info == nil {
			return nil
		}
		return []Update{ForumTopicInfoChanged{Topic: n.topicLocked(&td.ForumTopic{Info: update.Info})}}
	case *td.UpdateForumTopic:
		return []Update{ForumTopicStateChanged{
			ChatID:             domain.ChatID(update.ChatId),
			TopicID:            domain.TopicID(update.ForumTopicId),
			IsPinned:           update.IsPinned,
			UnreadMentionCount: int(update.UnreadMentionCount),
			Draft:              draftMessage(update.DraftMessage),
		}}
	case *td.UpdateNewMessage:
		if update.Message != nil {
			return []Update{MessageUpserted{Message: n.messageLocked(update.Message)}}
		}
	case *td.UpdateMessageContent:
		kind, text, fileName, media := n.messageContentLocked(update.NewContent, nil)
		var sticker domain.StickerRef
		if content, ok := update.NewContent.(*td.MessageSticker); ok {
			sticker = stickerRef(content.Sticker)
		}
		return []Update{MessageContentUpdated{ChatID: domain.ChatID(update.ChatId), MessageID: domain.MessageID(update.MessageId), Kind: kind, Text: text, Entities: messageEntities(update.NewContent), FileName: fileName, Media: media, Sticker: sticker}}
	case *td.UpdateMessageEdited:
		return []Update{MessageEdited{ChatID: domain.ChatID(update.ChatId), MessageID: domain.MessageID(update.MessageId), EditedAt: time.Unix(int64(update.EditDate), 0)}}
	case *td.UpdateMessageIsPinned:
		return []Update{MessagePinnedUpdated{ChatID: domain.ChatID(update.ChatId), MessageID: domain.MessageID(update.MessageId), Pinned: update.IsPinned}}
	case *td.UpdateMessageReactions:
		return []Update{MessageReactionsUpdated{ChatID: domain.ChatID(update.ChatId), MessageID: domain.MessageID(update.MessageId), Reactions: reactionsFromLocked(update.Reactions)}}
	case *td.UpdateMessageInteractionInfo:
		return []Update{MessageReactionsUpdated{ChatID: domain.ChatID(update.ChatId), MessageID: domain.MessageID(update.MessageId), Reactions: reactionsFromMessageLocked(update.InteractionInfo)}}
	case *td.UpdateDeleteMessages:
		messageIDs := make([]domain.MessageID, len(update.MessageIds))
		for index, messageID := range update.MessageIds {
			messageIDs[index] = domain.MessageID(messageID)
		}
		return []Update{MessagesDeleted{ChatID: domain.ChatID(update.ChatId), MessageIDs: messageIDs, FromCache: update.FromCache}}
	case *td.UpdateMessageSendSucceeded:
		if update.Message != nil {
			message := n.messageLocked(update.Message)
			message.SendState = domain.SendSucceeded
			message.Failure = nil
			return []Update{MessageSendSucceeded{OldID: domain.MessageID(update.OldMessageId), Message: message}}
		}
	case *td.UpdateMessageSendFailed:
		if update.Message != nil {
			message := n.messageLocked(update.Message)
			operation := "send text"
			switch message.Kind {
			case domain.MessagePhoto:
				operation = "send photo"
			case domain.MessageVideo:
				operation = "send video"
			case domain.MessageAudio:
				operation = "send audio"
			case domain.MessageDocument:
				operation = "send document"
			case domain.MessageSticker:
				operation = "send sticker"
			}
			failure := normalizeTDError(operation, update.Error)
			message.SendState = domain.SendFailed
			message.Failure = &failure
			return []Update{MessageSendFailed{OldID: domain.MessageID(update.OldMessageId), Message: message, Error: failure}}
		}
	}
	return nil
}

func (n *normalizer) chat(value *td.Chat) domain.Chat {
	n.mu.Lock()
	defer n.mu.Unlock()
	if value == nil {
		return domain.Chat{}
	}
	// GetChat snapshots can be delivered to a request waiter before newer
	// updates are normalized. Once updates established a chat, they are the
	// authoritative cache and an older request snapshot must not replace it.
	if chat, ok := n.chats[value.Id]; ok && n.authoritativeChats[value.Id] {
		return chat
	}
	return n.chatLocked(value)
}

func (n *normalizer) chatLocked(value *td.Chat) domain.Chat {
	chat, _ := n.chatWithLastMessageLocked(value)
	return chat
}

func (n *normalizer) chatWithLastMessageLocked(value *td.Chat) (domain.Chat, domain.Message) {
	if value == nil {
		return domain.Chat{}, domain.Message{}
	}
	peer := n.peerLocked(value.Type)
	n.chatPeers[value.Id] = peer
	n.chatChangeInfo[value.Id] = value.Permissions != nil && value.Permissions.CanChangeInfo
	n.chatNotifications[value.Id] = cloneChatNotificationSettings(value.NotificationSettings)
	chat := domain.Chat{
		ID:                   domain.ChatID(value.Id),
		Kind:                 peer.kind,
		Title:                value.Title,
		Username:             n.peerUsernameLocked(peer),
		AccentColorID:        value.AccentColorId,
		Avatar:               avatarFromChatPhoto(value.Photo),
		UnreadCount:          int(value.UnreadCount),
		UnreadMentionCount:   int(value.UnreadMentionCount),
		Muted:                n.effectiveMutedLocked(value.Id, peer.kind),
		CanSend:              canSend(value.Permissions),
		CanReact:             canReact(value.Permissions),
		IsPinned:             mainPinned(value.Positions),
		IsArchived:           archivedChat(value.ChatLists, value.Positions),
		IsMarkedUnread:       value.IsMarkedAsUnread,
		CanDeleteForSelf:     value.CanBeDeletedOnlyForSelf,
		CanDeleteForAll:      value.CanBeDeletedForAllUsers,
		IsMember:             n.peerIsMemberLocked(peer),
		CanManageInviteLinks: n.peerCanManageInviteLinksLocked(peer),
		CanRestrictMembers:   n.peerCanRestrictMembersLocked(peer),
		CanPromoteMembers:    n.peerCanPromoteMembersLocked(peer),
		CanChangeInfo:        n.peerCanChangeInfoLocked(peer, n.chatChangeInfo[value.Id]),
		Order:                mainOrder(value.Positions),
		Draft:                draftMessage(value.DraftMessage),
	}
	if group := n.supergroups[peer.supergroupID]; group != nil {
		chat.IsForum = group.IsForum
	}
	// A chat sender can refer to the chat being introduced by this update.
	n.chats[value.Id] = chat
	lastMessage := n.messageLocked(value.LastMessage)
	setLastMessage(&chat, lastMessage)
	n.chats[value.Id] = chat
	return chat, lastMessage
}

func (n *normalizer) user(value *td.User) domain.User {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.userLocked(value)
}

func (n *normalizer) userLocked(value *td.User) domain.User {
	if value == nil {
		return domain.User{}
	}
	name := strings.TrimSpace(strings.Join([]string{value.FirstName, value.LastName}, " "))
	if name == "" {
		name = firstUsername(value.Usernames)
	}
	if name == "" {
		name = "Unknown"
	}
	user := domain.User{
		ID:            domain.UserID(value.Id),
		Name:          name,
		Username:      firstUsername(value.Usernames),
		AccentColorID: value.AccentColorId,
		Avatar:        avatarFromProfilePhoto(value.ProfilePhoto),
	}
	n.users[value.Id] = user
	return user
}

func (n *normalizer) message(value *td.Message) domain.Message {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.messageLocked(value)
}

func (n *normalizer) messageLocked(value *td.Message) domain.Message {
	if value == nil {
		return domain.Message{}
	}
	sender, senderName, senderAvatar := n.senderLocked(value.SenderId)
	accentID, accentKnown := n.senderAccentLocked(sender)
	kind, text, fileName, media := n.messageContentLocked(value.Content, value.SenderId)
	editedAt := time.Time{}
	if value.EditDate > 0 {
		editedAt = time.Unix(int64(value.EditDate), 0)
	}
	message := domain.Message{
		ID:                domain.MessageID(value.Id),
		ChatID:            domain.ChatID(value.ChatId),
		TopicID:           topicIDFromMessage(value.TopicId),
		Sender:            sender,
		SenderName:        senderName,
		SenderAvatar:      senderAvatar,
		SenderAccentID:    accentID,
		SenderAccentKnown: accentKnown,
		SentAt:            time.Unix(int64(value.Date), 0),
		EditedAt:          editedAt,
		Kind:              kind,
		Text:              text,
		Entities:          messageEntities(value.Content),
		FileName:          fileName,
		Media:             media,
		Outgoing:          value.IsOutgoing,
		Service:           kind == domain.MessageService,
		HasReply:          value.ReplyTo != nil,
		HasForward:        value.ForwardInfo != nil,
		Pinned:            value.IsPinned,
		Reactions:         reactionsFromMessageLocked(value.InteractionInfo),
	}
	if content, ok := value.Content.(*td.MessageSticker); ok {
		message.Sticker = stickerRef(content.Sticker)
	}
	if reply, ok := value.ReplyTo.(*td.MessageReplyToMessage); ok && (reply.ChatId == 0 || reply.ChatId == value.ChatId) && reply.MessageId > 0 {
		message.ReplyToMessageID = domain.MessageID(reply.MessageId)
	}
	switch state := value.SendingState.(type) {
	case *td.MessageSendingStatePending:
		message.SendState = domain.SendPending
	case *td.MessageSendingStateFailed:
		failure := normalizeTDError("send text", state.Error)
		message.SendState = domain.SendFailed
		message.Failure = &failure
	}
	return message
}

func (n *normalizer) topic(value *td.ForumTopic) domain.ForumTopic {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.topicLocked(value)
}

func (n *normalizer) topicLocked(value *td.ForumTopic) domain.ForumTopic {
	if value == nil {
		return domain.ForumTopic{}
	}
	topic := domain.ForumTopic{
		IsPinned:           value.IsPinned,
		UnreadCount:        int(value.UnreadCount),
		UnreadMentionCount: int(value.UnreadMentionCount),
		Order:              int64(value.Order),
		Draft:              draftMessage(value.DraftMessage),
	}
	if info := value.Info; info != nil {
		topic.ID = domain.TopicID(info.ForumTopicId)
		topic.ChatID = domain.ChatID(info.ChatId)
		topic.Name = info.Name
		if icon := info.Icon; icon != nil {
			topic.IconColor = icon.Color
		}
		topic.IsGeneral = info.IsGeneral
		topic.IsClosed = info.IsClosed
		topic.IsHidden = info.IsHidden
	}
	if lastMessage := n.messageLocked(value.LastMessage); lastMessage.ID != 0 {
		topic.LastMessage = lastMessage.DisplayText()
		topic.LastMessageAt = lastMessage.SentAt.Unix()
	}
	return topic
}

func topicIDFromMessage(value td.MessageTopic) domain.TopicID {
	if topic, ok := value.(*td.MessageTopicForum); ok && topic != nil {
		return domain.TopicID(topic.ForumTopicId)
	}
	return 0
}

func (n *normalizer) sender(value td.MessageSender) (domain.SenderRef, string, domain.AvatarRef) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.senderLocked(value)
}

func (n *normalizer) senderLocked(value td.MessageSender) (domain.SenderRef, string, domain.AvatarRef) {
	switch sender := value.(type) {
	case *td.MessageSenderUser:
		ref := domain.SenderRef{Kind: domain.SenderUser, ID: sender.UserId}
		if user, ok := n.users[sender.UserId]; ok {
			return ref, user.Name, user.Avatar
		}
		return ref, "Unknown", domain.AvatarRef{}
	case *td.MessageSenderChat:
		ref := domain.SenderRef{Kind: domain.SenderChat, ID: sender.ChatId}
		if chat, ok := n.chats[sender.ChatId]; ok {
			return ref, chat.Title, chat.Avatar
		}
		return ref, "Unknown", domain.AvatarRef{}
	default:
		return domain.SenderRef{}, "Unknown", domain.AvatarRef{}
	}
}

func (n *normalizer) senderAccentLocked(sender domain.SenderRef) (int32, bool) {
	switch sender.Kind {
	case domain.SenderUser:
		user, ok := n.users[sender.ID]
		return user.AccentColorID, ok
	case domain.SenderChat:
		chat, ok := n.chats[sender.ID]
		return chat.AccentColorID, ok
	}
	return 0, false
}

func staticDocumentThumbnail(value *td.Thumbnail) domain.MediaFileRef {
	if value == nil {
		return domain.MediaFileRef{}
	}
	switch value.Format.(type) {
	case *td.ThumbnailFormatJpeg, *td.ThumbnailFormatPng:
		return mediaFileRef(value.File)
	default:
		return domain.MediaFileRef{}
	}
}

func staticJPEGThumbnail(value *td.Thumbnail) domain.MediaFileRef {
	if value == nil {
		return domain.MediaFileRef{}
	}
	if _, ok := value.Format.(*td.ThumbnailFormatJpeg); !ok {
		return domain.MediaFileRef{}
	}
	return mediaFileRef(value.File)
}

func mediaFileRef(value *td.File) domain.MediaFileRef {
	if value == nil {
		return domain.MediaFileRef{}
	}
	ref := domain.MediaFileRef{
		ID:           value.Id,
		Size:         value.Size,
		ExpectedSize: value.ExpectedSize,
	}
	if value.Remote != nil {
		ref.UniqueID = value.Remote.UniqueId
	}
	if value.Local != nil {
		ref.LocalPath = value.Local.Path
		ref.CanDownload = value.Local.CanBeDownloaded
		ref.Downloaded = value.Local.IsDownloadingCompleted && value.Local.Path != ""
	}
	return ref
}

// messageEntities keeps TDLib's UTF-16 ranges only for text actually displayed
// as Message.Text. Other entity types require separate behavior (custom emoji,
// timestamps) or have no distinct terminal formatting.
func messageEntities(value td.MessageContent) []domain.TextEntity {
	var formatted *td.FormattedText
	switch content := value.(type) {
	case *td.MessageText:
		formatted = content.Text
	case *td.MessagePhoto:
		formatted = content.Caption
	case *td.MessageVideo:
		formatted = content.Caption
	case *td.MessageAudio:
		formatted = content.Caption
	case *td.MessageDocument:
		formatted = content.Caption
	case *td.MessageAnimation:
		formatted = content.Caption
	case *td.MessageVoiceNote:
		formatted = content.Caption
	}
	if formatted == nil {
		return nil
	}
	var result []domain.TextEntity
	for _, entity := range formatted.Entities {
		if entity == nil || entity.Offset < 0 || entity.Length <= 0 {
			continue
		}
		var kind domain.TextEntityKind
		var link domain.TextLinkKind
		var url string
		switch value := entity.Type.(type) {
		case *td.TextEntityTypeBold:
			kind = domain.EntityBold
		case *td.TextEntityTypeItalic:
			kind = domain.EntityItalic
		case *td.TextEntityTypeUnderline:
			kind = domain.EntityUnderline
		case *td.TextEntityTypeStrikethrough:
			kind = domain.EntityStrikethrough
		case *td.TextEntityTypeSpoiler:
			kind = domain.EntitySpoiler
		case *td.TextEntityTypeCode:
			kind = domain.EntityCode
		case *td.TextEntityTypePre, *td.TextEntityTypePreCode:
			kind = domain.EntityPre
		case *td.TextEntityTypeBlockQuote, *td.TextEntityTypeExpandableBlockQuote:
			kind = domain.EntityQuote
		case *td.TextEntityTypeTextUrl:
			kind = domain.EntityLink
			link = domain.LinkTextURL
			url = value.Url
		case *td.TextEntityTypeUrl:
			kind = domain.EntityLink
			link = domain.LinkPlainURL
		case *td.TextEntityTypeEmailAddress, *td.TextEntityTypePhoneNumber, *td.TextEntityTypeBankCardNumber:
			kind = domain.EntityLink
		case *td.TextEntityTypeMention, *td.TextEntityTypeMentionName:
			kind = domain.EntityMention
		case *td.TextEntityTypeHashtag, *td.TextEntityTypeCashtag:
			kind = domain.EntityTag
		case *td.TextEntityTypeBotCommand:
			kind = domain.EntityCommand
		default:
			continue
		}
		result = append(result, domain.TextEntity{Offset: int(entity.Offset), Length: int(entity.Length), Kind: kind, Link: link, URL: url})
	}
	return result
}

func (n *normalizer) messageContentLocked(value td.MessageContent, sender td.MessageSender) (domain.MessageKind, string, string, domain.MessageMedia) {
	var text string
	actor, name, _ := n.senderLocked(sender)
	if actor.Kind == domain.SenderUser && actor.ID != 0 {
		name = n.memberNameLocked(actor.ID)
	}
	switch content := value.(type) {
	case *td.MessageChatAddMembers:
		if len(content.MemberUserIds) == 1 && actor.Kind == domain.SenderUser && actor.ID != 0 && content.MemberUserIds[0] == actor.ID {
			text = name + " joined the group"
		} else {
			members := make([]string, len(content.MemberUserIds))
			for i, id := range content.MemberUserIds {
				members[i] = n.memberNameLocked(id)
			}
			if len(members) == 0 {
				text = name + " added members"
			} else {
				text = name + " added " + strings.Join(members, ", ")
			}
		}
	case *td.MessageChatJoinByLink:
		text = name + " joined the group via an invite link"
	case *td.MessageChatJoinByRequest:
		text = name + " joined the group after approval"
	case *td.MessageChatDeleteMember:
		if actor.Kind == domain.SenderUser && actor.ID != 0 && content.UserId == actor.ID {
			text = name + " left the group"
		} else {
			text = name + " removed " + n.memberNameLocked(content.UserId)
		}
	default:
		return messageContent(value)
	}
	return domain.MessageService, text, "", domain.MessageMedia{}
}

func (n *normalizer) memberNameLocked(id int64) string {
	if user, ok := n.users[id]; ok && user.Name != "" && user.Name != "Unknown" {
		return user.Name
	}
	return "User " + strconv.FormatInt(id, 10)
}

func messageContent(value td.MessageContent) (domain.MessageKind, string, string, domain.MessageMedia) {
	switch content := value.(type) {
	case *td.MessageText:
		if content.Text == nil {
			return domain.MessageText, "", "", domain.MessageMedia{}
		}
		return domain.MessageText, content.Text.Text, "", domain.MessageMedia{}
	case *td.MessagePhoto:
		caption := ""
		if content.Caption != nil {
			caption = content.Caption.Text
		}
		if content.Photo == nil {
			return domain.MessagePhoto, caption, "", domain.MessageMedia{}
		}
		return domain.MessagePhoto, caption, "", photoMedia(content.Photo.Sizes)
	case *td.MessageVideo:
		caption := ""
		if content.Caption != nil {
			caption = content.Caption.Text
		}
		if content.Video == nil {
			return domain.MessageVideo, caption, "", domain.MessageMedia{}
		}
		v := content.Video
		var thumbnail domain.MediaFileRef
		if v.Thumbnail != nil {
			thumbnail = mediaFileRef(v.Thumbnail.File)
		}
		w := int(v.Width)
		if w < 0 {
			w = 0
		}
		h := int(v.Height)
		if h < 0 {
			h = 0
		}
		dur := time.Duration(max(v.Duration, 0)) * time.Second
		return domain.MessageVideo, caption, v.FileName, domain.MessageMedia{
			File:      mediaFileRef(v.Video),
			Thumbnail: thumbnail,
			MIMEType:  v.MimeType,
			Width:     w,
			Height:    h,
			Duration:  dur,
		}
	case *td.MessageAudio:
		caption := ""
		if content.Caption != nil {
			caption = content.Caption.Text
		}
		if content.Audio == nil {
			return domain.MessageAudio, caption, "", domain.MessageMedia{}
		}
		a := content.Audio
		return domain.MessageAudio, caption, a.FileName, domain.MessageMedia{
			File:     mediaFileRef(a.Audio),
			MIMEType: a.MimeType,
			Duration: time.Duration(max(a.Duration, 0)) * time.Second,
		}
	case *td.MessageSticker:
		if content.Sticker == nil {
			return domain.MessageSticker, "", "", domain.MessageMedia{}
		}
		s := content.Sticker
		var thumbnail domain.MediaFileRef
		if s.Thumbnail != nil {
			thumbnail = mediaFileRef(s.Thumbnail.File)
		}
		w := int(s.Width)
		if w < 0 {
			w = 0
		}
		h := int(s.Height)
		if h < 0 {
			h = 0
		}
		return domain.MessageSticker, "", "", domain.MessageMedia{
			File:      mediaFileRef(s.Sticker),
			Thumbnail: thumbnail,
			Width:     w,
			Height:    h,
		}
	case *td.MessageDocument:
		caption := ""
		if content.Caption != nil {
			caption = content.Caption.Text
		}
		if content.Document == nil {
			return domain.MessageDocument, caption, "", domain.MessageMedia{}
		}
		d := content.Document
		return domain.MessageDocument, caption, d.FileName, domain.MessageMedia{
			File:      mediaFileRef(d.Document),
			Thumbnail: staticDocumentThumbnail(d.Thumbnail),
			MIMEType:  d.MimeType,
		}
	case *td.MessageAnimation:
		caption := ""
		if content.Caption != nil {
			caption = content.Caption.Text
		}
		if content.Animation == nil {
			return domain.MessageAnimation, caption, "", domain.MessageMedia{}
		}
		a := content.Animation
		var thumbnail domain.MediaFileRef
		// Never expose spoiler/secret previews. The thumbnail's own format,
		// rather than the animation main-file MIME, decides static eligibility.
		if !content.HasSpoiler && !content.IsSecret {
			thumbnail = staticJPEGThumbnail(a.Thumbnail)
		}
		w := int(a.Width)
		if w < 0 {
			w = 0
		}
		h := int(a.Height)
		if h < 0 {
			h = 0
		}
		return domain.MessageAnimation, caption, a.FileName, domain.MessageMedia{
			File:      mediaFileRef(a.Animation),
			Thumbnail: thumbnail,
			MIMEType:  a.MimeType,
			Width:     w,
			Height:    h,
			Duration:  time.Duration(max(a.Duration, 0)) * time.Second,
		}
	case *td.MessageVoiceNote:
		caption := ""
		if content.Caption != nil {
			caption = content.Caption.Text
		}
		if content.VoiceNote == nil {
			return domain.MessageVoiceNote, caption, "", domain.MessageMedia{}
		}
		v := content.VoiceNote
		// Voice notes never carry a renderable thumbnail.
		return domain.MessageVoiceNote, caption, "", domain.MessageMedia{
			File:     mediaFileRef(v.Voice),
			MIMEType: v.MimeType,
			Duration: time.Duration(max(v.Duration, 0)) * time.Second,
		}
	case *td.MessageVideoNote:
		if content.VideoNote == nil {
			return domain.MessageVideoNote, "", "", domain.MessageMedia{}
		}
		vn := content.VideoNote
		var thumbnail domain.MediaFileRef
		// JPEG thumbnail, gated by the secret flag.
		if !content.IsSecret {
			thumbnail = staticJPEGThumbnail(vn.Thumbnail)
		}
		length := int(vn.Length)
		if length < 0 {
			length = 0
		}
		return domain.MessageVideoNote, "", "", domain.MessageMedia{
			File:      mediaFileRef(vn.Video),
			Thumbnail: thumbnail,
			Width:     length,
			Height:    length,
			Duration:  time.Duration(max(vn.Duration, 0)) * time.Second,
		}
	default:
		return domain.MessageUnsupported, "", "", domain.MessageMedia{}
	}
}

func photoMedia(sizes []*td.PhotoSize) domain.MessageMedia {
	type sized struct {
		area   int64
		file   domain.MediaFileRef
		width  int
		height int
	}
	var valid []sized
	for _, s := range sizes {
		if s == nil || s.Photo == nil || s.Photo.Id == 0 {
			continue
		}
		w := int(s.Width)
		if w < 0 {
			w = 0
		}
		h := int(s.Height)
		if h < 0 {
			h = 0
		}
		valid = append(valid, sized{
			area:   int64(w) * int64(h),
			file:   mediaFileRef(s.Photo),
			width:  w,
			height: h,
		})
	}
	if len(valid) == 0 {
		return domain.MessageMedia{}
	}
	pickMax := func(comparisons []sized) sized {
		best := comparisons[0]
		for i := 1; i < len(comparisons); i++ {
			if comparisons[i].area > best.area {
				best = comparisons[i]
			} else if comparisons[i].area == best.area && comparisons[i].file.ID > best.file.ID {
				best = comparisons[i]
			}
		}
		return best
	}
	pickMin := func(comparisons []sized) sized {
		best := comparisons[0]
		for i := 1; i < len(comparisons); i++ {
			if comparisons[i].area < best.area {
				best = comparisons[i]
			} else if comparisons[i].area == best.area && comparisons[i].file.ID < best.file.ID {
				best = comparisons[i]
			}
		}
		return best
	}
	primary := pickMax(valid)
	thumbnail := pickMin(valid)
	if len(valid) == 1 {
		thumbnail = primary
	}
	return domain.MessageMedia{
		File:      primary.file,
		Thumbnail: thumbnail.file,
		Width:     primary.width,
		Height:    primary.height,
	}
}

func (n *normalizer) changeChatLocked(chatID int64, change func(*domain.Chat)) []Update {
	chat, ok := n.chats[chatID]
	if !ok {
		return nil
	}
	change(&chat)
	n.chats[chatID] = chat
	n.authoritativeChats[chatID] = true
	return []Update{ChatUpserted{Chat: chat}}
}

func (n *normalizer) refreshBasicGroupChatsLocked(basicGroupID int64) []Update {
	var chatIDs []int64
	for chatID, peer := range n.chatPeers {
		if peer.basicGroupID == basicGroupID {
			chatIDs = append(chatIDs, chatID)
		}
	}
	sort.Slice(chatIDs, func(i, j int) bool { return chatIDs[i] < chatIDs[j] })
	updates := make([]Update, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		chat := n.chats[chatID]
		chat.IsMember = chatMemberStatusIsMember(n.basicGroups[basicGroupID].Status)
		chat.CanManageInviteLinks = chatMemberStatusCanManageInviteLinks(n.basicGroups[basicGroupID].Status)
		chat.CanRestrictMembers = chatMemberStatusCanRestrictMembers(n.basicGroups[basicGroupID].Status)
		chat.CanPromoteMembers = chatMemberStatusCanPromoteMembers(n.basicGroups[basicGroupID].Status)
		chat.CanChangeInfo = n.peerCanChangeInfoLocked(n.chatPeers[chatID], n.chatChangeInfo[chatID])
		n.chats[chatID] = chat
		n.authoritativeChats[chatID] = true
		updates = append(updates, ChatUpserted{Chat: chat})
	}
	return updates
}

func (n *normalizer) refreshSupergroupChatsLocked(supergroupID int64) []Update {
	var chatIDs []int64
	for chatID, peer := range n.chatPeers {
		if peer.supergroupID == supergroupID {
			chatIDs = append(chatIDs, chatID)
		}
	}
	sort.Slice(chatIDs, func(i, j int) bool { return chatIDs[i] < chatIDs[j] })
	updates := make([]Update, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		chat := n.chats[chatID]
		peer := n.chatPeers[chatID]
		group := n.supergroups[supergroupID]
		if group.IsChannel {
			chat.Kind = domain.ChatChannel
			peer.kind = domain.ChatChannel
		} else {
			chat.Kind = domain.ChatSupergroup
			peer.kind = domain.ChatSupergroup
		}
		n.chatPeers[chatID] = peer
		chat.Username = firstUsername(group.Usernames)
		chat.IsForum = group.IsForum
		chat.IsMember = chatMemberStatusIsMember(group.Status)
		chat.CanManageInviteLinks = chatMemberStatusCanManageInviteLinks(group.Status)
		chat.CanRestrictMembers = chatMemberStatusCanRestrictMembers(group.Status)
		chat.CanPromoteMembers = chatMemberStatusCanPromoteMembers(group.Status)
		chat.CanChangeInfo = n.peerCanChangeInfoLocked(peer, n.chatChangeInfo[chatID])
		chat.Muted = n.effectiveMutedLocked(chatID, chat.Kind)
		n.chats[chatID] = chat
		n.authoritativeChats[chatID] = true
		updates = append(updates, ChatUpserted{Chat: chat})
	}
	return updates
}

func (n *normalizer) refreshNotificationScopeLocked(scope notificationScope) []Update {
	chatIDs := make([]int64, 0)
	for chatID, chat := range n.chats {
		settings := n.chatNotifications[chatID]
		if settings != nil && settings.UseDefaultMuteFor && notificationScopeForKind(chat.Kind) == scope {
			chatIDs = append(chatIDs, chatID)
		}
	}
	sort.Slice(chatIDs, func(i, j int) bool { return chatIDs[i] < chatIDs[j] })
	updates := make([]Update, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		chat := n.chats[chatID]
		chat.Muted = n.effectiveMutedLocked(chatID, chat.Kind)
		n.chats[chatID] = chat
		n.authoritativeChats[chatID] = true
		updates = append(updates, ChatUpserted{Chat: chat})
	}
	return updates
}

func (n *normalizer) effectiveMutedLocked(chatID int64, kind domain.ChatKind) bool {
	settings := n.chatNotifications[chatID]
	if settings == nil {
		return false
	}
	if !settings.UseDefaultMuteFor {
		return settings.MuteFor > 0
	}
	scopeSettings := n.scopeNotifications[notificationScopeForKind(kind)]
	return scopeSettings != nil && scopeSettings.MuteFor > 0
}

func (n *normalizer) peerLocked(value td.ChatType) chatPeer {
	switch chatType := value.(type) {
	case *td.ChatTypePrivate:
		return chatPeer{kind: domain.ChatPrivate, userID: chatType.UserId}
	case *td.ChatTypeBasicGroup:
		return chatPeer{kind: domain.ChatBasicGroup, basicGroupID: chatType.BasicGroupId}
	case *td.ChatTypeSupergroup:
		kind := domain.ChatSupergroup
		if chatType.IsChannel {
			kind = domain.ChatChannel
		}
		if group := n.supergroups[chatType.SupergroupId]; group != nil && group.IsChannel {
			kind = domain.ChatChannel
		}
		return chatPeer{kind: kind, supergroupID: chatType.SupergroupId}
	default:
		return chatPeer{kind: domain.ChatPrivate}
	}
}

func (n *normalizer) peerUsernameLocked(peer chatPeer) string {
	if peer.userID != 0 {
		return n.users[peer.userID].Username
	}
	if peer.supergroupID != 0 {
		if group := n.supergroups[peer.supergroupID]; group != nil {
			return firstUsername(group.Usernames)
		}
	}
	return ""
}

func connectionState(value td.ConnectionState) domain.ConnectionState {
	switch value.(type) {
	case *td.ConnectionStateReady:
		return domain.ConnectionOnline
	case *td.ConnectionStateWaitingForNetwork:
		return domain.ConnectionOffline
	case *td.ConnectionStateConnecting, *td.ConnectionStateConnectingToProxy, *td.ConnectionStateUpdating:
		return domain.ConnectionReconnecting
	default:
		return domain.ConnectionWaiting
	}
}

func firstUsername(value *td.Usernames) string {
	if value == nil || len(value.ActiveUsernames) == 0 {
		return ""
	}
	return value.ActiveUsernames[0]
}

func avatarFromProfilePhoto(value *td.ProfilePhoto) domain.AvatarRef {
	if value == nil {
		return domain.AvatarRef{}
	}
	return avatarFromFiles(value.Small, value.Big)
}

func avatarFromChatPhoto(value *td.ChatPhotoInfo) domain.AvatarRef {
	if value == nil {
		return domain.AvatarRef{}
	}
	return avatarFromFiles(value.Small, value.Big)
}

func avatarFromFiles(small, original *td.File) domain.AvatarRef {
	var avatar domain.AvatarRef
	if small != nil {
		avatar.FileID = small.Id
		if small.Remote != nil {
			avatar.UniqueID = small.Remote.UniqueId
		}
	}
	if original != nil {
		avatar.OriginalFileID = original.Id
		if original.Remote != nil {
			avatar.OriginalUniqueID = original.Remote.UniqueId
		}
	}
	return avatar
}

func cloneSupergroup(value *td.Supergroup) *td.Supergroup {
	return &td.Supergroup{
		Id:        value.Id,
		Usernames: cloneUsernames(value.Usernames),
		Status:    cloneChatMemberStatus(value.Status),
		IsChannel: value.IsChannel,
		IsForum:   value.IsForum,
	}
}

func cloneBasicGroup(value *td.BasicGroup) *td.BasicGroup {
	return &td.BasicGroup{
		Id:                     value.Id,
		MemberCount:            value.MemberCount,
		Status:                 cloneChatMemberStatus(value.Status),
		IsActive:               value.IsActive,
		UpgradedToSupergroupId: value.UpgradedToSupergroupId,
	}
}

func cloneChatMemberStatus(value td.ChatMemberStatus) td.ChatMemberStatus {
	switch status := value.(type) {
	case *td.ChatMemberStatusCreator:
		if status == nil {
			return nil
		}
		copy := *status
		return &copy
	case *td.ChatMemberStatusAdministrator:
		if status == nil {
			return nil
		}
		copy := *status
		if status.Rights != nil {
			rights := *status.Rights
			copy.Rights = &rights
		}
		return &copy
	case *td.ChatMemberStatusMember:
		if status == nil {
			return nil
		}
		copy := *status
		return &copy
	case *td.ChatMemberStatusRestricted:
		if status == nil {
			return nil
		}
		copy := *status
		if status.Permissions != nil {
			permissions := *status.Permissions
			copy.Permissions = &permissions
		}
		return &copy
	case *td.ChatMemberStatusLeft:
		if status == nil {
			return nil
		}
		return &td.ChatMemberStatusLeft{}
	case *td.ChatMemberStatusBanned:
		if status == nil {
			return nil
		}
		copy := *status
		return &copy
	default:
		return nil
	}
}

func chatMemberStatusIsMember(value td.ChatMemberStatus) bool {
	switch status := value.(type) {
	case *td.ChatMemberStatusCreator:
		return status != nil && status.IsMember
	case *td.ChatMemberStatusAdministrator, *td.ChatMemberStatusMember:
		return status != nil
	case *td.ChatMemberStatusRestricted:
		return status != nil && status.IsMember
	default:
		return false
	}
}

func chatMemberStatusCanManageInviteLinks(value td.ChatMemberStatus) bool {
	switch status := value.(type) {
	case *td.ChatMemberStatusCreator:
		return status != nil && status.IsMember
	case *td.ChatMemberStatusAdministrator:
		return status != nil && status.Rights != nil && status.Rights.CanInviteUsers
	default:
		return false
	}
}

func chatMemberStatusCanRestrictMembers(value td.ChatMemberStatus) bool {
	switch status := value.(type) {
	case *td.ChatMemberStatusCreator:
		return status != nil && status.IsMember
	case *td.ChatMemberStatusAdministrator:
		return status != nil && status.Rights != nil && status.Rights.CanRestrictMembers
	default:
		return false
	}
}

func chatMemberStatusCanPromoteMembers(value td.ChatMemberStatus) bool {
	switch status := value.(type) {
	case *td.ChatMemberStatusCreator:
		return status != nil && status.IsMember
	case *td.ChatMemberStatusAdministrator:
		return status != nil && status.Rights != nil && status.Rights.CanPromoteMembers
	default:
		return false
	}
}

func chatMemberStatusCanChangeInfo(value td.ChatMemberStatus, defaultAllowed bool) bool {
	switch status := value.(type) {
	case *td.ChatMemberStatusCreator:
		return status != nil && status.IsMember
	case *td.ChatMemberStatusAdministrator:
		return status != nil && status.Rights != nil && status.Rights.CanChangeInfo
	case *td.ChatMemberStatusMember:
		return status != nil && defaultAllowed
	case *td.ChatMemberStatusRestricted:
		return status != nil && status.IsMember && status.Permissions != nil && status.Permissions.CanChangeInfo
	default:
		return false
	}
}

func (n *normalizer) peerCanChangeInfoLocked(peer chatPeer, defaultAllowed bool) bool {
	if peer.basicGroupID != 0 {
		if group := n.basicGroups[peer.basicGroupID]; group != nil {
			return chatMemberStatusCanChangeInfo(group.Status, defaultAllowed)
		}
	}
	if peer.supergroupID != 0 {
		if group := n.supergroups[peer.supergroupID]; group != nil {
			return chatMemberStatusCanChangeInfo(group.Status, defaultAllowed && !group.IsChannel)
		}
	}
	return false
}

func (n *normalizer) peerCanManageInviteLinksLocked(peer chatPeer) bool {
	if peer.basicGroupID != 0 {
		if group := n.basicGroups[peer.basicGroupID]; group != nil {
			return chatMemberStatusCanManageInviteLinks(group.Status)
		}
	}
	if peer.supergroupID != 0 {
		if group := n.supergroups[peer.supergroupID]; group != nil {
			return chatMemberStatusCanManageInviteLinks(group.Status)
		}
	}
	return false
}

func (n *normalizer) peerCanRestrictMembersLocked(peer chatPeer) bool {
	if peer.basicGroupID != 0 {
		if group := n.basicGroups[peer.basicGroupID]; group != nil {
			return chatMemberStatusCanRestrictMembers(group.Status)
		}
	}
	if peer.supergroupID != 0 {
		if group := n.supergroups[peer.supergroupID]; group != nil {
			return chatMemberStatusCanRestrictMembers(group.Status)
		}
	}
	return false
}

func (n *normalizer) peerCanPromoteMembersLocked(peer chatPeer) bool {
	if peer.basicGroupID != 0 {
		if group := n.basicGroups[peer.basicGroupID]; group != nil {
			return chatMemberStatusCanPromoteMembers(group.Status)
		}
	}
	if peer.supergroupID != 0 {
		if group := n.supergroups[peer.supergroupID]; group != nil {
			return chatMemberStatusCanPromoteMembers(group.Status)
		}
	}
	return false
}

func (n *normalizer) peerIsMemberLocked(peer chatPeer) bool {
	if peer.basicGroupID != 0 {
		if group := n.basicGroups[peer.basicGroupID]; group != nil {
			return chatMemberStatusIsMember(group.Status)
		}
	}
	if peer.supergroupID != 0 {
		if group := n.supergroups[peer.supergroupID]; group != nil {
			return chatMemberStatusIsMember(group.Status)
		}
	}
	return false
}

func cloneUsernames(value *td.Usernames) *td.Usernames {
	if value == nil {
		return nil
	}
	return &td.Usernames{
		ActiveUsernames:      append([]string(nil), value.ActiveUsernames...),
		DisabledUsernames:    append([]string(nil), value.DisabledUsernames...),
		EditableUsername:     value.EditableUsername,
		CollectibleUsernames: append([]string(nil), value.CollectibleUsernames...),
	}
}

func cloneChatNotificationSettings(value *td.ChatNotificationSettings) *td.ChatNotificationSettings {
	if value == nil {
		return nil
	}
	settings := *value
	return &settings
}

func notificationScopeOf(value td.NotificationSettingsScope) (notificationScope, bool) {
	switch value.(type) {
	case *td.NotificationSettingsScopePrivateChats:
		return notificationScopePrivate, true
	case *td.NotificationSettingsScopeGroupChats:
		return notificationScopeGroup, true
	case *td.NotificationSettingsScopeChannelChats:
		return notificationScopeChannel, true
	default:
		return 0, false
	}
}

func notificationScopeForKind(kind domain.ChatKind) notificationScope {
	switch kind {
	case domain.ChatBasicGroup, domain.ChatSupergroup:
		return notificationScopeGroup
	case domain.ChatChannel:
		return notificationScopeChannel
	default:
		return notificationScopePrivate
	}
}

func canSend(value *td.ChatPermissions) bool {
	return value != nil && value.CanSendBasicMessages
}

func canReact(value *td.ChatPermissions) bool {
	return value != nil && value.CanReactToMessages
}

func reactionsFromLocked(values []*td.MessageReaction) []domain.MessageReaction {
	reactions := make([]domain.MessageReaction, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		emoji, ok := value.Type.(*td.ReactionTypeEmoji)
		if !ok || emoji == nil {
			continue
		}
		reactions = append(reactions, domain.MessageReaction{
			Emoji:  emoji.Emoji,
			Count:  int(value.TotalCount),
			Chosen: value.IsChosen,
		})
	}
	return reactions
}

func reactionsFromMessageLocked(value *td.MessageInteractionInfo) []domain.MessageReaction {
	if value == nil || value.Reactions == nil {
		return nil
	}
	return reactionsFromLocked(value.Reactions.Reactions)
}

func mainPinned(positions []*td.ChatPosition) bool {
	for _, position := range positions {
		if position != nil && isMainList(position.List) && position.Order != 0 {
			return position.IsPinned
		}
	}
	return false
}

func archivedChat(lists []td.ChatList, positions []*td.ChatPosition) bool {
	for _, list := range lists {
		if _, ok := list.(*td.ChatListArchive); ok {
			return true
		}
	}
	for _, position := range positions {
		if position != nil && position.Order != 0 {
			if _, ok := position.List.(*td.ChatListArchive); ok {
				return true
			}
		}
	}
	return false
}

func mainOrder(positions []*td.ChatPosition) int64 {
	for _, position := range positions {
		if position != nil && isMainList(position.List) && position.Order != 0 {
			return int64(position.Order)
		}
	}
	return 0
}

func isMainList(list td.ChatList) bool {
	_, ok := list.(*td.ChatListMain)
	return ok
}

func draftMessage(value *td.DraftMessage) domain.Draft {
	if value == nil {
		return domain.Draft{}
	}
	content, ok := value.InputMessageText.(*td.InputMessageText)
	if !ok || content == nil {
		return domain.Draft{}
	}
	draft := domain.Draft{Date: int64(value.Date)}
	if content.Text != nil {
		draft.Text = content.Text.Text
	}
	if reply, ok := value.ReplyTo.(*td.InputMessageReplyToMessage); ok && reply != nil && reply.MessageId > 0 {
		draft.ReplyToMessageID = domain.MessageID(reply.MessageId)
	}
	if draft.Text == "" && draft.ReplyToMessageID == 0 {
		return domain.Draft{}
	}
	return draft
}

func setLastMessage(chat *domain.Chat, message domain.Message) {
	if message.ID == 0 {
		chat.LastMessage = ""
		chat.LastMessageAt = 0
		return
	}
	chat.LastMessage = message.DisplayText()
	chat.LastMessageAt = message.SentAt.Unix()
}

func normalizeTDError(op string, value *td.Error) domain.AppError {
	if value == nil {
		return normalizeError(op, nil)
	}
	return normalizeError(op, td.ResponseError{Err: value})
}
