package frontend

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/zylen-det/tuilegram/internal/domain"
	"github.com/zylen-det/tuilegram/internal/media/pixel"
	"github.com/zylen-det/tuilegram/internal/media/thumbnail"
	"github.com/zylen-det/tuilegram/internal/telegram"
)

func messageGroupCanvas(
	group RenderedMessageGroup,
	width int,
	location *time.Location,
	selection messageSelection,
	inlineThumbnails map[domain.MessageID]thumbnail.Block,
	styles renderStyles,
) (*lipgloss.Canvas, *lipgloss.Compositor, messageGroupResult) {
	result := buildMessageGroupLayer(group, width, location, selection, inlineThumbnails, styles)
	height := max(1, result.Height)
	root := lipgloss.NewLayer(lipgloss.NewStyle().Width(width).Height(height).Render("")).X(0).Y(0).Z(zFrame)
	if result.Layer != nil {
		root.AddLayers(result.Layer)
	}
	compositor := lipgloss.NewCompositor(root)
	return lipgloss.NewCanvas(width, height).Compose(compositor), compositor, result
}

func messageGroupRender(
	group RenderedMessageGroup,
	width int,
	location *time.Location,
	selection messageSelection,
	inlineThumbnails map[domain.MessageID]thumbnail.Block,
	styles renderStyles,
) string {
	result := buildMessageGroupLayer(group, width, location, selection, inlineThumbnails, styles)
	height := max(1, result.Height)
	root := lipgloss.NewLayer(lipgloss.NewStyle().Width(width).Height(height).Render("")).X(0).Y(0).Z(zFrame)
	if result.Layer != nil {
		root.AddLayers(result.Layer)
	}
	return lipgloss.NewCompositor(root).Render()
}

func testMessage(id domain.MessageID, chat domain.ChatID, text string) domain.Message {
	return domain.Message{
		ID:     id,
		ChatID: chat,
		Text:   text,
		Kind:   domain.MessageText,
	}
}

func styledMessageGroup(name string, showAvatar bool, msgs ...domain.Message) RenderedMessageGroup {
	group := RenderedMessageGroup{
		MessageGroup: MessageGroup{
			SenderName:    name,
			ShowAvatar:    showAvatar,
			Messages:      msgs,
			ReplyContexts: map[domain.MessageID]ReplyContext{},
		},
		AvatarKey: "avatar-key",
	}
	return group
}

func selectMessageInteractions(result messageGroupResult) []messageGroupLocalInteraction {
	var out []messageGroupLocalInteraction
	for _, li := range result.LocalInteractions {
		if li.Click.Action == SelectMessage {
			out = append(out, li)
		}
	}
	return out
}

func TestSenderAccentPaletteAndUpdatesColorOnlyName(t *testing.T) {
	state := InitialState()
	state.Chats = []domain.Chat{{ID: 90, Kind: domain.ChatSupergroup, Title: "Group"}}
	message := testMessage(1, 90, "Hello")
	message.Sender = domain.SenderRef{Kind: domain.SenderUser, ID: 41}
	message.SenderName = "Ada"
	message.SenderAccentKnown = true
	message.SenderAccentID = 3
	message.SentAt = time.Date(2026, time.July, 20, 15, 4, 0, 0, time.UTC)
	state.Messages[90] = []domain.Message{message}

	check := func(want color.RGBA) {
		t.Helper()
		groups := Select(state, time.UTC).Groups
		if len(groups) != 1 || !groups[0].HasSenderColor || groups[0].SenderColor != want {
			t.Fatalf("sender color = %+v, want %v", groups, want)
		}
		canvas, _, _ := messageGroupCanvas(groups[0], 40, time.UTC, messageSelection{}, nil, newRenderStyles(false))
		if got := colorOf(canvas.CellAt(5, 0).Style.Fg); got != want {
			t.Errorf("name color = %v, want %v", got, want)
		}
		if got := colorOf(canvas.CellAt(10, 0).Style.Fg); got != rgba(textColor) {
			t.Errorf("timestamp color = %v, want text color", got)
		}
	}
	check(state.SenderPalette[3]) // Loaded message can carry its sender's ID.
	updateState(&state, TelegramEvent{Value: telegram.UserUpserted{User: domain.User{ID: 41, AccentColorID: 0}}})
	state.SenderPalette[0] = color.RGBA{R: 1, G: 2, B: 3, A: 255}
	check(state.SenderPalette[0]) // ID 0 is valid and the user update supersedes old messages.
	updateState(&state, TelegramEvent{Value: telegram.UserUpserted{User: domain.User{ID: 41, AccentColorID: 8}}})
	updateState(&state, TelegramEvent{Value: telegram.AccentColorsChanged{DarkRGB: map[int32]uint32{8: 0x123456}}})
	check(color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 255})
}

func TestMessageGroupEmptyOrNonPositiveWidthZeroResult(t *testing.T) {
	styles := newRenderStyles(false)
	empty := styledMessageGroup("Nobody", false)
	if got := buildMessageGroupLayer(empty, 40, time.Local, messageSelection{}, nil, styles); got.Layer != nil || got.Width != 0 || got.Height != 0 || len(got.Rows) != 0 || len(got.LocalInteractions) != 0 {
		t.Errorf("empty group result not zero: %+v", got)
	}
	group := styledMessageGroup("Mina", false, testMessage(1, 7, "hello"))
	for _, width := range []int{0, -5} {
		got := buildMessageGroupLayer(group, width, time.Local, messageSelection{}, nil, styles)
		if got.Layer != nil || got.Width != 0 || got.Height != 0 || len(got.Rows) != 0 {
			t.Errorf("width %d result not zero: %+v", width, got)
		}
	}
}

