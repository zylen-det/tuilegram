package frontend

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zylen-det/tuilegram/internal/domain"
	"github.com/zylen-det/tuilegram/internal/media/thumbnail"
)

// messageSelection is the active message selection used to decide whether a
// message renders its selected-card border.
type messageSelection struct {
	ChatID    domain.ChatID
	MessageID domain.MessageID
}

// stickerMessageInlineID derives a stable Kitty image identity from the
// placement owner rather than the picker-cached Sticker block identity.
func stickerMessageInlineID(chatID domain.ChatID, messageID domain.MessageID) uint32 {
	hash := fnv.New32a()
	var identity [17]byte
	identity[0] = 0x53 // Sticker-message placement namespace.
	binary.LittleEndian.PutUint64(identity[1:9], uint64(chatID))
	binary.LittleEndian.PutUint64(identity[9:17], uint64(messageID))
	_, _ = hash.Write(identity[:])
	id := hash.Sum32()
	if id == 0 {
		return 1
	}
	return id
}

// messageRowKind is the semantic style tier of one message row.
type messageRowKind uint8

const (
	messageRowPanel messageRowKind = iota
	messageRowMuted
	messageRowEmphasis
	messageRowError
)

// messageChipSpec is one reaction chip rendered intrinsically so the chosen
// accent cell is preserved.
type messageChipSpec struct {
	text   string
	chosen bool
}

// messageRowSpec is one semantic row of a message group.
type messageRowSpec struct {
	text         string
	kind         messageRowKind
	outgoing     bool
	chatID       domain.ChatID
	messageID    domain.MessageID
	frameID      domain.MessageID // includes reply/marker/padding rows without changing click identity
	chips        []messageChipSpec
	spans        []messageTextSpan
	senderHeader bool
	centered     bool
}

// messageGroupLocalInteraction is one interactive region of a message group.
// Rect is local to the message-group root; only a later stage translates it to
// the absolute viewport rectangle.
type messageGroupLocalInteraction struct {
	ID    string
	Rect  image.Rectangle
	Z     int
	Click ActionReceived
}

// messageGroupResult is the product of one message-group builder.
type messageGroupResult struct {
	Layer             *lipgloss.Layer // local root at (0,0), no ID
	Width             int
	Height            int
	Rows              []messageRowSpec
	Group             RenderedMessageGroup
	Selected          bool
	SelectedStart     int // selected message's first full-group row
	SelectedEnd       int // exclusive; zero when no message is selected
	RowOffset         int // original full-group row index represented by Rows[0]
	LocalInteractions []messageGroupLocalInteraction
	Inline            []inlinePlacement // Kitty thumbnails in full-group-local cells
}

// buildMessageGroupLayer builds one local message-group surface rooted at
// (0,0). An empty group or width <= 0 returns a zero result. The finished
// Layer width/height and result dimensions always equal width x len(Rows).
func buildMessageGroupLayer(
	group RenderedMessageGroup,
	width int,
	location *time.Location,
	selection messageSelection,
	inlineThumbnails map[domain.MessageID]thumbnail.Block,
	styles renderStyles,
) messageGroupResult {
	if len(group.Messages) == 0 || width <= 0 {
		return messageGroupResult{}
	}
	if location == nil {
		location = time.Local
	}

	rows, selected := buildMessageRows(group, width, location, selection, inlineThumbnails)
	start, end := 0, 0
	if selected {
		start, end = selectedMessageRows(rows, selection)
	}
	avatarVisible := group.ShowAvatar && image.Rect(0, 0, 4, 2).In(image.Rect(0, 0, width, len(rows)))

	root, interactions, inline := renderMessageGroupLayer(group, width, rows, 0, start, end, avatarVisible, 0, styles, inlineThumbnails, messageGroupFirstRows(rows))

	return messageGroupResult{
		Layer:             root,
		Width:             width,
		Height:            len(rows),
		Rows:              rows,
		Group:             group,
		Selected:          selected,
		SelectedStart:     start,
		SelectedEnd:       end,
		RowOffset:         0,
		LocalInteractions: interactions,
		Inline:            inline,
	}
}

