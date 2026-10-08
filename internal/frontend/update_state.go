package frontend

import (
	"image/color"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zylen-det/tuilegram/internal/auth"
	"github.com/zylen-det/tuilegram/internal/domain"
	"github.com/zylen-det/tuilegram/internal/media/avatar"
	"github.com/zylen-det/tuilegram/internal/media/pixel"
	"github.com/zylen-det/tuilegram/internal/media/thumbnail"
	"github.com/zylen-det/tuilegram/internal/telegram"
)

const (
	pageSize           = 50
	maxMessagesPerChat = 500
)

var ReactionPalette = []string{"👍", "❤️", "🔥", "😁", "😮", "😢", "🙏", "🎉"}

// updateState applies one event to the caller-owned state in place and returns
// the effects the application loop must deliver. AppModel.Update is the sole
// authoritative mutation loop; no other goroutine may call this.
func updateState(state *State, raw Event) []Effect {
	event := normalizeEvent(raw)
	if event == nil {
		return nil
	}

	// Composer typing is the hottest reducer path. It updates only a draft or
	// the active edit target, so it is dispatched ahead of the general switch.
	if event, ok := event.(ComposerValueChanged); ok {
		return reduceComposerValueChanged(state, event)
	}

	// The remaining value events each own exactly one overlay (auth prompt,
	// photo path, message search, chat search, chat settings input).
	switch event := event.(type) {
	case PromptValueChanged:
		return reducePromptValueChanged(state, event)
	case PhotoPathValueChanged:
		return reducePhotoPathValueChanged(state, event)
	case MessageSearchValueChanged:
		return reduceMessageSearchValueChanged(state, event)
	case ChatSearchValueChanged:
		return reduceChatSearchValueChanged(state, event)
	case ChatSettingsValueChanged:
		return reduceChatSettingsValueChanged(state, event)
	}

	switch event := event.(type) {
	case Started:
		return []Effect{LoadBootstrap{}}
	case Resized:
		state.Width, state.Height = event.Width, event.Height
		state.Layout = layoutForSize(event.Width, event.Height)
		clampAllHistoryOffsets(state)
		resizeStickerPicker(state)
	case ChatSettingsLoaded:
		return reduceChatSettingsLoaded(state, event)
	case ChatSettingsLoadFailed:
		return reduceChatSettingsLoadFailed(state, event)
	case ChatSettingSaved:
		return reduceChatSettingSaved(state, event)
	case ChatSettingSaveFailed:
		return reduceChatSettingSaveFailed(state, event)
	case ActionReceived:
		if state.Quitting {
			return nil
		}
		if event.Action == Quit {
			state.Quitting = true
			commands := []Effect{BeginShutdown{}}
			if chatID, ok := activeChatID(*state); ok {
				commands = append(commands, CloseChatCommand{ChatID: chatID})
			}
			return commands
		}
		if state.Prompt != nil {
			return reducePromptAction(state, event)
		}
		return reduceAction(state, event)
	case ChatMessagesSearched:
		return reduceChatMessagesSearched(state, event)
	case ChatMessagesSearchFailed:
		return reduceChatMessagesSearchFailed(state, event)
	case SearchMessageContextLoaded:
		if state.MessageMenu != nil && state.MessageMenu.JumpRequestID == event.RequestID {
			return reduceReferencedMessageLoaded(state, event)
		}
		return reduceSearchMessageContextLoaded(state, event)
	case SearchMessageContextFailed:
		if state.MessageMenu != nil && state.MessageMenu.JumpRequestID == event.RequestID {
			return reduceReferencedMessageFailed(state, event)
		}
		return reduceSearchMessageContextFailed(state, event)
	case PublicChatSearched:
		return reducePublicChatSearched(state, event)
	case PublicChatSearchFailed:
		return reducePublicChatSearchFailed(state, event)
	case PublicChatsSearched:
		return reducePublicChatsSearched(state, event)
	case PublicChatsSearchFailed:
		return reducePublicChatsSearchFailed(state, event)
	case AllMessagesSearched:
		return reduceAllMessagesSearched(state, event)
	case AllMessagesSearchFailed:
		return reduceAllMessagesSearchFailed(state, event)
	case UserInfoLoaded:
		return reduceUserInfoLoaded(state, event)
	case UserInfoLoadFailed:
		return reduceUserInfoFailed(state, event)
	case MembersLoaded:
		return reduceMembersLoaded(state, event)
	case MembersLoadFailed:
		return reduceMembersLoadFailed(state, event)
	case InviteLinksLoaded:
		return reduceInviteLinksLoaded(state, event)
	case InviteLinksLoadFailed:
		return reduceInviteLinksLoadFailed(state, event)
	case InviteLinkCreated:
		return reduceInviteLinkCreated(state, event)
	case InviteLinkCreateFailed:
		return reduceInviteLinkCreateFailed(state, event)
	case InviteLinkRevoked:
		return reduceInviteLinkRevoked(state, event)
	case InviteLinkRevokeFailed:
		return reduceInviteLinkRevokeFailed(state, event)
	case InviteLinkCopied:
		return reduceInviteLinkCopied(state, event)
	case InviteLinkCopyFailed:
		return reduceInviteLinkCopyFailed(state, event)
	case AdministrationLoaded:
		return reduceAdministrationLoaded(state, event)
	case MemberAdministrationLoaded:
		return reduceMemberAdministrationLoaded(state, event)
	case AdministrationLoadFailed:
		if state.Administration != nil && state.Administration.RequestID == event.RequestID && state.Administration.ChatID == event.ChatID && state.Administration.Loading {
			state.Administration.Loading = false
			state.Administration.Error = &domain.AppError{Kind: domain.ErrorNetwork, Op: "load administration", Message: "Could not load administration"}
		}
		return nil
	case MemberAdministrationLoadFailed:
		if state.Administration != nil && state.Administration.RequestID == event.RequestID && state.Administration.ChatID == event.ChatID && state.Administration.UserID == event.UserID && state.Administration.MemberLoading {
			state.Administration.MemberLoading = false
			state.Administration.Error = &domain.AppError{Kind: domain.ErrorNetwork, Op: "load administration", Message: "Could not load administration"}
		}
		return nil
	case DefaultPermissionsSaved:
		if state.Administration != nil && state.Administration.RequestID == event.RequestID && state.Administration.ChatID == event.ChatID && state.Administration.UserID == 0 && state.Administration.Mode == AdministrationDefaultPermissions && state.Administration.Working && administrationChatActive(*state, event.ChatID) {
			adminToast(state, "Permissions saved")
			state.Administration = nil
			state.Focus = FocusDetails
		}
		return nil
	case DefaultPermissionsSaveFailed:
		if state.Administration != nil && state.Administration.RequestID == event.RequestID && state.Administration.ChatID == event.ChatID && state.Administration.UserID == 0 && state.Administration.Mode == AdministrationDefaultPermissions && state.Administration.Working && administrationChatActive(*state, event.ChatID) {
			state.Administration.Working = false
			state.Administration.Notice = "Could not save permissions"
			adminToast(state, "Could not save permissions")
		}
		return nil
	case MemberAdministrationApplied:
		if state.Administration != nil && state.Administration.RequestID == event.RequestID && state.Administration.ChatID == event.ChatID && state.Administration.UserID == event.UserID && state.Administration.PendingAction == event.Action && state.Administration.Working && administrationChatActive(*state, event.ChatID) {
			adminToast(state, "Member updated")
			state.Administration = nil
			state.Focus = FocusDetails
			commands := openMembers(state)
			if state.Members != nil {
				state.Members.Notice = "Member updated"
			}
			return commands
		}
		return nil
	case MemberAdministrationApplyFailed:
		if state.Administration != nil && state.Administration.RequestID == event.RequestID && state.Administration.ChatID == event.ChatID && state.Administration.UserID == event.UserID && state.Administration.PendingAction == event.Action && state.Administration.Working && administrationChatActive(*state, event.ChatID) {
			state.Administration.Working = false
			state.Administration.Notice = "Could not update member"
			adminToast(state, "Could not update member")
		}
		return nil
	case MemberUsernameCopied:
		return reduceMemberUsernameCopied(state, event)
	case MemberUsernameCopyFailed:
		return reduceMemberUsernameCopyFailed(state, event)
	case MemberContactChanged:
		return reduceMemberContactChanged(state, event)
	case MemberContactFailed:
		return reduceMemberContactFailed(state, event)
	case MemberBlockChanged:
		return reduceMemberBlockChanged(state, event)
	case MemberBlockFailed:
		return reduceMemberBlockFailed(state, event)
	case ChatActionApplied:
		return reduceChatActionApplied(state, event)
	case ChatActionFailed:
		return reduceChatActionFailed(state, event)
	case PinnedMessagesLoaded:
		return reducePinnedMessagesLoaded(state, event)
	case PinnedMessagesLoadFailed:
		return reducePinnedMessagesLoadFailed(state, event)
	case PinnedMessageContextLoaded:
		return reducePinnedMessageContextLoaded(state, event)
	case PinnedMessageContextFailed:
		return reducePinnedMessageContextFailed(state, event)
	case BotCommandsLoaded:
		return reduceBotCommandsLoaded(state, event)
	case BotCommandsLoadFailed:
		return reduceBotCommandsLoadFailed(state, event)
	case TelegramEvent:
		return reduceTelegramUpdate(state, event)
	case DraftSaved:
		if key, ok := showAllActive(*state); ok && event.ChatID == key.ChatID && event.TopicID == 0 {
			syncState, exists := state.TopicDraftSync[key]
			if exists && syncState.RequestID == event.RequestID {
				syncState.Pending = false
				if syncState.Draft.Text != "" || syncState.Draft.ReplyToMessageID > 0 {
					syncState.Draft.Date = event.Date
					if state.TopicDraftDates == nil {
						state.TopicDraftDates = make(map[topicKey]int64)
					}
					state.TopicDraftDates[key] = event.Date
				} else {
					syncState.Draft.Date = 0
					delete(state.TopicDraftDates, key)
				}
				state.TopicDraftSync[key] = syncState
			}
			break
		}
		if event.TopicID != 0 {
			key := topicKey{ChatID: event.ChatID, TopicID: event.TopicID}
			syncState, exists := state.TopicDraftSync[key]
			if exists && syncState.RequestID == event.RequestID {
				syncState.Pending = false
				if syncState.Draft.Text != "" || syncState.Draft.ReplyToMessageID > 0 {
					syncState.Draft.Date = event.Date
					if state.TopicDraftDates == nil {
						state.TopicDraftDates = make(map[topicKey]int64)
					}
					state.TopicDraftDates[key] = event.Date
				} else {
					syncState.Draft.Date = 0
					delete(state.TopicDraftDates, key)
				}
				state.TopicDraftSync[key] = syncState
				setForumTopicDraftSnapshot(state, key, syncState.Draft)
			}
			break
		}
		syncState, exists := state.DraftSync[event.ChatID]
		if exists && syncState.RequestID == event.RequestID {
			syncState.Pending = false
			if syncState.Draft.Text != "" || syncState.Draft.ReplyToMessageID > 0 {
				syncState.Draft.Date = event.Date
				state.DraftDates[event.ChatID] = event.Date
			} else {
				syncState.Draft.Date = 0
				delete(state.DraftDates, event.ChatID)
			}
			state.DraftSync[event.ChatID] = syncState
			setChatDraftSnapshot(state, event.ChatID, syncState.Draft)
		}
	case DraftSaveFailed:
		if event.TopicID == 0 {
			if key, ok := showAllActive(*state); ok && event.ChatID == key.ChatID {
				syncState, exists := state.TopicDraftSync[key]
				if exists && syncState.RequestID == event.RequestID {
					syncState.Pending = false
					state.TopicDraftSync[key] = syncState
					setToast(state, draftSyncError(), 3*time.Second)
				}
				break
			}
		}
		if event.TopicID != 0 {
			key := topicKey{ChatID: event.ChatID, TopicID: event.TopicID}
			syncState, exists := state.TopicDraftSync[key]
			if exists && syncState.RequestID == event.RequestID {
				syncState.Pending = false
				state.TopicDraftSync[key] = syncState
				setToast(state, draftSyncError(), 3*time.Second)
			}
			break
		}
		syncState, exists := state.DraftSync[event.ChatID]
		if exists && syncState.RequestID == event.RequestID {
			syncState.Pending = false
			state.DraftSync[event.ChatID] = syncState
			setToast(state, draftSyncError(), 3*time.Second)
		}
	case TerminalFocusChanged:
		state.TerminalFocused = event.Focused
	case ChatsLoaded:
		return reduceChatsLoaded(state, event)
	case ChatsLoadFailed:
		if event.RequestID == state.ChatRequestID {
			state.ChatsLoading = false
			failure := event.Error
			state.ChatsError = &failure
		}
	case TopicsLoaded:
		return reduceTopicsLoaded(state, event)
	case TopicsLoadFailed:
		return reduceTopicsLoadFailed(state, event)
	case MessagesLoaded:
		return reduceMessagesLoaded(state, event)
	case MessagesLoadFailed:
		if event.TopicID != 0 {
			key := topicKey{ChatID: event.ChatID, TopicID: event.TopicID}
			history, exists := state.TopicHistory[key]
			if exists && history.RequestID == event.RequestID {
				history.Loading = false
				failure := event.Error
				history.Error = &failure
				state.TopicHistory[key] = history
				if activeID, active := activeChatID(*state); active && activeID == event.ChatID && state.SelectedTopics[event.ChatID] == event.TopicID {
					failure := event.Error
					setToast(state, failure, 4*time.Second)
				}
			}
			break
		}
		history, exists := state.History[event.ChatID]
		if exists && history.RequestID == event.RequestID {
			history.Loading = false
			failure := event.Error
			history.Error = &failure
			state.History[event.ChatID] = history
			if activeID, active := activeChatID(*state); active && activeID == event.ChatID {
				failure := event.Error
				setToast(state, failure, 4*time.Second)
			}
		}
	case MessagePropertiesLoaded:
		if messageMenuMatches(state.MessageMenu, event.RequestID, event.ChatID, event.MessageID) {
			selectedAction := selectedMenuAction(state.MessageMenu)
			state.MessageMenu.Capabilities = event.Capabilities
			state.MessageMenu.Loading = false
			state.MessageMenu.Error = nil
			selectMessageMenuAction(state.MessageMenu, selectedAction)
			if state.MessageMenu.PreferEdit && event.Capabilities.Edit {
				selectMessageMenuAction(state.MessageMenu, EditMessage)
			}
		}
	case MessagePropertiesLoadFailed:
		if messageMenuMatches(state.MessageMenu, event.RequestID, event.ChatID, event.MessageID) {
			state.MessageMenu.Capabilities = domain.MessageCapabilities{Copy: true}
			state.MessageMenu.Loading = false
			failure := domain.AppError{Kind: domain.ErrorInternal, Op: "get message properties", Message: "Could not load message actions"}
			state.MessageMenu.Error = &failure
			clampMessageMenuSelection(state.MessageMenu)
		}
	case TextQueued:
		replaceQueuedMessage(state, event)
	case TextQueueFailed:
		failQueuedMessage(state, event)
	case PhotoQueued:
		replaceQueuedPhoto(state, event)
		if idx := messageIndex(state.Messages[event.ChatID], event.Message.ID); idx >= 0 {
			msg := state.Messages[event.ChatID][idx]
			if cmds := requestMissingThumbnails(state, []domain.Message{msg}); len(cmds) > 0 {
				return cmds
			}
		}
	case PhotoQueueFailed:
		failQueuedPhoto(state, event)
	case VideoQueued:
		replaceQueuedVideo(state, event)
		if idx := messageIndex(state.Messages[event.ChatID], event.Message.ID); idx >= 0 {
			msg := state.Messages[event.ChatID][idx]
			if cmds := requestMissingThumbnails(state, []domain.Message{msg}); len(cmds) > 0 {
				return cmds
			}
		}
	case VideoQueueFailed:
		failQueuedVideo(state, event)
	case AudioQueued:
		replaceQueuedAudio(state, event)
	case AudioQueueFailed:
		failQueuedAudio(state, event)
	case DocumentQueued:
		replaceQueuedDocument(state, event)
		if idx := messageIndex(state.Messages[event.ChatID], event.Message.ID); idx >= 0 {
			msg := state.Messages[event.ChatID][idx]
			if cmds := requestMissingThumbnails(state, []domain.Message{msg}); len(cmds) > 0 {
				return cmds
			}
		}
	case DocumentQueueFailed:
		failQueuedDocument(state, event)
	case StickersLoaded:
		return reduceStickersLoaded(state, event)
	case StickersLoadFailed:
		return reduceStickersLoadFailed(state, event)
	case StickerThumbnailRendered:
		return reduceStickerThumbnailRendered(state, event)
	case StickerThumbnailFailed:
		return reduceStickerThumbnailFailed(state, event)
	case StickerQueued:
		if !replaceQueuedSticker(state, event) {
			return nil
		}
		if idx := messageIndex(state.Messages[event.ChatID], event.Message.ID); idx >= 0 {
			message := state.Messages[event.ChatID][idx]
			if commands := requestMissingThumbnails(state, []domain.Message{message}); len(commands) > 0 {
				return commands
			}
		}
	case StickerQueueFailed:
		failQueuedSticker(state, event)
	case TextEdited:
		if editTargetMatches(state.EditTarget, event.RequestID, event.ChatID, event.MessageID) {
			if index := messageIndex(state.Messages[event.ChatID], event.MessageID); index >= 0 {
				original := state.Messages[event.ChatID][index]
				replacement := original
				replacement.Kind = event.Message.Kind
				replacement.Text = event.Message.Text
				replacement.Entities = append([]domain.TextEntity(nil), event.Message.Entities...)
				if !event.Message.EditedAt.IsZero() {
					replacement.EditedAt = event.Message.EditedAt
				}
				state.Messages[event.ChatID][index] = replacement
			}
			state.EditTarget = nil
			state.Focus = FocusComposer
			restoreActiveDraftReply(state)
		}
	case TextEditFailed:
		if editTargetMatches(state.EditTarget, event.RequestID, event.ChatID, event.MessageID) {
			state.EditTarget.Submitting = false
			failure := domain.AppError{Kind: domain.ErrorInternal, Op: "edit text", Message: "Edit failed"}
			state.EditTarget.Error = &failure
		}
	case MessageDeleted:
		if messageMenuMatches(state.MessageMenu, event.RequestID, event.ChatID, event.MessageID) {
			clearDraftReply := state.DraftReplies[event.ChatID] == event.MessageID
			deleteMessageSuccess(state, event.ChatID, event.MessageID)
			if clearDraftReply {
				delete(state.DraftReplies, event.ChatID)
				return []Effect{queueDraftSave(state, event.ChatID)}
			}
		}
	case MessageDeleteFailed:
		if messageMenuMatches(state.MessageMenu, event.RequestID, event.ChatID, event.MessageID) {
			failure := domain.AppError{Kind: domain.ErrorInternal, Op: "delete message", Message: "Delete failed"}
			setToast(state, failure, 3*time.Second)
		}
	case MessagePinChanged:
		if messageMenuMatches(state.MessageMenu, event.RequestID, event.ChatID, event.MessageID) {
			if index := messageIndex(state.Messages[event.ChatID], event.MessageID); index >= 0 {
				state.Messages[event.ChatID][index].Pinned = event.Pinned
			}
			label := "Message unpinned"
			if event.Pinned {
				label = "Message pinned"
			}
			success := domain.AppError{Message: label}
			setToast(state, success, 2*time.Second)
			state.MessageMenu = nil
			state.Focus = FocusConversation
		}
	case MessagePinFailed:
		if messageMenuMatches(state.MessageMenu, event.RequestID, event.ChatID, event.MessageID) {
			label := "Pin failed"
			if state.MessageMenu.Pinned {
				label = "Unpin failed"
			}
			failure := domain.AppError{Kind: domain.ErrorInternal, Op: "pin message", Message: label}
			setToast(state, failure, 3*time.Second)
			state.MessageMenu = nil
			state.Focus = FocusConversation
		}
	case ReactionChanged:
		if reactionPickerMatches(state.ReactionPicker, event.RequestID, event.ChatID, event.MessageID) {
			label := "Reaction added"
			if event.Removed {
				label = "Reaction removed"
			}
			success := domain.AppError{Message: label}
			setToast(state, success, 2*time.Second)
			state.ReactionPicker = nil
			state.Focus = FocusConversation
		}
	case ReactionFailed:
		if reactionPickerMatches(state.ReactionPicker, event.RequestID, event.ChatID, event.MessageID) {
			failure := domain.AppError{Kind: domain.ErrorInternal, Op: "react to message", Message: "Reaction failed"}
			setToast(state, failure, 3*time.Second)
			state.ReactionPicker = nil
			state.Focus = FocusConversation
		}
	case MessageForwarded:
		if forwardPickerMatches(state.ForwardPicker, event.RequestID, event.DestinationChatID, state.Chats) {
			state.ForwardPicker = nil
			state.Focus = FocusConversation
			success := domain.AppError{Message: "Message forwarded"}
			setToast(state, success, 2*time.Second)
		}
	case MessageForwardFailed:
		if forwardPickerMatches(state.ForwardPicker, event.RequestID, event.DestinationChatID, state.Chats) {
			state.ForwardPicker = nil
			state.Focus = FocusConversation
			failure := domain.AppError{Kind: domain.ErrorInternal, Op: "forward message", Message: "Forward failed"}
			setToast(state, failure, 3*time.Second)
		}
	case AvatarRendered:
		entry := state.Avatars[event.Key]
		entry.Loading = false
		entry.Error = nil
		entry.Cells = clonePixelAvatar(event.Cells)
		state.Avatars[event.Key] = entry
	case AvatarRenderFailed:
		entry, exists := state.Avatars[event.Key]
		if exists {
			entry.Loading = false
			failure := event.Error
			entry.Error = &failure
			width, height := avatarPixelDimensions(entry.Role)
			entry.Cells = pixel.Placeholder(entry.Label, width, height, color.NRGBA{A: 255})
			state.Avatars[event.Key] = entry
		}
	case AvatarOpened:
		if state.Modal != nil && state.Modal.RequestID == event.RequestID {
			state.Modal.Loading = false
			state.Modal.Error = nil
			state.Modal.Title = event.Title
			state.Modal.Path = event.Path
		}
	case AvatarOpenFailed:
		if state.Modal != nil && state.Modal.RequestID == event.RequestID {
			state.Modal.Loading = false
			failure := event.Error
			state.Modal.Error = &failure
		}
	case MessageMediaOpened:
		if event.File.Downloaded && event.File.LocalPath != "" && modalMatchesMedia(state.Modal, event.RequestID, event.ChatID, event.MessageID, event.File) {
			state.Modal.Loading = false
			state.Modal.Error = nil
			state.Modal.Title = event.Title
			state.Modal.Path = event.File.LocalPath
			state.Modal.MediaFile = event.File
			if index := messageIndex(state.Messages[event.ChatID], event.MessageID); index >= 0 {
				message := state.Messages[event.ChatID][index]
				if mediaIdentityMatches(message.Media.File, event.File) {
					state.Messages[event.ChatID][index].Media.File.LocalPath = event.File.LocalPath
					state.Messages[event.ChatID][index].Media.File.Downloaded = true
				}
			}
		}
		// Video toast path: stale-guard by request, chat, message and file identity.
		if videoReq, hasPending := state.VideoOpenPending[event.MessageID]; hasPending &&
			videoReq == event.RequestID && event.Title == "Video" && state.Chats != nil {
			idx := messageIndex(state.Messages[event.ChatID], event.MessageID)
			if idx >= 0 && state.Messages[event.ChatID][idx].Kind == domain.MessageVideo && mediaIdentityMatches(state.Messages[event.ChatID][idx].Media.File, event.File) {
				state.Messages[event.ChatID][idx].Media.File = event.File
				delete(state.VideoOpenPending, event.MessageID)
				setToast(state, domain.AppError{Message: "Video opened"}, 2*time.Second)
			}
		}
		// Audio toast path mirrors Video without a terminal modal.
		if audioReq, hasPending := state.AudioOpenPending[event.MessageID]; hasPending &&
			audioReq == event.RequestID && event.Title == "Audio" && state.Chats != nil {
			idx := messageIndex(state.Messages[event.ChatID], event.MessageID)
			if idx >= 0 && state.Messages[event.ChatID][idx].Kind == domain.MessageAudio && mediaIdentityMatches(state.Messages[event.ChatID][idx].Media.File, event.File) {
				state.Messages[event.ChatID][idx].Media.File = event.File
				delete(state.AudioOpenPending, event.MessageID)
				setToast(state, domain.AppError{Message: "Audio opened"}, 2*time.Second)
			}
		}
		// Attachment toast path: title-aware, stale-guarded by request, chat,
		// message, kind and both requested/current file identity.
		attachmentKey := attachmentOpenKey{ChatID: event.ChatID, MessageID: event.MessageID}
		if pending, hasPending := state.AttachmentOpenPending[attachmentKey]; hasPending &&
			pending.RequestID == event.RequestID && pending.Title == event.Title {
			idx := messageIndex(state.Messages[event.ChatID], event.MessageID)
			if idx >= 0 && state.Messages[event.ChatID][idx].Kind == pending.Kind &&
				mediaIdentityMatches(state.Messages[event.ChatID][idx].Media.File, pending.File) &&
				mediaIdentityMatches(pending.File, event.File) {
				state.Messages[event.ChatID][idx].Media.File = event.File
				delete(state.AttachmentOpenPending, attachmentKey)
				if _, success, _ := attachmentToasts(pending.Title); success != "" {
					setToast(state, domain.AppError{Message: success}, 2*time.Second)
				}
			}
		}
	case ThumbnailDownloaded:
		return reduceThumbnailDownloaded(state, event)
	case ThumbnailRendered:
		if state.Thumbnails == nil {
			state.Thumbnails = make(map[domain.ChatID]map[domain.MessageID]thumbnail.Block)
		}
		if state.Thumbnails[event.ChatID] == nil {
			state.Thumbnails[event.ChatID] = make(map[domain.MessageID]thumbnail.Block)
		}
		state.Thumbnails[event.ChatID][event.MessageID] = event.Block
		return nil
	case ThumbnailDownloadFailed:
		return nil // silent; placeholder stays
	case MessageMediaOpenFailed:
		if state.Modal != nil && state.Modal.RequestID == event.RequestID && state.Modal.MediaChatID == event.ChatID && state.Modal.MediaMessageID == event.MessageID {
			state.Modal.Loading = false
			failure := event.Error
			state.Modal.Error = &failure
		}
		// Video toast path: stale-guard by request, chat, message identity.
		// A matched failure clears pending so manual retry becomes possible.
		if videoReq, hasPending := state.VideoOpenPending[event.MessageID]; hasPending &&
			videoReq == event.RequestID && state.Chats != nil {
			idx := messageIndex(state.Messages[event.ChatID], event.MessageID)
			if idx >= 0 && state.Messages[event.ChatID][idx].Kind == domain.MessageVideo {
				delete(state.VideoOpenPending, event.MessageID)
				// Sanitize: strip Cause from failure to avoid leaking private details.
				setToast(state, domain.AppError{Kind: event.Error.Kind, Op: event.Error.Op, Message: event.Error.Message}, 2*time.Second)
			}
		}
		// Audio failures use the same stale and privacy guards.
		if audioReq, hasPending := state.AudioOpenPending[event.MessageID]; hasPending &&
			audioReq == event.RequestID && state.Chats != nil {
			idx := messageIndex(state.Messages[event.ChatID], event.MessageID)
			if idx >= 0 && state.Messages[event.ChatID][idx].Kind == domain.MessageAudio {
				delete(state.AudioOpenPending, event.MessageID)
				setToast(state, domain.AppError{Kind: event.Error.Kind, Op: event.Error.Op, Message: event.Error.Message}, 2*time.Second)
			}
		}
		// Attachment failures clear pending only when request, chat, message,
		// kind and requested/current file identity still match. The displayed
		// failure is derived from the frozen title, never from a raw event cause.
		attachmentKey := attachmentOpenKey{ChatID: event.ChatID, MessageID: event.MessageID}
		if pending, hasPending := state.AttachmentOpenPending[attachmentKey]; hasPending && pending.RequestID == event.RequestID {
			idx := messageIndex(state.Messages[event.ChatID], event.MessageID)
			if idx >= 0 && state.Messages[event.ChatID][idx].Kind == pending.Kind &&
				mediaIdentityMatches(state.Messages[event.ChatID][idx].Media.File, pending.File) {
				delete(state.AttachmentOpenPending, attachmentKey)
				failure := mediaOpenError(pending.Title, nil)
				setToast(state, failure, 2*time.Second)
			}
		}
	case StartupFailed:
		failure := event.Error
		state.Fatal = &failure
		if !state.Quitting {
			state.Quitting = true
			return []Effect{BeginShutdown{}}
		}
	case ShutdownComplete:
		if event.Error != nil {
			failure := *event.Error
			setToast(state, failure, 4*time.Second)
		}
	case OperationFailed:
		failure := event.Error
		setToast(state, failure, 4*time.Second)
	case ClipboardWriteFailed:
		failure := event.Error
		setToast(state, failure, 4*time.Second)
	case ClipboardWritten:
		label := event.Label
		if label == "" {
			label = "Message copied"
		}
		success := domain.AppError{Message: label}
		setToast(state, success, 2*time.Second)
	case WebLinkOpening:
		setToast(state, domain.AppError{Message: "Opening link in browser"}, 2*time.Second)
	case ToastExpired:
		if state.Toast != nil && event.Generation == state.ToastGeneration {
			state.Toast = nil
			state.ToastDuration = 0
		}
	case PromptRequested:
		previousFocus := state.Focus
		if state.Prompt != nil && state.Prompt.Submitting {
			previousFocus = state.Prompt.PreviousFocus
		}
		state.Prompt = &PromptState{Prompt: event.Prompt, PreviousFocus: previousFocus}
		state.Focus = FocusAuth
	}
	return nil
}