func TestMessageGroupHeaderWrappingAndMarkers(t *testing.T) {
	styles := newRenderStyles(false)
	sentAt := time.Date(2026, time.July, 20, 15, 4, 0, 0, time.UTC)
	pending := testMessage(2, 10, "short")
	pending.SendState = domain.SendPending
	failed := testMessage(3, 10, "boom")
	failed.SendState = domain.SendFailed
	edited := testMessage(4, 10, "edited text")
	edited.EditedAt = sentAt
	edited.Pinned = true

	group := styledMessageGroup("Mina Chen", true, pending, failed, edited)
	group.Messages[0].SentAt = sentAt

	_, _, result := messageGroupCanvas(group, 40, time.UTC, messageSelection{}, nil, styles)

	var texts []string
	for _, row := range result.Rows {
		texts = append(texts, row.text)
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"Mina Chen  15:04", "short …", "boom !", "edited", "pinned"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rows missing %q, got %q", want, joined)
		}
	}

	// Header is emphasis and never carries identity.
	if result.Rows[0].kind != messageRowEmphasis {
		t.Errorf("header kind = %v, want emphasis", result.Rows[0].kind)
	}
	if result.Rows[0].messageID != 0 {
		t.Errorf("header must not carry identity")
	}

	// Pending/failed modify the body text.
	foundPending, foundFailed, foundEdited, foundPinned := false, false, false, false
	for _, row := range result.Rows {
		if row.text == "short …" {
			foundPending = true
		}
		if row.text == "boom !" {
			foundFailed = true
			if row.kind != messageRowError {
				t.Errorf("failed body kind = %v, want error", row.kind)
			}
		}
		if row.text == "edited" {
			foundEdited = true
			if row.kind != messageRowMuted || row.messageID != 4 {
				t.Errorf("edited row wrong: %+v", row)
			}
		}
		if row.text == "pinned" {
			foundPinned = true
			if row.kind != messageRowMuted || row.messageID != 0 {
				t.Errorf("pinned row wrong: %+v", row)
			}
		}
	}
	if !foundPending || !foundFailed || !foundEdited || !foundPinned {
		t.Errorf("missing marker rows pending=%v failed=%v edited=%v pinned=%v", foundPending, foundFailed, foundEdited, foundPinned)
	}

	// Body wrapping: a long message wraps to multiple rows, each carrying identity.
	long := testMessage(5, 10, strings.Repeat("word ", 20))
	group2 := styledMessageGroup("Mina", false, long)
	_, _, r2 := messageGroupCanvas(group2, 15, time.Local, messageSelection{}, nil, styles)
	bodyCount := 0
	for _, row := range r2.Rows {
		if row.text != "" && row.messageID == 5 {
			bodyCount++
		}
	}
	if bodyCount < 2 {
		t.Errorf("long message wrapped to %d body rows, want >= 2", bodyCount)
	}
}

func TestMessageGroupReplyRows(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(9, 7, "the body")
	msg.Outgoing = true
	msg.HasReply = true
	group := styledMessageGroup("Mina", false, msg)
	group.ReplyContexts = map[domain.MessageID]ReplyContext{
		9: {Available: true, Sender: "Lou  ", Preview: "   a    long preview   "},
	}
	_, _, result := messageGroupCanvas(group, 30, time.Local, messageSelection{}, nil, styles)

	var texts []string
	for _, row := range result.Rows {
		texts = append(texts, row.text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "Reply · Lou") {
		t.Errorf("missing reply-sender row, got %q", joined)
	}
	if !strings.Contains(joined, "a long preview") {
		t.Errorf("whitespace not normalized in preview, got %q", joined)
	}

	// Reply rows are muted, inherit outgoing, carry no click identity.
	replySender := result.Rows[1]
	if replySender.kind != messageRowMuted {
		t.Errorf("reply sender kind = %v, want muted", replySender.kind)
	}
	if !replySender.outgoing {
		t.Errorf("reply rows must inherit outgoing")
	}
	if replySender.messageID != 0 || replySender.chatID != 0 {
		t.Errorf("reply rows must not carry identity")
	}

	// Unavailable reply.
	msg2 := testMessage(10, 7, "second")
	msg2.HasReply = true
	group2 := styledMessageGroup("Mina", false, msg2)
	group2.ReplyContexts = map[domain.MessageID]ReplyContext{10: {Available: false}}
	_, _, r2 := messageGroupCanvas(group2, 42, time.Local, messageSelection{}, nil, styles)
	if got := r2.Rows[1].text; got != "Reply · Original message unavailable" {
		t.Errorf("unavailable reply = %q", got)
	}

	// Reply clipping to width.
	msg3 := testMessage(11, 7, "x")
	msg3.HasReply = true
	group3 := styledMessageGroup("Mina", false, msg3)
	group3.ReplyContexts = map[domain.MessageID]ReplyContext{11: {Available: true, Sender: "Alice", Preview: strings.Repeat("yes ", 30)}}
	_, _, r3 := messageGroupCanvas(group3, 10, time.Local, messageSelection{}, nil, styles)
	if got := displayWidth(r3.Rows[2].text); got > 10 {
		t.Errorf("reply preview not clipped: width %d row %q", got, r3.Rows[2].text)
	}

	// Unavailable reply at a wide enough width keeps the full message visible.
	msg2b := testMessage(12, 7, "y")
	msg2b.HasReply = true
	group2b := styledMessageGroup("Mina", false, msg2b)
	group2b.ReplyContexts = map[domain.MessageID]ReplyContext{12: {Available: false}}
	_, _, r2b := messageGroupCanvas(group2b, 42, time.Local, messageSelection{}, nil, styles)
	if got := r2b.Rows[1].text; got != "Reply · Original message unavailable" {
		t.Errorf("unavailable reply = %q", got)
	}
}

func TestMessageGroupReactionChips(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(20, 5, "reactions")
	msg.Reactions = []domain.MessageReaction{
		{Emoji: "👍", Count: 3, Chosen: true},
		{Emoji: "", Count: 5},
		{Emoji: "❤️", Count: 0},
		{Emoji: "😀", Count: 2},
	}
	group := styledMessageGroup("Mina", false, msg)
	canvas, _, result := messageGroupCanvas(group, 40, time.Local, messageSelection{}, nil, styles)

	// One reaction row, joined with spaces, muted, no identity.
	var chipRow *messageRowSpec
	for i := range result.Rows {
		if len(result.Rows[i].chips) > 0 {
			chipRow = &result.Rows[i]
			break
		}
	}
	if chipRow == nil {
		t.Fatal("no reaction chip row")
	}
	if chipRow.text != "👍3 😀2" {
		t.Errorf("reaction row text = %q", chipRow.text)
	}
	if chipRow.kind != messageRowMuted || chipRow.messageID != 0 {
		t.Errorf("reaction row must be muted with no identity: %+v", chipRow)
	}
	if len(chipRow.chips) != 2 {
		t.Fatalf("filtered chips = %d, want 2", len(chipRow.chips))
	}

	// Chosen chip renders accent; the other muted.
	// Locate the reaction row index and its text start X (incoming, no avatar -> 1).
	rowIndex := -1
	for i, row := range result.Rows {
		if len(row.chips) > 0 {
			rowIndex = i
			break
		}
	}
	firstCell := canvas.CellAt(1, rowIndex)
	if got := colorOf(firstCell.Style.Fg); got != rgba(accentColor) {
		t.Errorf("chosen chip fg = %v, want accentColor", got)
	}
	if firstCell.Style.Attrs&uv.AttrBold == 0 {
		t.Errorf("chosen chip is not bold")
	}

	// No reaction click/interaction identity for the reaction row.
	if got := len(selectMessageInteractions(result)); got != 1 {
		// Only the single body row (id 20) should have a SelectMessage interaction.
		t.Errorf("SelectMessage interactions = %d, want 1", got)
	}
}

func TestMessageGroupSelectedPaddingAndRoundedBorder(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(30, 3, "selected body")
	group := styledMessageGroup("Mina", false, msg)
	canvas, _, result := messageGroupCanvas(group, 25, time.Local, messageSelection{ChatID: 3, MessageID: 30}, nil, styles)

	if !result.Selected {
		t.Fatal("group not selected")
	}
	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 (2 padding + 1 body)", len(result.Rows))
	}
	if result.Rows[0].text != "" || result.Rows[0].messageID != 0 {
		t.Errorf("leading padding row wrong: %+v", result.Rows[0])
	}
	if result.Rows[2].text != "" || result.Rows[2].messageID != 0 {
		t.Errorf("trailing padding row wrong: %+v", result.Rows[2])
	}
	if result.Rows[1].messageID != 30 {
		t.Errorf("body row identity = %d, want 30", result.Rows[1].messageID)
	}
	if result.Width != 25 || result.Height != 3 {
		t.Errorf("dimensions %dx%d, want 25x3", result.Width, result.Height)
	}
	if result.Layer.Width() != 25 || result.Layer.Height() != 3 {
		t.Errorf("layer dims %dx%d, want 25x3", result.Layer.Width(), result.Layer.Height())
	}

	// Actual RoundedBorder corners.
	if got := canvas.CellAt(0, 0).Content; got != "╭" {
		t.Errorf("top-left corner = %q, want ╭", got)
	}
	if got := canvas.CellAt(24, 0).Content; got != "╮" {
		t.Errorf("top-right corner = %q, want ╮", got)
	}
	if got := canvas.CellAt(0, result.Height-1).Content; got != "╰" {
		t.Errorf("bottom-left corner = %q, want ╰", got)
	}
	if got := canvas.CellAt(result.Width-1, result.Height-1).Content; got != "╯" {
		t.Errorf("bottom-right corner = %q, want ╯", got)
	}
	// Focused border color on the frame.
	if got := colorOf(canvas.CellAt(1, 0).Style.Fg); got != rgba(focusedBorderColor) {
		t.Errorf("border fg = %v, want focusedBorderColor", got)
	}
}

