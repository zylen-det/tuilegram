# tuilegram

[![CI](https://github.com/zylen-det/tuilegram/actions/workflows/ci.yml/badge.svg)](https://github.com/zylen-det/tuilegram/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## What is this?

A Telegram client that lives in your terminal. Built with [Bubble Tea v2](https://github.com/charmbracelet/bubbletea) and [TDLib](https://github.com/tdlib/td), tuilegram runs in [Kitty](https://sw.kovidgoyal.net/kitty/) with inline images, mouse support, desktop notifications, and a persistent Telegram session.

![Animated demo of tuilegram showing chats and message actions](docs/assets/demo.gif)

> **Project status:** This is an early release for Arch Linux x86-64 and Kitty; see [Known limitations](#known-limitations).

tuilegram is unofficial and is not affiliated with Telegram.

## Features

- **Chat:** Read and send messages in private chats, groups, supergroups, channels, and forum topics. Reply, edit, delete, forward, pin, copy, and react when Telegram permits it.
- **Find your way:** Search chats and messages, jump to unread or mentioned chats, browse pinned messages, and complete bot commands. Navigate with Vim-style keys, conventional keys, or a mouse.
- **Images and media:** See images and available thumbnails inline as terminal pixel cells, or open images at full fidelity with Kitty's graphics protocol. Send photos, videos, audio, documents, and stickers; open other media in the system application.
- **Drafts and offline use:** Sync Telegram cloud drafts (including reply targets) across devices. Keep reading cached content offline and reconnect without losing the active view.
- **Groups and channels:** Browse members and use supported administration controls. See group service messages for joining (including invite links and approved requests), adding members, leaving, and removing members.
- **Terminal integration:** Use desktop notifications when available and follow Kitty's configured background opacity.

## Installation

### Requirements

The packaged release currently requires Arch Linux x86-64, Kitty, and the `gcc-libs`, `openssl`, and `zlib` runtime packages. You'll also need a personal Telegram `api_id` and `api_hash` from <https://my.telegram.org/apps> for first-run sign-in.

Desktop notifications are optional; they need a freedesktop-compatible notification daemon such as Mako, Dunst, or SwayNotificationCenter.

### Install the release

The renamed installer supports `tuilegram` release archives starting with v0.4.0. Older releases use the previous archive name.

Install the latest release without `sudo`:

```bash
curl -fsSL https://raw.githubusercontent.com/zylen-det/tuilegram/main/scripts/install.sh | sh
```

The installer verifies the release checksum, places versioned files under `~/.local/opt/tuilegram`, and links the executable as `~/.local/bin/tuilegram`. If necessary, add that directory to `PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Install a specific version or use another prefix:

```bash
curl -fsSL https://raw.githubusercontent.com/zylen-det/tuilegram/main/scripts/install.sh \
  | VERSION=v0.4.0 PREFIX="$HOME/.local" sh
```

You can also download the archive and `SHA256SUMS` from [GitHub Releases](https://github.com/zylen-det/tuilegram/releases), verify it, and run it in place:

```bash
sha256sum --check SHA256SUMS
tar -xzf tuilegram_<version>_linux_x86_64.tar.gz
./tuilegram_<version>_linux_x86_64/tuilegram
```

Keep the archive's `lib` directory beside the executable; it contains the matching TDLib shared library. To build from source instead, see [Build from source](#build-from-source).

## First run

Start the client:

```bash
tuilegram
```

Follow the prompts in the TUI:

1. Enter your Telegram application **`api_id`** and **`api_hash`** from <https://my.telegram.org/apps>.
2. Enter your **phone number**, including country code.
3. Enter the **verification code** Telegram sends you.
4. If enabled, enter your **two-step verification (2FA) password**.

That's it. If Telegram rejects the phone number, you can enter it again without restarting. The app credentials and a generated TDLib database key are saved in `config.toml` with mode `0600`; login codes and passwords are not saved.

You can also supply app credentials for a one-off launch:

```bash
TELEGRAM_API_ID=12345 TELEGRAM_API_HASH=... tuilegram
```

Credential lookup order is environment → config file → first-run prompt.

## Everyday controls

Keys depend on the focused pane. These are the everyday controls:

| Action | Keyboard | Mouse |
|---|---|---|
| Move through a list | `j` / `k` or Up / Down | Click a row or scroll |
| Cycle visible panes (not the input) | `h` / `l` or Left / Right; Shift-Tab / Tab | Click a pane |
| Open a chat | Enter in the chat list | Double-click a chat row |
| Open chat actions | `a` in the chat list | Double-click an action in the open menu |
| Show chat info | `K` or F2 | Click Info |
| Focus message input | `i` in a conversation | Click the input |
| Activate a selected message or menu action | Enter | Double-click a list action |
| Close / go back | Esc; `q` inside a modal | Click close or outside |
| Search chats | `/` in the chat list | Double-click a result |
| Next unread / mentioned chat | `u` / `m` in the chat list | — |
| View pinned messages | `p` in a conversation | — |
| Open a reply's referenced message | Enter on the reply, then choose Go to referenced message | Double-click that action |
| Browse forum topics | `t` in a forum conversation | Double-click a topic in the open list |
| Page through history | Ctrl-u / Ctrl-d or Page Up / Page Down | Scroll |
| Send a message / insert a newline | Enter / Shift-Enter in the input | Click Send |
| Send media or a document | Ctrl+O | Click `[Photo]` |
| Open the sticker picker | Ctrl+S | Click `[Sticker]` |
| Quit the application | Ctrl-C | — |

**Action menus:** Use `j`/`k` and Enter, or press the direct key shown beside a visible action:

- **Messages:** `v` view/open media, `r` reply, `g` go to referenced message, `f` forward, `e` edit, `y` copy (yank), `c` copy link, `o` open link, `i` user info, `a` react, `p` pin/unpin, `d` delete for self, `D` delete for everyone.
- **Chats:** `o` open, `i` info, `a` archive/unarchive, `p` pin/unpin, `m` mute/unmute, `r` mark read/unread, `c` clear history, `d` delete, `l` leave, `J` join (`j` remains navigation).
- **Confirmations:** `c` cancel, `y` confirm.

Link actions open a second menu of TDLib-marked links to choose from; links behind display text appear as `URL(display text)`.

**Focus and mouse:** One click focuses a list row (including modal actions, search results, bot commands, Info actions, and sticker tiles); a second click on the same row within 400 ms activates it. Send and Close act on one click. Moving through the chat list changes focus without opening another conversation; press Enter to open the focused chat while keeping focus in Chats. Pane navigation wraps among visible Chats, Conversation, and Info panes without opening another chat. Esc or `q` goes back from a modal or page; plain `q` never quits the application.

**Writing messages:** Type `/` at the start of the input for bot-command completion where available. Sent text supports Telegram's human-friendly Markdown (`**bold**`, `__italic__`, `` `code` ``, `~~strikethrough~~`); the input does not preview formatting.

Command-line help and version information are available without starting the TUI:

```bash
tuilegram --help
tuilegram --version
```

## Other information

### Layout and appearance

The interface adapts to the terminal size:

- **Wide:** 120×24 or larger, with chat and conversation panes plus optional details.
- **Normal:** 80–119 columns and at least 20 rows.
- **Narrow:** 60–79 columns and at least 18 rows, using separate pages.
- **Too small:** below 60×18, where the application preserves state and asks for more room.

tuilegram uses the terminal's default background. Configure translucency in Kitty—for example, `background_opacity 0.9`—rather than in the application.

Sender names above messages use the sender accent-color ID assigned by Telegram; the adjacent timestamp keeps the normal text color. IDs 0–6 default to a Telegram Android-style palette. Newly generated `config.toml` files include a `[sender_colors]` section with all seven defaults. Edit any of its six-digit RGB values in `~/.config/tuilegram/config.toml` (or under `XDG_CONFIG_HOME`) and restart to apply changes. For an older config, add the section yourself:

```toml
[sender_colors]
"0" = "#CC5049" # red
"3" = "#40A920" # green
```

Unspecified IDs retain their defaults. For other accent IDs, tuilegram uses TDLib's dark-theme RGB values when available. This does not sync themes with another Telegram app.

### Local data and privacy

| Data | Default location |
|---|---|
| Preferences and credentials | `~/.config/tuilegram/config.toml` |
| Application state and log | `~/.local/state/tuilegram/` |
| TDLib database | `~/.local/share/tuilegram/tdlib/database/` |
| Downloaded avatar files | `~/.cache/tuilegram/avatars/files/` |
| Disposable pixel cache | `~/.cache/tuilegram/avatars/pixels/` |

The corresponding XDG environment variables override these roots. Existing installation data is not migrated to the new directories; you will need to sign in again. Removing the avatar cache does not remove the Telegram session.

Logs are rotating and restricted to an allow-list. They do not contain credentials, message or draft text, Telegram payloads, local media paths, commands, or raw errors. Normal and failed exits are recorded in `~/.local/state/tuilegram/tuilegram.log` (or under `XDG_STATE_HOME`); failed startup logs only a safe error category, not raw TDLib details, and returns a nonzero exit status.

### Known limitations

- Packaged releases currently support only Linux x86-64 and Kitty.
- Only one Telegram account is supported.
- Calls and stories are not implemented.
- Topic administration, advanced invite-link options, join-request moderation, and timed restrictions remain on the roadmap.
- Some recently implemented group/channel controls and topic flows still need broader real-account manual acceptance; see [`docs/manual-acceptance.md`](docs/manual-acceptance.md).

### Build from source

Install the Go/CGO toolchain and runtime dependencies:

```bash
sudo pacman -S --needed base-devel openssl zlib go git curl kitty
```

Then clone the repository and build:

```bash
git clone https://github.com/zylen-det/tuilegram.git
cd tuilegram
make tdlib
make build
./bin/tuilegram
```

`make tdlib` downloads and verifies the pinned prebuilt TDLib library. To compile that same TDLib commit locally instead:

```bash
sudo pacman -S --needed cmake gperf
make tdlib-source
make build
```

Neither path uses `sudo` inside the repository.

### Development and verification

Default tests do not require Telegram credentials, Kitty, or native TDLib:

```bash
make verify
```

Run the native integration and production build checks with:

```bash
make tdlib
make test-tdlib
make build
./bin/tuilegram --version
git diff --check
```

See [`docs/architecture.md`](docs/architecture.md) for package boundaries and [`docs/manual-acceptance.md`](docs/manual-acceptance.md) for real-account checks.

### Roadmap

- **Near-term:** Richer group/channel administration, including named or expiring invite links, join requests, timed restrictions, and topic administration.
- **Longer-term possibilities:** Multiple accounts, additional terminal image protocols, macOS, calls, and stories.

### License

tuilegram is available under the [MIT License](LICENSE). Release archives also include license material for TDLib, the prebuilt package, and Go dependencies.
