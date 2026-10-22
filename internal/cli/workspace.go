package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/barad-io/barad/internal/api"
	"github.com/barad-io/barad/internal/shell"
	"github.com/barad-io/barad/wire"
)

// A session's computer, directly: a shell in it, and its files, as the
// web app's Terminal and Files tabs give them.

var shellCommand = command{
	usage: "shell SESSION",
	about: "a login shell in a session's computer, in this terminal (waking it if it sleeps); exit to leave",
	run: func(c *call) error {
		s, err := c.sessionArg(true)
		if err != nil {
			return err
		}
		in, ok := c.in.(*os.File)
		if _, term := c.terminal(); !ok || !term {
			return errors.New("a shell needs a terminal")
		}
		if err := shell.Wake(c.ctx, c.api, s, c.out); err != nil {
			return err
		}
		status, err := shell.Run(c.ctx, c.api, s.SessionID, in, c.out)
		if err != nil {
			return err
		}
		if status != 0 {
			return exitStatus(status)
		}
		return nil
	},
}

// exitStatus is a shell's exit status, passed on as barad's own.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("the shell exited with status %d", int(e)) }

// ExitCode is the status barad exits with for err: a shell's own (which
// said what it had to), or 1, saying err.
func ExitCode(err error) (code int, say bool) {
	var e exitStatus
	if errors.As(err, &e) {
		return int(e), false
	}
	return 1, true
}

// workspacePath is a path in the workspace as the commands take it:
// relative to /workspace, whether or not it is written with it.
func workspacePath(p string) string {
	p = strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(p, "/workspace")), "/")
	if p == "" {
		return "."
	}
	return p
}

// awake is the session the first argument names, its computer awake.
func (c *call) awake() (wire.Session, error) {
	s, err := c.sessionArg(true)
	if err != nil {
		return s, err
	}
	return s, shell.Wake(c.ctx, c.api, s, io.Discard)
}

var filesList = command{
	usage: "files SESSION [PATH]",
	about: "what is in a directory of a session's workspace (/workspace, or PATH in it)",
	run: func(c *call) error {
		s, err := c.awake()
		if err != nil {
			return err
		}
		dir := "."
		if len(c.args) > 1 {
			dir = workspacePath(c.args[1])
		}
		var all []wire.FileEntry
		cursor := ""
		for {
			args := map[string]string{"path": dir}
			if cursor != "" {
				args["cursor"] = cursor
			}
			out, err := c.api.Command(c.ctx, s.SessionID, requestID(), "fs_list", args)
			if err != nil {
				return err
			}
			var page wire.FileList
			if err := json.Unmarshal(out.Result, &page); err != nil {
				return err
			}
			all = append(all, page.Entries...)
			if cursor = page.NextCursor; cursor == "" {
				break
			}
		}
		var rows [][]string
		for _, e := range all {
			name, size := e.Name, fmt.Sprint(e.Size)
			switch e.Type {
			case "directory":
				name, size = name+"/", ""
			case "symlink":
				name, size = name+" -> "+e.Target, ""
			}
			rows = append(rows, []string{name, size})
		}
		return c.table(all, []string{"NAME", "BYTES"}, rows)
	},
}

var filesCat = command{
	usage: "files cat SESSION PATH",
	about: "print a file in a session's workspace",
	run: func(c *call) error {
		s, err := c.awake()
		if err != nil {
			return err
		}
		p, err := c.arg(1, "the file")
		if err != nil {
			return err
		}
		for offset := int64(0); ; {
			out, err := c.api.Command(c.ctx, s.SessionID, requestID(), "fs_read", map[string]any{"path": workspacePath(p), "offset": offset, "length": wire.FileReadMaximumBytes})
			if err != nil {
				return err
			}
			var r wire.FileRange
			if err := json.Unmarshal(out.Result, &r); err != nil {
				return err
			}
			if out.Download != nil && r.Length > 0 {
				if err := c.api.Download(c.ctx, out.Download.URL, c.out); err != nil {
					return err
				}
			}
			offset += r.Length
			if r.EOF || r.Length == 0 {
				return nil
			}
		}
	},
}