func TestJoinedMessagesKeepOneAvatarAndMoveOnlyTheSelectedFrame(t *testing.T) {
	styles := newRenderStyles(false)
	messages := []domain.Message{
		testMessage(1, 7, "first"),
		testMessage(2, 7, "second"),
		testMessage(3, 7, "third"),
	}
	groups := GroupMessages(domain.ChatBasicGroup, messages, time.UTC)
	if len(groups) != 1 || len(groups[0].Messages) != 3 {
		t.Fatalf("consecutive messages = %#v, want one avatar group", groups)
	}
	group := RenderedMessageGroup{MessageGroup: groups[0]}
	const width = 40
	var height int
	for _, want := range []struct {
		id       domain.MessageID
		frameTop int
	}{
		{1, 1}, {2, 4}, {3, 7},
	} {
		canvas, compositor, result := messageGroupCanvas(group, width, time.UTC, messageSelection{ChatID: 7, MessageID: want.id}, nil, styles)
		if height == 0 {
			height = result.Height
		}
		if result.Height != height || result.SelectedStart != want.frameTop || result.SelectedEnd != want.frameTop+3 {
			t.Fatalf("selected %d: height %d frame [%d,%d), want stable %d and [%d,%d)", want.id, result.Height, result.SelectedStart, result.SelectedEnd, height, want.frameTop, want.frameTop+3)
		}
		if got := canvas.CellAt(4, want.frameTop).Content; got != "╭" {
			t.Errorf("selected %d top corner = %q", want.id, got)
		}
		if got := canvas.CellAt(4, want.frameTop+2).Content; got != "╰" {
			t.Errorf("selected %d bottom corner = %q", want.id, got)
		}
		if got := canvas.CellAt(0, 0).Content; got != "░" {
			t.Errorf("selected %d shifted avatar: %q", want.id, got)
		}
		for _, other := range []int{1, 4, 7} {
			if other != want.frameTop && canvas.CellAt(4, other).Content == "╭" {
				t.Errorf("selected %d also framed row %d", want.id, other)
			}
		}
		bodyY := want.frameTop + 1
		if hit := compositor.Hit(6, bodyY); hit.ID() != fmt.Sprintf("message:7:%d:%d", want.id, bodyY) {
			t.Errorf("selected %d body hit = %q", want.id, hit.ID())
		}
	}
	_, _, unselected := messageGroupCanvas(group, width, time.UTC, messageSelection{}, nil, styles)
	if unselected.Height != height {
		t.Fatalf("unselected height %d != selected %d", unselected.Height, height)
	}
}

func TestMessageGroupSelectedDegradesForTinyFrame(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(40, 2, "x")
	group := styledMessageGroup("Mina", false, msg)
	// width=1, height will be 3 (selected padding).
	_, _, result := messageGroupCanvas(group, 1, time.Local, messageSelection{ChatID: 2, MessageID: 40}, nil, styles)
	if !result.Selected {
		t.Fatal("should be selected")
	}
	// Tiny frame must not produce an out of range corner.
	if result.Layer == nil {
		t.Fatal("layer nil")
	}
}