func administrationChatActive(state State, chatID domain.ChatID) bool {
	return chatID != 0 && chatIndex(state.Chats, chatID) >= 0
}

func reduceTelegramUpdate(state *State, event TelegramEvent) []Effect {
	switch update := event.Value.(type) {
	case *telegram.Ready:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.ConnectionChanged:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.ChatUpserted:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.DraftChanged:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.UserUpserted:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.AccentColorsChanged:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.MessageUpserted:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.MessageSendSucceeded:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.MessageSendFailed:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.MessagePinnedUpdated:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.MessageReactionsUpdated:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.Closed:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.ForumTopicInfoChanged:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case *telegram.ForumTopicStateChanged:
		if update != nil {
			event.Value = *update
			return reduceTelegramUpdate(state, event)
		}
	case telegram.Ready:
		if state.Prompt != nil {
			state.Prompt.Input = nil
			state.Focus = state.Prompt.PreviousFocus
			state.Prompt = nil
		}
		requestID := allocateRequestID(state)
		state.ChatRequestID = requestID
		state.ChatsLoading = true
		state.ChatsError = nil
		return []Effect{LoadChats{RequestID: requestID, Cursor: telegram.ChatCursor{Limit: pageSize}}}
	case telegram.ConnectionChanged:
		state.Connection = update.State
	case telegram.ChatUpserted:
		if state.SenderAccents == nil {
			state.SenderAccents = make(map[domain.SenderRef]int32)
		}
		state.SenderAccents[domain.SenderRef{Kind: domain.SenderChat, ID: int64(update.Chat.ID)}] = update.Chat.AccentColorID
		selectedID, _ := activeChatID(*state)
		focusedID, _ := focusedChatID(*state)
		if update.Chat.IsArchived && (state.ChatActions == nil || state.ChatActions.ChatID != update.Chat.ID || !state.ChatActions.Working) {
			removeChatFromMainList(state, update.Chat.ID, false)
			if state.ChatActions != nil && state.ChatActions.ChatID == update.Chat.ID {
				state.Focus = state.ChatActions.PreviousFocus
				state.ChatActions = nil
			}
			return nil
		}
		upsertChat(&state.Chats, update.Chat)
		sortChats(state.Chats)
		preserveChatSelection(state, selectedID)
		preserveChatFocus(state, focusedID)
		applyCloudDraft(state, update.Chat.ID, update.Chat.Draft)
		return requestMissingChatAvatars(state, []domain.Chat{update.Chat})
	case telegram.DraftChanged:
		applyCloudDraft(state, update.ChatID, update.Draft)
	case telegram.UserUpserted:
		if state.SenderAccents == nil {
			state.SenderAccents = make(map[domain.SenderRef]int32)
		}
		state.SenderAccents[domain.SenderRef{Kind: domain.SenderUser, ID: int64(update.User.ID)}] = update.User.AccentColorID
	case telegram.AccentColorsChanged:
		state.CustomSenderColors = update.DarkRGB
	case telegram.MessageUpserted:
		return reduceMessageUpsertedAndNotify(state, update.Message)
	case telegram.MessageContentUpdated:
		var commands []Effect
		if index := messageIndex(state.Messages[update.ChatID], update.MessageID); index >= 0 {
			existing := state.Messages[update.ChatID][index].Media.Thumbnail
			state.Messages[update.ChatID][index].Kind = update.Kind
			state.Messages[update.ChatID][index].Service = update.Kind == domain.MessageService
			state.Messages[update.ChatID][index].Text = update.Text
			state.Messages[update.ChatID][index].Entities = append([]domain.TextEntity(nil), update.Entities...)
			syncMessageMenuLinks(state, state.Messages[update.ChatID][index])
			state.Messages[update.ChatID][index].FileName = update.FileName
			state.Messages[update.ChatID][index].Media = update.Media
			state.Messages[update.ChatID][index].Sticker = update.Sticker
			updated := state.Messages[update.ChatID][index].Media.Thumbnail
			if existing.Downloaded && existing.LocalPath != "" && updated.ID == existing.ID {
				newThumb := updated
				newThumb.Downloaded = true
				newThumb.LocalPath = existing.LocalPath
				state.Messages[update.ChatID][index].Media.Thumbnail = newThumb
			}
			// TDLib may populate media refs after a send (e.g. a photo reply whose
			// thumbnail was pending). Re-request any now-reachable inline
			// thumbnail that has not been rendered yet.
			commands = append(commands, requestMissingThumbnails(state, []domain.Message{state.Messages[update.ChatID][index]})...)
		}
		if state.EditTarget != nil && state.EditTarget.ChatID == update.ChatID && state.EditTarget.MessageID == update.MessageID {
			state.EditTarget = nil
		}
		return commands
	case telegram.MessageEdited:
		if index := messageIndex(state.Messages[update.ChatID], update.MessageID); index >= 0 {
			state.Messages[update.ChatID][index].EditedAt = update.EditedAt
		}
	case telegram.MessagePinnedUpdated:
		if index := messageIndex(state.Messages[update.ChatID], update.MessageID); index >= 0 {
			state.Messages[update.ChatID][index].Pinned = update.Pinned
		}
		reconcilePinnedMessages(state, update.ChatID, update.MessageID, update.Pinned)
	case telegram.MessageReactionsUpdated:
		if index := messageIndex(state.Messages[update.ChatID], update.MessageID); index >= 0 {
			state.Messages[update.ChatID][index].Reactions = append([]domain.MessageReaction(nil), update.Reactions...)
		}
	case telegram.MessagesDeleted:
		if update.FromCache {
			// TDLib periodically clears its own message cache and reports it as
			// from_cache=true. The messages still exist in the database and can
			// be retrieved again; the application keeps its own bounded in-memory
			// copies, so cache eviction must not remove them from the UI.
			return nil
		}
		deleted := make(map[domain.MessageID]struct{}, len(update.MessageIDs))
		clearDraftReply := false
		for _, messageID := range update.MessageIDs {
			deleted[messageID] = struct{}{}
			clearDraftReply = clearDraftReply || state.DraftReplies[update.ChatID] == messageID
		}
		messages := state.Messages[update.ChatID][:0]
		for _, message := range state.Messages[update.ChatID] {
			if _, remove := deleted[message.ID]; !remove {
				messages = append(messages, message)
			}
		}
		state.Messages[update.ChatID] = messages
		reconcileReplyTarget(state, update.ChatID)
		reconcileEditTarget(state, update.ChatID)
		reconcileForwardPicker(state)
		reconcileReactionPicker(state)
		if state.SelectedMessageChat == update.ChatID {
			reconcileMessageSelection(state)
		}
		if clearDraftReply {
			delete(state.DraftReplies, update.ChatID)
			return []Effect{queueDraftSave(state, update.ChatID)}
		}
	case telegram.MessageSendSucceeded:
		replaceSentMessage(state, update.OldID, update.Message)
		if idx := messageIndex(state.Messages[update.Message.ChatID], update.Message.ID); idx >= 0 {
			msg := state.Messages[update.Message.ChatID][idx]
			if cmds := requestMissingThumbnails(state, []domain.Message{msg}); len(cmds) > 0 {
				return cmds
			}
		}
	case telegram.MessageSendFailed:
		failSentMessage(state, update, event.ReceivedAt)
	case telegram.ForumTopicInfoChanged:
		return reduceForumTopicInfoChanged(state, update)
	case telegram.ForumTopicStateChanged:
		return reduceForumTopicStateChanged(state, update)
	}
	return nil
}

func reconcilePinnedMessages(state *State, chatID domain.ChatID, messageID domain.MessageID, pinned bool) {
	view := state.PinnedMessages
	if view == nil || view.ChatID != chatID {
		return
	}
	index := messageIndex(state.Messages[chatID], messageID)
	// Cross-topic pin events are ignored: a topic view only reconciles its
	// own topic's messages and a TopicID-0 view only TopicID-0 messages.
	if index >= 0 && state.Messages[chatID][index].TopicID != view.TopicID {
		return
	}
	selectedID := domain.MessageID(0)
	if view.Selected >= 0 && view.Selected < len(view.Results) {
		selectedID = view.Results[view.Selected].ID
	}
	resultIndex := messageIndex(view.Results, messageID)
	if pinned {
		message := cloneDomainMessage(state.Messages[chatID][index])
		message.Pinned = true
		if resultIndex >= 0 {
			view.Results[resultIndex] = message
		} else {
			view.Results = append([]domain.Message{message}, view.Results...)
			view.TotalCount++
		}
	} else if resultIndex >= 0 {
		view.Results = append(view.Results[:resultIndex], view.Results[resultIndex+1:]...)
		if view.TotalCount > 0 {
			view.TotalCount--
		}
	}

	if len(view.Results) == 0 {
		view.Selected = 0
		return
	}
	if selectedID != 0 {
		if index := messageIndex(view.Results, selectedID); index >= 0 {
			view.Selected = index
			return
		}
	}
	view.Selected = max(0, min(len(view.Results)-1, view.Selected))
}

func reduceChatsLoaded(state *State, event ChatsLoaded) []Effect {
	if event.RequestID != state.ChatRequestID {
		return nil
	}
	selectedID, selected := activeChatID(*state)
	focusedID, focused := focusedChatID(*state)
	state.Chats = append([]domain.Chat(nil), event.Page.Chats...)
	sortChats(state.Chats)
	if selected {
		preserveChatSelection(state, selectedID)
	} else if len(state.Chats) > 0 {
		state.SelectedChat = 0
	}
	if focused {
		preserveChatFocus(state, focusedID)
	} else if len(state.Chats) > 0 {
		state.FocusedChat = state.SelectedChat
	}
	state.ChatsLoading = false
	state.ChatsLoaded = true
	state.ChatsError = nil
	for _, chat := range event.Page.Chats {
		applyCloudDraft(state, chat.ID, chat.Draft)
	}

	commands := make([]Effect, 0)
	if chatID, ok := activeChatID(*state); ok && !state.Chats[state.SelectedChat].IsForum {
		commands = append(commands, requestHistoryIfAbsent(state, chatID)...)
	}
	commands = append(commands, requestMissingChatAvatars(state, event.Page.Chats)...)
	return commands
}