var filesGet = command{
	usage: "files get SESSION PATH… [--to DIR]",
	about: "copy files from a session's workspace here (or to DIR); at most 32 files, 512 MB",
	flags: func(fs *flag.FlagSet, c *call) { strFlag(fs, c, "to", "the directory they go to (default: here)") },
	run: func(c *call) error {
		s, err := c.awake()
		if err != nil {
			return err
		}
		if len(c.args) < 2 {
			return errors.New("missing the files to get")
		}
		to := firstNonEmpty(c.str("to"), ".")
		// One download per directory: the API zips files of one directory.
		byDir := map[string][]string{}
		var dirs []string
		for _, p := range c.args[1:] {
			p = workspacePath(p)
			dir, name := path.Split(p)
			dir = firstNonEmpty(strings.TrimSuffix(dir, "/"), ".")
			if byDir[dir] == nil {
				dirs = append(dirs, dir)
			}
			byDir[dir] = append(byDir[dir], name)
		}
		var got []string
		for _, dir := range dirs {
			out, err := c.api.Command(c.ctx, s.SessionID, requestID(), "fs_download", map[string]any{"directory": dir, "files": byDir[dir]})
			if err != nil {
				return err
			}
			if out.Download == nil {
				return errors.New("the download is unavailable")
			}
			var zipped bytes.Buffer
			if err := c.api.Download(c.ctx, out.Download.URL, &zipped); err != nil {
				return err
			}
			names, err := unzip(zipped.Bytes(), to)
			if err != nil {
				return err
			}
			got = append(got, names...)
		}
		return c.say(got, "Copied %s to %s.", strings.Join(got, ", "), to)
	},
}

// unzip writes a download's files into dir, refusing names that would
// leave it.
func unzip(data []byte, dir string) ([]string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, f := range r.File {
		name := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(name) || f.FileInfo().IsDir() {
			return nil, fmt.Errorf("the download holds an unexpected entry %q", f.Name)
		}
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		src, err := f.Open()
		if err != nil {
			return nil, err
		}
		dst, err := os.Create(target)
		if err == nil {
			_, err = io.Copy(dst, src)
			if cerr := dst.Close(); err == nil {
				err = cerr
			}
		}
		src.Close()
		if err != nil {
			return nil, err
		}
		names = append(names, target)
	}
	return names, nil
}

var filesPut = command{
	usage: "files put SESSION FILE… [--to DIR] [--overwrite]",
	about: "copy files here into a session's workspace (/workspace, or DIR in it); at most 32 files, 512 MB",
	flags: func(fs *flag.FlagSet, c *call) {
		strFlag(fs, c, "to", "the workspace directory they go to")
		boolFlag(fs, c, "overwrite", "replace files that are there already")
	},
	run: func(c *call) error {
		s, err := c.awake()
		if err != nil {
			return err
		}
		if len(c.args) < 2 {
			return errors.New("missing the files to put")
		}
		var files []api.FileSize
		var total int64
		for _, p := range c.args[1:] {
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a file", p)
			}
			files = append(files, api.FileSize{Name: filepath.Base(p), Bytes: info.Size()})
			total += info.Size()
		}
		if len(files) > wire.FileTransferMaximumFiles || total > wire.FileTransferMaximumBytes {
			return errors.New("a transfer takes at most 32 files, 512 MB together")
		}
		dir := workspacePath(c.str("to"))
		transfer := "cli_" + strings.TrimPrefix(requestID(), "msg_")
		res, err := c.api.ReserveUploads(c.ctx, s.SessionID, transfer, dir, files, c.flag("overwrite"))
		if err != nil {
			return err
		}
		if len(res.Uploads) != len(files) {
			return errors.New("the API reserved the wrong number of uploads")
		}
		for i, p := range c.args[1:] {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			err = c.api.Put(c.ctx, res.Uploads[i], f, files[i].Bytes)
			f.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
		}
		if _, err := c.api.Command(c.ctx, s.SessionID, transfer, "fs_upload", res.Arguments); err != nil {
			return err
		}
		return c.say(files, "Put %s in /workspace/%s.", count(len(files), "file"), strings.TrimPrefix(dir, "."))
	},
}