func TestMessageGroupAlignmentsNoRowEscape(t *testing.T) {
	styles := newRenderStyles(false)

	// Outgoing long ZWJ emoji text, no avatar.
	out := testMessage(50, 5, "👨‍👩‍👧‍👦🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀🚀")
	out.Outgoing = true
	group := styledMessageGroup("Mina", false, out)
	width := 20
	content := messageGroupRender(group, width, time.Local, messageSelection{}, nil, styles)
	for _, line := range strings.Split(content, "\n") {
		if got := displayWidth(line); got > width {
			t.Errorf("outgoing line width = %d, want <= %d: %q", got, width, line)
		}
	}

	// Incoming alignment with avatar gutter.
	inc := testMessage(51, 5, "hello")
	groupIn := styledMessageGroup("Mina", true, inc)
	_, _, rIn := messageGroupCanvas(groupIn, 30, time.Local, messageSelection{}, nil, styles)
	// Header at emphasis, body row text aligned at incomingTextX = 5.
	var bodyX int
	for i, row := range rIn.Rows {
		if row.messageID == 51 {
			bodyX = messageRowTextX(groupIn, 30, row)
			_ = i
		}
	}
	if bodyX != 5 {
		t.Errorf("incoming body X = %d, want 5", bodyX)
	}
}

func TestMessageGroupIdentityInteractions(t *testing.T) {
	styles := newRenderStyles(false)

	reply := testMessage(60, 8, "reply body")
	reply.HasReply = true
	group := styledMessageGroup("Mina", false, reply)
	group.ReplyContexts = map[domain.MessageID]ReplyContext{60: {Available: true, Sender: "X", Preview: "p"}}

	longBody := testMessage(61, 8, strings.Repeat("go ", 20)) // wraps
	editedMsg := testMessage(62, 8, "ed")
	editedMsg.EditedAt = time.Now().Add(-time.Hour)
	pinnedMsg := testMessage(63, 8, "pin body")
	pinnedMsg.Pinned = true

	group2 := styledMessageGroup("Mina", false, reply, longBody, editedMsg, pinnedMsg)
	group2.ReplyContexts = map[domain.MessageID]ReplyContext{60: {Available: true, Sender: "X", Preview: "p"}}

	_, _, result := messageGroupCanvas(group2, 40, time.Local, messageSelection{}, nil, styles)

	selections := selectMessageInteractions(result)

	// Identity rows = reply body (60) + wrapped body rows (61) + edited (62) +
	// pinned body (63). Pinned marker itself has no identity. Count identity
	// rows precisely from the semantic model.
	bodyAndEdited := 0
	for _, row := range result.Rows {
		if row.messageID == 60 || row.messageID == 61 || row.messageID == 62 || row.messageID == 63 {
			bodyAndEdited++
		}
	}
	if len(selections) != bodyAndEdited {
		t.Errorf("SelectMessage count = %d, want %d", len(selections), bodyAndEdited)
	}

	// Every payload maps to a permitted identity message ID, and its row was
	// either a body row or the edited marker (both carry the identity).
	payloadIDs := map[domain.MessageID]bool{}
	for _, li := range selections {
		if !strings.HasPrefix(li.ID, "message:") {
			t.Errorf("interaction ID = %q, want message: prefix", li.ID)
		}
		if li.Click.ChatID != 8 || (li.Click.MessageID != 60 && li.Click.MessageID != 61 && li.Click.MessageID != 62 && li.Click.MessageID != 63) {
			t.Errorf("SelectMessage payload = %d/%d, want chat 8 with body/edited id", li.Click.ChatID, li.Click.MessageID)
		}
		payloadIDs[li.Click.MessageID] = true
		if !li.Rect.Eq(image.Rect(0, li.Rect.Min.Y, 40, li.Rect.Min.Y+1)) {
			t.Errorf("rect = %v, want full-width row", li.Rect)
		}
	}
	// Header/reply/pinned rows must not appear as payload IDs; 60..63 all exist
	// as identity-bearing rows, so every permitted ID must be present.
	for _, id := range []domain.MessageID{60, 61, 62, 63} {
		if !payloadIDs[id] {
			t.Errorf("no SelectMessage for identity row %d", id)
		}
	}

	// Unique IDs across the group.
	seen := map[string]bool{}
	for _, li := range selections {
		if seen[li.ID] {
			t.Errorf("duplicate interaction ID %q", li.ID)
		}
		seen[li.ID] = true
	}
}

func TestMessageGroupCompositorHitParity(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(70, 9, "hit me")
	msg.EditedAt = time.Now().Add(-time.Minute)
	group := styledMessageGroup("Mina", false, msg)
	width := 30
	_, compositor, result := messageGroupCanvas(group, width, time.Local, messageSelection{}, nil, styles)

	if len(result.LocalInteractions) == 0 {
		t.Fatal("no local interactions")
	}
	for _, li := range result.LocalInteractions {
		if li.Rect.Empty() {
			continue
		}
		pt := image.Pt(li.Rect.Min.X+li.Rect.Dx()/2, li.Rect.Min.Y+li.Rect.Dy()/2)
		hit := compositor.Hit(pt.X, pt.Y)
		if hit.ID() != li.ID {
			t.Errorf("Hit(%v) = %q, want %q", pt, hit.ID(), li.ID)
		}
		if got := hit.Bounds(); !got.Eq(li.Rect) {
			t.Errorf("Hit(%v) bounds = %v, want %v", pt, got, li.Rect)
		}
	}

	// A point in a non-identity padding region (before row 0 in selected case
	// there is none here) — verify header/reply/pinned rows produce no hit.
	// This group has body(70) + edited(70) identity rows only.
	for _, li := range result.LocalInteractions {
		if li.Click.Action != SelectMessage {
			continue
		}
		pt := image.Pt(2, li.Rect.Min.Y)
		if hit := compositor.Hit(pt.X, pt.Y); hit.ID() != li.ID {
			t.Errorf("full-width row hit(%v) = %q, want %q", pt, hit.ID(), li.ID)
		}
	}
}