// renderMessageGroupLayer is the single shared renderer for message-group
// surfaces. It lays out the semantic row slice, the selected message's frame,
// and the avatar inside a fresh local root at (0,0). rowOffset is the original
// full-group row index of rows[0]; message row IDs use that offset so sliced
// fragments keep the message's true row index. avatarVisible controls whether
// the avatar renders; avatarNudgeY shifts the avatar rectangle upward by that
// amount (0 for a full group, the slice start for a fragment that already
// applied vertical clipping above the avatar). firstRows maps each message to
// its first full-group row index. The returned Inline placements use
// full-group-local coordinates.
func renderMessageGroupLayer(
	group RenderedMessageGroup,
	width int,
	rows []messageRowSpec,
	rowOffset int,
	selectedStart, selectedEnd int,
	avatarVisible bool,
	avatarNudgeY int,
	styles renderStyles,
	inlineThumbnails map[domain.MessageID]thumbnail.Block,
	firstRows map[domain.MessageID]int,
) (*lipgloss.Layer, []messageGroupLocalInteraction, []inlinePlacement) {
	height := len(rows)

	root := lipgloss.NewLayer("").X(0).Y(0).Z(zCardFrame)
	var interactions []messageGroupLocalInteraction
	var inline []inlinePlacement

	// Frame only the selected message, not the whole sender/avatar group.
	// Clip the frame to the fragment when history scrolls through a message.
	if selectedEnd > selectedStart {
		start := max(0, selectedStart-rowOffset)
		end := min(height, selectedEnd-rowOffset)
		if start < end {
			x := 0
			if group.ShowAvatar {
				x = 4 // leave the avatar outside the frame at a stable position
			}
			frameWidth := width - x
			if frameWidth >= 2 && end-start >= 2 {
				content := styles.Panel.
					Border(lipgloss.RoundedBorder()).
					BorderForeground(styles.FocusedBorder.GetForeground()).
					BorderBackground(styles.FocusedBorder.GetBackground()).
					Width(frameWidth).Height(end - start).Render("")
				root.AddLayers(lipgloss.NewLayer(content).X(x).Y(start).Z(zCardFrame))
			} else if frameWidth > 0 {
				// A one-row viewport still shows where selection is.
				root.AddLayers(lipgloss.NewLayer(styles.FocusedBorder.Render("│")).X(x).Y(start).Z(zCardFrame))
			}
		}
	}

	// Semantic rows: full-width Panel background (optional identity/click) plus
	// intrinsic clipped text/chips at global Z content.
	for i, row := range rows {
		background := styles.Panel.Width(width).Height(1).Render("")
		bgLayer := lipgloss.NewLayer(background).X(0).Y(i).Z(zRowBackground)
		if row.messageID != 0 {
			id := fmt.Sprintf("message:%d:%d:%d", row.chatID, row.messageID, rowOffset+i)
			bgLayer.ID(id)
			interactions = append(interactions, messageGroupLocalInteraction{
				ID:    id,
				Rect:  image.Rect(0, i, width, i+1),
				Z:     zRowBackground,
				Click: ActionReceived{Action: SelectMessage, ChatID: row.chatID, MessageID: row.messageID},
			})
		}
		root.AddLayers(bgLayer)

		x := messageRowTextX(group, width, row)
		if len(row.chips) > 0 {
			cx := x
			for _, chip := range row.chips {
				chipStyle := styles.Muted
				if chip.chosen {
					chipStyle = styles.Accent
				}
				if cx >= width {
					break
				}
				clip := ansi.Truncate(chip.text, width-cx, "")
				if clip != "" {
					root.AddLayers(lipgloss.NewLayer(chipStyle.Render(clip)).X(cx).Y(i).Z(zContent))
				}
				cx += displayWidth(chip.text) + 1
			}
			continue
		}
		available := max(0, width-x)
		if available > 0 && row.text != "" {
			if row.senderHeader && group.HasSenderColor {
				name := strings.TrimSpace(group.SenderName)
				nameClip := ansi.Truncate(name, available, "")
				if nameClip != "" {
					root.AddLayers(lipgloss.NewLayer(styles.Emphasis.Foreground(group.SenderColor).Render(nameClip)).X(x).Y(i).Z(zContent))
				}
				used := displayWidth(nameClip)
				if used < available && used == displayWidth(name) {
					tail := ansi.Truncate(strings.TrimPrefix(row.text, name), available-used, "")
					if tail != "" {
						root.AddLayers(lipgloss.NewLayer(styles.Emphasis.Render(tail)).X(x + used).Y(i).Z(zContent))
					}
				}
				continue
			}
			if len(row.spans) > 0 {
				clip := ansi.Truncate(renderEntityRow(row, styles), available, "")
				if clip != "" {
					root.AddLayers(lipgloss.NewLayer(clip).X(x).Y(i).Z(zContent))
				}
			} else {
				clip := ansi.Truncate(row.text, available, "")
				if clip != "" {
					style := messageRowStyle(row.kind, styles)
					root.AddLayers(lipgloss.NewLayer(style.Render(clip)).X(x).Y(i).Z(zContent))
				}
			}
		}
	}

	// Avatar only when it fits the rendered group/visible slice so it never
	// escapes the box. The avatar rectangle is nudged upward by avatarNudgeY.
	if avatarVisible {
		avatarRect := image.Rect(0, 0, 4, 2)
		adjusted := image.Rect(avatarRect.Min.X, avatarRect.Min.Y-avatarNudgeY, avatarRect.Max.X, avatarRect.Max.Y-avatarNudgeY)
		avatarZ := zContent
		id := ""
		if group.AvatarError != nil {
			avatarZ = zControl
			first := group.Messages[0]
			id = fmt.Sprintf("message-avatar-retry:%d:%d", first.ChatID, first.ID)
		}
		if avatarLayer := buildAvatarLayer(adjusted, group.Avatar, group.AvatarKey, group.SenderName, styles, avatarZ); avatarLayer != nil {
			if id != "" {
				avatarLayer.ID(id)
				interactions = append(interactions, messageGroupLocalInteraction{
					ID:    id,
					Rect:  adjusted,
					Z:     zControl,
					Click: ActionReceived{Action: Retry, AvatarKey: group.AvatarKey},
				})
			}
			root.AddLayers(avatarLayer)
		}
	}

	// Place a rendered thumbnail block for each photo message in this group
	// that has one in inlineThumbnails. The thumbnail occupies block.Height
	// placeholder rows reserved before the message text. Half-block blocks are
	// plain ANSI content laid out as Layers; Kitty blocks are graphics
	// transmits that cannot pass through the text compositor, so they are
	// recorded as placements for post-frame emission.
	if len(inlineThumbnails) > 0 {
		// Build a map: for each messageID, the FIRST row index in this rows slice.
		// Inline thumbnail placeholders are reserved ABOVE the message text (see
		// buildMessageRows), so the thumbnail Layer anchors to the first row of
		// the message and occupies the next block.Height rows.
		firstRowIndex := make(map[domain.MessageID]int, len(rows))
		for i, row := range rows {
			if _, exists := firstRowIndex[row.messageID]; !exists && row.messageID != 0 {
				firstRowIndex[row.messageID] = i
			}
		}

		for _, msg := range group.Messages {
			block, hasBlock := inlineThumbnails[msg.ID]
			if !hasBlock || block.Width <= 0 || block.Height <= 0 {
				continue
			}

			// Right-align outgoing thumbnails against the message width;
			// left-align incoming ones starting at the avatar gutter (or 1).
			var x int
			if msg.Outgoing {
				x = max(0, width-block.Width-1)
			} else {
				incomingTextX := 1
				if group.ShowAvatar {
					incomingTextX = min(width, 5)
				}
				x = incomingTextX
			}

			if block.Kitty {
				imageID := block.ImageID
				transmit := block.Text
				if msg.Kind == domain.MessageSticker {
					sourceWidth, sourceHeight, ok := parseKittySourceDims(transmit)
					if !ok {
						continue
					}
					imageID = stickerMessageInlineID(msg.ChatID, msg.ID)
					var err error
					transmit, err = rewriteKittyTransmit(transmit, imageID, block.Width, block.Height, 0, 0, sourceWidth, sourceHeight)
					if err != nil {
						continue
					}
				}
				// Place any thumbnail whose full cell box intersects the
				// rendered rows so partially scrolled thumbnails surface a
				// cropped visible part; buildHistoryLayer clips each placement
				// to the pane. Placements use full-group-local coordinates.
				fullFirstRow, ok := firstRows[msg.ID]
				if !ok {
					continue
				}
				if fullFirstRow < rowOffset+height && fullFirstRow+block.Height > rowOffset {
					inline = append(inline, inlinePlacement{
						ImageID: imageID,
						X:       x,
						Y:       fullFirstRow,
						Width:   block.Width,
						Height:  block.Height,
						Text:    transmit,
					})
				}
				continue
			}

			firstIdx, ok := firstRowIndex[msg.ID]
			if !ok {
				continue
			}
			root.AddLayers(lipgloss.NewLayer(block.Text).X(x).Y(firstIdx).Z(zContent))
		}
	}

	return root, interactions, inline
}