func reduceMessagesLoaded(state *State, event MessagesLoaded) []Effect {
	state.Messages[event.ChatID] = limitMessagesForOlderPage(
		mergeMessages(state.Messages[event.ChatID], event.Page.Messages),
	)
	reconcileReplyTarget(state, event.ChatID)
	reconcileEditTarget(state, event.ChatID)
	reconcileForwardPicker(state)
	reconcileReactionPicker(state)
	if state.SelectedMessageChat == event.ChatID {
		reconcileMessageSelection(state)
	}
	if event.TopicID != 0 {
		return reduceTopicMessagesLoaded(state, event)
	}
	history, exists := state.History[event.ChatID]
	current := exists && history.RequestID == event.RequestID
	if !current {
		return nil
	}
	history.Loading = false
	history.Error = nil
	history.Done = event.Page.Done
	if messages := state.Messages[event.ChatID]; len(messages) > 0 {
		history.OldestID = messages[0].ID
	}
	history.ViewOffset = clampOffset(history.ViewOffset, len(state.Messages[event.ChatID]))
	state.History[event.ChatID] = history
	activeID, active := activeChatID(*state)
	if !active || activeID != event.ChatID {
		return nil
	}
	restoreActiveDraftReply(state)
	if state.SelectedMessage == 0 {
		messages := state.Messages[event.ChatID]
		if len(messages) > 0 {
			state.SelectedMessage = messages[len(messages)-1].ID
			state.SelectedMessageChat = event.ChatID
		}
	}
	commands := requestMissingMessageAvatars(state, event.Page.Messages)
	commands = append(commands, requestMissingThumbnails(state, event.Page.Messages)...)
	return commands
}

func reduceMessageUpserted(state *State, message domain.Message) []Effect {
	messages := state.Messages[message.ChatID]
	existed := messageIndex(messages, message.ID) >= 0
	state.Messages[message.ChatID] = limitMessages(mergeMessages(messages, []domain.Message{message}))
	if index := messageIndex(state.Messages[message.ChatID], message.ID); index >= 0 {
		syncMessageMenuLinks(state, state.Messages[message.ChatID][index])
	}
	reconcileReplyTarget(state, message.ChatID)
	reconcileEditTarget(state, message.ChatID)
	reconcileForwardPicker(state)
	reconcileReactionPicker(state)
	if state.SelectedMessageChat == message.ChatID {
		reconcileMessageSelection(state)
	}
	chatMessages := state.Messages[message.ChatID]
	newest := len(chatMessages) > 0 && chatMessages[len(chatMessages)-1].ID == message.ID
	if chatIndex := chatIndex(state.Chats, message.ChatID); chatIndex >= 0 && len(chatMessages) > 0 {
		latest := chatMessages[len(chatMessages)-1]
		state.Chats[chatIndex].LastMessage = latest.DisplayText()
		state.Chats[chatIndex].LastMessageAt = latest.SentAt.Unix()
		state.Chats[chatIndex].Order = latest.SentAt.Unix()
		selectedID, _ := activeChatID(*state)
		focusedID, _ := focusedChatID(*state)
		sortChats(state.Chats)
		preserveChatSelection(state, selectedID)
		preserveChatFocus(state, focusedID)
	}
	if activeID, ok := activeChatID(*state); ok && activeID == message.ChatID {
		restoreActiveDraftReply(state)
		history := state.History[message.ChatID]
		if !existed && newest && history.ViewOffset > 0 {
			history.ViewOffset++
		}
		state.History[message.ChatID] = history
		if state.SelectedMessage == 0 {
			state.SelectedMessage = newestMessageID(chatMessages)
			state.SelectedMessageChat = message.ChatID
		}
	}
	commands := requestMissingMessageAvatars(state, []domain.Message{message})
	commands = append(commands, requestMissingThumbnails(state, []domain.Message{message})...)
	return commands
}

// reduceMessageUpsertedAndNotify is like reduceMessageUpserted but also
// emits a ShowDesktopNotification command when the terminal is unfocused,
// the message is incoming (not outgoing), non-service, and the chat is
// unmuted.  Re-upserts (existed==true) suppress notifications.
func reduceMessageUpsertedAndNotify(state *State, message domain.Message) []Effect {
	// Capture existence BEFORE the upsert so we can filter on it.
	existed := messageIndex(state.Messages[message.ChatID], message.ID) >= 0

	commands := reduceMessageUpserted(state, message)

	// Only notify on new messages while the terminal is unfocused.
	// The initial state has TerminalFocused=true, so nothing fires on
	// startup before focus reporting begins.
	if state.TerminalFocused {
		return commands
	}

	// Outgoing, service, or already-seen messages do not trigger.
	if message.Outgoing || message.Service || message.Kind == domain.MessageService || existed {
		return commands
	}

	// Check that the chat is known and unmuted.
	chatIdx := chatIndex(state.Chats, message.ChatID)
	if chatIdx < 0 {
		return commands
	}
	if state.Chats[chatIdx].Muted {
		return commands
	}

	// Build the notification body.
	body := buildNotificationBody(message)
	if body == "" {
		return commands
	}

	title := strings.Join(strings.Fields(state.Chats[chatIdx].Title), " ")
	commands = append(commands, ShowDesktopNotification{Title: title, Body: body})
	return commands
}

const notificationBodyLimit = 240 // runes

// buildNotificationBody creates a concise "Sender: preview" body for the
// desktop notification.  Text messages show their text; non-text messages
// start with Message.DisplayText() and append a caption (message.Text) when
// non-empty.  Whitespace is normalized via strings.Fields; the combined
// sender+preview is rune-bounded to notificationBodyLimit with an ellipsis.
func buildNotificationBody(message domain.Message) string {
	sender := strings.Join(strings.Fields(message.SenderName), " ")

	var preview string
	if message.Kind == domain.MessageText {
		preview = strings.Join(strings.Fields(message.Text), " ")
	} else {
		preview = message.DisplayText()
		if caption := strings.Join(strings.Fields(message.Text), " "); caption != "" {
			preview += " " + caption
		}
		preview = strings.Join(strings.Fields(preview), " ")
	}
	if preview == "" {
		return ""
	}

	body := preview
	if sender != "" {
		body = sender + ": " + preview
	}
	runes := []rune(body)
	if len(runes) <= notificationBodyLimit {
		return body
	}
	return string(runes[:notificationBodyLimit-1]) + "…"
}

func reduceThumbnailDownloaded(state *State, event ThumbnailDownloaded) []Effect {
	if index := messageIndex(state.Messages[event.ChatID], event.MessageID); index >= 0 {
		message := state.Messages[event.ChatID][index]
		message.Media.Thumbnail.Downloaded = true
		message.Media.Thumbnail.LocalPath = event.File.LocalPath
		state.Messages[event.ChatID][index] = message
	}
	return nil
}

func reducePromptAction(state *State, event ActionReceived) []Effect {
	if state.Prompt.Submitting {
		return nil
	}
	switch event.Action {
	case ComposerBackspace:
		if size := len(state.Prompt.Input); size > 0 {
			state.Prompt.Input = state.Prompt.Input[:size-1]
		}
	case ComposerSubmit:
		command := SubmitPrompt{Response: auth.Response{PromptID: state.Prompt.Prompt.ID, Value: string(state.Prompt.Input)}}
		state.Prompt.Input = nil
		state.Prompt.Submitting = true
		return []Effect{command}
	case NoAction:
		if event.Rune != 0 {
			state.Prompt.Input = append(state.Prompt.Input, event.Rune)
		}
	}
	return nil
}

func reduceAction(state *State, event ActionReceived) []Effect {
	if state.CommandMenu != nil {
		switch event.Action {
		case CommandMenuNext, CommandMenuPrevious, CommandMenuActivate, CommandMenuDismiss:
			return reduceCommandMenuAction(state, event)
		default:
			state.CommandMenu = nil
		}
	}
	if state.ChatSearch != nil {
		return reduceChatSearchAction(state, event)
	}
	if state.ChatActions != nil {
		return reduceChatActionMenu(state, event)
	}
	// The media modal is topmost: a member-avatar view keeps Members state
	// alive underneath, so the modal owns keys while open. This combination
	// is new; all other overlay orderings are unchanged.
	if state.Modal != nil && state.Members != nil {
		switch event.Action {
		case Close:
			state.Focus = state.Modal.PreviousFocus
			state.Modal = nil
		case Retry:
			if state.Modal.Error != nil {
				requestID := allocateRequestID(state)
				state.Modal.RequestID = requestID
				state.Modal.Loading = true
				state.Modal.Error = nil
				state.Modal.Path = ""
				if mediaEligible(state.Modal.MediaFile) {
					return []Effect{OpenMessageMediaFile{RequestID: requestID, ChatID: state.Modal.MediaChatID, MessageID: state.Modal.MediaMessageID, Title: state.Modal.Title, File: state.Modal.MediaFile}}
				}
				return []Effect{OpenAvatar{RequestID: requestID, Title: state.Modal.Title, Ref: state.Modal.Ref}}
			}
		}
		return nil
	}
	if state.Topics != nil {
		return reduceTopicsAction(state, event)
	}

	if state.Members != nil {
		return reduceMembersAction(state, event)
	}
	if state.Administration != nil {
		return reduceAdministrationAction(state, event)
	}
	if state.ChatSettings != nil {
		return reduceChatSettingsAction(state, event)
	}
	if state.InviteLinks != nil {
		return reduceInviteLinksAction(state, event)
	}
	if state.PinnedMessages != nil {
		return reducePinnedMessagesAction(state, event)
	}
	if state.MessageSearch != nil {
		return reduceMessageSearchAction(state, event)
	}
	if state.StickerPicker != nil {
		return reduceStickerPicker(state, event)
	}
	if state.PhotoSend != nil {
		return reducePhotoSend(state, event)
	}
	if state.MessageMenu != nil {
		menu := state.MessageMenu
		if menu.LinkAction != NoAction {
			switch event.Action {
			case Close:
				menu.LinkAction = NoAction
			case SelectNext, SelectPrevious:
				if len(menu.Links) > 0 {
					delta := 1
					if event.Action == SelectPrevious {
						delta = -1
					}
					menu.LinkSelected = (menu.LinkSelected + delta + len(menu.Links)) % len(menu.Links)
				}
			case Activate:
				return reduceAction(state, ActionReceived{Action: SelectMessageLink, ChatID: menu.ChatID, MessageID: menu.MessageID, CommandIndex: menu.LinkSelected})
			case SelectMessageLink:
				if event.ChatID != menu.ChatID || event.MessageID != menu.MessageID || event.CommandIndex < 0 || event.CommandIndex >= len(menu.Links) {
					return nil
				}
				message, ok := messageByIdentity(*state, menu.ChatID, menu.MessageID)
				if !ok {
					return nil
				}
				links := messageLinks(message)
				if event.CommandIndex >= len(links) || links[event.CommandIndex] != menu.Links[event.CommandIndex] {
					return nil
				}
				url := links[event.CommandIndex].URL
				action := menu.LinkAction
				state.Focus = menu.PreviousFocus
				state.MessageMenu = nil
				if action == CopyMessageLink {
					return []Effect{WriteClipboard{Text: url, Label: "Link copied"}}
				}
				return []Effect{OpenWebLink{URL: url}}
			}
			return nil
		}
		if state.MessageMenu.JumpRequestID != 0 && event.Action != Close {
			return nil
		}
		switch event.Action {
		case Close:
			state.Focus = state.MessageMenu.PreviousFocus
			state.MessageMenu = nil
		case ViewMessageMedia:
			return openMediaModalFromMenu(state)
		case SelectNext, SelectPrevious:
			count := actionMenuItemCount(state.MessageMenu)
			if count > 0 {
				delta := 1
				if event.Action == SelectPrevious {
					delta = -1
				}
				state.MessageMenu.Selected = (state.MessageMenu.Selected + delta + count) % count
			}
		case Activate:
			event.Action = selectedMenuAction(state.MessageMenu)
			return reduceAction(state, event)
		case GoToReferencedMessage:
			return beginReferencedMessageJump(state)
		case ReplyMessage:
			if message, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.Reply {
				return beginReply(state, message)
			}
		case ForwardMessageSource:
			if message, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.Forward {
				return beginForward(state, message)
			}
		case EditMessage:
			if message, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.Edit && editableMessage(message) {
				return beginEdit(state, message)
			}
		case DeleteMessage:
			if _, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.DeleteForSelf {
				return beginDelete(state, state.MessageMenu, false)
			}
		case PinMessage:
			if _, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.Pin {
				return beginPin(state, state.MessageMenu)
			}
		case ReactMessage:
			if message, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && reactRowVisible(state.MessageMenu) {
				return beginReact(state, message)
			}
		case DeleteForEveryone:
			if _, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.DeleteForAll {
				return beginDelete(state, state.MessageMenu, true)
			}
		case CopyMessageLink, OpenMessageLink:
			if len(menu.Links) > 0 {
				if _, ok := messageByIdentity(*state, menu.ChatID, menu.MessageID); ok {
					menu.LinkAction = event.Action
					menu.LinkSelected = 0
				}
			}
		case CopyMessage:
			if message, ok := messageByIdentity(*state, state.MessageMenu.ChatID, state.MessageMenu.MessageID); ok && state.MessageMenu.Capabilities.Copy {
				state.Focus = state.MessageMenu.PreviousFocus
				state.MessageMenu = nil
				return []Effect{WriteClipboard{Text: message.DisplayText()}}
			}
		case ViewUserInfo:
			menu := state.MessageMenu
			if message, ok := messageByIdentity(*state, menu.ChatID, menu.MessageID); ok && menu.UserID != 0 && message.Sender.Kind == domain.SenderUser && domain.UserID(message.Sender.ID) == menu.UserID {
				state.MessageMenu = nil
				return openUserInfo(state, message.ChatID, menu.UserID, menu.PreviousFocus)
			}
		}
		return nil
	}
	if state.ReactionPicker != nil {
		return reduceReactionPicker(state, event)
	}
	if state.ForwardPicker != nil {
		return reduceForwardPicker(state, event)
	}
	if state.Modal != nil {
		switch event.Action {
		case Close:
			state.Focus = state.Modal.PreviousFocus
			state.Modal = nil
		case Retry:
			if state.Modal.Error != nil {
				requestID := allocateRequestID(state)
				state.Modal.RequestID = requestID
				state.Modal.Loading = true
				state.Modal.Error = nil
				state.Modal.Path = ""
				if mediaEligible(state.Modal.MediaFile) {
					return []Effect{OpenMessageMediaFile{RequestID: requestID, ChatID: state.Modal.MediaChatID, MessageID: state.Modal.MediaMessageID, Title: state.Modal.Title, File: state.Modal.MediaFile}}
				}
				return []Effect{OpenAvatar{RequestID: requestID, Title: state.Modal.Title, Ref: state.Modal.Ref}}
			}
		}
		return nil
	}

	switch event.Action {
	case FocusChat:
		if index := chatIndex(state.Chats, event.ChatID); index >= 0 {
			state.FocusedChat = index
			if focusVisible(*state, FocusChats) {
				state.Focus = FocusChats
			}
		}
	case SelectChat:
		if index := chatIndex(state.Chats, event.ChatID); index >= 0 {
			var commands []Effect
			if state.ForwardPicker != nil {
				state.ForwardPicker = nil
				state.Focus = FocusConversation
			}
			if state.ReactionPicker != nil {
				state.ReactionPicker = nil
				state.Focus = FocusConversation
			}
			state.ReplyTarget = nil
			state.EditTarget = nil
			if state.SelectedChat >= 0 && state.SelectedChat < len(state.Chats) {
				previousID := state.Chats[state.SelectedChat].ID
				if previousID != event.ChatID {
					releaseDraftGuard(state, previousID)
					commands = append(commands, CloseChatCommand{ChatID: previousID})
				}
			}
			state.FocusedChat = index
			state.SelectedChat = index
			if state.DetailsOpen {
				state.DetailsChatID = event.ChatID
			}
			restoreActiveDraftReply(state)
			clampDetailsSelection(state)
			selectNewestMessage(state, event.ChatID)
			commands = append(commands, requestHistoryIfAbsent(state, event.ChatID)...)
			commands = append(commands, OpenChatCommand{ChatID: event.ChatID})
			commands = append(commands, requestMissingMessageAvatars(state, state.Messages[event.ChatID])...)
			return commands
		}
	case SelectNextUnread, SelectNextMention:
		if state.Focus != FocusChats {
			break
		}
		match := func(chat domain.Chat) bool { return chat.UnreadCount > 0 }
		if event.Action == SelectNextMention {
			match = func(chat domain.Chat) bool { return chat.UnreadMentionCount > 0 }
		}
		if chatID, ok := nextMatchingChatID(*state, match); ok {
			return reduceAction(state, ActionReceived{Action: FocusChat, ChatID: chatID})
		}
		message := "No unread chats"
		if event.Action == SelectNextMention {
			message = "No unread mentions"
		}
		setToast(state, domain.AppError{Message: message}, 2*time.Second)
	case SelectNext, SelectPrevious:
		if state.Focus == FocusDetails {
			count := 0
			if chat, ok := detailsChat(*state); ok {
				count = detailsActionCount(chat)
			}
			if count > 0 {
				delta := 1
				if event.Action == SelectPrevious {
					delta = -1
				}
				state.DetailsSelected = (state.DetailsSelected + delta + count) % count
			}
			break
		}
		if len(state.Chats) == 0 {
			break
		}
		delta := 1
		if event.Action == SelectPrevious {
			delta = -1
		}
		focused := focusedChatIndex(*state)
		if focused < 0 {
			break
		}
		state.FocusedChat = max(0, min(len(state.Chats)-1, focused+delta))
	case FocusPane:
		if focusVisible(*state, event.TargetFocus) {
			state.Focus = event.TargetFocus
		}
	case FocusNext, FocusPrevious:
		cycleFocus(state, event.Action == FocusPrevious)
	case Activate:
		return activate(state, event)
	case SelectMessage:
		if event.ChatID != 0 && event.MessageID != 0 && messageIndex(state.Messages[event.ChatID], event.MessageID) >= 0 {
			state.SelectedMessageChat, state.SelectedMessage = event.ChatID, event.MessageID
			state.Focus = FocusConversation
		}
	case OpenMessageActionMenu:
		return openMessageActionMenu(state)
	case EditMessage:
		commands := openMessageActionMenu(state)
		if state.MessageMenu != nil {
			state.MessageMenu.PreferEdit = true
		}
		return commands
	case SelectNextMessage, SelectPreviousMessage:
		return selectAdjacentMessage(state, event.Action == SelectPreviousMessage)
	case CopyMessage:
		if message, ok := selectedMessage(*state); ok && message.Capabilities().Copy {
			return []Effect{WriteClipboard{Text: message.DisplayText()}}
		}
	case ReplyMessage:
		if message, ok := selectedMessage(*state); ok && message.Capabilities().Reply {
			return beginReply(state, message)
		}
	case CancelReply:
		if allKey, ok := showAllActive(*state); ok {
			if state.TopicDraftReplies[allKey] != 0 || state.ReplyTarget != nil {
				state.ReplyTarget = nil
				if state.TopicDraftReplies == nil {
					state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
				}
				delete(state.TopicDraftReplies, allKey)
				return []Effect{queueTopicDraftSave(state, allKey)}
			}
			state.ReplyTarget = nil
			break
		}
		if key, ok := activeTopicKey(*state); ok {
			if state.TopicDraftReplies[key] != 0 || state.ReplyTarget != nil {
				state.ReplyTarget = nil
				if state.TopicDraftReplies == nil {
					state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
				}
				delete(state.TopicDraftReplies, key)
				return []Effect{queueTopicDraftSave(state, key)}
			}
			state.ReplyTarget = nil
			break
		}
		if chatID, ok := activeChatID(*state); ok && (state.DraftReplies[chatID] != 0 || state.ReplyTarget != nil) {
			state.ReplyTarget = nil
			delete(state.DraftReplies, chatID)
			return []Effect{queueDraftSave(state, chatID)}
		}
		state.ReplyTarget = nil
	case CancelEdit:
		state.EditTarget = nil
		restoreActiveDraftReply(state)
	case ToggleDetails:
		if state.DetailsOpen {
			state.DetailsOpen = false
			state.DetailsChatID = 0
			state.Focus = state.FocusBeforeInfo
		} else if chatID, ok := activeChatID(*state); ok {
			state.FocusBeforeInfo = state.Focus
			state.DetailsOpen = true
			state.DetailsChatID = chatID
			state.Focus = FocusDetails
			state.DetailsSelected = 0
		}
	case Close:
		if state.DetailsOpen && state.Focus != FocusComposer {
			state.DetailsOpen = false
			state.DetailsChatID = 0
			state.Focus = state.FocusBeforeInfo
		} else if state.Focus == FocusComposer && state.EditTarget != nil {
			state.EditTarget = nil
			restoreActiveDraftReply(state)
		} else if state.Focus == FocusComposer && state.ReplyTarget != nil {
			if allKey, ok := showAllActive(*state); ok {
				state.ReplyTarget = nil
				if state.TopicDraftReplies == nil {
					state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
				}
				delete(state.TopicDraftReplies, allKey)
				return []Effect{queueTopicDraftSave(state, allKey)}
			}
			if key, ok := activeTopicKey(*state); ok {
				state.ReplyTarget = nil
				if state.TopicDraftReplies == nil {
					state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
				}
				delete(state.TopicDraftReplies, key)
				return []Effect{queueTopicDraftSave(state, key)}
			}
			if chatID, ok := activeChatID(*state); ok {
				state.ReplyTarget = nil
				delete(state.DraftReplies, chatID)
				return []Effect{queueDraftSave(state, chatID)}
			}
			state.ReplyTarget = nil
		} else if state.Focus == FocusComposer {
			if allKey, ok := showAllActive(*state); ok {
				if state.TopicDraftReplies[allKey] != 0 {
					if state.TopicDraftReplies == nil {
						state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
					}
					delete(state.TopicDraftReplies, allKey)
					return []Effect{queueTopicDraftSave(state, allKey)}
				}
				if state.TopicDrafts[allKey] != "" {
					state.TopicDrafts[allKey] = ""
					return []Effect{queueTopicDraftSave(state, allKey)}
				}
			}
			if key, ok := activeTopicKey(*state); ok {
				if state.TopicDraftReplies[key] != 0 {
					if state.TopicDraftReplies == nil {
						state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
					}
					delete(state.TopicDraftReplies, key)
					return []Effect{queueTopicDraftSave(state, key)}
				}
				if state.TopicDrafts[key] != "" {
					state.TopicDrafts[key] = ""
					return []Effect{queueTopicDraftSave(state, key)}
				}
			} else if chatID, ok := activeChatID(*state); ok {
				if state.DraftReplies[chatID] != 0 {
					delete(state.DraftReplies, chatID)
					return []Effect{queueDraftSave(state, chatID)}
				}
				if state.Drafts[chatID] != "" {
					state.Drafts[chatID] = ""
					return []Effect{queueDraftSave(state, chatID)}
				}
			}
			state.Focus = FocusConversation
		} else if state.Layout == LayoutNarrow && state.Focus == FocusConversation {
			state.Focus = FocusChats
		}
	case PageUp, PageDown:
		return paginate(state, event.Action)
	case ComposerBackspace, ComposerNewline, ComposerSubmit, NoAction:
		return compose(state, event)
	case Retry:
		return retry(state, event)
	case OpenPhotoSend:
		return openPhotoSend(state)
	case OpenStickerPicker:
		return openStickerPicker(state)
	case OpenMessageSearch:
		return openMessageSearch(state)
	case OpenChatSearch:
		return openChatSearch(state)
	case OpenChatActionMenu:
		return openChatActionMenu(state)
	case OpenChat:
		if state.Focus == FocusChats {
			return openActiveChatFromList(state)
		}
	case OpenPinnedMessages:
		return openPinnedMessages(state)
	case OpenMembers:
		return openMembers(state)
	case OpenInviteLinks:
		return openInviteLinks(state)
	case OpenGroupPermissions:
		return openGroupPermissions(state)
	case OpenChatSettings:
		return openChatSettings(state)
	case OpenTopics:
		return openTopics(state)
	case SelectTopic, SelectAllMessages:
		if state.Topics != nil {
			return reduceTopicsAction(state, event)
		}
	case OpenDetailsAvatar:
		return openDetailsAvatar(state)
	}
	return nil
}