func TestMessageGroupAvatarGeometryRetry(t *testing.T) {
	styles := newRenderStyles(false)

	// Placeholder avatar content.
	msg := testMessage(80, 11, "with avatar")
	group := styledMessageGroup("Mina", true, msg)
	canvas, _, _ := messageGroupCanvas(group, 40, time.Local, messageSelection{}, nil, styles)
	if got := canvas.CellAt(0, 0).Content; got != "░" {
		t.Errorf("avatar placeholder cell (0,0) = %q, want ░", got)
	}
	if got := canvas.CellAt(3, 1).Content; got != "░" {
		t.Errorf("avatar placeholder cell (3,1) = %q, want ░", got)
	}

	// Real avatar content.
	real := pixel.Avatar{Width: 4, Height: 2, Cells: make([]pixel.Cell, 8)}
	for i := range real.Cells {
		real.Cells[i] = pixel.Cell{Rune: ' ', Foreground: toNRGBA(accentColor), Background: toNRGBA(panelColor)}
	}
	group.Avatar = real
	c2, _, _ := messageGroupCanvas(group, 40, time.Local, messageSelection{}, nil, styles)
	if got := c2.CellAt(0, 0).Content; got != "▀" {
		t.Errorf("real avatar cell (0,0) = %q, want ▀", got)
	}

	// Error retry wins over the row at the avatar point.
	group.AvatarError = &domain.AppError{Kind: domain.ErrorMedia, Message: "failed"}
	canvasErr, compositorErr, resultErr := messageGroupCanvas(group, 40, time.Local, messageSelection{}, nil, styles)
	var retry *messageGroupLocalInteraction
	for i := range resultErr.LocalInteractions {
		if resultErr.LocalInteractions[i].ID == "message-avatar-retry:11:80" {
			retry = &resultErr.LocalInteractions[i]
		}
	}
	if retry == nil {
		t.Fatal("no avatar retry interaction")
	}
	if !retry.Rect.Eq(image.Rect(0, 0, 4, 2)) {
		t.Errorf("retry rect = %v, want (0,0)-(4,2)", retry.Rect)
	}
	if retry.Z != zControl {
		t.Errorf("retry Z = %d, want %d", retry.Z, zControl)
	}
	if retry.Click.Action != Retry || retry.Click.AvatarKey != "avatar-key" {
		t.Errorf("retry click = %#v", retry.Click)
	}
	pt := image.Pt(2, 1)
	hit := compositorErr.Hit(pt.X, pt.Y)
	if hit.ID() != "message-avatar-retry:11:80" {
		t.Errorf("Hit(%v) = %q, want avatar retry", pt, hit.ID())
	}
	if got := hit.Bounds(); !got.Eq(retry.Rect) {
		t.Errorf("retry hit bounds = %v, want %v", got, retry.Rect)
	}
	// Avatar cell preserved at the retry point.
	if got := canvasErr.CellAt(0, 0).Content; got != "▀" {
		t.Errorf("avatar cell under retry = %q, want ▀", got)
	}
}

func TestMessageGroupSelectedAvatarShiftAndOmission(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(90, 12, "selected avatar")
	group := styledMessageGroup("Mina", true, msg)
	group.AvatarError = &domain.AppError{Kind: domain.ErrorMedia, Message: "x"}

	// Selection does not shift the avatar; its gutter is outside the frame.
	_, compositor, result := messageGroupCanvas(group, 40, time.Local, messageSelection{ChatID: 12, MessageID: 90}, nil, styles)
	var retry *messageGroupLocalInteraction
	for i := range result.LocalInteractions {
		if result.LocalInteractions[i].ID == "message-avatar-retry:12:90" {
			retry = &result.LocalInteractions[i]
		}
	}
	if retry == nil || !retry.Rect.Eq(image.Rect(0, 0, 4, 2)) {
		t.Errorf("selected retry rect = %v, want (0,0)-(4,2)", retry.Rect)
	}
	pt := image.Pt(3, 1)
	if hit := compositor.Hit(pt.X, pt.Y); hit.ID() != "message-avatar-retry:12:90" {
		t.Errorf("Hit(%v) = %q, want avatar retry", pt, hit.ID())
	}

	// Too-narrow group omits the avatar and its retry.
	group2 := styledMessageGroup("Mina", true, msg)
	group2.AvatarError = &domain.AppError{Kind: domain.ErrorMedia, Message: "x"}
	_, _, r2 := messageGroupCanvas(group2, 3, time.Local, messageSelection{}, nil, styles)
	for _, li := range r2.LocalInteractions {
		if strings.HasPrefix(li.ID, "message-avatar-retry:") {
			t.Errorf("narrow group unexpectedly published retry %q", li.ID)
		}
	}
	if r2.Selected {
		t.Errorf("narrow group should not be selected")
	}
}

func TestMessageGroupServiceRowMutedSelection(t *testing.T) {
	styles := newRenderStyles(false)
	for _, test := range []struct {
		name     string
		text     string
		width    int
		outgoing bool
		lines    []string
		x        []int
	}{
		{"plain", "service notice", 40, false, []string{"service notice"}, []int{13}},
		{"wide characters", "小明加入群組", 21, false, []string{"小明加入群組"}, []int{4}},
		{"wrapped", "Alice added Bob and Carol", 14, false, []string{"Alice added", "Bob and Caro", "l"}, []int{1, 1, 6}},
		{"outgoing", "service notice", 41, true, []string{"service notice"}, []int{13}},
	} {
		t.Run(test.name, func(t *testing.T) {
			msg := testMessage(100, 20, test.text)
			msg.Service, msg.Outgoing = true, test.outgoing
			group := styledMessageGroup("Mina", false, msg)
			canvas, _, result := messageGroupCanvas(group, test.width, time.Local, messageSelection{ChatID: 20, MessageID: 100}, nil, styles)
			if len(result.Rows) != len(test.lines)+2 {
				t.Fatalf("rows = %+v, want %d content rows", result.Rows, len(test.lines))
			}
			for i, line := range test.lines {
				row := result.Rows[i+1]
				if row.text != line || row.kind != messageRowMuted || row.messageID != 100 {
					t.Fatalf("service row = %+v, want %q with muted selection identity", row, line)
				}
				first := string([]rune(line)[0])
				if got := canvas.CellAt(test.x[i], i+1).Content; got != first {
					t.Errorf("cell at centered x=%d = %q, want %q", test.x[i], got, first)
				}
			}
			selections := selectMessageInteractions(result)
			if len(selections) != len(test.lines) || selections[0].Click.MessageID != 100 {
				t.Errorf("service row SelectMessage = %+v", selections)
			}
		})
	}
}