// messageGroupFirstRows maps each message ID to its first row index in a full
// group row slice. Inline thumbnail placeholder rows precede each message's
// text, so the first row is the message's (and its thumbnail's) top.
func messageGroupFirstRows(rows []messageRowSpec) map[domain.MessageID]int {
	first := make(map[domain.MessageID]int, len(rows))
	for i, row := range rows {
		if _, exists := first[row.messageID]; !exists && row.messageID != 0 {
			first[row.messageID] = i
		}
	}
	return first
}

// sliceMessageGroupLayer slices a full message-group result to the half-open
// original row range [startRow,endRow), rebuilding a fresh local root for the
// visible rows. It never mutates or reuses the full Layer. Message row IDs keep
// their original full-group row index; local interaction rectangles restart at
// row 0 within the fragment. The avatar renders only when its full original
// rectangle is wholly inside the slice.
func sliceMessageGroupLayer(
	full messageGroupResult,
	startRow, endRow int, // half-open original row indices
	styles renderStyles,
	inlineThumbnails map[domain.MessageID]thumbnail.Block,
) messageGroupResult {
	if full.Layer == nil {
		return messageGroupResult{}
	}
	startRow = max(0, min(startRow, full.Height))
	endRow = max(0, min(endRow, full.Height))
	if startRow >= endRow {
		return messageGroupResult{}
	}

	rows := full.Rows[startRow:endRow]
	height := endRow - startRow

	baseAvatar := image.Rect(0, 0, 4, 2)
	avatarVisible := full.Group.ShowAvatar &&
		baseAvatar.In(image.Rect(0, 0, full.Width, full.Height)) &&
		baseAvatar.Min.Y >= startRow && baseAvatar.Max.Y <= endRow

	root, interactions, inline := renderMessageGroupLayer(
		full.Group, full.Width, rows, full.RowOffset+startRow, full.SelectedStart, full.SelectedEnd,
		avatarVisible, startRow, styles, inlineThumbnails, messageGroupFirstRows(full.Rows),
	)

	return messageGroupResult{
		Layer:             root,
		Width:             full.Width,
		Height:            height,
		Rows:              rows,
		Group:             full.Group,
		Selected:          full.Selected,
		SelectedStart:     full.SelectedStart,
		SelectedEnd:       full.SelectedEnd,
		RowOffset:         full.RowOffset + startRow,
		LocalInteractions: interactions,
		Inline:            inline,
	}
}