func activate(state *State, event ActionReceived) []Effect {
	chatID, ok := activeChatID(*state)
	if !ok {
		return nil
	}
	switch state.Focus {
	case FocusChats:
		return openChatActionMenu(state)
	case FocusConversation:
		messageID := event.MessageID
		if messageID == 0 {
			messageID = state.SelectedMessage
		}
		if index := messageIndex(state.Messages[chatID], messageID); index >= 0 && state.Messages[chatID][index].SendState == domain.SendFailed {
			if command, retried := retryMessage(state, messageID, event.At); retried {
				return []Effect{command}
			}
			return nil
		}
		return openMessageActionMenu(state)
	case FocusDetails:
		clampDetailsSelection(state)
		chat, ok := detailsChat(*state)
		if !ok {
			return nil
		}
		items := DetailsActionItems(chat)
		if state.DetailsSelected >= 0 && state.DetailsSelected < len(items) {
			switch items[state.DetailsSelected].Action {
			case OpenMembers:
				return openMembers(state)
			case OpenInviteLinks:
				return openInviteLinks(state)
			case OpenGroupPermissions:
				return openGroupPermissions(state)
			case OpenChatSettings:
				return openChatSettings(state)
			}
		}
		return openDetailsAvatar(state)
	}
	return nil
}

func compose(state *State, event ActionReceived) []Effect {
	if state.Focus != FocusComposer {
		return nil
	}
	chatID, ok := activeChatID(*state)
	if !ok {
		return nil
	}
	if state.EditTarget != nil {
		if state.EditTarget.ChatID != chatID || !editableTargetPresent(*state) {
			state.EditTarget = nil
			return nil
		}
		if state.EditTarget.Submitting {
			return nil
		}
		switch event.Action {
		case NoAction:
			if event.Rune != 0 {
				state.EditTarget.Buffer += string(event.Rune)
			}
		case ComposerBackspace:
			runes := []rune(state.EditTarget.Buffer)
			if len(runes) > 0 {
				state.EditTarget.Buffer = string(runes[:len(runes)-1])
			}
		case ComposerNewline:
			state.EditTarget.Buffer += "\n"
		case ComposerSubmit:
			if strings.TrimSpace(state.EditTarget.Buffer) == "" || state.EditTarget.Buffer == state.EditTarget.Original {
				return nil
			}
			requestID := allocateRequestID(state)
			state.EditTarget.RequestID = requestID
			state.EditTarget.Submitting = true
			state.EditTarget.Error = nil
			return []Effect{EditText{RequestID: requestID, ChatID: state.EditTarget.ChatID, MessageID: state.EditTarget.MessageID, Text: state.EditTarget.Buffer}}
		}
		return nil
	}
	changed := false
	switch event.Action {
	case NoAction:
		if event.Rune != 0 {
			state.Drafts[chatID] += string(event.Rune)
			changed = true
		}
	case ComposerBackspace:
		runes := []rune(state.Drafts[chatID])
		if len(runes) > 0 {
			state.Drafts[chatID] = string(runes[:len(runes)-1])
			changed = true
		}
	case ComposerNewline:
		state.Drafts[chatID] += "\n"
		changed = true
	case ComposerSubmit:
		if forumTopicClosed(*state, chatID) {
			return nil
		}
		key, topicOK := activeTopicKey(*state)
		allKey, allOK := showAllActive(*state)
		if !topicOK && !allOK && state.Chats[state.SelectedChat].IsForum {
			// A forum without a selected topic has no composer target.
			return nil
		}
		chat := state.Chats[state.SelectedChat]
		var text string
		var replyID domain.MessageID
		if allOK {
			text = state.TopicDrafts[allKey]
			if state.ReplyTarget != nil && state.ReplyTarget.ChatID == allKey.ChatID && state.ReplyTarget.TopicID == allKey.TopicID {
				replyID = state.ReplyTarget.MessageID
			}
		} else if topicOK {
			text = state.TopicDrafts[key]
			if state.ReplyTarget != nil && state.ReplyTarget.ChatID == key.ChatID && state.ReplyTarget.TopicID == key.TopicID {
				replyID = state.ReplyTarget.MessageID
			}
		} else {
			text = state.Drafts[chatID]
			if state.ReplyTarget != nil && state.ReplyTarget.ChatID == chatID {
				replyID = state.ReplyTarget.MessageID
			}
		}
		if strings.TrimSpace(text) == "" || !chat.CanSend || state.Connection != domain.ConnectionOnline {
			return nil
		}
		localID := allocateLocalID(state)
		requestID := allocateRequestID(state)
		sendTopicID := domain.TopicID(0)
		if allOK {
			sendTopicID = generalTopicID(*state, chatID)
		} else if topicOK {
			sendTopicID = key.TopicID
		}
		message := domain.Message{ID: localID, ChatID: chatID, TopicID: sendTopicID, SentAt: event.At, Kind: domain.MessageText, Text: text, Outgoing: true, SendState: domain.SendPending, ReplyToMessageID: replyID, HasReply: replyID > 0}
		state.Messages[chatID] = limitMessages(mergeMessages(state.Messages[chatID], []domain.Message{message}))
		if allOK || topicOK {
			draftKey := key
			if allOK {
				draftKey = allKey
			}
			if state.TopicDrafts == nil {
				state.TopicDrafts = make(map[topicKey]string)
			}
			state.TopicDrafts[draftKey] = ""
			if state.TopicDraftReplies == nil {
				state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
			}
			delete(state.TopicDraftReplies, draftKey)
			if state.ReplyTarget != nil && state.ReplyTarget.ChatID == draftKey.ChatID && state.ReplyTarget.TopicID == draftKey.TopicID {
				state.ReplyTarget = nil
			}
			state.SelectedMessage = localID
			return []Effect{
				SendText{RequestID: requestID, LocalID: localID, ChatID: chatID, TopicID: sendTopicID, Text: text, ReplyToMessageID: replyID},
				queueTopicDraftSave(state, draftKey),
			}
		}
		state.Drafts[chatID] = ""
		delete(state.DraftReplies, chatID)
		state.ReplyTarget = nil
		state.SelectedMessage = localID
		return []Effect{
			SendText{RequestID: requestID, LocalID: localID, ChatID: chatID, Text: text, ReplyToMessageID: replyID},
			queueDraftSave(state, chatID),
		}
	}
	if changed {
		return []Effect{queueDraftSave(state, chatID)}
	}
	return nil
}

func paginate(state *State, action Action) []Effect {
	chatID, ok := activeChatID(*state)
	if !ok {
		return nil
	}
	history := state.History[chatID]
	step := max(1, (state.Height-6)/2)
	if action == PageDown {
		history.ViewOffset = max(0, history.ViewOffset-step)
		history.FollowSelection = false
		state.History[chatID] = history
		return nil
	}
	messages := state.Messages[chatID]
	target := history.ViewOffset + step
	history.ViewOffset = clampOffset(target, len(messages))
	history.FollowSelection = false
	if target >= len(messages) && !history.Loading && !history.Done {
		requestID := allocateRequestID(state)
		history.Loading = true
		history.RequestID = requestID
		cursor := telegram.MessageCursor{FromMessageID: history.OldestID, Limit: pageSize}
		state.History[chatID] = history
		return []Effect{LoadMessages{RequestID: requestID, ChatID: chatID, Cursor: cursor}}
	}
	state.History[chatID] = history
	return nil
}

func retry(state *State, event ActionReceived) []Effect {
	if event.AvatarKey != "" {
		entry, ok := state.Avatars[event.AvatarKey]
		if ok && entry.Error != nil {
			entry.Loading = true
			entry.Error = nil
			state.Avatars[event.AvatarKey] = entry
			return []Effect{RenderAvatar{Key: event.AvatarKey, Ref: entry.Ref, Role: entry.Role}}
		}
		return nil
	}
	if command, ok := retryMessage(state, event.MessageID, event.At); ok {
		return []Effect{command}
	}
	if chatID, ok := activeChatID(*state); ok {
		history := state.History[chatID]
		if history.Error != nil && !history.Loading {
			requestID := allocateRequestID(state)
			history.Loading = true
			history.Error = nil
			history.RequestID = requestID
			state.History[chatID] = history
			return []Effect{LoadMessages{
				RequestID: requestID,
				ChatID:    chatID,
				Cursor: telegram.MessageCursor{
					FromMessageID: history.OldestID,
					Limit:         pageSize,
				},
			}}
		}
	}
	return nil
}

func retryMessage(state *State, messageID domain.MessageID, at time.Time) (Effect, bool) {
	chatID, ok := activeChatID(*state)
	if !ok {
		return nil, false
	}
	if messageID == 0 {
		messageID = state.SelectedMessage
	}
	index := messageIndex(state.Messages[chatID], messageID)
	if index < 0 {
		return nil, false
	}
	message := &state.Messages[chatID][index]
	if message.SendState != domain.SendFailed || (!message.RetryAt.IsZero() && at.Before(message.RetryAt)) {
		return nil, false
	}
	// Validate kind and path BEFORE any mutation.
	switch message.Kind {
	case domain.MessageText:
		// Text is always valid.
	case domain.MessagePhoto:
		if message.Media.File.LocalPath == "" {
			// Missing path: full-state no-op.
			return nil, false
		}
	case domain.MessageVideo, domain.MessageAudio:
		if message.Media.File.LocalPath == "" {
			return nil, false
		}
	case domain.MessageDocument:
		if message.Media.File.LocalPath == "" {
			return nil, false
		}
	case domain.MessageSticker:
		if message.Sticker.File.ID == 0 {
			return nil, false
		}
	default:
		// Unsupported kind: full-state no-op.
		return nil, false
	}
	// All validations passed — mutate and allocate.
	message.SendState = domain.SendPending
	message.Failure = nil
	message.RetryAt = time.Time{}
	requestID := allocateRequestID(state)
	switch message.Kind {
	case domain.MessageText:
		text := message.Text
		if message.RetryText != "" {
			text = message.RetryText
		}
		return SendText{RequestID: requestID, LocalID: message.ID, ChatID: chatID, Text: text, ReplyToMessageID: message.ReplyToMessageID}, true
	case domain.MessagePhoto:
		if state.PhotoSendRequests == nil {
			state.PhotoSendRequests = make(map[domain.MessageID]uint64)
		}
		state.PhotoSendRequests[message.ID] = requestID
		return SendPhoto{RequestID: requestID, LocalID: message.ID, ChatID: chatID, TopicID: message.TopicID, LocalPath: message.Media.File.LocalPath, Caption: message.Text, ReplyToMessageID: message.ReplyToMessageID}, true
	case domain.MessageVideo:
		if state.VideoSendRequests == nil {
			state.VideoSendRequests = make(map[domain.MessageID]uint64)
		}
		state.VideoSendRequests[message.ID] = requestID
		return SendVideo{RequestID: requestID, LocalID: message.ID, ChatID: chatID, TopicID: message.TopicID, LocalPath: message.Media.File.LocalPath, Caption: message.Text, ReplyToMessageID: message.ReplyToMessageID}, true
	case domain.MessageAudio:
		if state.AudioSendRequests == nil {
			state.AudioSendRequests = make(map[domain.MessageID]uint64)
		}
		state.AudioSendRequests[message.ID] = requestID
		return SendAudio{RequestID: requestID, LocalID: message.ID, ChatID: chatID, TopicID: message.TopicID, LocalPath: message.Media.File.LocalPath, Caption: message.Text, ReplyToMessageID: message.ReplyToMessageID}, true
	case domain.MessageDocument:
		if state.DocumentSendRequests == nil {
			state.DocumentSendRequests = make(map[domain.MessageID]uint64)
		}
		state.DocumentSendRequests[message.ID] = requestID
		return SendDocument{RequestID: requestID, LocalID: message.ID, ChatID: chatID, TopicID: message.TopicID, LocalPath: message.Media.File.LocalPath, Caption: message.Text, ReplyToMessageID: message.ReplyToMessageID}, true
	case domain.MessageSticker:
		if state.StickerSendRequests == nil {
			state.StickerSendRequests = make(map[domain.MessageID]uint64)
		}
		state.StickerSendRequests[message.ID] = requestID
		return SendSticker{RequestID: requestID, LocalID: message.ID, ChatID: chatID, TopicID: message.TopicID, Sticker: message.Sticker, ReplyToMessageID: message.ReplyToMessageID}, true
	}
	return nil, false
}

func requestHistoryIfAbsent(state *State, chatID domain.ChatID) []Effect {
	if _, exists := state.History[chatID]; exists {
		return nil
	}
	requestID := allocateRequestID(state)
	state.History[chatID] = HistoryState{Loading: true, RequestID: requestID}
	return []Effect{LoadMessages{RequestID: requestID, ChatID: chatID, Cursor: telegram.MessageCursor{Limit: pageSize}}}
}

func requestMissingChatAvatars(state *State, chats []domain.Chat) []Effect {
	commands := make([]Effect, 0)
	for _, chat := range chats {
		if chat.Avatar.UniqueID == "" {
			continue
		}
		key := avatar.CacheKey(chat.Avatar, avatar.RoleChatList)
		if _, exists := state.Avatars[key]; exists {
			continue
		}
		state.Avatars[key] = AvatarState{Loading: true, Ref: chat.Avatar, Role: avatar.RoleChatList, Label: chat.Title}
		commands = append(commands, RenderAvatar{Key: key, Ref: chat.Avatar, Role: avatar.RoleChatList})
	}
	return commands
}

func requestMissingMessageAvatars(state *State, messages []domain.Message) []Effect {
	commands := make([]Effect, 0)
	for _, message := range messages {
		if message.Outgoing || message.SenderAvatar.UniqueID == "" {
			continue
		}
		key := avatar.CacheKey(message.SenderAvatar, avatar.RoleMessageGroup)
		if _, exists := state.Avatars[key]; exists {
			continue
		}
		state.Avatars[key] = AvatarState{Loading: true, Ref: message.SenderAvatar, Role: avatar.RoleMessageGroup, Label: message.SenderName}
		commands = append(commands, RenderAvatar{Key: key, Ref: message.SenderAvatar, Role: avatar.RoleMessageGroup})
	}
	return commands
}

// requestMissingThumbnails appends a DownloadThumbnail command for every
// photo, video, sticker, document, animation or video-note message whose
// thumbnail block has not been rendered yet and whose thumbnail file is
// reachable (either still downloadable or already on disk). The shared
// thumbnail intake covers both message-loading entries (history page load and
// live message upsert) so inline previews trigger in every path.
func requestMissingThumbnails(state *State, messages []domain.Message) []Effect {
	commands := make([]Effect, 0)
	for _, message := range messages {
		switch message.Kind {
		case domain.MessagePhoto, domain.MessageVideo, domain.MessageSticker,
			domain.MessageDocument, domain.MessageAnimation, domain.MessageVideoNote:
		default:
			continue
		}
		if rendered := state.Thumbnails[message.ChatID][message.ID]; rendered.Width > 0 && rendered.Height > 0 {
			continue
		}
		thumb := message.Media.Thumbnail
		if thumb.ID == 0 {
			continue
		}
		if !thumb.CanDownload && !(thumb.Downloaded && thumb.LocalPath != "") {
			continue
		}
		requestID := allocateRequestID(state)
		commands = append(commands, DownloadThumbnail{RequestID: requestID, ChatID: message.ChatID, MessageID: message.ID, File: thumb})
	}
	return commands
}

