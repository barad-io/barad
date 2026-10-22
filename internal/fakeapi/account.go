package fakeapi

import (
	"archive/zip"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	jinn "usejinn.com/go"

	"github.com/barad-io/barad/wire"
)

// The account's routes, as the fake keeps them: provider connections,
// source control, billing, and a session's files and terminal.

type account struct {
	Providers []wire.ProviderView
	Sources   []wire.AccountResource
	GitHub    wire.GitHubAccount
	Git       wire.GitSettings
	Billing   wire.BillingAccount
	History   []wire.BillingEntry
	Bought    []int64           // cents, at each checkout
	Password  string            // the account's password, for ChangePassword
	Flows     map[string]*flow  // provider sign-ins, by flow ID
	Objects   map[string][]byte // what a command left, or an upload brought, by key
	hosts     map[string]*ecdh.PrivateKey
}

type flow struct {
	Type, Name, ResourceID string
	Polls                  int
	Code                   string // Claude: the code you pasted
}

func (f *API) seedAccount() {
	f.Providers = []wire.ProviderView{
		{AccountResource: wire.AccountResource{ResourceID: "prov_claude", Name: "Claude Max", ProviderType: "claude"}, Ready: true},
		{AccountResource: wire.AccountResource{ResourceID: "prov_openai", Name: "OpenAI", ProviderType: "openai_api"}, Ready: true},
	}
	f.Sources = []wire.AccountResource{{ResourceID: "src_1", Name: "acme", GitSourceType: "github", InstallationID: 1, AccountLogin: "acme"}}
	f.GitHub = wire.GitHubAccount{SignedIn: true, Login: "you", Installations: []wire.GitHubInstallation{
		{InstallationID: 1, AccountLogin: "acme", AccountType: "Organization", SourceID: "src_1"},
		{InstallationID: 2, AccountLogin: "you", AccountType: "User"},
	}}
	f.Git = wire.GitSettings{UserName: "You", HumanEmail: "you@example.com"}
	f.Billing = wire.BillingAccount{Currency: "usd", BalanceUSDNanos: 8_200_000_000, WorkspaceRateUSDNanosPerHour: 200_000_000,
		TermsVersion: "2026-07-01", TermsURL: "https://barad.io/terms", TermsAccepted: true, MinimumPurchaseUSDCents: 500, MaximumPurchaseUSDCents: 50_000}
	f.History = []wire.BillingEntry{
		{OperationID: "op_3", Kind: "workspace_runtime", AmountUSDNanos: -400_000_000, SessionID: "ses_checkout", EffectiveAt: Start.Add(-2 * time.Hour)},
		{OperationID: "op_2", Kind: "purchase", AmountUSDNanos: 10_000_000_000, EffectiveAt: Start.Add(-72 * time.Hour)},
		{OperationID: "op_1", Kind: "account_initialized", AmountUSDNanos: 0, EffectiveAt: Start.Add(-30 * 24 * time.Hour)},
	}
	f.Password = "correct horse"
	f.Flows, f.Objects, f.hosts = map[string]*flow{}, map[string][]byte{}, map[string]*ecdh.PrivateKey{}
}

func (f *API) provider(id string) *wire.ProviderView {
	for i := range f.Providers {
		if f.Providers[i].ResourceID == id {
			return &f.Providers[i]
		}
	}
	return nil
}