// buildMessageRows constructs a stable row sequence: each message reserves
// top and bottom frame rows whether selected or not, so keyboard navigation
// never moves its content or the shared avatar.
func buildMessageRows(
	group RenderedMessageGroup,
	width int,
	location *time.Location,
	selection messageSelection,
	inlineThumbnails map[domain.MessageID]thumbnail.Block,
) ([]messageRowSpec, bool) {
	contentWidth := max(1, width-2)
	var rows []messageRowSpec
	if group.ShowAvatar {
		contentWidth = max(1, width-6) // frame starts at x=4, text at x=5
		first := group.Messages[0]
		header := strings.TrimSpace(group.SenderName + "  " + first.SentAt.In(location).Format("15:04"))
		rows = append(rows, messageRowSpec{text: header, kind: messageRowEmphasis, senderHeader: true})
	}

	selected := false
	for _, message := range group.Messages {
		start := len(rows)
		rows = append(rows, messageRowSpec{kind: messageRowPanel}) // reserved top border
		if selection.ChatID != 0 && selection.MessageID != 0 &&
			message.ChatID == selection.ChatID && message.ID == selection.MessageID {
			selected = true
		}

		widthForMessage := contentWidth
		if message.Outgoing && !group.ShowAvatar {
			widthForMessage = max(1, width-2)
		}

		// Append placeholder rows for media messages that carry an inline
		// thumbnail block. Placeholders go before the message text so the
		// rendered half-block layer sits above the caption and metadata rows.
		// Each placeholder is a messageRowPanel with the same (chatID,
		// messageID) identity so clicking it selects the message.
		if block, ok := inlineThumbnails[message.ID]; ok && block.Width > 0 && block.Height > 0 {
			for rowIdx := 0; rowIdx < block.Height; rowIdx++ {
				rows = append(rows, messageRowSpec{
					text:      "",
					kind:      messageRowPanel,
					outgoing:  message.Outgoing,
					chatID:    message.ChatID,
					messageID: message.ID,
				})
			}
		}

		if message.HasReply {
			context := group.ReplyContexts[message.ID]
			if context.Available {
				rows = append(rows,
					messageRowSpec{text: clipLine("Reply · "+context.Sender, widthForMessage), kind: messageRowMuted, outgoing: message.Outgoing},
					messageRowSpec{text: clipLine(context.Preview, widthForMessage), kind: messageRowMuted, outgoing: message.Outgoing},
				)
			} else {
				rows = append(rows, messageRowSpec{text: clipLine("Reply · Original message unavailable", widthForMessage), kind: messageRowMuted, outgoing: message.Outgoing})
			}
		}

		if isAttachmentMetadataKind(message.Kind) {
			// Caption wraps above one bounded, ANSI-safe metadata row.
			if strings.TrimSpace(message.Text) != "" {
				if len(message.Entities) > 0 {
					for _, line := range wrapEntityText(message.Text, message.Entities, widthForMessage) {
						line.kind, line.outgoing, line.chatID, line.messageID = messageRowPanel, message.Outgoing, message.ChatID, message.ID
						rows = append(rows, line)
					}
				} else {
					for _, line := range wrapText(message.Text, widthForMessage) {
						rows = append(rows, messageRowSpec{text: line, kind: messageRowPanel, outgoing: message.Outgoing, chatID: message.ChatID, messageID: message.ID})
					}
				}
			}
			rows = append(rows, messageRowSpec{
				text:      clipLine(attachmentMetadata(message), widthForMessage),
				kind:      messageRowPanel,
				outgoing:  message.Outgoing,
				chatID:    message.ChatID,
				messageID: message.ID,
			})
		} else {
			text := message.DisplayText()
			switch message.SendState {
			case domain.SendPending:
				text += " …"
			case domain.SendFailed:
				text += " !"
			}
			kind := messageRowPanel
			if message.Service {
				kind = messageRowMuted
			}
			if message.Kind == domain.MessageText && len(message.Entities) > 0 {
				for _, line := range wrapEntityText(text, message.Entities, widthForMessage) {
					line.kind, line.outgoing, line.chatID, line.messageID = kind, message.Outgoing, message.ChatID, message.ID
					rows = append(rows, line)
				}
			} else {
				lines := wrapText(text, widthForMessage)
				if len(lines) == 0 {
					lines = []string{""}
				}
				for _, line := range lines {
					if message.Service {
						line = strings.TrimSpace(line)
					}
					rows = append(rows, messageRowSpec{text: line, kind: kind, outgoing: message.Outgoing, centered: message.Service, chatID: message.ChatID, messageID: message.ID})
				}
			}
		}
		if message.Edited() {
			rows = append(rows, messageRowSpec{text: "edited", kind: messageRowMuted, outgoing: message.Outgoing, chatID: message.ChatID, messageID: message.ID})
		}
		if message.Pinned {
			rows = append(rows, messageRowSpec{text: "pinned", kind: messageRowMuted, outgoing: message.Outgoing})
		}
		if chips := messageReactionChips(message.Reactions); len(chips) > 0 {
			parts := make([]string, len(chips))
			for index, chip := range chips {
				parts[index] = chip.text
			}
			rows = append(rows, messageRowSpec{text: strings.Join(parts, " "), kind: messageRowMuted, outgoing: message.Outgoing, chips: chips})
		}
		// Preserve the legacy failed-state rule: after markers/reactions the
		// final appended row becomes error unless it carries chips.
		if message.SendState == domain.SendFailed && len(rows) > 0 && len(rows[len(rows)-1].chips) == 0 {
			rows[len(rows)-1].kind = messageRowError
		}
		rows = append(rows, messageRowSpec{kind: messageRowPanel}) // reserved bottom border
		for index := start; index < len(rows); index++ {
			rows[index].frameID = message.ID
		}
	}
	return rows, selected
}