func toNRGBA(c rgb) color.NRGBA {
	return color.NRGBA{R: c.r, G: c.g, B: c.b, A: 255}
}

func TestMessageGroupNilLocationUsesLocal(t *testing.T) {
	styles := newRenderStyles(false)
	sentAt := time.Date(2026, time.July, 20, 15, 4, 0, 0, time.UTC)
	msg := testMessage(110, 6, "hi")
	msg.SentAt = sentAt
	group := styledMessageGroup("Mina", true, msg)

	// Deterministic header using a fixed location.
	loc := time.FixedZone("explicit", 2*3600)
	_, _, rWithLoc := messageGroupCanvas(group, 40, loc, messageSelection{}, nil, styles)
	headerWith := rWithLoc.Rows[0].text

	// Nil path must use time.Local exactly; do not assume the host zone differs
	// from the explicit comparison zone.
	_, _, rNil := messageGroupCanvas(group, 40, nil, messageSelection{}, nil, styles)
	headerNil := rNil.Rows[0].text
	wantNil := "Mina  " + sentAt.In(time.Local).Format("15:04")
	if headerNil != wantNil {
		t.Errorf("nil-location header = %q, want %q", headerNil, wantNil)
	}
	if headerWith != "Mina  17:04" {
		t.Errorf("explicit-location header = %q, want %q", headerWith, "Mina  17:04")
	}
}

// slicedMessageGroupCanvas composes a sliced message-group fragment under a
// viewport root at (0,0) and returns the canvas plus compositor for hit
// testing.
func slicedMessageGroupCanvas(result messageGroupResult, x, y int) (*lipgloss.Canvas, *lipgloss.Compositor) {
	viewport := 100
	root := lipgloss.NewLayer(lipgloss.NewStyle().Width(viewport).Height(viewport).Render("")).X(0).Y(0).Z(zFrame)
	if result.Layer != nil {
		result.Layer.X(x).Y(y)
		root.AddLayers(result.Layer)
	}
	compositor := lipgloss.NewCompositor(root)
	return lipgloss.NewCanvas(viewport, viewport).Compose(compositor), compositor
}

func TestMessageGroupSliceRetainsOriginalRowIDsLocalRects(t *testing.T) {
	styles := newRenderStyles(false)
	base := testMessage(200, 7, "first")
	long := testMessage(201, 7, strings.Repeat("word ", 20)) // wraps to multiple rows
	group := styledMessageGroup("Mina", false, base, long)

	full := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, nil, styles)
	if full.RowOffset != 0 {
		t.Fatalf("full row offset = %d, want 0", full.RowOffset)
	}

	var id201Rows int
	for _, row := range full.Rows {
		if row.messageID == 201 {
			id201Rows++
		}
	}
	if id201Rows < 2 {
		t.Fatalf("long message rows = %d, want >= 2", id201Rows)
	}

	slice := sliceMessageGroupLayer(full, 1, 3, styles, nil)
	if slice.Layer == nil {
		t.Fatal("slice layer nil")
	}
	if slice.RowOffset != 1 {
		t.Errorf("slice row offset = %d, want 1", slice.RowOffset)
	}
	if slice.Height != 2 {
		t.Errorf("slice height = %d, want 2", slice.Height)
	}

	// Message row IDs carry original row index: local rows 0 and 1 of the
	// slice keep suffixes 1 and 2 (their original full-group indices).
	for _, li := range slice.LocalInteractions {
		if li.Click.Action != SelectMessage {
			continue
		}
		if li.Click.MessageID != 201 {
			continue
		}
		want := fmt.Sprintf("message:%d:%d:%d", 7, 201, li.Rect.Min.Y+slice.RowOffset)
		if li.ID != want {
			t.Errorf("interaction ID = %q, want %q", li.ID, want)
		}
		// Local rects restart at row 0 within the fragment (full-width row).
		if !li.Rect.Eq(image.Rect(0, li.Rect.Min.Y, 40, li.Rect.Min.Y+1)) {
			t.Errorf("slice interaction rect = %v, want full-width local row", li.Rect)
		}
	}

	canvas, compositor := slicedMessageGroupCanvas(slice, 0, 0)
	_ = canvas
	for _, li := range slice.LocalInteractions {
		if li.Rect.Empty() {
			continue
		}
		pt := image.Pt(li.Rect.Min.X+1, li.Rect.Min.Y)
		if hit := compositor.Hit(pt.X, pt.Y); hit.ID() != li.ID {
			t.Errorf("Hit(%v) = %q, want %q", pt, hit.ID(), li.ID)
		}
	}
}

func TestMessageGroupSelectedSliceRoundedFrame(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(210, 9, "selected body")
	group := styledMessageGroup("Mina", false, msg)
	full := buildMessageGroupLayer(group, 25, time.Local, messageSelection{ChatID: 9, MessageID: 210}, nil, styles)
	if !full.Selected || full.Height != 3 {
		t.Fatalf("full selected height = %d, want 3", full.Height)
	}

	slice := sliceMessageGroupLayer(full, 1, 2, styles, nil)
	if slice.Selected != true {
		t.Errorf("slice must preserve selection")
	}
	if slice.Height != 1 || slice.Width != 25 {
		t.Errorf("slice dims = %dx%d, want 25x1", slice.Width, slice.Height)
	}
	canvas, _ := slicedMessageGroupCanvas(slice, 0, 0)
	// Even a one-row fragment keeps a visible selection edge.
	if slice.Layer == nil || canvas.CellAt(0, 0).Content != "│" {
		t.Fatalf("selected one-row fragment lost border: %q", canvas.CellAt(0, 0).Content)
	}
	// The body row still carries its identity with original suffix 1.
	var bodyRow *messageRowSpec
	for i := range slice.Rows {
		if slice.Rows[i].messageID == 210 {
			bodyRow = &slice.Rows[i]
			break
		}
	}
	if bodyRow == nil {
		t.Fatal("selected body row not in slice")
	}
}