// serveAccount answers the account's routes and a session's files and
// terminal; it reports whether it did. It runs with f.Mu held.
func (f *API) serveAccount(w http.ResponseWriter, r *http.Request, parts []string, body map[string]any, reply func(int, any), fail func(int, string, string)) bool {
	p := strings.Join(parts, "/")
	str := func(k string) string { s, _ := body[k].(string); return s }
	switch {
	case p == "account/providers" && r.Method == http.MethodGet:
		reply(200, map[string]any{"providers": f.Providers})
	case p == "account/providers/api-key":
		v := wire.ProviderView{AccountResource: wire.AccountResource{ResourceID: fmt.Sprintf("prov_%d", len(f.Providers)), Name: str("name"), ProviderType: str("type")}, Ready: str("api_key") != ""}
		f.Providers = append(f.Providers, v)
		reply(201, v)
	case p == "account/providers/authorize":
		id := str("resource_id")
		if id == "" {
			id = fmt.Sprintf("prov_%d", len(f.Providers))
			f.Providers = append(f.Providers, wire.ProviderView{AccountResource: wire.AccountResource{ResourceID: id, Name: str("name"), ProviderType: str("type")}})
		}
		fid := fmt.Sprintf("flow_%d", len(f.Flows))
		f.Flows[fid] = &flow{Type: str("type"), Name: str("name"), ResourceID: id}
		out := wire.ProviderAuthorization{FlowID: fid, ResourceID: id, ExpiresAt: time.Now().Add(15 * time.Minute)}
		if str("type") != "claude" {
			out.VerificationURL, out.UserCode = f.Srv.URL+"/device", "WXYZ-1234"
		}
		reply(201, out)
	case len(parts) == 5 && parts[1] == "providers" && parts[2] == "authorization":
		fl := f.Flows[parts[3]]
		if fl == nil {
			fail(404, "not_found", "no such flow")
			return true
		}
		switch parts[4] {
		case "code":
			fl.Code = str("code")
			reply(200, map[string]any{"accepted": true})
		case "cancel":
			delete(f.Flows, parts[3])
			reply(200, map[string]any{"accepted": true})
		case "poll":
			fl.Polls++
			out := wire.ProviderAuthorizationPoll{Pending: true}
			switch {
			case fl.Type == "claude" && fl.Code == "":
				out.VerificationURL = f.Srv.URL + "/claude-sign-in"
			case fl.Type == "claude" || fl.Polls >= 2:
				out = wire.ProviderAuthorizationPoll{Connected: true}
				f.provider(fl.ResourceID).Ready = true
			}
			if out.Pending {
				reply(202, out)
			} else {
				reply(200, out)
			}
		}
	case len(parts) == 4 && parts[1] == "providers" && parts[3] == "api-key":
		v := f.provider(parts[2])
		v.Ready = true
		reply(200, v)
	case len(parts) == 4 && parts[1] == "providers" && parts[3] == "credential":
		f.provider(parts[2]).Ready = false
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 4 && parts[0] == "account" && parts[3] == "dependencies":
		var out []wire.ResourceDependency
		for _, s := range f.List {
			if s.S.ProviderResourceID == parts[2] || parts[1] == "git-sources" && len(s.Repos) > 0 {
				out = append(out, wire.ResourceDependency{SessionID: s.S.SessionID, AttachedAt: s.S.CreatedAt})
			}
		}
		reply(200, map[string]any{"sessions": out})
	case len(parts) == 3 && parts[0] == "account" && (parts[1] == "providers" || parts[1] == "git-sources") && r.Method == http.MethodPatch:
		if v := f.provider(parts[2]); v != nil {
			v.Name = str("name")
		}
		for i := range f.Sources {
			if f.Sources[i].ResourceID == parts[2] {
				f.Sources[i].Name = str("name")
			}
		}
		reply(200, map[string]any{})
	case len(parts) == 3 && parts[0] == "account" && (parts[1] == "providers" || parts[1] == "git-sources") && r.Method == http.MethodDelete:
		f.Providers = slices.DeleteFunc(f.Providers, func(v wire.ProviderView) bool { return v.ResourceID == parts[2] })
		f.Sources = slices.DeleteFunc(f.Sources, func(v wire.AccountResource) bool { return v.ResourceID == parts[2] })
		w.WriteHeader(http.StatusNoContent)
	case p == "account/git-sources" && r.Method == http.MethodGet:
		reply(200, map[string]any{"sources": f.Sources})
	case p == "account/github" && r.Method == http.MethodGet:
		reply(200, f.GitHub)
	case p == "account/github" && r.Method == http.MethodDelete:
		f.GitHub = wire.GitHubAccount{Login: f.GitHub.Login, Installations: []wire.GitHubInstallation{}}
		w.WriteHeader(http.StatusNoContent)
	case p == "account/github/sign-in":
		f.GitHub.SignedIn = true // as if you finished on GitHub at once
		reply(201, wire.GitHubLink{URL: f.Srv.URL + "/github/sign-in", ExpiresAt: time.Now().Add(10 * time.Minute)})
	case p == "account/git-sources/github/install":
		reply(201, wire.GitHubLink{URL: f.Srv.URL + "/github/install", ExpiresAt: time.Now().Add(10 * time.Minute)})
	case len(parts) == 4 && parts[1] == "github" && parts[2] == "installations":
		for i, in := range f.GitHub.Installations {
			if fmt.Sprint(in.InstallationID) == parts[3] {
				src := wire.AccountResource{ResourceID: fmt.Sprintf("src_%d", len(f.Sources)+1), Name: in.AccountLogin, GitSourceType: "github", InstallationID: in.InstallationID, AccountLogin: in.AccountLogin}
				f.Sources = append(f.Sources, src)
				f.GitHub.Installations[i].SourceID = src.ResourceID
				reply(201, src)
				return true
			}
		}
		fail(404, "not_found", "no such installation")
	case p == "account/git-settings" && r.Method == http.MethodGet:
		reply(200, f.Git)
	case p == "account/git-settings" && r.Method == http.MethodPut:
		_ = json.Unmarshal(raw(body), &f.Git)
		reply(200, f.Git)
	case p == "account/billing":
		reply(200, f.Billing)
	case p == "account/billing/terms":
		f.Billing.TermsAccepted = str("version") == f.Billing.TermsVersion
		reply(201, map[string]any{"version": str("version")})
	case p == "account/billing/checkout":
		if !f.Billing.TermsAccepted || str("terms_version") != f.Billing.TermsVersion {
			fail(409, "terms_required", "Accept the current Terms of Service to continue.")
			return true
		}
		cents, _ := body["amount_usd_cents"].(float64)
		f.Bought = append(f.Bought, int64(cents))
		f.Billing.BalanceUSDNanos += int64(cents) * 10_000_000 // as if you paid at once
		reply(201, wire.Checkout{PurchaseID: "pur_1", URL: f.Srv.URL + "/pay/pur_1"})
	case p == "account/billing/history":
		reply(200, wire.BillingHistory{Entries: f.History})
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "commands":
		f.command(parts[1], body, reply, fail)
	case len(parts) == 4 && parts[0] == "sessions" && parts[2] == "commands":
		var out wire.OwnerCommand
		if json.Unmarshal(f.Objects["command/"+parts[1]+"/"+parts[3]], &out) != nil {
			fail(404, "not_found", "no such command")
			return true
		}
		reply(200, out)
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "file-uploads":
		var in struct {
			TransferID string `json:"transfer_id"`
			Files      []struct {
				Name string `json:"name"`
			} `json:"files"`
		}
		_ = json.Unmarshal(raw(body), &in)
		out := wire.FileUploads{Arguments: raw(body)}
		for i := range in.Files {
			out.Uploads = append(out.Uploads, wire.PresignedHTTPRequest{Method: "PUT", URL: fmt.Sprintf("%s/object/upload/%s/%d", f.Srv.URL, in.TransferID, i)})
		}
		reply(201, out)
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "terminal":
		s := f.Find(parts[1])
		if s.S.MachineStatus != wire.MachineStatusHydrated {
			fail(409, "terminal_unavailable", "terminal access requires an already hydrated session")
			return true
		}
		host, _ := ecdh.P256().GenerateKey(rand.Reader)
		route := fmt.Sprintf("route_%d", len(f.hosts))
		f.hosts[route+"\n"+str("public_key")] = host
		reply(201, map[string]any{"relay_url": "ws" + strings.TrimPrefix(f.Srv.URL, "http") + "/relay", "route": route, "public_key": jinn.EncodeTerminalKey(host.PublicKey())})
	case len(parts) == 3 && parts[0] == "sessions" && parts[2] == "terminal-activity":
		reply(200, map[string]string{"status": "ok"})
	default:
		return false
	}
	return true
}