func selectedMessageRows(rows []messageRowSpec, selection messageSelection) (int, int) {
	if selection.ChatID == 0 || selection.MessageID == 0 {
		return 0, 0
	}
	start, end := -1, 0
	for index, row := range rows {
		if row.frameID != selection.MessageID {
			continue
		}
		if start < 0 {
			start = index
		}
		end = index + 1
	}
	if start < 0 {
		return 0, 0
	}
	return start, end
}

func videoMetadata(message domain.Message) string {
	values := make([]string, 0, 4)
	if fileName := strings.TrimSpace(message.FileName); fileName != "" {
		values = append(values, fileName)
	}
	if message.Media.Duration > 0 {
		totalSeconds := int64(message.Media.Duration / time.Second)
		values = append(values, fmt.Sprintf("%d:%02d", totalSeconds/60, totalSeconds%60))
	}
	if message.Media.Width > 0 && message.Media.Height > 0 {
		values = append(values, fmt.Sprintf("%d×%d", message.Media.Width, message.Media.Height))
	}
	if mimeType := strings.TrimSpace(message.Media.MIMEType); mimeType != "" {
		values = append(values, mimeType)
	}
	if len(values) == 0 {
		return "[Video]"
	}
	return "[Video] " + strings.Join(values, " · ")
}