func TestMessageGroupSliceAvatarOmittedWhenPartial(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(220, 11, "hello")
	group := styledMessageGroup("Mina", true, msg)
	group.AvatarError = &domain.AppError{Kind: domain.ErrorMedia, Message: "failed"}

	full := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, nil, styles)
	// Full rows: header(0) + body(1); avatar spans original rows [0,2).
	if full.Height < 2 {
		t.Fatalf("full height = %d, want >= 2", full.Height)
	}

	// Slice rows [1, full.Height): the full avatar (0,0)-(4,2) is not wholly
	// within the slice (row 0 is absent), so it must be omitted.
	slice := sliceMessageGroupLayer(full, 1, full.Height, styles, nil)
	for _, li := range slice.LocalInteractions {
		if strings.HasPrefix(li.ID, "message-avatar-retry:") {
			t.Errorf("partially sliced avatar published retry %q", li.ID)
		}
	}
	// No avatar placeholder cell may appear at the fragment's (0,0).
	canvas, _ := slicedMessageGroupCanvas(slice, 0, 0)
	if got := canvas.CellAt(0, 0).Content; got == "░" {
		t.Errorf("partial slice retains avatar cell at (0,0)")
	}
}

func TestMessageGroupSliceAvatarRetainedYAdjustment(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(230, 12, "hello")
	group := styledMessageGroup("Mina", true, msg)

	full := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, nil, styles)
	// Full rows: header(0) + body(1). Avatar at (0,0)-(4,2) spans original
	// rows [0,2). Slicing [0, overall) contains it wholly at start 0.
	slice := sliceMessageGroupLayer(full, 0, full.Height, styles, nil)
	if slice.Height != full.Height {
		t.Fatalf("slice height = %d, want %d", slice.Height, full.Height)
	}
	canvas, _ := slicedMessageGroupCanvas(slice, 0, 0)
	if got := canvas.CellAt(0, 0).Content; got != "░" {
		t.Errorf("avatar cell (0,0) = %q, want ░", got)
	}
}

func TestMessageGroupSliceAvatarNonzeroStartOmitsPartial(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(231, 13, "hello")
	group := styledMessageGroup("Mina", true, msg)
	group.AvatarError = &domain.AppError{Kind: domain.ErrorMedia, Message: "x"}
	full := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, nil, styles)
	slice := sliceMessageGroupLayer(full, 1, 3, styles, nil)
	for _, li := range slice.LocalInteractions {
		if strings.HasPrefix(li.ID, "message-avatar-retry:") {
			t.Errorf("nonzero-start slice unexpectedly retained retry %q", li.ID)
		}
	}
}

func TestMessageGroupSliceFullUnchangedSourceNotMutated(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(240, 14, "hello")
	group := styledMessageGroup("Mina", true, msg)
	full := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, nil, styles)

	beforeRows := fmt.Sprint(full.Rows)
	beforeWidth, beforeHeight := full.Width, full.Height
	beforeInteractions := fmt.Sprint(full.LocalInteractions)
	fullLayer := full.Layer
	fullLayerSource := fullLayer.GetContent()

	slice := sliceMessageGroupLayer(full, 1, 3, styles, nil)
	if slice.Layer == nil {
		t.Fatal("slice layer nil")
	}

	// The full result's values must be untouched.
	if full.Height != beforeHeight || full.Width != beforeWidth {
		t.Errorf("full dims mutated: %dx%d", full.Width, full.Height)
	}
	if fmt.Sprint(full.Rows) != beforeRows {
		t.Errorf("full rows mutated")
	}
	if fmt.Sprint(full.LocalInteractions) != beforeInteractions {
		t.Errorf("full interactions mutated")
	}
	// The full Layer root must be a different object and its content immutable.
	if slice.Layer == fullLayer {
		t.Errorf("slice and full share the same Layer root")
	}
	if fullLayer.GetContent() != fullLayerSource {
		t.Errorf("full layer content mutated")
	}
}

// TestMessageGroupWithThumbnailReservesRows asserts that a photo message with an
// inline thumbnail block reserves the correct number of placeholder rows.
func TestMessageGroupWithThumbnailReservesRows(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(1001, 5, "")
	msg.Kind = domain.MessagePhoto
	group := styledMessageGroup("Mina", false, msg)

	block := thumbnail.Block{Text: "THUMB", Width: 20, Height: 8}
	thumbs := map[domain.MessageID]thumbnail.Block{1001: block}

	result := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, thumbs, styles)
	// Body + 8 thumbnail rows + two permanently reserved border rows.
	if result.Height != 11 {
		t.Fatalf("Height = %d, want 11", result.Height)
	}
	if len(result.Rows) != 11 {
		t.Fatalf("len(Rows) = %d, want 11", len(result.Rows))
	}
}

// TestMessageGroupThumbnailPlacedAsLayer asserts that the rendered output
// contains the thumbnail block text via the compositor.
func TestMessageGroupThumbnailPlacedAsLayer(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(1002, 6, "")
	msg.Kind = domain.MessagePhoto
	group := styledMessageGroup("Mina", false, msg)

	// Use a multi-line block with a distinct marker string.
	lines := make([]string, 8)
	for i := range lines {
		lines[i] = "AAA"
	}
	block := thumbnail.Block{Text: strings.Join(lines, "\n"), Width: 20, Height: 8}
	thumbs := map[domain.MessageID]thumbnail.Block{1002: block}

	_, _, result := messageGroupCanvas(group, 40, time.Local, messageSelection{}, thumbs, styles)
	if result.Height != 11 {
		t.Fatalf("Height = %d, want 11", result.Height)
	}

	// Compose the result into a canvas and check that the marker "AAA" appears.
	root := lipgloss.NewLayer(lipgloss.NewStyle().Width(40).Height(result.Height).Render(""))
	root.X(0).Y(0).Z(zCardFrame)
	if result.Layer != nil {
		root.AddLayers(result.Layer)
	}
	compositor := lipgloss.NewCompositor(root)
	rendered := compositor.Render()
	if !strings.Contains(rendered, "AAA") {
		t.Errorf("rendered output missing thumbnail content 'AAA': %q", rendered)
	}
}

// TestMessageGroupNoThumbnailKeepsSingleRow checks that only the border space
// is reserved when a photo has no inline thumbnail.
func TestMessageGroupNoThumbnailKeepsSingleRow(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(1003, 7, "")
	msg.Kind = domain.MessagePhoto
	group := styledMessageGroup("Mina", false, msg)

	// nil inlineThumbnails -> no placeholder rows.
	result := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, nil, styles)
	if result.Height != 3 {
		t.Fatalf("Height = %d, want 3", result.Height)
	}
	if len(result.Rows) != 3 {
		t.Fatalf("len(Rows) = %d, want 3", len(result.Rows))
	}
	if result.Rows[1].text != "[Photo]" {
		t.Errorf("body text = %q, want [Photo]", result.Rows[1].text)
	}
}