// command runs an owner command against the session's files at once.
func (f *API) command(id string, body map[string]any, reply func(int, any), fail func(int, string, string)) {
	s := f.Find(id)
	if s.Files == nil {
		s.Files = map[string][]byte{}
	}
	var in struct {
		OwnerRequestID string          `json:"owner_request_id"`
		Kind           string          `json:"kind"`
		Arguments      json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(raw(body), &in)
	var args struct {
		Path        string   `json:"path"`
		Offset      int64    `json:"offset"`
		Length      int64    `json:"length"`
		Directory   string   `json:"directory"`
		Files       []string `json:"files"`
		TransferID  string   `json:"transfer_id"`
		Destination string   `json:"destination"`
	}
	_ = json.Unmarshal(in.Arguments, &args)
	out := wire.OwnerCommand{OwnerRequestID: in.OwnerRequestID, Kind: in.Kind, Status: "completed"}
	clean := func(p string) string { return strings.TrimPrefix(path.Clean("/"+p), "/") }
	switch in.Kind {
	case "fs_list":
		dir := clean(args.Path)
		seen := map[string]wire.FileEntry{}
		for name, data := range s.Files {
			rel := name
			if dir != "" {
				if !strings.HasPrefix(name, dir+"/") {
					continue
				}
				rel = strings.TrimPrefix(name, dir+"/")
			}
			first, rest, nested := strings.Cut(rel, "/")
			_ = rest
			if nested {
				seen[first] = wire.FileEntry{Name: first, Type: "directory"}
			} else {
				seen[first] = wire.FileEntry{Name: first, Type: "file", Size: int64(len(data))}
			}
		}
		if len(seen) == 0 && dir != "" {
			out.Status, out.Error = "failed", &wire.ErrorInfo{Code: "not_found", Message: args.Path + ": no such directory"}
			break
		}
		var list wire.FileList
		for _, e := range seen {
			list.Entries = append(list.Entries, e)
		}
		sort.Slice(list.Entries, func(i, j int) bool { return list.Entries[i].Name < list.Entries[j].Name })
		out.Result = raw(list)
	case "fs_read":
		data, ok := s.Files[clean(args.Path)]
		if !ok {
			out.Status, out.Error = "failed", &wire.ErrorInfo{Code: "not_found", Message: args.Path + ": no such file"}
			break
		}
		end := min(int64(len(data)), args.Offset+args.Length)
		chunk := data[min(args.Offset, end):end]
		key := "read/" + in.OwnerRequestID
		f.Objects[key] = chunk
		out.Result = raw(wire.FileRange{Offset: args.Offset, Length: int64(len(chunk)), Size: int64(len(data)), EOF: end == int64(len(data))})
		out.Download = &wire.OwnerCommandDownload{URL: f.Srv.URL + "/object/" + key, Bytes: int64(len(chunk))}
	case "fs_download":
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for _, name := range args.Files {
			data, ok := s.Files[clean(path.Join(args.Directory, name))]
			if !ok {
				out.Status, out.Error = "failed", &wire.ErrorInfo{Code: "not_found", Message: name + ": no such file"}
				break
			}
			fw, _ := zw.Create(name)
			_, _ = fw.Write(data)
		}
		_ = zw.Close()
		key := "zip/" + in.OwnerRequestID
		f.Objects[key] = b.Bytes()
		out.Download = &wire.OwnerCommandDownload{URL: f.Srv.URL + "/object/" + key, Filename: "barad-files.zip", Bytes: int64(b.Len())}
	case "fs_upload":
		var up struct {
			TransferID  string `json:"transfer_id"`
			Destination string `json:"destination"`
			Files       []struct {
				Name string `json:"name"`
			} `json:"files"`
			Overwrite bool `json:"overwrite"`
		}
		_ = json.Unmarshal(in.Arguments, &up)
		for i, file := range up.Files {
			name := clean(path.Join(up.Destination, file.Name))
			if _, exists := s.Files[name]; exists && !up.Overwrite {
				out.Status, out.Error = "failed", &wire.ErrorInfo{Code: "exists", Message: file.Name + " exists"}
				break
			}
			s.Files[name] = f.Objects[fmt.Sprintf("upload/%s/%d", up.TransferID, i)]
		}
		out.Result = raw(map[string]any{"files": len(up.Files)})
	default:
		fail(400, "invalid_kind", "unsupported command kind "+in.Kind)
		return
	}
	data, _ := json.Marshal(out)
	f.Objects["command/"+id+"/"+in.OwnerRequestID] = data
	reply(202, map[string]any{"owner_request_id": in.OwnerRequestID, "status": "queued"})
}

// objects serves what commands left and takes uploads, as S3 would.
func (f *API) objects(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/object/")
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if r.Method == http.MethodPut {
		data, _ := io.ReadAll(r.Body)
		f.Objects[key] = data
		return
	}
	data, ok := f.Objects[key]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

// relay is the terminal relay and the run's host in one: a shell that
// says where it is, echoes each line, and exits on "exit".
func (f *API) relay(w http.ResponseWriter, r *http.Request) {
	route := r.URL.Query().Get("route")
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	f.Mu.Lock()
	var host *ecdh.PrivateKey
	var caller string
	for k, v := range f.hosts {
		if r, key, _ := strings.Cut(k, "\n"); r == route {
			host, caller = v, key
		}
	}
	f.Mu.Unlock()
	peer, err := jinn.DecodeTerminalKey(caller)
	if host == nil || err != nil {
		return
	}
	ch, err := jinn.NewTerminalChannel(host, peer, false)
	if err != nil {
		return
	}
	ctx := r.Context()
	send := func(kind byte, data []byte) { _ = conn.Write(ctx, websocket.MessageBinary, ch.Seal(kind, data)) }
	send(jinn.FrameOutput, []byte("you@workspace:~$ "))
	var line []byte
	for {
		_, frame, err := conn.Read(ctx)
		if err != nil {
			return
		}
		kind, data, err := ch.Open(frame)
		if err != nil {
			return
		}
		switch kind {
		case jinn.FrameResize:
			send(jinn.FrameOutput, fmt.Appendf(nil, "[%dx%d]", binary.BigEndian.Uint16(data[2:]), binary.BigEndian.Uint16(data)))
		case jinn.FrameInput:
			for _, c := range data {
				if c != '\r' && c != '\n' {
					line = append(line, c)
					send(jinn.FrameOutput, []byte{c})
					continue
				}
				if string(line) == "exit" {
					send(jinn.FrameOutput, []byte("\r\nlogout\r\n"))
					send(jinn.FrameExit, []byte{0, 0, 0, 0})
					return
				}
				send(jinn.FrameOutput, fmt.Appendf(nil, "\r\nyou said %s\r\nyou@workspace:~$ ", line))
				line = line[:0]
			}
		}
	}
}
