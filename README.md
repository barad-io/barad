# barad

The coding-agent TUI and CLI for Barad: persistent cloud workspaces, four native harnesses (`claude`, `codex`, `grok`, `agy`), full web-app parity, and first-class terminal multiplexer citizenship (Rex, Ghostty, tmux).

```sh
curl -fsSL https://github.com/barad-io/barad/releases/latest/download/install.sh | sh
barad
```

The script picks the binary for your computer from the same release, checks it against the release's `SHA256SUMS`, and, if the [GitHub CLI](https://cli.github.com) is signed in, verifies its build provenance. It installs to `~/.local/bin` (or `$BARAD_INSTALL_DIR`) and never runs as root. On Windows, download `barad-windows-amd64.exe` from the [latest release](https://github.com/barad-io/barad/releases/latest).

Every release is built from its tag by [`release.yml`](.github/workflows/release.yml) and carries a signed [build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations). To check a binary yourself:

```sh
gh attestation verify "$(command -v barad)" --repo barad-io/barad \
  --signer-workflow barad-io/barad/.github/workflows/release.yml
```

### Updating

`barad update` replaces barad with the newest release. The release workflow signs each release's `SHA256SUMS` with an Ed25519 key (`SHA256SUMS.sig`). barad carries the public half in [`internal/update/release-signing.pem`](internal/update/release-signing.pem) and installs a download only when the signed checksums verify.

## Using it

```sh
barad                # open fuzzy session picker (signs in on first run)
barad -r             # resume the last session directly
barad <session>      # attach straight into a session's TUI
barad login          # sign in: approve this computer in your browser
barad logout         # sign out on this computer
barad update         # replace barad with its newest release
barad version
```

Signing in never asks for your password. `barad login` prints a short code such as `BCDF-GHJK` and opens Barad in your browser where you approve the computer. `barad logout` revokes the CLI session.

### TUI layout & interaction

- **Primary-buffer inline viewport**: no alternate screen, no permanent sidebars, no mouse hijacking. Settled exchanges print cleanly into your terminal's native scrollback so scrolling, text selection, copying, and narrow Rex/Ghostty/tmux splits work natively.
- **Pinned bottom composer**: multiline input (`Enter` sends, `Shift+Enter` / `Alt+Enter` inserts a newline; configurable via `/account` or `barad config`), inline `/` slash-command completion, `@path` workspace file completion, drag-dropped file paths and `Ctrl+V` clipboard image attachments, uniform post-turn prompt queueing (`Queued · runs after current turn`), and `Esc` to gracefully stop an active turn.
- **Single-line footer**: `session name · harness/model/effort (+fast on Codex) · ctx % · awake|sleeping|waking|sign-in required`.
- **Per-pane OSC 7501 status**: emits Rex's Program Status Protocol (`working`, `blocked` for auth/proposals, `done`, `error`, `idle`, `clear`) strictly for the attached session so multiple panes run cleanly side by side.
- **Tool output toggle**: `Ctrl+T` cycles compact / shown / full tool diffs and reasoning; `/tools` inspects any historical step's full diff/output and loads older transcript pages (`o`).

### Slash commands & overlays

| Command | Action |
|---|---|
| `/sessions` (`Ctrl+P`) | Fuzzy session picker across projects with unread dots, live status, and `+ New session` |
| `/new` | Create a new session (project defaults, `claude`/`codex`/`grok`/`agy`, model + custom ID, effort, Codex fast mode, size, GitHub repos) |
| `/model` | Switch model (catalog or custom ID), reasoning effort, and Codex `fast_mode` (fenced while a turn is active) |
| `/login` | Sign in to the session's native harness CLI (`claude`, `codex`, `grok`, `agy`) directly in your terminal and report auth |
| `/shell` | Drop into an interactive login shell inside the session's workspace VM (`exit` returns to the TUI) |
| `/settings` | Rename, move project, Git repos, env & secret vars, wake VM, publish share link, archive/delete |
| `/files` | Browse `/workspace`, preview files, insert `@path` (`i`) into the composer |
| `/tools` | Inspect full diffs/outputs of tool calls and load older transcript history (`o`) |
| `/projects` | Manage projects, environment variables, and defaults |
| `/usage` | Workspace compute & model inference usage (7 / 30 / 90 days) |
| `/billing` | Credit balance, billing history, Terms of Service, buy credits |
| `/account` | Account info, GitHub status, git identity, theme (`auto` / `night` / `paper`), composer Enter mode |
| `/continue` | Continue a legacy read-only session into a new v2 session |
| `/open` | Open the latest screenshot or attached image URL in your browser |
| `/stop` | Gracefully stop the active turn |
| `/quit` | Exit barad |

## Scriptable CLI

Everything is also available as scriptable commands (`barad help` lists them all). Pass `--json` for machine-readable output and `--yes` to skip confirmations.

```sh
# Sessions
barad sessions --json                                    # list sessions (NAME  STATE  AGENT  MODEL  PROJECT  ACTIVE  ID)
barad sessions --unread
barad sessions show stripe
barad sessions log stripe --last
barad sessions new "Upgrade stripe-go to v86" --agent codex --effort high --fast --size l --repo acme/web@release --wait
barad sessions edit stripe --model gpt-5.5 --effort medium --fast on
barad sessions continue legacy-id --agent claude
barad sessions share stripe                              # publish read-only transcript URL
git diff | barad send stripe "Review this" --wait
barad send stripe "What does this show?" --file shot.png
barad login-agent stripe                                 # interactive native harness login in VM

# Workspace shell & files
barad shell stripe                                       # interactive login shell (wakes VM if sleeping)
barad shell stripe -- uname -a                           # run a command non-interactively and forward its exit code
barad files stripe web/src
barad files cat stripe web/README.md
barad files get stripe web/dist/report.html --to ~/Downloads
barad files put stripe ./fixtures.json --to web/test

# Projects, environment & account
barad projects new Payments --agent claude --model default --effort high --size l --repo acme/api --colour teal
barad env set STRIPE_KEY --secret --project payments
barad git connect acme
barad git identity --separate on --barad-author on
barad billing buy 25
barad usage models --days 7
barad config theme auto                                  # auto | night | paper
barad account
```

Sessions, projects, and Git sources can be named by ID, exact name (case-insensitive), or any unique substring.

## Files and environment variables

barad stores its credentials (`credentials.json`) and shared preferences (`prefs.json`) in `~/.config/barad` (Linux / `$XDG_CONFIG_HOME/barad`) or `~/Library/Application Support/barad` (macOS).

| Variable | Values |
|---|---|
| `BARAD_THEME` | `night` or `paper` (overrides `auto` detection) |
| `BARAD_API_URL` | Point at another Barad API endpoint |

## Building & testing

```sh
go build ./cmd/barad
go vet ./...
go test ./...
```

`go test` runs the CLI, TUI, and shell against an in-memory Barad API (`internal/fakeapi`). To run the TUI manually against the fake API:

```sh
mkdir -p /tmp/fake && BARAD_FAKE_API_DIR=/tmp/fake go test ./internal/fakeapi -run TestServeFakeAPI -timeout 0 &
XDG_CONFIG_HOME=/tmp/fake BARAD_API_URL=$(cat /tmp/fake/url) go run ./cmd/barad
```

`wire` holds the API's JSON shapes; the Barad backend's `internal/clicontract` test verifies every field remains compatible.
