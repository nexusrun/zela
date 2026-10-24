// Package cli implements the remote zela interface. Commands use only the
// shared platform client, also used by the MCP server.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/zela-dev/zela/internal/mcpserver"
	"github.com/zela-dev/zela/internal/platformclient"
	"github.com/zela-dev/zela/internal/version"
)

var userAgent = "zela/" + version.Version

// errNoClient means the arguments parsed but no request could be made because
// help resolution runs without a client.
var errNoClient = errors.New("supply --help as a command flag, rather than another flag's value")

// Command flags that take no value; any other command flag consumes the next argument.
var commandBoolFlags = map[string]bool{"sudo": true, "all": true, "h": true, "help": true, "read-only": true}

type Runner struct {
	In           io.Reader
	Out, Err     io.Writer
	Env          func(string) string
	transport    http.RoundTripper
	pollInterval time.Duration
}
type options struct {
	config, profile, url, tokenFile, ca string
	timeout, waitTimeout                time.Duration
	json, wait                          bool
}
type result struct {
	data      json.RawMessage
	columns   []string
	rows      []map[string]any
	rawText   string
	operation bool
}

func (r *Runner) Run(ctx context.Context, args []string) error {
	if r.In == nil {
		r.In = os.Stdin
	}
	if r.Out == nil {
		r.Out = os.Stdout
	}
	if r.Err == nil {
		r.Err = os.Stderr
	}
	if r.Env == nil {
		r.Env = os.Getenv
	}
	o, args, err := parseGlobal(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err = io.WriteString(r.Out, help)
		return err
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return fmt.Errorf("usage: zela version")
		}
		_, err = fmt.Fprintln(r.Out, "zela "+version.Version)
		return err
	}
	isMCP := args[0] == "mcp"
	var mcpReadOnly bool
	if isMCP {
		if len(args) < 2 || args[1] != "serve" {
			return fmt.Errorf("usage: zela mcp serve [--read-only]")
		}
		fs := r.flags("zela mcp serve")
		fs.BoolVar(&mcpReadOnly, "read-only", false, "Expose only read tools")
		if err := parseFlags(fs, args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("usage: zela mcp serve [--read-only]")
		}
		if o.tokenFile == "-" || (o.tokenFile == "" && r.Env("ZELA_TOKEN_FILE") == "-") {
			return fmt.Errorf("MCP reserves stdin for protocol messages; use a token file, ZELA_TOKEN or a saved profile")
		}
	}
	// Command help is local and must work before login. Configuration commands
	// handle --help themselves after a local config read.
	isConfig := args[0] == "config" || args[0] == "login" || args[0] == "logout"
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		if isConfig || (arg != "--help" && arg != "-h") {
			continue
		}
		if len(args) == 2 {
			_, err = io.WriteString(r.Out, help)
			return err
		}
		_, err = r.command(ctx, nil, args)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if !errors.Is(err, errNoClient) {
			return err
		}
		// -h was another flag's value; run the command normally.
		break
	}
	if o.config == "" {
		o.config = r.Env("ZELA_CONFIG")
	}
	if o.config == "" {
		o.config, err = defaultConfigPath(r.Env)
		if err != nil {
			return err
		}
	}
	cfg, err := loadConfig(o.config)
	if err != nil {
		return err
	}
	if o.profile == "" {
		o.profile = r.Env("ZELA_PROFILE")
	}
	if o.profile == "" {
		o.profile = cfg.Current
	}
	if isConfig {
		return r.configure(ctx, o, cfg, args)
	}
	p, err := r.connection(o, cfg)
	if err != nil {
		return err
	}
	if p.Token == "" && args[0] != "health" && args[0] != "ready" {
		return fmt.Errorf("no token configured; use zela login --url URL --token-file FILE, or ZELA_TOKEN")
	}
	client, err := platformclient.New(platformclient.Config{URL: p.URL, Token: p.Token, CACert: p.CACert, Timeout: o.timeout, Transport: r.transport, UserAgent: userAgent})
	if err != nil {
		return err
	}
	defer client.Close()
	if isMCP {
		data, err := client.Do(ctx, "GET", "/api/mcp/access", nil)
		if err != nil {
			return err
		}
		var access struct {
			ReadOnly bool `json:"readOnly"`
		}
		if json.Unmarshal(data, &access) != nil {
			return fmt.Errorf("invalid MCP access response")
		}
		return mcpserver.ServeStdio(ctx, client.Do, mcpReadOnly || access.ReadOnly, r.In, r.Out)
	}
	if len(args) > 1 && args[0] == "operations" && args[1] == "wait" {
		o.wait = true
	}
	res, err := r.command(ctx, client, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if o.wait && !res.operation {
		fmt.Fprintln(r.Err, "zela: --wait ignored; this command did not start an operation")
	}
	if res.operation && o.wait {
		var op struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(res.data, &op) != nil || op.ID == "" {
			return fmt.Errorf("operation response has no ID")
		}
		if !o.json {
			fmt.Fprintln(r.Err, "Waiting for operation "+op.ID)
		}
		res.data, err = r.wait(ctx, client, op.ID, o.waitTimeout)
		if res.data != nil {
			if printErr := r.print(o, res); printErr != nil {
				return printErr
			}
		}
		return err
	}
	return r.print(o, res)
}