func openPhotoSend(state *State) []Effect {
	// Guards: online, can send, no active edit session, valid chat.
	if state.Connection != domain.ConnectionOnline {
		return nil
	}
	if state.EditTarget != nil {
		return nil
	}
	if state.Chats == nil {
		return nil
	}
	if state.SelectedChat < 0 || state.SelectedChat >= len(state.Chats) {
		return nil
	}
	chat := state.Chats[state.SelectedChat]
	if !chat.CanSend {
		return nil
	}
	if forumTopicClosed(*state, chat.ID) {
		return nil
	}
	ps := state.PhotoSend
	if ps == nil {
		ps = &PhotoSendState{}
	}
	ps.PreviousFocus = state.Focus
	ps.ChatID = chat.ID
	if chat.IsForum {
		ps.TopicID = state.SelectedTopics[chat.ID]
	} else {
		ps.TopicID = 0
	}
	state.PhotoSend = ps
	state.Focus = FocusPhotoSend
	return nil
}

func reducePhotoSend(state *State, event ActionReceived) []Effect {
	ps := state.PhotoSend
	switch event.Action {
	case NoAction:
		if event.Rune != 0 && event.Rune != '\r' && event.Rune != '\n' {
			ps.Input = append(ps.Input, event.Rune)
		}
	case ComposerBackspace:
		if size := len(ps.Input); size > 0 {
			ps.Input = ps.Input[:size-1]
		}
	case Close:
		state.Focus = ps.PreviousFocus
		state.PhotoSend = nil
		return nil
	case PhotoSendSubmit:
		// Submit contract:
		// 1. chatID must still be active
		activeChatIDVal, activeOk := activeChatID(*state)
		if !activeOk || activeChatIDVal != ps.ChatID {
			return nil
		}
		path := string(ps.Input)
		if strings.TrimSpace(path) == "" {
			return nil
		}
		// Recheck guards
		chatIdx := chatIndex(state.Chats, activeChatIDVal)
		if chatIdx < 0 || !state.Chats[chatIdx].CanSend || state.Connection != domain.ConnectionOnline || state.EditTarget != nil {
			return nil
		}
		caption := state.Drafts[activeChatIDVal]
		replyID := domain.MessageID(0)
		if state.ReplyTarget != nil && state.ReplyTarget.ChatID == activeChatIDVal {
			replyID = state.ReplyTarget.MessageID
		}
		localID := allocateLocalID(state)
		requestID := allocateRequestID(state)
		isVideo := isVideoPath(path)
		isAudio := isAudioPath(path)
		isPhoto := isPhotoPath(path)
		var messageKind domain.MessageKind
		switch {
		case isVideo:
			messageKind = domain.MessageVideo
		case isAudio:
			messageKind = domain.MessageAudio
		case isPhoto:
			messageKind = domain.MessagePhoto
		default:
			messageKind = domain.MessageDocument
		}
		message := domain.Message{
			ID:        localID,
			ChatID:    activeChatIDVal,
			TopicID:   ps.TopicID,
			SentAt:    event.At,
			Kind:      messageKind,
			Text:      caption,
			Outgoing:  true,
			SendState: domain.SendPending,
			Media: domain.MessageMedia{
				File: domain.MediaFileRef{
					LocalPath:  path,
					Downloaded: true,
				},
			},
		}
		if messageKind == domain.MessageDocument {
			message.FileName = filepath.Base(path)
		}
		if replyID > 0 {
			message.ReplyToMessageID = replyID
			message.HasReply = true
		}
		state.Messages[activeChatIDVal] = limitMessages(mergeMessages(state.Messages[activeChatIDVal], []domain.Message{message}))
		state.Drafts[activeChatIDVal] = ""
		delete(state.DraftReplies, activeChatIDVal)
		state.ReplyTarget = nil
		state.PhotoSend = nil
		state.Focus = FocusConversation
		state.SelectedMessageChat = activeChatIDVal
		state.SelectedMessage = localID
		saveDraftCommand := queueDraftSave(state, activeChatIDVal)
		if isVideo {
			if state.VideoSendRequests == nil {
				state.VideoSendRequests = make(map[domain.MessageID]uint64)
			}
			state.VideoSendRequests[localID] = requestID
			return []Effect{SendVideo{
				RequestID:        requestID,
				LocalID:          localID,
				ChatID:           activeChatIDVal,
				TopicID:          ps.TopicID,
				LocalPath:        path,
				Caption:          caption,
				ReplyToMessageID: replyID,
			}, saveDraftCommand}
		}
		if isAudio {
			if state.AudioSendRequests == nil {
				state.AudioSendRequests = make(map[domain.MessageID]uint64)
			}
			state.AudioSendRequests[localID] = requestID
			return []Effect{SendAudio{
				RequestID:        requestID,
				LocalID:          localID,
				ChatID:           activeChatIDVal,
				TopicID:          ps.TopicID,
				LocalPath:        path,
				Caption:          caption,
				ReplyToMessageID: replyID,
			}, saveDraftCommand}
		}
		if !isPhoto {
			if state.DocumentSendRequests == nil {
				state.DocumentSendRequests = make(map[domain.MessageID]uint64)
			}
			state.DocumentSendRequests[localID] = requestID
			return []Effect{SendDocument{
				RequestID:        requestID,
				LocalID:          localID,
				ChatID:           activeChatIDVal,
				TopicID:          ps.TopicID,
				LocalPath:        path,
				Caption:          caption,
				ReplyToMessageID: replyID,
			}, saveDraftCommand}
		}
		if state.PhotoSendRequests == nil {
			state.PhotoSendRequests = make(map[domain.MessageID]uint64)
		}
		state.PhotoSendRequests[localID] = requestID
		return []Effect{SendPhoto{
			RequestID:        requestID,
			LocalID:          localID,
			ChatID:           activeChatIDVal,
			TopicID:          ps.TopicID,
			LocalPath:        path,
			Caption:          caption,
			ReplyToMessageID: replyID,
		}, saveDraftCommand}
	default:
		// every other action is full-state no-op
		return nil
	}
	state.PhotoSend = ps
	return nil
}

// isVideoPath returns true if the file path looks like a video file.
// It checks for common video extensions and video/* MIME prefixes.
func isVideoPath(path string) bool {
	lower := strings.ToLower(path)
	videoExtensions := []string{".mp4", ".mov", ".avi", ".mkv", ".webm"}
	for _, ext := range videoExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	if strings.HasPrefix(lower, "video/") {
		return true
	}
	return false
}