// TestMessageGroupMultipleThumbnailsCoexist asserts that two photo messages
// each with their own thumbnail block both get rendered as layers.
func TestMessageGroupMultipleThumbnailsCoexist(t *testing.T) {
	styles := newRenderStyles(false)
	msg1 := testMessage(1004, 8, "")
	msg1.Kind = domain.MessagePhoto
	msg2 := testMessage(1005, 8, "")
	msg2.Kind = domain.MessagePhoto
	group := styledMessageGroup("Mina", false, msg1, msg2)

	blockA := thumbnail.Block{Text: "AAAAA\nAAAAA\nAAAAA\nAAAAA\nAAAAA\nAAAAA\nAAAAA\nAAAAA", Width: 20, Height: 8}
	blockB := thumbnail.Block{Text: "BBBBB\nBBBBB\nBBBBB\nBBBBB\nBBBBB\nBBBBB\nBBBBB\nBBBBB", Width: 20, Height: 8}
	thumbs := map[domain.MessageID]thumbnail.Block{
		1004: blockA,
		1005: blockB,
	}

	_, _, result := messageGroupCanvas(group, 40, time.Local, messageSelection{}, thumbs, styles)
	// 2 photo bodies + 8*2 thumbnails + 2*2 reserved border rows.
	if result.Height != 22 {
		t.Fatalf("Height = %d, want 22", result.Height)
	}

	// Both blocks must appear in the rendered output.
	root := lipgloss.NewLayer(lipgloss.NewStyle().Width(40).Height(result.Height).Render(""))
	root.X(0).Y(0).Z(zCardFrame)
	if result.Layer != nil {
		root.AddLayers(result.Layer)
	}
	compositor := lipgloss.NewCompositor(root)
	rendered := compositor.Render()
	if !strings.Contains(rendered, "AAAAA") {
		t.Errorf("missing first thumbnail content")
	}
	if !strings.Contains(rendered, "BBBBB") {
		t.Errorf("missing second thumbnail content")
	}
}

// TestMessageGroupKittyThumbnailRecordedAsPlacement asserts that a Kitty
// thumbnail becomes an Inline placement (not a text Layer) so the transmit is
// emitted outside the text compositor.
func TestMessageGroupKittyThumbnailRecordedAsPlacement(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(2001, 7, "")
	msg.Kind = domain.MessagePhoto
	group := styledMessageGroup("Mina", false, msg)

	transmit := "\x1b_Ga=T,i=77,c=20,r=8,q=2;AAAA\x1b\\"
	block := thumbnail.Block{Text: transmit, Width: 20, Height: 8, Kitty: true, ImageID: 77}
	thumbs := map[domain.MessageID]thumbnail.Block{2001: block}

	_, _, result := messageGroupCanvas(group, 40, time.Local, messageSelection{}, thumbs, styles)
	if len(result.Inline) != 1 {
		t.Fatalf("Inline placements = %d, want 1", len(result.Inline))
	}
	placement := result.Inline[0]
	if placement.ImageID != 77 || placement.X != 1 || placement.Y != 1 ||
		placement.Width != 20 || placement.Height != 8 || placement.Text != transmit {
		t.Fatalf("placement = %#v, want {ImageID:77 X:1 Y:0 W:20 H:8}", placement)
	}

	root := lipgloss.NewLayer(lipgloss.NewStyle().Width(40).Height(result.Height).Render(""))
	root.X(0).Y(0).Z(zCardFrame)
	if result.Layer != nil {
		root.AddLayers(result.Layer)
	}
	rendered := lipgloss.NewCompositor(root).Render()
	if strings.Contains(rendered, "AAAA") || strings.Contains(rendered, "\x1b_G") {
		t.Fatalf("Kitty transmit leaked into the text compositor output")
	}
}

// TestMessageGroupKittyPlacementSurvivesSlice asserts that a partially scrolled
// Kitty block (top above the rendered slice) still surfaces a full-box
// placement for the pane-level clip; only placements intersecting the slice are
// emitted.
func TestMessageGroupKittyPlacementSurvivesSlice(t *testing.T) {
	styles := newRenderStyles(false)
	msg := testMessage(3001, 8, "")
	msg.Kind = domain.MessagePhoto
	group := styledMessageGroup("Mina", false, msg)

	block := thumbnail.Block{Text: "kitty", Width: 20, Height: 8, Kitty: true, ImageID: 88}
	thumbs := map[domain.MessageID]thumbnail.Block{3001: block}

	full := buildMessageGroupLayer(group, 40, time.Local, messageSelection{}, thumbs, styles)
	if len(full.Inline) != 1 {
		t.Fatalf("full Inline = %d, want 1", len(full.Inline))
	}

	// The block occupies rows 0..7 (text at row 8). Slicing from row 5 keeps
	// the placement because the block intersects the slice; buildHistoryLayer
	// crops it against the pane.
	clipped := sliceMessageGroupLayer(full, 5, full.Height, styles, thumbs)
	if len(clipped.Inline) != 1 {
		t.Fatalf("clipped Inline = %d, want 1", len(clipped.Inline))
	}
	if clipped.Inline[0].Y != 1 {
		t.Fatalf("clipped placement Y = %d, want full-group 1", clipped.Inline[0].Y)
	}

	// A slice covering the whole block keeps the placement at the original
	// full-group row offset.
	whole := sliceMessageGroupLayer(full, 0, full.Height, styles, thumbs)
	if len(whole.Inline) != 1 {
		t.Fatalf("whole Inline = %d, want 1", len(whole.Inline))
	}
	if whole.Inline[0].Y != 1 {
		t.Fatalf("whole placement Y = %d, want 1", whole.Inline[0].Y)
	}

	// A slice covering only the text rows below the block drops the placement
	// because the block no longer intersects the slice.
	below := sliceMessageGroupLayer(full, full.Height-1, full.Height, styles, thumbs)
	if len(below.Inline) != 0 {
		t.Fatalf("below Inline = %d, want 0", len(below.Inline))
	}
}