var globalFlags = map[string]bool{"--json": true, "--wait": true, "--config": true, "--profile": true, "--url": true, "--token-file": true, "--ca-cert": true, "--timeout": true, "--wait-timeout": true}

func parseGlobal(args []string) (options, []string, error) {
	o := options{timeout: 30 * time.Second, waitTimeout: 35 * time.Minute}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		key, value, hasValue := strings.Cut(a, "=")
		if !globalFlags[key] {
			rest = append(rest, a)
			// Keep a command flag's value with it, even if it looks like a global flag.
			if name := strings.TrimLeft(a, "-"); strings.HasPrefix(a, "-") && !hasValue && name != "" && !commandBoolFlags[name] && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		switch key {
		case "--json", "--wait":
			v := true
			var err error
			if hasValue {
				v, err = strconv.ParseBool(value)
				if err != nil {
					return o, nil, fmt.Errorf("%s must be true or false", key)
				}
			}
			if key == "--json" {
				o.json = v
			} else {
				o.wait = v
			}
		case "--config", "--profile", "--url", "--token-file", "--ca-cert", "--timeout", "--wait-timeout":
			if !hasValue {
				i++
				if i >= len(args) {
					return o, nil, fmt.Errorf("%s needs a value", key)
				}
				value = args[i]
			}
			if value == "" {
				return o, nil, fmt.Errorf("%s cannot be empty", key)
			}
			switch key {
			case "--config":
				o.config = value
			case "--profile":
				o.profile = value
			case "--url":
				o.url = value
			case "--token-file":
				o.tokenFile = value
			case "--ca-cert":
				o.ca = value
			default:
				d, err := time.ParseDuration(value)
				if err != nil || d <= 0 {
					return o, nil, fmt.Errorf("%s must be a positive duration", key)
				}
				if key == "--timeout" {
					o.timeout = d
				} else {
					o.waitTimeout = d
				}
			}
		default:
			rest = append(rest, a)
		}
	}
	return o, rest, nil
}
func (r *Runner) connection(o options, cfg Config) (Profile, error) {
	p := Profile{}
	if o.profile != "" {
		var ok bool
		p, ok = cfg.Profiles[o.profile]
		if !ok && o.url == "" && r.Env("ZELA_URL") == "" {
			return p, fmt.Errorf("profile %q not found", o.profile)
		}
	}
	originalURL := p.URL
	if v := r.Env("ZELA_URL"); v != "" {
		p.URL = v
	}
	if o.url != "" {
		p.URL = o.url
	}
	// A changed origin must never inherit credentials from another endpoint.
	if strings.TrimRight(p.URL, "/") != strings.TrimRight(originalURL, "/") {
		p.Token = ""
	}
	if v := r.Env("ZELA_CA_CERT"); v != "" {
		p.CACert = v
	}
	if o.ca != "" {
		p.CACert = o.ca
	}
	if v := r.Env("ZELA_TOKEN"); v != "" {
		p.Token = v
	}
	file := r.Env("ZELA_TOKEN_FILE")
	if o.tokenFile != "" {
		file = o.tokenFile
	}
	if file != "" {
		v, err := readSecret(file, r.In)
		if err != nil {
			return p, err
		}
		p.Token = v
	}
	if p.URL == "" {
		return p, fmt.Errorf("no cluster endpoint configured; use --url or zela login")
	}
	if _, err := platformclient.ParseURL(p.URL); err != nil {
		return p, err
	}
	return p, nil
}
func (r *Runner) configure(ctx context.Context, o options, cfg Config, args []string) error {
	group := args[0]
	verb := ""
	var pos []string
	if group == "config" {
		if len(args) < 2 {
			return fmt.Errorf("usage: zela config set|list|use|delete")
		}
		verb = args[1]
		pos = args[2:]
	} else {
		verb = group
		pos = args[1:]
	}
	fs := r.flags("zela " + group + " " + verb)
	if err := parseFlags(fs, pos); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	pos = fs.Args()
	switch verb {
	case "list":
		if len(pos) != 0 {
			return fmt.Errorf("config list takes no arguments")
		}
		rows := []map[string]any{}
		for name, p := range cfg.Profiles {
			rows = append(rows, map[string]any{"name": name, "url": p.URL, "current": cfg.Current == name, "authenticated": p.Token != "", "caCert": p.CACert})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i]["name"].(string) < rows[j]["name"].(string) })
		b, _ := json.Marshal(rows)
		return r.print(o, result{data: b, columns: []string{"name", "url", "current", "authenticated"}, rows: rows})
	case "use", "delete":
		if len(pos) != 1 {
			return fmt.Errorf("config %s requires a profile name", verb)
		}
		name := pos[0]
		if _, ok := cfg.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		if verb == "use" {
			cfg.Current = name
		} else {
			delete(cfg.Profiles, name)
			if cfg.Current == name {
				cfg.Current = ""
			}
		}
	case "logout":
		if len(pos) != 0 {
			return fmt.Errorf("logout takes no arguments; select --profile")
		}
		if o.profile == "" {
			return fmt.Errorf("select a profile to log out")
		}
		p, ok := cfg.Profiles[o.profile]
		if !ok {
			return fmt.Errorf("profile not found")
		}
		p.Token = ""
		cfg.Profiles[o.profile] = p
	case "set", "login":
		name := o.profile
		if verb == "set" {
			if len(pos) != 1 {
				return fmt.Errorf("config set requires a profile name")
			}
			name = pos[0]
		} else if len(pos) > 0 {
			return fmt.Errorf("login takes no positional arguments")
		}
		if name == "" {
			name = "default"
		}
		if !profileName.MatchString(name) {
			return fmt.Errorf("profile name must be 1–63 letters, digits, dots, underscores or dashes")
		}
		// Explicit profile updates do not silently copy another profile's credentials.
		target := o
		target.profile = name
		p, err := r.connection(target, cfg)
		if err != nil {
			return err
		}
		// config set never persists ambient ZELA_TOKEN/ZELA_TOKEN_FILE; only an
		// explicit --token-file, or the profile's own token for the same URL.
		if verb == "set" && o.tokenFile == "" {
			p.Token = ""
			if old, ok := cfg.Profiles[name]; ok && strings.TrimRight(old.URL, "/") == strings.TrimRight(p.URL, "/") {
				p.Token = old.Token
			}
		}
		if verb == "login" {
			if p.Token == "" {
				return fmt.Errorf("login requires --token-file or ZELA_TOKEN")
			}
			c, err := platformclient.New(platformclient.Config{URL: p.URL, Token: p.Token, CACert: p.CACert, Timeout: o.timeout, Transport: r.transport, UserAgent: userAgent})
			if err != nil {
				return err
			}
			defer c.Close()
			if _, err = c.Do(ctx, "GET", "/api/cluster", nil); err != nil {
				return err
			}
		}
		if p.CACert != "" {
			absolute, err := filepathAbs(p.CACert)
			if err != nil {
				return err
			}
			p.CACert = absolute
		}
		cfg.Profiles[name] = p
		cfg.Current = name
	default:
		return fmt.Errorf("unknown config command %q", verb)
	}
	if err := saveConfig(o.config, cfg); err != nil {
		return fmt.Errorf("save CLI configuration: %w", err)
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "current": cfg.Current})
	return r.print(o, result{data: b})
}
func (r *Runner) flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(r.Err)
	return f
}