func isAudioPath(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range []string{".mp3", ".m4a", ".aac", ".flac", ".wav", ".ogg", ".opus"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func isPhotoPath(path string) bool {
	lower := strings.ToLower(path)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func replaceQueuedMessage(state *State, event TextQueued) {
	chatID, index, ok := findMessage(*state, event.LocalID)
	if !ok {
		return
	}
	original := state.Messages[chatID][index]
	replacement := event.Message
	preserveReplyIdentity(original, &replacement)
	preserveTopicIdentity(original, &replacement)
	if replacement.ChatID == 0 {
		replacement.ChatID = chatID
	}
	if replacement.Text == "" {
		replacement.Text = original.Text
	}
	if replacement.SentAt.IsZero() {
		replacement.SentAt = original.SentAt
	}
	replacement.Outgoing = true
	replacement.SendState = domain.SendPending
	removeMessageAt(state, chatID, index)
	state.Messages[replacement.ChatID] = mergeMessages(state.Messages[replacement.ChatID], []domain.Message{replacement})
	if state.SelectedMessage == event.LocalID {
		state.SelectedMessage = replacement.ID
	}
	reconcileMenuIdentity(state, chatID, event.LocalID, replacement)
}

func failQueuedMessage(state *State, event TextQueueFailed) {
	chatID, index, ok := findMessage(*state, event.LocalID)
	if !ok {
		return
	}
	message := &state.Messages[chatID][index]
	message.SendState = domain.SendFailed
	failure := event.Error
	message.Failure = &failure
	message.RetryAt = event.FailedAt.Add(event.Error.RetryAfter)
}

// photoCorrelation validates that the photo event matches a pending photo send.
// Returns (chatID, index, true) if valid, (0, -1, false) if not.
func photoCorrelation(state State, event PhotoQueued) (domain.ChatID, int, bool) {
	if event.ChatID == 0 {
		return 0, -1, false
	}
	requestID, ok := state.PhotoSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return 0, -1, false
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return 0, -1, false
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessagePhoto || msg.SendState != domain.SendPending {
		return 0, -1, false
	}
	return event.ChatID, index, true
}

func replaceQueuedPhoto(state *State, event PhotoQueued) {
	chatID, index, ok := photoCorrelation(*state, event)
	if !ok {
		return
	}
	// Validate returned message
	if event.Message.ID == 0 {
		return
	}
	if event.Message.Kind != domain.MessagePhoto {
		return
	}
	if event.Message.ChatID != 0 && event.Message.ChatID != chatID {
		return
	}
	original := state.Messages[chatID][index]
	replacement := event.Message
	// Force required fields
	replacement.ChatID = chatID
	replacement.Kind = domain.MessagePhoto
	replacement.Outgoing = true
	replacement.SendState = domain.SendPending
	replacement.Failure = nil
	// Preserve original caption if replacement empty
	if replacement.Text == "" {
		replacement.Text = original.Text
	}
	// Preserve SentAt if zero
	if replacement.SentAt.IsZero() {
		replacement.SentAt = original.SentAt
	}
	// Preserve reply identity
	preserveReplyIdentity(original, &replacement)
	preserveTopicIdentity(original, &replacement)
	// Preserve media: keep original LocalPath/Downloaded if replacement empty
	if replacement.Media.File.LocalPath == "" {
		replacement.Media.File.LocalPath = original.Media.File.LocalPath
		replacement.Media.File.Downloaded = original.Media.File.Downloaded
	}
	removeMessageAt(state, chatID, index)
	state.Messages[replacement.ChatID] = limitMessages(mergeMessages(state.Messages[replacement.ChatID], []domain.Message{replacement}))
	if state.SelectedMessage == event.LocalID {
		state.SelectedMessage = replacement.ID
	}
	reconcileMenuIdentity(state, chatID, event.LocalID, replacement)
	delete(state.PhotoSendRequests, event.LocalID)
}

func failQueuedPhoto(state *State, event PhotoQueueFailed) {
	if event.ChatID == 0 {
		return
	}
	requestID, ok := state.PhotoSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessagePhoto || msg.SendState != domain.SendPending {
		return
	}
	message := &state.Messages[event.ChatID][index]
	message.SendState = domain.SendFailed
	failure := event.Error
	message.Failure = &failure
	message.RetryAt = event.FailedAt.Add(event.Error.RetryAfter)
	delete(state.PhotoSendRequests, event.LocalID)
}

// videoCorrelation validates that the video event matches a pending video send.
func videoCorrelation(state State, event VideoQueued) (domain.ChatID, int, bool) {
	if event.ChatID == 0 {
		return 0, -1, false
	}
	requestID, ok := state.VideoSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return 0, -1, false
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return 0, -1, false
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessageVideo || msg.SendState != domain.SendPending {
		return 0, -1, false
	}
	return event.ChatID, index, true
}

func replaceQueuedVideo(state *State, event VideoQueued) {
	chatID, index, ok := videoCorrelation(*state, event)
	if !ok {
		return
	}
	if event.Message.ID == 0 {
		return
	}
	if event.Message.Kind != domain.MessageVideo {
		return
	}
	if event.Message.ChatID != 0 && event.Message.ChatID != chatID {
		return
	}
	original := state.Messages[chatID][index]
	replacement := event.Message
	replacement.ChatID = chatID
	replacement.Kind = domain.MessageVideo
	replacement.Outgoing = true
	replacement.SendState = domain.SendPending
	replacement.Failure = nil
	if replacement.Text == "" {
		replacement.Text = original.Text
	}
	if replacement.SentAt.IsZero() {
		replacement.SentAt = original.SentAt
	}
	preserveReplyIdentity(original, &replacement)
	preserveTopicIdentity(original, &replacement)
	if replacement.Media.File.LocalPath == "" {
		replacement.Media.File.LocalPath = original.Media.File.LocalPath
		replacement.Media.File.Downloaded = original.Media.File.Downloaded
	}
	removeMessageAt(state, chatID, index)
	state.Messages[replacement.ChatID] = limitMessages(mergeMessages(state.Messages[replacement.ChatID], []domain.Message{replacement}))
	if state.SelectedMessage == event.LocalID {
		state.SelectedMessage = replacement.ID
	}
	reconcileMenuIdentity(state, chatID, event.LocalID, replacement)
	delete(state.VideoSendRequests, event.LocalID)
}

func failQueuedVideo(state *State, event VideoQueueFailed) {
	if event.ChatID == 0 {
		return
	}
	requestID, ok := state.VideoSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessageVideo || msg.SendState != domain.SendPending {
		return
	}
	message := &state.Messages[event.ChatID][index]
	message.SendState = domain.SendFailed
	failure := event.Error
	message.Failure = &failure
	message.RetryAt = event.FailedAt.Add(event.Error.RetryAfter)
	delete(state.VideoSendRequests, event.LocalID)
}

func audioCorrelation(state State, event AudioQueued) (domain.ChatID, int, bool) {
	if event.ChatID == 0 {
		return 0, -1, false
	}
	requestID, ok := state.AudioSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return 0, -1, false
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return 0, -1, false
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessageAudio || msg.SendState != domain.SendPending {
		return 0, -1, false
	}
	return event.ChatID, index, true
}

func replaceQueuedAudio(state *State, event AudioQueued) {
	chatID, index, ok := audioCorrelation(*state, event)
	if !ok || event.Message.ID == 0 || event.Message.Kind != domain.MessageAudio {
		return
	}
	if event.Message.ChatID != 0 && event.Message.ChatID != chatID {
		return
	}
	original := state.Messages[chatID][index]
	replacement := event.Message
	replacement.ChatID = chatID
	replacement.Kind = domain.MessageAudio
	replacement.Outgoing = true
	replacement.SendState = domain.SendPending
	replacement.Failure = nil
	if replacement.Text == "" {
		replacement.Text = original.Text
	}
	if replacement.SentAt.IsZero() {
		replacement.SentAt = original.SentAt
	}
	preserveReplyIdentity(original, &replacement)
	preserveTopicIdentity(original, &replacement)
	if replacement.Media.File.LocalPath == "" {
		replacement.Media.File.LocalPath = original.Media.File.LocalPath
		replacement.Media.File.Downloaded = original.Media.File.Downloaded
	}
	removeMessageAt(state, chatID, index)
	state.Messages[replacement.ChatID] = limitMessages(mergeMessages(state.Messages[replacement.ChatID], []domain.Message{replacement}))
	if state.SelectedMessage == event.LocalID {
		state.SelectedMessage = replacement.ID
	}
	reconcileMenuIdentity(state, chatID, event.LocalID, replacement)
	delete(state.AudioSendRequests, event.LocalID)
}

func failQueuedAudio(state *State, event AudioQueueFailed) {
	if event.ChatID == 0 {
		return
	}
	requestID, ok := state.AudioSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessageAudio || msg.SendState != domain.SendPending {
		return
	}
	message := &state.Messages[event.ChatID][index]
	message.SendState = domain.SendFailed
	failure := event.Error
	message.Failure = &failure
	message.RetryAt = event.FailedAt.Add(event.Error.RetryAfter)
	delete(state.AudioSendRequests, event.LocalID)
}

func documentCorrelation(state State, event DocumentQueued) (domain.ChatID, int, bool) {
	if event.ChatID == 0 {
		return 0, -1, false
	}
	requestID, ok := state.DocumentSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return 0, -1, false
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return 0, -1, false
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessageDocument || msg.SendState != domain.SendPending {
		return 0, -1, false
	}
	return event.ChatID, index, true
}

func replaceQueuedDocument(state *State, event DocumentQueued) {
	chatID, index, ok := documentCorrelation(*state, event)
	if !ok || event.Message.ID == 0 || event.Message.Kind != domain.MessageDocument {
		return
	}
	if event.Message.ChatID != 0 && event.Message.ChatID != chatID {
		return
	}
	if event.Message.ID != event.LocalID && messageIndex(state.Messages[chatID], event.Message.ID) >= 0 {
		return
	}
	original := state.Messages[chatID][index]
	replacement := event.Message
	replacement.ChatID = chatID
	replacement.Kind = domain.MessageDocument
	replacement.Outgoing = true
	replacement.SendState = domain.SendPending
	replacement.Failure = nil
	if replacement.Text == "" {
		replacement.Text = original.Text
	}
	if replacement.FileName == "" {
		replacement.FileName = original.FileName
	}
	if replacement.SentAt.IsZero() {
		replacement.SentAt = original.SentAt
	}
	preserveReplyIdentity(original, &replacement)
	preserveTopicIdentity(original, &replacement)
	if replacement.Media.File.LocalPath == "" {
		replacement.Media.File.LocalPath = original.Media.File.LocalPath
		replacement.Media.File.Downloaded = original.Media.File.Downloaded
	}
	removeMessageAt(state, chatID, index)
	state.Messages[replacement.ChatID] = limitMessages(mergeMessages(state.Messages[replacement.ChatID], []domain.Message{replacement}))
	if state.SelectedMessage == event.LocalID {
		state.SelectedMessage = replacement.ID
	}
	reconcileMenuIdentity(state, chatID, event.LocalID, replacement)
	delete(state.DocumentSendRequests, event.LocalID)
}

func failQueuedDocument(state *State, event DocumentQueueFailed) {
	if event.ChatID == 0 {
		return
	}
	requestID, ok := state.DocumentSendRequests[event.LocalID]
	if !ok || requestID != event.RequestID {
		return
	}
	index := messageIndex(state.Messages[event.ChatID], event.LocalID)
	if index < 0 {
		return
	}
	msg := state.Messages[event.ChatID][index]
	if msg.Kind != domain.MessageDocument || msg.SendState != domain.SendPending {
		return
	}
	message := &state.Messages[event.ChatID][index]
	message.SendState = domain.SendFailed
	failure := event.Error
	message.Failure = &failure
	message.RetryAt = event.FailedAt.Add(event.Error.RetryAfter)
	delete(state.DocumentSendRequests, event.LocalID)
}

func replaceSentMessage(state *State, oldID domain.MessageID, replacement domain.Message) {
	chatID := replacement.ChatID
	if chatID == 0 {
		return
	}
	index := messageIndex(state.Messages[chatID], oldID)
	if index < 0 {
		return
	}
	original := state.Messages[chatID][index]
	preserveReplyIdentity(original, &replacement)
	preserveTopicIdentity(original, &replacement)
	if replacement.Text == "" {
		replacement.Text = original.Text
	}
	// Preserve optimistic media when TDLib omits it from a terminal update.
	if replacement.Media.File.LocalPath == "" {
		if original.Kind == domain.MessagePhoto && replacement.Kind == domain.MessagePhoto {
			replacement.Media.File.LocalPath = original.Media.File.LocalPath
			replacement.Media.File.Downloaded = original.Media.File.Downloaded
		}
		if original.Kind == domain.MessageVideo && replacement.Kind == domain.MessageVideo {
			replacement.Media.File.LocalPath = original.Media.File.LocalPath
			replacement.Media.File.Downloaded = original.Media.File.Downloaded
		}
		if original.Kind == domain.MessageAudio && replacement.Kind == domain.MessageAudio {
			replacement.Media.File.LocalPath = original.Media.File.LocalPath
			replacement.Media.File.Downloaded = original.Media.File.Downloaded
		}
		if original.Kind == domain.MessageDocument && replacement.Kind == domain.MessageDocument {
			replacement.Media.File.LocalPath = original.Media.File.LocalPath
			replacement.Media.File.Downloaded = original.Media.File.Downloaded
		}
	}
	if original.Kind == domain.MessageSticker && replacement.Kind == domain.MessageSticker {
		mergeStickerMedia(original, &replacement)
		moveThumbnail(state, chatID, oldID, replacement.ID)
	}
	removeMessageAt(state, chatID, index)
	state.Messages[replacement.ChatID] = limitMessages(mergeMessages(state.Messages[replacement.ChatID], []domain.Message{replacement}))
	if state.SelectedMessage == oldID {
		state.SelectedMessage = replacement.ID
	}
	reconcileMenuIdentity(state, chatID, oldID, replacement)
}

func failSentMessage(state *State, update telegram.MessageSendFailed, receivedAt time.Time) {
	chatID := update.Message.ChatID
	if chatID == 0 {
		return
	}
	index := messageIndex(state.Messages[chatID], update.OldID)
	if index < 0 {
		return
	}
	original := state.Messages[chatID][index]
	failed := update.Message
	failed.RetryText = original.RetryText
	preserveReplyIdentity(original, &failed)
	preserveTopicIdentity(original, &failed)
	if failed.ID == 0 {
		failed.ID = update.OldID
	}
	if failed.ChatID == 0 {
		failed.ChatID = chatID
	}
	if failed.Text == "" {
		failed.Text = original.Text
	}
	if failed.SentAt.IsZero() {
		failed.SentAt = original.SentAt
	}
	failed.Outgoing = true
	failed.SendState = domain.SendFailed
	failure := update.Error
	failed.Failure = &failure
	failed.RetryAt = receivedAt.Add(update.Error.RetryAfter)
	// Preserve optimistic photo source media when TDLib returns empty LocalPath.
	if original.Kind == domain.MessagePhoto && failed.Kind == domain.MessagePhoto &&
		failed.Media.File.LocalPath == "" {
		failed.Media.File.LocalPath = original.Media.File.LocalPath
		failed.Media.File.Downloaded = original.Media.File.Downloaded
	}
	// Preserve optimistic video source media when TDLib returns empty LocalPath.
	if original.Kind == domain.MessageVideo && failed.Kind == domain.MessageVideo &&
		failed.Media.File.LocalPath == "" {
		failed.Media.File.LocalPath = original.Media.File.LocalPath
		failed.Media.File.Downloaded = original.Media.File.Downloaded
	}
	// Preserve optimistic audio source media when TDLib returns empty LocalPath.
	if original.Kind == domain.MessageAudio && failed.Kind == domain.MessageAudio &&
		failed.Media.File.LocalPath == "" {
		failed.Media.File.LocalPath = original.Media.File.LocalPath
		failed.Media.File.Downloaded = original.Media.File.Downloaded
	}
	// Preserve optimistic document source media when TDLib returns empty LocalPath.
	if original.Kind == domain.MessageDocument && failed.Kind == domain.MessageDocument &&
		failed.Media.File.LocalPath == "" {
		failed.Media.File.LocalPath = original.Media.File.LocalPath
		failed.Media.File.Downloaded = original.Media.File.Downloaded
	}
	if original.Kind == domain.MessageSticker && failed.Kind == domain.MessageSticker {
		mergeStickerMedia(original, &failed)
		moveThumbnail(state, chatID, update.OldID, failed.ID)
	}
	state.Messages[chatID][index] = failed
	if state.SelectedMessage == update.OldID {
		state.SelectedMessage = failed.ID
	}
	reconcileMenuIdentity(state, chatID, update.OldID, failed)
}

func reconcileMenuIdentity(state *State, chatID domain.ChatID, oldID domain.MessageID, replacement domain.Message) {
	if state.MessageMenu == nil || state.MessageMenu.ChatID != chatID || state.MessageMenu.MessageID != oldID {
		return
	}
	state.MessageMenu.MessageID = replacement.ID
	state.MessageMenu.Capabilities = replacement.Capabilities()
}

func mergeMessages(existing, incoming []domain.Message) []domain.Message {
	byID := make(map[domain.MessageID]domain.Message, len(existing)+len(incoming))
	for _, message := range existing {
		byID[message.ID] = cloneDomainMessage(message)
	}
	for _, message := range incoming {
		cloned := cloneDomainMessage(message)
		if existing, ok := byID[message.ID]; ok && len(cloned.Reactions) == 0 && len(existing.Reactions) > 0 {
			// A same-ID re-upsert snapshot often carries no reaction data (TDLib
			// sends reactions only via UpdateMessageReactions). Preserve the
			// live-updated reactions so they survive history reloads.
			cloned.Reactions = append([]domain.MessageReaction(nil), existing.Reactions...)
		}
		byID[message.ID] = cloned
	}
	result := make([]domain.Message, 0, len(byID))
	for _, message := range byID {
		result = append(result, message)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].SentAt.Equal(result[right].SentAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].SentAt.Before(result[right].SentAt)
	})
	return result
}

func limitMessagesForOlderPage(messages []domain.Message) []domain.Message {
	for len(messages) > maxMessagesPerChat {
		remove := -1
		for index := len(messages) - 1; index >= 0; index-- {
			if !protectedOutgoingMessage(messages[index]) {
				remove = index
				break
			}
		}
		if remove < 0 {
			break
		}
		messages = append(messages[:remove], messages[remove+1:]...)
	}
	return messages
}

func limitMessages(messages []domain.Message) []domain.Message {
	for len(messages) > maxMessagesPerChat {
		remove := -1
		for index, message := range messages {
			if !protectedOutgoingMessage(message) {
				remove = index
				break
			}
		}
		if remove < 0 {
			break
		}
		messages = append(messages[:remove], messages[remove+1:]...)
	}
	return messages
}

func protectedOutgoingMessage(message domain.Message) bool {
	return message.Outgoing && (message.SendState == domain.SendPending || message.SendState == domain.SendFailed)
}

func preserveReplyIdentity(original domain.Message, replacement *domain.Message) {
	if replacement == nil || replacement.ReplyToMessageID > 0 || original.ReplyToMessageID <= 0 {
		return
	}
	replacement.ReplyToMessageID = original.ReplyToMessageID
	replacement.HasReply = true
}

func preserveTopicIdentity(original domain.Message, replacement *domain.Message) {
	if replacement == nil || replacement.TopicID != 0 || original.TopicID == 0 {
		return
	}
	replacement.TopicID = original.TopicID
}

func sortChats(chats []domain.Chat) {
	sort.SliceStable(chats, func(left, right int) bool {
		if chats[left].Order == chats[right].Order {
			return chats[left].ID > chats[right].ID
		}
		return chats[left].Order > chats[right].Order
	})
}

func upsertChat(chats *[]domain.Chat, chat domain.Chat) {
	if index := chatIndex(*chats, chat.ID); index >= 0 {
		(*chats)[index] = chat
		return
	}
	*chats = append(*chats, chat)
}

func preserveChatSelection(state *State, selectedID domain.ChatID) {
	if index := chatIndex(state.Chats, selectedID); index >= 0 {
		state.SelectedChat = index
	} else if len(state.Chats) == 0 {
		state.SelectedChat = 0
	} else {
		state.SelectedChat = min(state.SelectedChat, len(state.Chats)-1)
	}
}

func preserveChatFocus(state *State, focusedID domain.ChatID) {
	if index := chatIndex(state.Chats, focusedID); index >= 0 {
		state.FocusedChat = index
	} else if len(state.Chats) == 0 {
		state.FocusedChat = -1
	} else {
		state.FocusedChat = max(0, min(state.FocusedChat, len(state.Chats)-1))
	}
}

func activeChatID(state State) (domain.ChatID, bool) {
	if state.SelectedChat < 0 || state.SelectedChat >= len(state.Chats) {
		return 0, false
	}
	return state.Chats[state.SelectedChat].ID, true
}

func detailsChatIndex(state State) int {
	if state.DetailsChatID != 0 {
		return chatIndex(state.Chats, state.DetailsChatID)
	}
	if state.SelectedChat >= 0 && state.SelectedChat < len(state.Chats) {
		return state.SelectedChat
	}
	return -1
}

func detailsChat(state State) (domain.Chat, bool) {
	index := detailsChatIndex(state)
	if index < 0 {
		return domain.Chat{}, false
	}
	return state.Chats[index], true
}

func focusedChatIndex(state State) int {
	if state.FocusedChat >= 0 && state.FocusedChat < len(state.Chats) {
		return state.FocusedChat
	}
	if state.SelectedChat >= 0 && state.SelectedChat < len(state.Chats) {
		return state.SelectedChat
	}
	return -1
}

func focusedChatID(state State) (domain.ChatID, bool) {
	index := focusedChatIndex(state)
	if index < 0 {
		return 0, false
	}
	return state.Chats[index].ID, true
}

func chatIndex(chats []domain.Chat, id domain.ChatID) int {
	for index, chat := range chats {
		if chat.ID == id {
			return index
		}
	}
	return -1
}

func nextMatchingChatID(state State, match func(domain.Chat) bool) (domain.ChatID, bool) {
	focused := focusedChatIndex(state)
	if match == nil || len(state.Chats) < 2 || focused < 0 {
		return 0, false
	}
	for step := 1; step < len(state.Chats); step++ {
		index := (focused + step) % len(state.Chats)
		if match(state.Chats[index]) {
			return state.Chats[index].ID, true
		}
	}
	return 0, false
}

func messageIndex(messages []domain.Message, id domain.MessageID) int {
	for index, message := range messages {
		if message.ID == id {
			return index
		}
	}
	return -1
}

func findMessage(state State, id domain.MessageID) (domain.ChatID, int, bool) {
	for chatID, messages := range state.Messages {
		if index := messageIndex(messages, id); index >= 0 {
			return chatID, index, true
		}
	}
	return 0, -1, false
}

func removeMessageAt(state *State, chatID domain.ChatID, index int) {
	messages := state.Messages[chatID]
	state.Messages[chatID] = append(messages[:index], messages[index+1:]...)
}

func newestMessageID(messages []domain.Message) domain.MessageID {
	if len(messages) == 0 {
		return 0
	}
	return messages[len(messages)-1].ID
}

func selectNewestMessage(state *State, chatID domain.ChatID) {
	state.SelectedMessage = newestMessageID(state.Messages[chatID])
	if state.SelectedMessage == 0 {
		state.SelectedMessageChat = 0
	} else {
		state.SelectedMessageChat = chatID
	}
	state.MessageMenu = nil
}

func selectedMessage(state State) (domain.Message, bool) {
	return messageByIdentity(state, state.SelectedMessageChat, state.SelectedMessage)
}

func messageByIdentity(state State, chatID domain.ChatID, messageID domain.MessageID) (domain.Message, bool) {
	if chatID == 0 || messageID == 0 {
		return domain.Message{}, false
	}
	index := messageIndex(state.Messages[chatID], messageID)
	if index < 0 {
		return domain.Message{}, false
	}
	return state.Messages[chatID][index], true
}

func selectAdjacentMessage(state *State, previous bool) []Effect {
	chatID, ok := activeChatID(*state)
	if !ok {
		return nil
	}
	messages := visibleConversationMessages(*state, chatID)
	if len(messages) == 0 {
		return nil
	}
	index := messageIndex(messages, state.SelectedMessage)
	moved := index < 0
	if index < 0 {
		index = len(messages) - 1
	} else {
		next := min(len(messages)-1, index+1)
		if previous {
			next = max(0, index-1)
		}
		moved = next != index
		index = next
	}
	state.SelectedMessageChat = chatID
	state.SelectedMessage = messages[index].ID
	state.MessageMenu = nil
	key, topicActive := activeTopicKey(*state)
	if _, known := visibleConversationTopic(*state, chatID); !known {
		topicActive = false
	}
	var history HistoryState
	var exists bool
	if topicActive {
		history, exists = state.TopicHistory[key]
	} else {
		history, exists = state.History[chatID]
	}
	if moved {
		history.ViewOffset = clampOffset(len(messages)-1-index, len(messages))
		history.FollowSelection = true
	}
	var commands []Effect
	if previous && index == 0 && exists && !history.Loading && !history.Done {
		requestID := allocateRequestID(state)
		history.Loading = true
		history.RequestID = requestID
		command := LoadMessages{RequestID: requestID, ChatID: chatID, Cursor: telegram.MessageCursor{FromMessageID: history.OldestID, Limit: pageSize}}
		if topicActive {
			command.TopicID = key.TopicID
		}
		commands = []Effect{command}
	}
	if moved || len(commands) != 0 {
		if topicActive {
			state.TopicHistory[key] = history
		} else {
			state.History[chatID] = history
		}
	}
	return commands
}

func openMessageActionMenu(state *State) []Effect {
	message, ok := selectedMessage(*state)
	if !ok {
		return nil
	}
	local := domain.MessageCapabilities{Copy: true}
	canReact := false
	if index := chatIndex(state.Chats, message.ChatID); index >= 0 {
		canReact = localReactCapable(state.Chats[index], message)
	}
	var mediaFile domain.MediaFileRef
	switch message.Kind {
	case domain.MessageVideo, domain.MessageAudio, domain.MessagePhoto,
		domain.MessageDocument, domain.MessageAnimation, domain.MessageVoiceNote, domain.MessageVideoNote:
		mediaFile, _ = messageKindFile(message, message.Kind)
	}
	// Hide media while an external open is pending to prevent double-dispatch.
	if _, pending := state.VideoOpenPending[message.ID]; pending {
		mediaFile = domain.MediaFileRef{}
	}
	if _, pending := state.AudioOpenPending[message.ID]; pending {
		mediaFile = domain.MediaFileRef{}
	}
	if _, pending := state.AttachmentOpenPending[attachmentOpenKey{ChatID: message.ChatID, MessageID: message.ID}]; pending {
		mediaFile = domain.MediaFileRef{}
	}
	var senderID domain.UserID
	if message.Sender.Kind == domain.SenderUser && message.Sender.ID != 0 {
		senderID = domain.UserID(message.Sender.ID)
	}
	referenceID := domain.MessageID(0)
	if message.HasReply && message.ReplyToMessageID > 0 {
		referenceID = message.ReplyToMessageID
	}
	state.MessageMenu = &MessageActionMenu{ChatID: message.ChatID, MessageID: message.ID, ReferencedMessageID: referenceID, UserID: senderID, Pinned: message.Pinned, Capabilities: local, PreviousFocus: state.Focus, CanReact: canReact, MediaFile: mediaFile, MediaKind: message.Kind, Links: messageLinks(message)}
	state.Focus = FocusModal
	if message.ID <= 0 || message.SendState == domain.SendPending || message.SendState == domain.SendFailed || message.Service || message.Kind == domain.MessageService {
		return nil
	}
	requestID := allocateRequestID(state)
	state.MessageMenu.RequestID = requestID
	state.MessageMenu.Loading = true
	return []Effect{GetMessageProperties{RequestID: requestID, ChatID: message.ChatID, MessageID: message.ID}}
}

func syncMessageMenuLinks(state *State, message domain.Message) {
	menu := state.MessageMenu
	if menu == nil || menu.ChatID != message.ChatID || menu.MessageID != message.ID {
		return
	}
	links := messageLinks(message)
	if !slices.Equal(menu.Links, links) {
		menu.Links = links
		menu.LinkAction = NoAction // an edited message requires a fresh target choice
		clampMessageMenuSelection(menu)
	}
}

func messageMenuMatches(menu *MessageActionMenu, requestID uint64, chatID domain.ChatID, messageID domain.MessageID) bool {
	return menu != nil && menu.RequestID == requestID && menu.ChatID == chatID && menu.MessageID == messageID
}

func modalMatchesMedia(modal *ModalState, requestID uint64, chatID domain.ChatID, messageID domain.MessageID, file domain.MediaFileRef) bool {
	if modal == nil {
		return false
	}
	if modal.RequestID != requestID {
		return false
	}
	if modal.MediaChatID != chatID || modal.MediaMessageID != messageID {
		return false
	}
	return mediaIdentityMatches(modal.MediaFile, file)
}

func clampMessageMenuSelection(menu *MessageActionMenu) {
	count := actionMenuItemCount(menu)
	if count == 0 {
		menu.Selected = 0
		return
	}
	menu.Selected = max(0, min(menu.Selected, count-1))
}

func selectMessageMenuAction(menu *MessageActionMenu, action Action) {
	index := 0
	if mediaEligible(menu.MediaFile) {
		if action == ViewMessageMedia {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.Reply {
		if action == ReplyMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.ReferencedMessageID > 0 {
		if action == GoToReferencedMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.Forward {
		if action == ForwardMessageSource {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.Edit {
		if action == EditMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.Copy {
		if action == CopyMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if len(menu.Links) > 0 {
		if action == CopyMessageLink {
			menu.Selected = index
			return
		}
		index++
		if action == OpenMessageLink {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.UserID != 0 {
		if action == ViewUserInfo {
			menu.Selected = index
			return
		}
		index++
	}
	if reactRowVisible(menu) {
		if action == ReactMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.Pin {
		if action == PinMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.DeleteForSelf {
		if action == DeleteMessage {
			menu.Selected = index
			return
		}
		index++
	}
	if menu.Capabilities.DeleteForAll {
		if action == DeleteForEveryone {
			menu.Selected = index
			return
		}
		index++
	}
	if index == 0 {
		menu.Selected = 0
		return
	}
	menu.Selected = min(menu.Selected, index-1)
}

func actionMenuItemCount(menu *MessageActionMenu) int {
	count := 0
	if mediaEligible(menu.MediaFile) {
		count++
	}
	if menu.Capabilities.Reply {
		count++
	}
	if menu.ReferencedMessageID > 0 {
		count++
	}
	if menu.Capabilities.Forward {
		count++
	}
	if menu.Capabilities.Edit {
		count++
	}
	if menu.Capabilities.Copy {
		count++
	}
	if len(menu.Links) > 0 {
		count += 2
	}
	if menu.UserID != 0 {
		count++
	}
	if reactRowVisible(menu) {
		count++
	}
	if menu.Capabilities.Pin {
		count++
	}
	if menu.Capabilities.DeleteForSelf {
		count++
	}
	if menu.Capabilities.DeleteForAll {
		count++
	}
	return count
}

func selectedMenuAction(menu *MessageActionMenu) Action {
	if menu == nil {
		return NoAction
	}
	index := 0
	if mediaEligible(menu.MediaFile) {
		if menu.Selected == index {
			return ViewMessageMedia
		}
		index++
	}
	if menu.Capabilities.Reply {
		if menu.Selected == index {
			return ReplyMessage
		}
		index++
	}
	if menu.ReferencedMessageID > 0 {
		if menu.Selected == index {
			return GoToReferencedMessage
		}
		index++
	}
	if menu.Capabilities.Forward {
		if menu.Selected == index {
			return ForwardMessageSource
		}
		index++
	}
	if menu.Capabilities.Edit {
		if menu.Selected == index {
			return EditMessage
		}
		index++
	}
	if menu.Capabilities.Copy {
		if menu.Selected == index {
			return CopyMessage
		}
		index++
	}
	if len(menu.Links) > 0 {
		if menu.Selected == index {
			return CopyMessageLink
		}
		index++
		if menu.Selected == index {
			return OpenMessageLink
		}
		index++
	}
	if menu.UserID != 0 {
		if menu.Selected == index {
			return ViewUserInfo
		}
		index++
	}
	if reactRowVisible(menu) {
		if menu.Selected == index {
			return ReactMessage
		}
		index++
	}
	if menu.Capabilities.Pin {
		if menu.Selected == index {
			return PinMessage
		}
		index++
	}
	if menu.Capabilities.DeleteForSelf {
		if menu.Selected == index {
			return DeleteMessage
		}
		index++
	}
	if menu.Capabilities.DeleteForAll && menu.Selected == index {
		return DeleteForEveryone
	}
	return NoAction
}

func editableMessage(message domain.Message) bool {
	return message.ID > 0 && message.Kind == domain.MessageText && !message.Service && message.SendState != domain.SendPending && message.SendState != domain.SendFailed
}

func beginEdit(state *State, message domain.Message) []Effect {
	state.EditTarget = &EditTarget{ChatID: message.ChatID, MessageID: message.ID, Original: message.Text, Buffer: message.Text}
	state.MessageMenu = nil
	state.ReplyTarget = nil
	state.Focus = FocusComposer
	return nil
}

func beginDelete(state *State, menu *MessageActionMenu, revoke bool) []Effect {
	requestID := allocateRequestID(state)
	menu.RequestID = requestID
	state.MessageMenu = menu
	return []Effect{DeleteMessageCommand{RequestID: requestID, ChatID: menu.ChatID, MessageID: menu.MessageID, Revoke: revoke}}
}

func beginPin(state *State, menu *MessageActionMenu) []Effect {
	requestID := allocateRequestID(state)
	menu.RequestID = requestID
	state.MessageMenu = menu
	return []Effect{PinMessageCommand{RequestID: requestID, ChatID: menu.ChatID, MessageID: menu.MessageID, Unpin: menu.Pinned}}
}

func deleteMessageSuccess(state *State, chatID domain.ChatID, messageID domain.MessageID) {
	index := messageIndex(state.Messages[chatID], messageID)
	if index >= 0 {
		removeMessageAt(state, chatID, index)
	}
	if state.SelectedMessageChat == chatID && state.SelectedMessage == messageID {
		messages := state.Messages[chatID]
		if len(messages) == 0 {
			state.SelectedMessageChat, state.SelectedMessage = 0, 0
		} else {
			nearest := index
			if nearest < 0 {
				nearest = 0
			}
			nearest = min(nearest, len(messages)-1)
			state.SelectedMessageChat = chatID
			state.SelectedMessage = messages[nearest].ID
		}
	}
	if state.ReplyTarget != nil && state.ReplyTarget.ChatID == chatID && state.ReplyTarget.MessageID == messageID {
		state.ReplyTarget = nil
	}
	if state.EditTarget != nil && state.EditTarget.ChatID == chatID && state.EditTarget.MessageID == messageID {
		state.EditTarget = nil
	}
	state.MessageMenu = nil
	state.Focus = FocusConversation
}

func beginForward(state *State, message domain.Message) []Effect {
	requestID := allocateRequestID(state)
	state.ForwardPicker = &ForwardPicker{
		SourceChatID:    message.ChatID,
		SourceMessageID: message.ID,
		SelectedChat:    0,
		RequestID:       requestID,
	}
	state.MessageMenu = nil
	state.Focus = FocusForwardPicker
	return nil
}

func reduceForwardPicker(state *State, event ActionReceived) []Effect {
	picker := state.ForwardPicker
	switch event.Action {
	case SelectNext, SelectPrevious:
		count := len(state.Chats)
		if count == 0 {
			break
		}
		delta := 1
		if event.Action == SelectPrevious {
			delta = -1
		}
		picker.SelectedChat = (picker.SelectedChat + delta + count) % count
	case SelectChat:
		state.ForwardPicker = nil
		state.Focus = FocusConversation
		return reduceAction(state, event)
	case Activate:
		if _, ok := messageByIdentity(*state, picker.SourceChatID, picker.SourceMessageID); !ok {
			state.ForwardPicker = nil
			state.Focus = FocusConversation
			break
		}
		destination := domain.ChatID(0)
		if event.ChatID != 0 {
			index := chatIndex(state.Chats, event.ChatID)
			if index < 0 {
				break
			}
			picker.SelectedChat = index
			destination = event.ChatID
		} else {
			if picker.SelectedChat < 0 || picker.SelectedChat >= len(state.Chats) {
				break
			}
			destination = state.Chats[picker.SelectedChat].ID
		}
		if destination == 0 {
			break
		}
		return []Effect{ForwardMessageCommand{
			RequestID:         picker.RequestID,
			SourceChatID:      picker.SourceChatID,
			SourceMessageID:   picker.SourceMessageID,
			DestinationChatID: destination,
		}}
	case Close:
		state.ForwardPicker = nil
		state.Focus = FocusConversation
	}
	return nil
}

func forwardPickerMatches(picker *ForwardPicker, requestID uint64, destinationChatID domain.ChatID, chats []domain.Chat) bool {
	if picker == nil || picker.RequestID != requestID || destinationChatID == 0 {
		return false
	}
	if picker.SelectedChat < 0 || picker.SelectedChat >= len(chats) {
		return false
	}
	return chats[picker.SelectedChat].ID == destinationChatID
}

func reconcileForwardPicker(state *State) {
	if state.ForwardPicker == nil {
		return
	}
	picker := state.ForwardPicker
	if _, ok := messageByIdentity(*state, picker.SourceChatID, picker.SourceMessageID); !ok {
		state.ForwardPicker = nil
		if state.Focus == FocusForwardPicker {
			state.Focus = FocusConversation
		}
	}
}

func localReactCapable(chat domain.Chat, message domain.Message) bool {
	return chat.CanReact &&
		message.ID > 0 &&
		!message.Service &&
		message.SendState != domain.SendPending &&
		message.SendState != domain.SendFailed
}

func reactRowVisible(menu *MessageActionMenu) bool {
	return menu != nil && menu.CanReact && !menu.Loading && menu.Error == nil
}

func mediaEligible(file domain.MediaFileRef) bool {
	return (file.Downloaded && file.LocalPath != "") || (file.ID != 0 && file.CanDownload)
}

func mediaIdentityMatches(a, b domain.MediaFileRef) bool {
	if a.ID != 0 && b.ID != 0 {
		return a.ID == b.ID
	}
	return a.UniqueID != "" && b.UniqueID != "" && a.UniqueID == b.UniqueID
}

// messageKindFile returns the main file for an exact-kind message when the
// file is eligible for an explicit open action: already downloaded with a
// local path, or remotely downloadable.
func messageKindFile(message domain.Message, kind domain.MessageKind) (domain.MediaFileRef, bool) {
	if message.Kind != kind {
		return domain.MediaFileRef{}, false
	}
	file := message.Media.File
	switch {
	case file.Downloaded && file.LocalPath != "":
		return file, true
	case file.ID != 0 && file.CanDownload:
		return file, true
	default:
		return domain.MediaFileRef{}, false
	}
}

func messagePhotoFile(message domain.Message) (domain.MediaFileRef, bool) {
	return messageKindFile(message, domain.MessagePhoto)
}

func messageVideoFile(message domain.Message) (domain.MediaFileRef, bool) {
	return messageKindFile(message, domain.MessageVideo)
}

func messageAudioFile(message domain.Message) (domain.MediaFileRef, bool) {
	return messageKindFile(message, domain.MessageAudio)
}

// attachmentTitleForKind maps an attachment kind to its external-open title.
func attachmentTitleForKind(kind domain.MessageKind) (string, bool) {
	switch kind {
	case domain.MessageDocument:
		return "File", true
	case domain.MessageAnimation:
		return "Animation", true
	case domain.MessageVoiceNote:
		return "Voice note", true
	case domain.MessageVideoNote:
		return "Video note", true
	}
	return "", false
}

// attachmentToasts returns the opening and success toast messages for an
// external-open attachment title.
func attachmentToasts(title string) (opening, success string, ok bool) {
	switch title {
	case "File":
		return "Opening file…", "File opened", true
	case "Animation":
		return "Opening animation…", "Animation opened", true
	case "Voice note":
		return "Opening voice note…", "Voice note opened", true
	case "Video note":
		return "Opening video note…", "Video note opened", true
	}
	return "", "", false
}

func reactionChosen(reactions []domain.MessageReaction, emoji string) bool {
	for _, reaction := range reactions {
		if reaction.Emoji == emoji && reaction.Chosen {
			return true
		}
	}
	return false
}

func beginReact(state *State, message domain.Message) []Effect {
	requestID := allocateRequestID(state)
	state.ReactionPicker = &ReactionPicker{
		ChatID:    message.ChatID,
		MessageID: message.ID,
		RequestID: requestID,
	}
	state.MessageMenu = nil
	state.Focus = FocusReactionPicker
	return nil
}

func openMediaModalFromMenu(state *State) []Effect {
	menu := state.MessageMenu
	if menu == nil || !mediaEligible(menu.MediaFile) {
		return nil
	}
	file := menu.MediaFile
	currentMessage, ok := messageByIdentity(*state, menu.ChatID, menu.MessageID)
	if !ok {
		return nil
	}

	// Check for Video first — video follows toast+no-modal path.
	if currentMessage.Kind == domain.MessageVideo {
		currentFile, eligible := messageVideoFile(currentMessage)
		if !eligible {
			return nil
		}
		if !mediaIdentityMatches(currentFile, file) {
			return nil
		}
		chatID := menu.ChatID
		messageID := menu.MessageID

		// Stale-guard: if already pending for this message, suppress.
		if _, pending := state.VideoOpenPending[messageID]; pending {
			state.MessageMenu = nil
			return nil
		}

		requestID := allocateRequestID(state)
		if state.VideoOpenPending == nil {
			state.VideoOpenPending = make(map[domain.MessageID]uint64)
		}
		state.VideoOpenPending[messageID] = requestID

		state.MessageMenu = nil
		state.Focus = FocusConversation
		setToast(state, domain.AppError{Message: "Opening video…"}, 2*time.Second)
		return []Effect{OpenMessageMediaFile{RequestID: requestID, ChatID: chatID, MessageID: messageID, Title: "Video", File: file}}
	}

	if currentMessage.Kind == domain.MessageAudio {
		currentFile, eligible := messageAudioFile(currentMessage)
		if !eligible || !mediaIdentityMatches(currentFile, file) {
			return nil
		}
		chatID := menu.ChatID
		messageID := menu.MessageID
		if _, pending := state.AudioOpenPending[messageID]; pending {
			state.MessageMenu = nil
			return nil
		}
		requestID := allocateRequestID(state)
		if state.AudioOpenPending == nil {
			state.AudioOpenPending = make(map[domain.MessageID]uint64)
		}
		state.AudioOpenPending[messageID] = requestID
		state.MessageMenu = nil
		state.Focus = FocusConversation
		setToast(state, domain.AppError{Message: "Opening audio…"}, 2*time.Second)
		return []Effect{OpenMessageMediaFile{RequestID: requestID, ChatID: chatID, MessageID: messageID, Title: "Audio", File: file}}
	}

	// Attachment paths (File, Animation, Voice note, Video note) follow the
	// toast+no-modal external-open path with title-aware correlation.
	if title, ok := attachmentTitleForKind(currentMessage.Kind); ok {
		currentFile, eligible := messageKindFile(currentMessage, currentMessage.Kind)
		if !eligible || !mediaIdentityMatches(currentFile, file) {
			return nil
		}
		chatID := menu.ChatID
		messageID := menu.MessageID
		attachmentKey := attachmentOpenKey{ChatID: chatID, MessageID: messageID}
		if _, pending := state.AttachmentOpenPending[attachmentKey]; pending {
			state.MessageMenu = nil
			return nil
		}
		requestID := allocateRequestID(state)
		if state.AttachmentOpenPending == nil {
			state.AttachmentOpenPending = make(map[attachmentOpenKey]attachmentOpenRequest)
		}
		state.AttachmentOpenPending[attachmentKey] = attachmentOpenRequest{
			RequestID: requestID,
			Kind:      currentMessage.Kind,
			Title:     title,
			File:      file,
		}
		state.MessageMenu = nil
		state.Focus = FocusConversation
		if opening, _, _ := attachmentToasts(title); opening != "" {
			setToast(state, domain.AppError{Message: opening}, 2*time.Second)
		}
		return []Effect{OpenMessageMediaFile{RequestID: requestID, ChatID: chatID, MessageID: messageID, Title: title, File: file}}
	}

	// Photo path — keep existing modal behavior.
	currentFile, eligible := messagePhotoFile(currentMessage)
	if !eligible {
		return nil
	}
	if !mediaIdentityMatches(currentFile, file) {
		return nil
	}
	chatID := menu.ChatID
	messageID := menu.MessageID
	previousFocus := menu.PreviousFocus
	state.MessageMenu = nil
	state.Focus = FocusModal
	if file.Downloaded && file.LocalPath != "" {
		state.Modal = &ModalState{
			RequestID:      0,
			Title:          "Photo",
			MediaChatID:    chatID,
			MediaMessageID: messageID,
			MediaFile:      file,
			Path:           file.LocalPath,
			Loading:        false,
			PreviousFocus:  previousFocus,
		}
		return nil
	}
	requestID := allocateRequestID(state)
	state.Modal = &ModalState{
		RequestID:      requestID,
		Title:          "Photo",
		MediaChatID:    chatID,
		MediaMessageID: messageID,
		MediaFile:      file,
		Loading:        true,
		PreviousFocus:  previousFocus,
	}
	return []Effect{OpenMessageMediaFile{RequestID: requestID, ChatID: chatID, MessageID: messageID, Title: "Photo", File: file}}
}

func reduceReactionPicker(state *State, event ActionReceived) []Effect {
	picker := state.ReactionPicker
	switch event.Action {
	case SelectNext, SelectPrevious:
		count := len(ReactionPalette)
		if count == 0 {
			break
		}
		delta := 1
		if event.Action == SelectPrevious {
			delta = -1
		}
		picker.Selected = (picker.Selected + delta + count) % count
	case Activate:
		index := picker.Selected
		if event.Rune >= 0x10000 {
			if target := int(event.Rune) - 0x10000; target >= 0 && target < len(ReactionPalette) {
				index = target
			}
		}
		if index < 0 || index >= len(ReactionPalette) {
			break
		}
		messageIdx := messageIndex(state.Messages[picker.ChatID], picker.MessageID)
		if messageIdx < 0 {
			state.ReactionPicker = nil
			state.Focus = FocusConversation
			break
		}
		emoji := ReactionPalette[index]
		remove := reactionChosen(state.Messages[picker.ChatID][messageIdx].Reactions, emoji)
		return []Effect{ReactToMessage{RequestID: picker.RequestID, ChatID: picker.ChatID, MessageID: picker.MessageID, Emoji: emoji, Remove: remove}}
	case Close:
		state.ReactionPicker = nil
		state.Focus = FocusConversation
	}
	return nil
}

func reactionPickerMatches(picker *ReactionPicker, requestID uint64, chatID domain.ChatID, messageID domain.MessageID) bool {
	return picker != nil && picker.RequestID == requestID && picker.ChatID == chatID && picker.MessageID == messageID
}

func reconcileReactionPicker(state *State) {
	if state.ReactionPicker == nil {
		return
	}
	picker := state.ReactionPicker
	if _, ok := messageByIdentity(*state, picker.ChatID, picker.MessageID); !ok {
		state.ReactionPicker = nil
		if state.Focus == FocusReactionPicker {
			state.Focus = FocusConversation
		}
	}
}

func editTargetMatches(target *EditTarget, requestID uint64, chatID domain.ChatID, messageID domain.MessageID) bool {
	return target != nil && target.RequestID == requestID && target.ChatID == chatID && target.MessageID == messageID
}

func editableTargetPresent(state State) bool {
	if state.EditTarget == nil {
		return false
	}
	message, ok := messageByIdentity(state, state.EditTarget.ChatID, state.EditTarget.MessageID)
	return ok && editableMessage(message)
}

func reduceComposerValueChanged(state *State, event ComposerValueChanged) []Effect {
	if state.Quitting {
		return nil
	}
	if state.Focus != FocusComposer {
		return nil
	}
	activeID, active := activeChatID(*state)
	if !active || activeID != event.ChatID {
		return nil
	}

	// Edit mode: EditTarget is set.
	if state.EditTarget != nil {
		if event.EditMessageID == 0 {
			// Zero edit ID with an edit target present — no-op; do not fall back to draft.
			return nil
		}
		if event.ChatID != state.EditTarget.ChatID || event.EditMessageID != state.EditTarget.MessageID {
			return nil
		}
		if state.EditTarget.Submitting {
			return nil
		}
		if !editableTargetPresent(*state) {
			return nil
		}
		target := state.EditTarget
		if target.Buffer == event.Value {
			return nil
		}
		target.Buffer = event.Value
		return nil
	}

	// Draft mode: EditTarget is nil.
	if event.EditMessageID != 0 {
		return nil
	}
	if allKey, ok := showAllActive(*state); ok {
		if state.TopicDrafts[allKey] == event.Value {
			return nil
		}
		if state.TopicDrafts == nil {
			state.TopicDrafts = make(map[topicKey]string)
		}
		state.TopicDrafts[allKey] = event.Value
		commands := syncCommandMenuForValue(state, event.ChatID, event.Value)
		commands = append(commands, queueTopicDraftSave(state, allKey))
		return commands
	}
	if key, ok := activeTopicKey(*state); ok {
		if state.TopicDrafts[key] == event.Value {
			return nil
		}
		if state.TopicDrafts == nil {
			state.TopicDrafts = make(map[topicKey]string)
		}
		state.TopicDrafts[key] = event.Value
		commands := syncCommandMenuForValue(state, event.ChatID, event.Value)
		commands = append(commands, queueTopicDraftSave(state, key))
		return commands
	}
	if state.SelectedChat >= 0 && state.SelectedChat < len(state.Chats) && state.Chats[state.SelectedChat].IsForum {
		// A forum without a selected topic (and not in ALL mode) has no
		// composer target.
		return nil
	}
	if state.Drafts[event.ChatID] == event.Value {
		return nil
	}
	if state.Drafts == nil {
		state.Drafts = make(map[domain.ChatID]string)
	}
	state.Drafts[event.ChatID] = event.Value
	commands := syncCommandMenuForValue(state, event.ChatID, event.Value)
	commands = append(commands, queueDraftSave(state, event.ChatID))
	return commands
}

func reducePromptValueChanged(state *State, event PromptValueChanged) []Effect {
	if state.Prompt == nil || state.Prompt.Submitting {
		return nil
	}
	if state.Focus != FocusAuth {
		return nil
	}
	if event.PromptID == 0 {
		return nil
	}
	if state.Prompt.Prompt.ID != event.PromptID {
		return nil
	}
	if state.Quitting {
		return nil
	}
	state.Prompt.Input = []rune(event.Value)
	return nil
}

func reducePhotoPathValueChanged(state *State, event PhotoPathValueChanged) []Effect {
	if state.PhotoSend == nil {
		return nil
	}
	if state.Focus != FocusPhotoSend {
		return nil
	}
	if event.ChatID == 0 {
		return nil
	}
	if state.PhotoSend.ChatID != event.ChatID {
		return nil
	}
	if state.Quitting {
		return nil
	}
	input := make([]rune, 0, len(event.Value))
	for _, r := range event.Value {
		if r == '\r' || r == '\n' {
			continue
		}
		input = append(input, r)
	}
	state.PhotoSend.Input = input
	return nil
}

func beginReply(state *State, message domain.Message) []Effect {
	if allKey, ok := showAllActive(*state); ok && message.ChatID == allKey.ChatID {
		// In ALL mode any visible message is a reply target; the reply is
		// stored at the ALL key (TopicID 0) and sent to the General topic.
		state.ReplyTarget = replyTargetFromMessage(message)
		state.ReplyTarget.TopicID = 0
		if state.TopicDraftReplies == nil {
			state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
		}
		state.TopicDraftReplies[allKey] = message.ID
		state.MessageMenu = nil
		state.Focus = FocusComposer
		return []Effect{queueTopicDraftSave(state, allKey)}
	}
	state.ReplyTarget = replyTargetFromMessage(message)
	if message.TopicID != 0 {
		key := topicKey{ChatID: message.ChatID, TopicID: message.TopicID}
		if state.TopicDraftReplies == nil {
			state.TopicDraftReplies = make(map[topicKey]domain.MessageID)
		}
		state.TopicDraftReplies[key] = message.ID
		state.MessageMenu = nil
		state.Focus = FocusComposer
		return []Effect{queueTopicDraftSave(state, key)}
	}
	if state.DraftReplies == nil {
		state.DraftReplies = make(map[domain.ChatID]domain.MessageID)
	}
	state.DraftReplies[message.ChatID] = message.ID
	state.MessageMenu = nil
	state.Focus = FocusComposer
	return []Effect{queueDraftSave(state, message.ChatID)}
}

func replyTargetFromMessage(message domain.Message) *ReplyTarget {
	preview := strings.ReplaceAll(message.DisplayText(), "\n", " ")
	const maxPreview = 48
	runes := []rune(preview)
	if len(runes) > maxPreview {
		preview = string(runes[:maxPreview])
	}
	return &ReplyTarget{ChatID: message.ChatID, TopicID: message.TopicID, MessageID: message.ID, Sender: message.SenderName, Preview: preview}
}

func restoreActiveDraftReply(state *State) {
	chatID, ok := activeChatID(*state)
	if !ok {
		state.ReplyTarget = nil
		return
	}
	if allKey, ok := showAllActive(*state); ok {
		replyID := state.TopicDraftReplies[allKey]
		if replyID <= 0 {
			state.ReplyTarget = nil
			return
		}
		if message, ok := messageByIdentity(*state, chatID, replyID); ok {
			target := replyTargetFromMessage(message)
			target.TopicID = 0
			state.ReplyTarget = target
			return
		}
		state.ReplyTarget = &ReplyTarget{ChatID: chatID, MessageID: replyID, Sender: "Message", Preview: "Reply draft"}
		return
	}
	if key, ok := activeTopicKey(*state); ok {
		replyID := state.TopicDraftReplies[key]
		if replyID <= 0 {
			state.ReplyTarget = nil
			return
		}
		if message, ok := messageByIdentity(*state, chatID, replyID); ok {
			target := replyTargetFromMessage(message)
			target.TopicID = key.TopicID
			state.ReplyTarget = target
			return
		}
		state.ReplyTarget = &ReplyTarget{ChatID: chatID, TopicID: key.TopicID, MessageID: replyID, Sender: "Message", Preview: "Reply draft"}
		return
	}
	replyID := state.DraftReplies[chatID]
	if replyID <= 0 {
		state.ReplyTarget = nil
		return
	}
	if message, ok := messageByIdentity(*state, chatID, replyID); ok {
		state.ReplyTarget = replyTargetFromMessage(message)
		return
	}
	state.ReplyTarget = &ReplyTarget{ChatID: chatID, MessageID: replyID, Sender: "Message", Preview: "Reply draft"}
}

func currentDraft(state State, chatID domain.ChatID) domain.Draft {
	replyID := state.DraftReplies[chatID]
	if state.ReplyTarget != nil && state.ReplyTarget.ChatID == chatID {
		replyID = state.ReplyTarget.MessageID
	}
	return domain.Draft{
		Text:             state.Drafts[chatID],
		ReplyToMessageID: replyID,
		Date:             state.DraftDates[chatID],
	}
}

func queueDraftSave(state *State, chatID domain.ChatID) SaveDraft {
	if state.DraftSync == nil {
		state.DraftSync = make(map[domain.ChatID]DraftSyncState)
	}
	requestID := allocateRequestID(state)
	draft := currentDraft(*state, chatID)
	syncState := DraftSyncState{RequestID: requestID, Draft: draft, Pending: true, Dirty: true}
	state.DraftSync[chatID] = syncState
	setChatDraftSnapshot(state, chatID, draft)
	return SaveDraft{RequestID: requestID, ChatID: chatID, Text: draft.Text, ReplyToMessageID: draft.ReplyToMessageID}
}

func currentTopicDraft(state State, key topicKey) domain.Draft {
	replyID := state.TopicDraftReplies[key]
	if state.ReplyTarget != nil && state.ReplyTarget.ChatID == key.ChatID && state.ReplyTarget.TopicID == key.TopicID {
		replyID = state.ReplyTarget.MessageID
	}
	return domain.Draft{
		Text:             state.TopicDrafts[key],
		ReplyToMessageID: replyID,
		Date:             state.TopicDraftDates[key],
	}
}

func queueTopicDraftSave(state *State, key topicKey) SaveDraft {
	if state.TopicDraftSync == nil {
		state.TopicDraftSync = make(map[topicKey]DraftSyncState)
	}
	requestID := allocateRequestID(state)
	draft := currentTopicDraft(*state, key)
	syncState := DraftSyncState{RequestID: requestID, Draft: draft, Pending: true, Dirty: true}
	state.TopicDraftSync[key] = syncState
	setForumTopicDraftSnapshot(state, key, draft)
	return SaveDraft{RequestID: requestID, ChatID: key.ChatID, TopicID: key.TopicID, Text: draft.Text, ReplyToMessageID: draft.ReplyToMessageID}
}

func applyCloudDraft(state *State, chatID domain.ChatID, draft domain.Draft) {
	if chatID == 0 {
		return
	}
	// ALL-mode drafts live in the topic draft stores at TopicID 0, even
	// when the chat is not the active one.
	if state.ShowAll[chatID] {
		applyTopicCloudDraft(state, topicKey{ChatID: chatID, TopicID: 0}, draft)
		return
	}
	if index := chatIndex(state.Chats, chatID); index >= 0 && state.Chats[index].IsForum {
		// A chat-level cloud draft in a forum is a topic-scoped draft
		// (the server scopes drafts per topic); it never touches the
		// chat-level draft maps.
		return
	}
	if state.Drafts == nil {
		state.Drafts = make(map[domain.ChatID]string)
	}
	if state.DraftReplies == nil {
		state.DraftReplies = make(map[domain.ChatID]domain.MessageID)
	}
	if state.DraftDates == nil {
		state.DraftDates = make(map[domain.ChatID]int64)
	}
	if state.DraftSync == nil {
		state.DraftSync = make(map[domain.ChatID]DraftSyncState)
	}

	syncState := state.DraftSync[chatID]
	local := currentDraft(*state, chatID)
	if (syncState.Dirty || syncState.Pending) && !sameDraftContent(local, draft) {
		return
	}
	state.Drafts[chatID] = draft.Text
	if draft.ReplyToMessageID > 0 {
		state.DraftReplies[chatID] = draft.ReplyToMessageID
	} else {
		delete(state.DraftReplies, chatID)
	}
	if draft.Date > 0 {
		state.DraftDates[chatID] = draft.Date
	} else {
		delete(state.DraftDates, chatID)
	}
	syncState.Draft = draft
	syncState.Dirty = false
	state.DraftSync[chatID] = syncState
	setChatDraftSnapshot(state, chatID, draft)
	if activeID, active := activeChatID(*state); active && activeID == chatID && state.EditTarget == nil {
		restoreActiveDraftReply(state)
	}
}

func releaseDraftGuard(state *State, chatID domain.ChatID) {
	if key, ok := activeTopicKey(*state); ok && key.ChatID == chatID {
		if syncState, exists := state.TopicDraftSync[key]; exists {
			syncState.Dirty = false
			state.TopicDraftSync[key] = syncState
		}
	}
	syncState, exists := state.DraftSync[chatID]
	if !exists {
		return
	}
	syncState.Dirty = false
	state.DraftSync[chatID] = syncState
}

func sameDraftContent(left, right domain.Draft) bool {
	return left.Text == right.Text && left.ReplyToMessageID == right.ReplyToMessageID
}

func setChatDraftSnapshot(state *State, chatID domain.ChatID, draft domain.Draft) {
	if index := chatIndex(state.Chats, chatID); index >= 0 {
		state.Chats[index].Draft = draft
	}
}

func setForumTopicDraftSnapshot(state *State, key topicKey, draft domain.Draft) {
	byTopic := state.ForumTopics[key.ChatID]
	if byTopic == nil {
		return
	}
	entry, exists := byTopic[key.TopicID]
	if !exists {
		return
	}
	entry.Draft = draft
	byTopic[key.TopicID] = entry
}

func setToast(state *State, value domain.AppError, duration time.Duration) {
	generation := state.NextToastGeneration
	if generation == 0 {
		generation = 1
	}
	state.NextToastGeneration = generation + 1
	state.ToastGeneration = generation
	state.ToastDuration = duration
	state.Toast = &value
}

func reconcileMessageSelection(state *State) {
	if _, ok := selectedMessage(*state); ok {
		return
	}
	state.SelectedMessageChat, state.SelectedMessage = 0, 0
	state.MessageMenu = nil
}

func reconcileReplyTarget(state *State, chatID domain.ChatID) {
	if state.ReplyTarget == nil || state.ReplyTarget.ChatID != chatID {
		return
	}
	if _, ok := messageByIdentity(*state, chatID, state.ReplyTarget.MessageID); !ok {
		state.ReplyTarget = nil
	}
}

func reconcileEditTarget(state *State, chatID domain.ChatID) {
	if state.EditTarget == nil || state.EditTarget.ChatID != chatID {
		return
	}
	message, ok := messageByIdentity(*state, chatID, state.EditTarget.MessageID)
	if !ok || !editableMessage(message) {
		state.EditTarget = nil
	}
}

func allocateRequestID(state *State) uint64 {
	if state.NextRequestID == 0 {
		state.NextRequestID = 1
	}
	requestID := state.NextRequestID
	state.NextRequestID++
	if state.NextRequestID == 0 {
		state.NextRequestID = 1
	}
	return requestID
}

func allocateLocalID(state *State) domain.MessageID {
	if state.NextLocalID >= 0 {
		state.NextLocalID = -1
	}
	localID := state.NextLocalID
	if state.NextLocalID == domain.MessageID(math.MinInt64) {
		state.NextLocalID = -1
	} else {
		state.NextLocalID--
	}
	return localID
}

func clampAllHistoryOffsets(state *State) {
	for chatID, history := range state.History {
		history.ViewOffset = clampOffset(history.ViewOffset, len(state.Messages[chatID]))
		state.History[chatID] = history
	}
}

func clampOffset(offset, count int) int {
	if count <= 1 {
		return 0
	}
	return max(0, min(offset, count-1))
}

func focusVisible(state State, focus Focus) bool {
	if state.DetailsOpen {
		if state.Layout == LayoutNormal || state.Layout == LayoutNarrow {
			return focus == FocusDetails
		}
		if focus == FocusDetails {
			return true
		}
	}
	switch state.Layout {
	case LayoutWide, LayoutNormal:
		return focus == FocusChats || focus == FocusConversation || focus == FocusComposer
	case LayoutNarrow:
		if state.Focus == FocusChats {
			return focus == FocusChats
		}
		return focus == FocusChats || focus == FocusConversation || focus == FocusComposer
	default:
		return false
	}
}

func cycleFocus(state *State, reverse bool) {
	// The composer is entered explicitly with i, not by cycling through panes.
	order := []Focus{FocusChats, FocusConversation, FocusDetails}
	visible := make([]Focus, 0, len(order))
	for _, focus := range order {
		if focusVisible(*state, focus) {
			visible = append(visible, focus)
		}
	}
	if len(visible) == 0 {
		return
	}
	// From the composer, either direction returns to the conversation pane.
	if state.Focus == FocusComposer && focusVisible(*state, FocusConversation) {
		state.Focus = FocusConversation
		return
	}
	index := 0
	for candidate, focus := range visible {
		if focus == state.Focus {
			index = candidate
			break
		}
	}
	if reverse {
		index = (index - 1 + len(visible)) % len(visible)
	} else {
		index = (index + 1) % len(visible)
	}
	state.Focus = visible[index]
}

func avatarPixelDimensions(role avatar.Role) (int, int) {
	if role == avatar.RoleMessageGroup {
		return 4, 4
	}
	return 6, 6
}

func cloneDomainMessage(message domain.Message) domain.Message {
	message.Entities = append([]domain.TextEntity(nil), message.Entities...)
	message.Failure = cloneDomainError(message.Failure)
	message.Reactions = append([]domain.MessageReaction(nil), message.Reactions...)
	return message
}

func cloneDomainError(value *domain.AppError) *domain.AppError {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func clonePixelAvatar(value pixel.Avatar) pixel.Avatar {
	value.Cells = append([]pixel.Cell(nil), value.Cells...)
	return value
}

func normalizeEvent(event Event) Event {
	switch event := event.(type) {
	case *Started:
		if event != nil {
			return *event
		}
	case *Resized:
		if event != nil {
			return *event
		}
	case *ActionReceived:
		if event != nil {
			return *event
		}
	case *ChatsLoaded:
		if event != nil {
			return *event
		}
	case *ChatsLoadFailed:
		if event != nil {
			return *event
		}
	case *MessagesLoaded:
		if event != nil {
			return *event
		}
	case *MessagesLoadFailed:
		if event != nil {
			return *event
		}
	case *TopicsLoaded:
		if event != nil {
			return *event
		}
	case *TopicsLoadFailed:
		if event != nil {
			return *event
		}
	case *UserInfoLoaded:
		if event != nil {
			return *event
		}
	case *UserInfoLoadFailed:
		if event != nil {
			return *event
		}
	case *MembersLoaded:
		if event != nil {
			return *event
		}
	case *MemberUsernameCopied:
		if event != nil {
			return *event
		}
	case *MemberUsernameCopyFailed:
		if event != nil {
			return *event
		}
	case *MemberContactChanged:
		if event != nil {
			return *event
		}
	case *MemberContactFailed:
		if event != nil {
			return *event
		}
	case *MemberBlockChanged:
		if event != nil {
			return *event
		}
	case *MemberBlockFailed:
		if event != nil {
			return *event
		}
	case *ChatActionApplied:
		if event != nil {
			return *event
		}
	case *ChatActionFailed:
		if event != nil {
			return *event
		}
	case *MembersLoadFailed:
		if event != nil {
			return *event
		}
	case *PinnedMessagesLoaded:
		if event != nil {
			return *event
		}
	case *PinnedMessagesLoadFailed:
		if event != nil {
			return *event
		}
	case *PinnedMessageContextLoaded:
		if event != nil {
			return *event
		}
	case *PinnedMessageContextFailed:
		if event != nil {
			return *event
		}
	case *TelegramEvent:
		if event != nil {
			return *event
		}
	case *DraftSaved:
		if event != nil {
			return *event
		}
	case *DraftSaveFailed:
		if event != nil {
			return *event
		}
	case *TextQueued:
		if event != nil {
			return *event
		}
	case *TextQueueFailed:
		if event != nil {
			return *event
		}
	case *PhotoQueued:
		if event != nil {
			return *event
		}
	case *PhotoQueueFailed:
		if event != nil {
			return *event
		}
	case *VideoQueued:
		if event != nil {
			return *event
		}
	case *VideoQueueFailed:
		if event != nil {
			return *event
		}
	case *AudioQueued:
		if event != nil {
			return *event
		}
	case *AudioQueueFailed:
		if event != nil {
			return *event
		}
	case *DocumentQueued:
		if event != nil {
			return *event
		}
	case *DocumentQueueFailed:
		if event != nil {
			return *event
		}
	case *StickersLoaded:
		if event != nil {
			return *event
		}
	case *StickersLoadFailed:
		if event != nil {
			return *event
		}
	case *StickerThumbnailRendered:
		if event != nil {
			return *event
		}
	case *StickerThumbnailFailed:
		if event != nil {
			return *event
		}
	case *StickerQueued:
		if event != nil {
			return *event
		}
	case *StickerQueueFailed:
		if event != nil {
			return *event
		}
	case *AvatarRendered:
		if event != nil {
			return *event
		}
	case *AvatarRenderFailed:
		if event != nil {
			return *event
		}
	case *AvatarOpenFailed:
		if event != nil {
			return *event
		}
	case *AvatarOpened:
		if event != nil {
			return *event
		}
	case *StartupFailed:
		if event != nil {
			return *event
		}
	case *ShutdownComplete:
		if event != nil {
			return *event
		}
	case *OperationFailed:
		if event != nil {
			return *event
		}
	case *PromptRequested:
		if event != nil {
			return *event
		}
	case *ClipboardWritten:
		if event != nil {
			return *event
		}
	case *WebLinkOpening:
		if event != nil {
			return *event
		}
	case *ClipboardWriteFailed:
		if event != nil {
			return *event
		}
	case *ToastExpired:
		if event != nil {
			return *event
		}
	case *MessageMediaOpened:
		if event != nil {
			return *event
		}
	case *MessageMediaOpenFailed:
		if event != nil {
			return *event
		}
	case *BotCommandsLoaded:
		if event != nil {
			return *event
		}
	case *BotCommandsLoadFailed:
		if event != nil {
			return *event
		}
	case *ThumbnailDownloaded:
		if event != nil {
			return *event
		}
	case *ThumbnailDownloadFailed:
		if event != nil {
			return *event
		}
	case *ThumbnailRendered:
		if event != nil {
			return *event
		}
	case *TerminalFocusChanged:
		if event != nil {
			return *event
		}
	case *PublicChatsSearched:
		if event != nil {
			return *event
		}
	case *PublicChatsSearchFailed:
		if event != nil {
			return *event
		}
	case *AllMessagesSearched:
		if event != nil {
			return *event
		}
	case *AllMessagesSearchFailed:
		if event != nil {
			return *event
		}
	case Started, Resized, AdministrationLoaded, ChatSettingsValueChanged, ChatSettingsLoaded, ChatSettingsLoadFailed, ChatSettingSaved, ChatSettingSaveFailed, AdministrationLoadFailed, MemberAdministrationLoaded, MemberAdministrationLoadFailed, DefaultPermissionsSaved, DefaultPermissionsSaveFailed, MemberAdministrationApplied, MemberAdministrationApplyFailed, ChatsLoaded, ChatsLoadFailed, TopicsLoaded, TopicsLoadFailed, MessagesLoaded, MessagesLoadFailed, ChatMessagesSearched, ChatMessagesSearchFailed, SearchMessageContextLoaded, SearchMessageContextFailed, MembersLoaded, MembersLoadFailed, InviteLinksLoaded, InviteLinksLoadFailed, InviteLinkCreated, InviteLinkCreateFailed, InviteLinkRevoked, InviteLinkRevokeFailed, InviteLinkCopied, InviteLinkCopyFailed, UserInfoLoaded, UserInfoLoadFailed, MemberUsernameCopied, MemberUsernameCopyFailed, MemberContactChanged, MemberContactFailed, MemberBlockChanged, MemberBlockFailed, ChatActionApplied, ChatActionFailed, PinnedMessagesLoaded, PinnedMessagesLoadFailed, PinnedMessageContextLoaded, PinnedMessageContextFailed, BotCommandsLoaded, BotCommandsLoadFailed, TelegramEvent, DraftSaved, DraftSaveFailed, TextQueued, TextQueueFailed, PhotoQueued, PhotoQueueFailed, VideoQueued, VideoQueueFailed, AudioQueued, AudioQueueFailed, DocumentQueued, DocumentQueueFailed, StickersLoaded, StickersLoadFailed, StickerThumbnailRendered, StickerThumbnailFailed, StickerQueued, StickerQueueFailed, AvatarRendered, AvatarRenderFailed, AvatarOpenFailed, AvatarOpened, StartupFailed, ShutdownComplete, OperationFailed, PromptRequested, ClipboardWritten, WebLinkOpening, ClipboardWriteFailed, ToastExpired, MessagePropertiesLoaded, MessagePropertiesLoadFailed, TextEdited, TextEditFailed, MessageDeleted, MessageDeleteFailed, MessageForwarded, MessageForwardFailed, MessagePinChanged, MessagePinFailed, ReactionChanged, ReactionFailed, MessageMediaOpened, MessageMediaOpenFailed, PublicChatSearched, PublicChatSearchFailed, PublicChatsSearched, PublicChatsSearchFailed, AllMessagesSearched, AllMessagesSearchFailed, ChatSearchValueChanged, ThumbnailDownloaded, ThumbnailDownloadFailed, ThumbnailRendered, ComposerValueChanged, PromptValueChanged, PhotoPathValueChanged, MessageSearchValueChanged, TerminalFocusChanged, ActionReceived:
		return event
	default:
		return nil
	}
	return nil
}
