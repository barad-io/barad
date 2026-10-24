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

- **Home**: your project's sessions, grouped by what they need from you: a reply you have not read or a failure, then what is running, then the rest. **R** marks every unread reply here read; **r** marks the selected one read, or unread again. **/** searches every project, by name or ID. **p** opens the projects: show another (each says how many sessions need you there), make a new one, and set up the shown one (name, colour, agent, effort, repositories, variables, delete). **m** moves the selected session to another project. **a** archives it (it asks first). **Tab** shows the archived ones; open one to read its conversation.
- **A session**: your messages on the right, the agent's replies on the left, each turn ending with when it finished and how long it took. Its work folds into one row when a run ends; **Tab** shows the steps, then their full output. Replies stream in as the agent writes them.
- **The message box**: writing in it keeps an awake session's computer awake, as on the web (it sleeps ten minutes after you stop). Enter sends and Shift+Enter or Alt+Enter starts a new line (or, with the preference, Enter starts a line and Alt+Enter sends); the arrow keys move through what you have written, Home and End to the line's ends.
- **Select and copy**: drag across any text to copy it; the copy leaves out the app's frames and indent. A click on a link opens it in your browser.
- **The footer** says how many sessions need you and how many are working, and briefly when one finishes; so does a desktop notification (OSC 9: Ghostty, iTerm2, WezTerm, kitty), unless you turn it off.
- **Switch sessions**: Ctrl+S, anywhere, lists every session in every project, what needs you first; type to narrow it to a name, Enter to open it.
- **Your messages**: Ctrl+P and Ctrl+N jump to your previous and next message, as the web app's arrows do; Ctrl+O lists them all, as the web app's conversation map does.
- **A shell** in the session's computer: Ctrl+T. barad steps aside for it (waking the computer first) and comes back when you exit.
- **One menu** (`?`, or Ctrl+K) for everything else, in groups: what needs you now (stop the agent, approve or dismiss a proposed session, wake it, open a shell, go to the session it came from); the conversation (your messages, copy the last reply, images, steps, attach an image, a read-only share link); the session's settings under its name (rename, model, reasoning effort, fast mode, move to a project, repositories its Git credentials reach, details and agent instructions, archive, delete); and Barad's own (usage, settings).
- **Settings**, as the web app's: this computer (theme, what Enter does, notifications, steps); provider connections (connect one with a key or a subscription sign-in in your browser, rename, sign in again or replace the key, the sessions using it, sign out, remove); source control (GitHub sign-in, connecting accounts and organizations or installing Barad on another, each source's repositories, rename, disconnect, and who commits are by); billing (buy credits in your browser, balance history); and the account (change your password, sign out).
- **New session** (`n`) starts from your project's agent, effort and repositories; choose others, each repository at a branch, and the variables it receives.
- **Variables** of a session (in its menu) or a project: add one, a secret (typed in dots and never shown again), change a value, remove one.
- **Images**: screenshots the agent took and images you attached. Attach one by dropping the file on the terminal or with Ctrl+V. Text pasted past 64 KB goes as a text attachment.
- **Usage**: workspace time and credits, by day and by session, with the balance's activity; and inference: tokens and their cost by day, provider and model. **←** and **→** choose 7, 30 or 90 days.

The footer always lists the keys that work on the current screen.

Pictures use the kitty graphics protocol where the terminal draws its Unicode placeholders (kitty, Ghostty) and truecolor half blocks elsewhere, Rex included. `BARAD_GRAPHICS=kitty` or `blocks` overrides the choice. Two themes, night and paper, set the terminal's colours while barad runs, so it looks the same whatever your terminal theme is.

## Without the app

Everything the app does is also a command, for scripts and quick changes: `barad help` lists them all. `--json` prints what a command returns, `--yes` skips a confirmation, and a value a command asks for (a key, a secret) can come on stdin instead.

```sh
# Sessions: what needs you, their conversations, and talking to them
barad sessions --unread                                  # NAME  STATE  PROJECT  MODEL  ACTIVE  ID
barad sessions read --all                                # mark every reply read (the app shares what you have read)
barad sessions log stripe --last                         # the agent's last reply, as it wrote it
barad sessions new "Upgrade stripe-go to v86" --project payments --repo acme/web@release --wait
git diff | barad send stripe "Review this" --wait        # piped input goes along; --wait prints the reply
barad send stripe "What does this show?" --file shot.png
barad sessions proposal docs --approve                   # start the session an agent proposes
barad sessions --archived

# A session's computer
barad shell stripe                                       # a login shell in it, here (it wakes the computer)
barad files stripe web/src                               # list a directory of /workspace
barad files cat stripe web/README.md
barad files get stripe web/dist/report.html --to ~/Downloads
barad files put stripe ./fixtures.json --to web/test

# Projects and their variables
barad projects new Payments --provider claude --model claude-opus-5 --effort high --repo acme/api --colour teal
barad env set STRIPE_KEY --secret --project payments     # the value is read unseen, or from stdin

# The account
barad providers add chatgpt                              # a subscription signs in in your browser
echo "$XAI_KEY" | barad providers add xai_api --name xAI
barad git connect acme                                   # a GitHub organization Barad is installed on
barad git identity --separate on --barad-author on
barad billing buy 25                                     # pay in your browser; the credits arrive here
barad usage models --days 7
barad config enter newline                               # this computer's preferences for the app
barad account password
```

Sessions, projects, provider connections and Git sources are named by ID, or by name: exactly in any case, or by a part only one of them has.

## Files and settings

barad keeps its files in your config directory (`~/.config/barad` on Linux, `~/Library/Application Support/barad` on macOS), readable only by you: `credentials.json` holds its session and `prefs.json` this computer's preferences and what you have read, which the app and the commands share (`barad config` changes the preferences). `barad logout` revokes the session and deletes the file.

| Variable | |
|---|---|
| `BARAD_THEME` | `night` or `paper` |
| `BARAD_GRAPHICS` | `kitty` or `blocks` |
| `BARAD_API_URL` | another Barad stack |

What stays in the web app is what belongs to a browser: installing it as an app, and keeping a phone's screen awake.

## Building

```sh
go build ./cmd/barad
go test ./...
```

`go test` drives the whole app and every command against an in-memory API (`internal/fakeapi`), terminal included. The same fake serves a manual session:

```sh
mkdir -p /tmp/fake && BARAD_FAKE_API_DIR=/tmp/fake go test ./internal/fakeapi -run TestServeFakeAPI -timeout 0 &
XDG_CONFIG_HOME=/tmp/fake BARAD_API_URL=$(cat /tmp/fake/url) go run ./cmd/barad
```

`wire` holds the API's JSON as barad uses it; the Barad service tests that its own types keep every field there.