// isAttachmentMetadataKind reports whether the kind renders the
// caption-plus-metadata conversation layout.
func isAttachmentMetadataKind(kind domain.MessageKind) bool {
	switch kind {
	case domain.MessagePhoto, domain.MessageVideo, domain.MessageAudio,
		domain.MessageDocument, domain.MessageAnimation, domain.MessageVoiceNote,
		domain.MessageVideoNote:
		return true
	}
	return false
}

// attachmentMetadata dispatches the one-line metadata for the caption-plus-
// metadata conversation layout.
func attachmentMetadata(message domain.Message) string {
	switch message.Kind {
	case domain.MessageVideo:
		return videoMetadata(message)
	case domain.MessageAudio:
		return audioMetadata(message)
	case domain.MessageDocument:
		return fileMetadata(message)
	case domain.MessageAnimation:
		return animationMetadata(message)
	case domain.MessageVoiceNote:
		return voiceNoteMetadata(message)
	case domain.MessageVideoNote:
		return videoNoteMetadata(message)
	}
	return message.DisplayText()
}

func audioMetadata(message domain.Message) string {
	values := make([]string, 0, 3)
	if fileName := strings.TrimSpace(message.FileName); fileName != "" {
		values = append(values, fileName)
	}
	if message.Media.Duration > 0 {
		totalSeconds := int64(message.Media.Duration / time.Second)
		values = append(values, fmt.Sprintf("%d:%02d", totalSeconds/60, totalSeconds%60))
	}
	if mimeType := strings.TrimSpace(message.Media.MIMEType); mimeType != "" {
		values = append(values, mimeType)
	}
	if len(values) == 0 {
		return "[Audio]"
	}
	return "[Audio] " + strings.Join(values, " · ")
}

// fileMetadata renders the document metadata row.
func fileMetadata(message domain.Message) string {
	values := make([]string, 0, 3)
	if fileName := sanitizeDisplayString(message.FileName); fileName != "" {
		values = append(values, fileName)
	}
	if size := availableFileSize(message.Media.File); size > 0 {
		values = append(values, humanReadableSize(size))
	}
	if mimeType := sanitizeDisplayString(message.Media.MIMEType); mimeType != "" {
		values = append(values, mimeType)
	}
	if len(values) == 0 {
		return "[File]"
	}
	return "[File] " + strings.Join(values, " · ")
}

// animationMetadata renders the animation metadata row.
func animationMetadata(message domain.Message) string {
	values := make([]string, 0, 5)
	if fileName := sanitizeDisplayString(message.FileName); fileName != "" {
		values = append(values, fileName)
	}
	if message.Media.Duration > 0 {
		totalSeconds := int64(message.Media.Duration / time.Second)
		values = append(values, fmt.Sprintf("%d:%02d", totalSeconds/60, totalSeconds%60))
	}
	if message.Media.Width > 0 && message.Media.Height > 0 {
		values = append(values, fmt.Sprintf("%d×%d", message.Media.Width, message.Media.Height))
	}
	if size := availableFileSize(message.Media.File); size > 0 {
		values = append(values, humanReadableSize(size))
	}
	if mimeType := sanitizeDisplayString(message.Media.MIMEType); mimeType != "" {
		values = append(values, mimeType)
	}
	if len(values) == 0 {
		return "[Animation]"
	}
	return "[Animation] " + strings.Join(values, " · ")
}

