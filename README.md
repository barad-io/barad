# barad

Barad in the terminal: your sessions, their conversations, and what the web app does with them.

```sh
curl -fsSL https://github.com/barad-io/barad/releases/latest/download/install.sh | sh
barad
```

The script picks the binary for your computer from the same release, checks it against the release's `SHA256SUMS`, and, if the [GitHub CLI](https://cli.github.com) is signed in, verifies its build provenance. It installs to `~/.local/bin` (or `$BARAD_INSTALL_DIR`) and never runs as root. On Windows, download `barad-windows-amd64.exe` from the [latest release](https://github.com/barad-io/barad/releases/latest).

Every release is built from its tag by [`release.yml`](.github/workflows/release.yml) and carries a signed [build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations). To check a binary yourself:

```sh
gh attestation verify "$(command -v barad)" --repo barad-io/barad
```

## Using it

```sh
barad            # signs you in the first time, then opens the app
barad login      # sign in with your Barad email and password
barad logout     # sign out on this computer
barad version
```

- **Home**: your project's sessions, grouped by what they need from you: a reply you have not read or a failure, then what is running, then the rest.
- **A session**: your messages on the right, the agent's replies on the left. Its work folds into one row when a run ends; **Tab** shows the steps, then their full output. Replies stream in as the agent writes them.
- **One menu** (`?`, or Ctrl+P) for everything else: stop the agent, approve or dismiss a proposed session, model, reasoning effort, images, a read-only share link, rename, theme and archive.
- **New session** (`n`) starts from your project's agent, repository and effort.
- **Images**: screenshots the agent took and images you attached. Attach one by dropping the file on the terminal or with Ctrl+V. Text pasted past 64 KB goes as a text attachment.
- **Usage**: tokens, workspace time and spend over 30 days.

The footer always lists the keys that work on the current screen.

Pictures use the kitty graphics protocol where the terminal supports it (kitty, Ghostty, rex) and truecolor half blocks elsewhere. Two themes, night and paper, set the terminal's colours while barad runs, so it looks the same whatever your terminal theme is.

## Files and settings

barad keeps its files in your config directory (`~/.config/barad` on Linux, `~/Library/Application Support/barad` on macOS), readable only by you: `credentials.json` holds your sign-in tokens and `prefs.json` your theme, project and what you have read. `barad logout` revokes the tokens and deletes the file.

| Variable | |
|---|---|
| `BARAD_THEME` | `night` or `paper` |
| `BARAD_GRAPHICS` | `kitty` or `blocks` |
| `BARAD_API_URL`, `BARAD_COGNITO_URL`, `BARAD_COGNITO_CLIENT_ID` | another Barad stack |

## Not here yet

Files and the terminal, a session's environment variables and repository access, and account settings (providers, GitHub, projects, billing). Use the web app at [app.barad.io](https://app.barad.io) for those.

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
