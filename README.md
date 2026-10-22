# barad

Barad in the terminal: your sessions, their conversations, and what the web app does with them.

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

`barad update` replaces barad with the newest release. The release workflow signs each release's `SHA256SUMS` with an Ed25519 key that only it can use (`SHA256SUMS.sig`; the key lives in the repository's `release` environment, open to `v*` tags alone). barad carries the public half, [`internal/update/release-signing.pem`](internal/update/release-signing.pem), and installs a download only when the signed checksums vouch for it. Once a day the home screen looks up the newest release and mentions it, quietly, when it is newer than yours; it never updates on its own.

## Using it

```sh
barad            # signs you in the first time, then opens the app
barad login      # sign in: approve this computer in your browser
barad logout     # sign out on this computer
barad update     # replace barad with the newest release
barad version
```

Signing in never asks for your password. `barad login` shows a code such as `BCDF-GHJK` and opens Barad in your browser, where you are already signed in. Check that the page shows the same code and your computer's name, then approve it. barad gets a session of its own, which `barad logout` revokes. On a computer without a browser, open the link it prints anywhere you are signed in to Barad.

- **Home**: your project's sessions, grouped by what they need from you: a reply you have not read or a failure, then what is running, then the rest.
- **A session**: your messages on the right, the agent's replies on the left, each turn ending with when it finished and how long it took. Its work folds into one row when a run ends; **Tab** shows the steps, then their full output. Replies stream in as the agent writes them.
- **The message box**: Shift+Enter starts a new line; the arrow keys move through what you have written, Home and End to the line's ends.
- **Select and copy**: drag across any text to copy it; the copy leaves out the app's frames and indent. A click on a link opens it in your browser.
- **The footer** says how many sessions need you and how many are working, and briefly when one finishes.
- **Your messages**: Ctrl+P and Ctrl+N jump to your previous and next message, as the web app's arrows do.
- **One menu** (`?`, or Ctrl+K) for everything else, in groups: what needs you now (stop the agent, approve or dismiss a proposed session, wake it); the conversation (copy the last reply, images, steps, attach an image, a read-only share link); the session's settings under its name (rename, model, reasoning effort, fast mode, project, repositories its Git credentials reach, details and agent instructions, archive, delete); and Barad's own (usage, settings: theme, providers, account).
- **New session** (`n`) starts from your project's agent, effort and repositories; choose others, each repository at a branch, and the variables it receives.
- **Projects**, in the menu at home: show another, make one, and set the shown one's name, agent, effort, repositories and variables, or delete it.
- **Variables** of a session (in its menu) or a project: add one, a secret (typed in dots and never shown again), change a value, remove one.
- **Images**: screenshots the agent took and images you attached. Attach one by dropping the file on the terminal or with Ctrl+V. Text pasted past 64 KB goes as a text attachment.
- **Usage**: tokens, workspace time and spend over 30 days.

The footer always lists the keys that work on the current screen.

Pictures use the kitty graphics protocol where the terminal draws its Unicode placeholders (kitty, Ghostty) and truecolor half blocks elsewhere, Rex included. `BARAD_GRAPHICS=kitty` or `blocks` overrides the choice. Two themes, night and paper, set the terminal's colours while barad runs, so it looks the same whatever your terminal theme is.

## Without the app

Everything the app does to sessions, projects and their variables is also a command, for scripts and quick changes. `barad help` lists them all; `--json` prints what a command returns, and `--yes` skips a confirmation.

```sh
barad projects new Payments --provider claude --model claude-opus-5 --effort high --repo acme/api
barad env set STRIPE_KEY --secret --project payments     # the value is read unseen, or from stdin
barad sessions new "Upgrade stripe-go to v86" --project payments --repo acme/web@release --env MODE=test
barad sessions                                           # NAME  STATE  PROJECT  MODEL  ACTIVE  ID
barad send "stripe" "Run the integration tests too"
barad sessions edit stripe --fast on --project ops
barad sessions repos stripe --add acme/infra
barad sessions archive stripe --yes
```

Sessions, projects and provider connections are named by ID, or by name: exactly in any case, or by a part only one of them has.

## Files and settings

barad keeps its files in your config directory (`~/.config/barad` on Linux, `~/Library/Application Support/barad` on macOS), readable only by you: `credentials.json` holds its session and `prefs.json` your theme, project and what you have read. `barad logout` revokes the session and deletes the file.

| Variable | |
|---|---|
| `BARAD_THEME` | `night` or `paper` |
| `BARAD_GRAPHICS` | `kitty` or `blocks` |
| `BARAD_API_URL` | another Barad stack |

## Not here yet

Files and the terminal; provider connections and source control (adding, renaming, removing); billing (adding credits, terms, usage by workspace and model); device preferences and changing your password. They are coming, in that order of need; until then use the web app at [app.barad.io](https://app.barad.io).

## Building

```sh
go build ./cmd/barad
go test ./...
```

`go test` drives the whole app against an in-memory API (`internal/tui/fake_test.go`). The same fake serves a manual session:

```sh
mkdir -p /tmp/fake && BARAD_FAKE_API_DIR=/tmp/fake go test ./internal/tui -run TestServeFakeAPI -timeout 0 &
XDG_CONFIG_HOME=/tmp/fake BARAD_API_URL=$(cat /tmp/fake/url) go run ./cmd/barad
```

`wire` holds the API's JSON as barad uses it; the Barad service tests that its own types keep every field there.