// voiceNoteMetadata renders the voice-note metadata row.
func voiceNoteMetadata(message domain.Message) string {
	values := make([]string, 0, 3)
	if message.Media.Duration > 0 {
		totalSeconds := int64(message.Media.Duration / time.Second)
		values = append(values, fmt.Sprintf("%d:%02d", totalSeconds/60, totalSeconds%60))
	}
	if size := availableFileSize(message.Media.File); size > 0 {
		values = append(values, humanReadableSize(size))
	}
	if mimeType := sanitizeDisplayString(message.Media.MIMEType); mimeType != "" {
		values = append(values, mimeType)
	}
	if len(values) == 0 {
		return "[Voice note]"
	}
	return "[Voice note] " + strings.Join(values, " · ")
}

// videoNoteMetadata renders the video-note metadata row (no filename, no MIME).
func videoNoteMetadata(message domain.Message) string {
	values := make([]string, 0, 3)
	if message.Media.Duration > 0 {
		totalSeconds := int64(message.Media.Duration / time.Second)
		values = append(values, fmt.Sprintf("%d:%02d", totalSeconds/60, totalSeconds%60))
	}
	if message.Media.Width > 0 && message.Media.Height > 0 {
		values = append(values, fmt.Sprintf("%d×%d", message.Media.Width, message.Media.Height))
	}
	if size := availableFileSize(message.Media.File); size > 0 {
		values = append(values, humanReadableSize(size))
	}
	if len(values) == 0 {
		return "[Video note]"
	}
	return "[Video note] " + strings.Join(values, " · ")
}

func availableFileSize(file domain.MediaFileRef) int64 {
	if file.Size > 0 {
		return file.Size
	}
	return max(file.ExpectedSize, 0)
}

// humanReadableSize renders a byte count as a bounded 1024-based size label
// with at most one decimal, e.g. "512 B", "1.5 MB".
func humanReadableSize(size int64) string {
	if size < 0 {
		size = 0
	}
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KB", "MB", "GB", "TB"}
	for index, unit := range units {
		value /= 1024
		if index == len(units)-1 || value < 1024 {
			label := strconv.FormatFloat(value, 'f', 1, 64)
			label = strings.TrimSuffix(label, ".0")
			return label + " " + unit
		}
	}
	return fmt.Sprintf("%d B", size)
}

// sanitizeDisplayString strips control characters and DEL so filenames and
// MIME types cannot inject terminal control sequences or escape row bounds.
func sanitizeDisplayString(value string) string {
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// messageReactionChips filters reactions to real chips and formats each emoji
// with its decimal count.
func messageReactionChips(reactions []domain.MessageReaction) []messageChipSpec {
	chips := make([]messageChipSpec, 0, len(reactions))
	for _, reaction := range reactions {
		if reaction.Emoji == "" || reaction.Count <= 0 {
			continue
		}
		chips = append(chips, messageChipSpec{text: reaction.Emoji + strconv.Itoa(reaction.Count), chosen: reaction.Chosen})
	}
	return chips
}

// messageRowStyle maps a semantic row kind onto the accepted inline style set.
func messageRowStyle(kind messageRowKind, styles renderStyles) lipgloss.Style {
	switch kind {
	case messageRowMuted:
		return styles.Muted
	case messageRowEmphasis:
		return styles.Emphasis
	case messageRowError:
		return styles.Error
	default:
		return styles.Panel
	}
}

// messageRowTextX returns the local X of a row's text. Incoming rows start at
// the avatar gutter (or 1); outgoing rows are right-aligned against their text
// width.
func messageRowTextX(group RenderedMessageGroup, width int, row messageRowSpec) int {
	if row.centered {
		return max(0, (width-displayWidth(row.text))/2)
	}
	incomingTextX := 1
	if group.ShowAvatar {
		incomingTextX = min(width, 5)
	}
	if row.outgoing {
		return max(0, width-displayWidth(row.text)-1)
	}
	return incomingTextX
}