// Accept flags before or after IDs, while preserving the -- positional delimiter.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var options, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		if strings.HasPrefix(a, "---") {
			return fmt.Errorf("invalid flag %s", a)
		}
		key, _, eq := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(key)
		options = append(options, a)
		if f != nil && !eq {
			isBool := false
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
				isBool = b.IsBoolFlag()
			}
			if !isBool {
				i++
				if i >= len(args) {
					return fmt.Errorf("--%s needs a value", key)
				}
				options = append(options, args[i])
			}
		}
	}
	options = append(options, "--")
	options = append(options, pos...)
	return fs.Parse(options)
}
func (r *Runner) print(o options, res result) error {
	if !o.json && res.rawText != "" {
		_, err := io.WriteString(r.Out, res.rawText)
		return err
	}
	if !o.json && res.columns != nil {
		w := tabwriter.NewWriter(r.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, strings.ToUpper(strings.Join(res.columns, "\t")))
		for _, row := range res.rows {
			v := make([]string, len(res.columns))
			for i, k := range res.columns {
				v[i] = safeCell(row[k])
			}
			fmt.Fprintln(w, strings.Join(v, "\t"))
		}
		return w.Flush()
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, res.data, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := r.Out.Write(buf.Bytes())
	return err
}
func safeCell(v any) string {
	if v == nil {
		return "-"
	}
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, fmt.Sprint(v))
}
func (r *Runner) wait(ctx context.Context, c *platformclient.Client, id string, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	interval := r.pollInterval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		data, err := c.Do(ctx, "GET", "/api/operations/"+segment(id), nil)
		if err == nil {
			var op struct{ Status, Detail string }
			if json.Unmarshal(data, &op) != nil {
				return nil, fmt.Errorf("invalid operation response")
			}
			switch op.Status {
			case "succeeded":
				return data, nil
			case "failed":
				return data, fmt.Errorf("operation %s failed: %s", id, op.Detail)
			case "running", "queued", "pending":
			default:
				return data, fmt.Errorf("operation %s has unknown status %q", id, op.Status)
			}
		} else {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("stopped waiting for operation %s: %w; inspect the operation before retrying", id, ctx.Err())
			}
			var api *platformclient.APIError
			var request *platformclient.RequestError
			if !errors.As(err, &request) && (!errors.As(err, &api) || (api.Status != 503 && api.Status != 502)) {
				return nil, err
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("stopped waiting for operation %s: %w; inspect the operation before retrying", id, ctx.Err())
		case <-timer.C:
		}
	}
}
