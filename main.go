package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/williamtcastro/wame/internal/admin"
	"github.com/williamtcastro/wame/internal/api"
	"github.com/williamtcastro/wame/internal/auth"
	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/mcpserver"
	"github.com/williamtcastro/wame/internal/store"
	"github.com/williamtcastro/wame/internal/tui"
)

// version is set by release builds (-ldflags "-X main.version=...").
var version = "dev"

const usage = `wame - WhatsApp manager for your terminal: TUI, HTTP API, bot and MCP server

Usage:
  wame login [-phone NUMBER]          link this bot to your WhatsApp (QR or pairing code)
  wame serve [-addr ADDR]             run the HTTP API
  wame tui   [-addr ADDR]             interactive terminal UI (optionally also serves the API)
  wame send  NUMBER|JID TEXT...       send one message and exit
  wame send  -at WHEN [-before WHEN] NUMBER|JID TEXT...
                                      queue it on a running server for WHEN (2h, 09:00, tomorrow 09:00, RFC 3339)
  wame token create NAME [-scopes read,send] [-chats N,...] [-rate N] [-expires 720h]
  wame token list | revoke NAME       manage API tokens for agents and bots (see docs/API.md)
  wame mcp [-allow-send] [-mcp-token TOKEN]
                                       MCP server over stdio for AI agents (see docs/MCP.md)
  wame admin [-profile NAME] [-server URL] [-insecure]
                                       admin panel for a running server: status, login, chats, tokens,
                                       audit, health
                                       (WAME_SERVER, WAME_TOKEN: env, ./.env or ~/.config/wame/admin.env)
  wame queue import [-batch NAME] [-text DEFAULT] [-send-at TIME] [-key KEY] [-dry-run] FILE.csv
                                       queue the messages of a CSV file on a running server, 1000 per
                                       request; safe to run again (see docs/QUEUE.md#csv-import)
                    [-template] [-dedupe] [-skip-sent 48h|batch:NAME] [-file PATH|ID]
                                       fill {name} per recipient, drop repeated rows, skip people
                                       messaged recently (to may be tag:NAME); -file uploads one image
                                       or document once and sends it to every row (text = caption)
  wame queue status [-batch NAME]     counts of queued, sent and failed messages
                                       (both take admin's -profile, -server, -token, -insecure)
  wame backup [-keep N] [PATH]        copy the database safely, even while a server has it open
                                       (default: wame-YYYYMMDD-HHMMSS.db next to it; PATH may be a dir/)
  wame version                        print the version
  wame logout                         unlink the device; delete the session, contacts and groups

Flags (right after the command, before other arguments):
  -db PATH               session database (env WAME_DB, default ~/.config/wame/wame.db)
  -addr ADDR             HTTP listen address (env WAME_ADDR; serve defaults to 127.0.0.1:8080)
  -token TOKEN           require "Authorization: Bearer TOKEN" on the API (env WAME_TOKEN)
  -webhook URL           POST incoming messages as JSON to URL (env WAME_WEBHOOK)
  -webhook-secret S      sign webhook bodies with HMAC-SHA256 (env WAME_WEBHOOK_SECRET)
  -webhook-filter Q      only deliver matching messages, e.g. "type=text&from_me=false" (env WAME_WEBHOOK_FILTER)
  -rate N                max messages sent per minute, 0 = unlimited (env WAME_RATE, default 20)
  -chat-gap D            min time between messages to the same chat (default 1s)
  -max-wait D            queue a send at most this long before answering 429 (default 30s)
  -typing                show "typing…" before each text message
  -online                appear "online" while connected (off: your phone keeps getting notifications)
  -autoreply             answer !ping, !time and !help (default true)
  -queue-jitter D        wait a random 0–D more between queued sends (env WAME_QUEUE_JITTER, default 2s)
  -queue-quiet-hours W   send nothing from the queue in this daily window, e.g. 21:00-08:00
                         (env WAME_QUEUE_QUIET_HOURS, default off)
  -queue-timezone TZ     time zone of the quiet hours, e.g. America/Sao_Paulo
                         (env WAME_QUEUE_TIMEZONE, default Local: the server's zone)
  -alert-gotify URL      push alerts (disconnected, logged out, queue and webhook trouble) to this Gotify server (env WAME_ALERT_GOTIFY)
  -alert-gotify-token T  the Gotify app token, sent as X-Gotify-Key (env WAME_ALERT_GOTIFY_TOKEN)
  -alert-delay D         alert when WhatsApp stays disconnected this long (env WAME_ALERT_DELAY, default 2m, min 30s)
  -log-format F          text or json (env WAME_LOG_FORMAT, default text)
  -log-file PATH         write app logs to PATH instead of stderr (env WAME_LOG_FILE)
  -debug                 verbose logs, including whatsmeow's

Docs: README.md, docs/*.md, or http://localhost:8080/docs while serving`

type config struct {
	db, addr, token, phone                string
	webhook, webhookSecret, webhookFilter string
	alertGotify, alertGotifyToken         string
	logFormat, logFile                    string
	quietHours, timezone                  string
	rate                                  int
	chatGap, maxWait, queueJitter         time.Duration
	alertDelay                            time.Duration
	autoreply, typing, online, debug      bool
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Println(usage)
		return
	case "version", "--version":
		fmt.Println("wame", version)
		return
	case "admin":
		exitOn(adminCommand(args))
		return
	case "backup":
		exitOn(backupCommand(args))
		return
	case "queue":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err := queueCommand(ctx, args, os.Stdout)
		stop()
		exitOn(err)
		return
	case "send":
		if scheduledSend(args) {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			err := sendAtCommand(ctx, args, os.Stdout)
			stop()
			exitOn(err)
			return
		}
	}

	var c config
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Println(usage) }
	fs.StringVar(&c.db, "db", envOr("WAME_DB", defaultDB()), "")
	fs.StringVar(&c.addr, "addr", envOr("WAME_ADDR", ""), "")
	fs.StringVar(&c.token, "token", envOr("WAME_TOKEN", ""), "")
	fs.StringVar(&c.webhook, "webhook", envOr("WAME_WEBHOOK", ""), "")
	fs.StringVar(&c.webhookSecret, "webhook-secret", envOr("WAME_WEBHOOK_SECRET", ""), "")
	fs.StringVar(&c.webhookFilter, "webhook-filter", envOr("WAME_WEBHOOK_FILTER", ""), "")
	fs.StringVar(&c.logFormat, "log-format", envOr("WAME_LOG_FORMAT", "text"), "")
	fs.StringVar(&c.logFile, "log-file", envOr("WAME_LOG_FILE", ""), "")
	fs.StringVar(&c.phone, "phone", "", "")
	rate, err := strconv.Atoi(envOr("WAME_RATE", "20"))
	if err != nil || rate < 0 {
		fmt.Fprintln(os.Stderr, "error: WAME_RATE must be a non-negative number")
		os.Exit(2)
	}
	fs.IntVar(&c.rate, "rate", rate, "")
	fs.DurationVar(&c.chatGap, "chat-gap", time.Second, "")
	fs.DurationVar(&c.maxWait, "max-wait", 30*time.Second, "")
	fs.BoolVar(&c.autoreply, "autoreply", true, "")
	queueJitter, err := time.ParseDuration(envOr("WAME_QUEUE_JITTER", "2s"))
	if err != nil || queueJitter < 0 {
		fmt.Fprintln(os.Stderr, "error: WAME_QUEUE_JITTER must be a duration like 2s")
		os.Exit(2)
	}
	fs.DurationVar(&c.queueJitter, "queue-jitter", queueJitter, "")
	fs.StringVar(&c.quietHours, "queue-quiet-hours", envOr("WAME_QUEUE_QUIET_HOURS", ""), "")
	fs.StringVar(&c.timezone, "queue-timezone", envOr("WAME_QUEUE_TIMEZONE", "Local"), "")
	fs.StringVar(&c.alertGotify, "alert-gotify", envOr("WAME_ALERT_GOTIFY", ""), "")
	fs.StringVar(&c.alertGotifyToken, "alert-gotify-token", envOr("WAME_ALERT_GOTIFY_TOKEN", ""), "")
	alertDelay, err := time.ParseDuration(envOr("WAME_ALERT_DELAY", "2m"))
	if err != nil || alertDelay < 0 {
		fmt.Fprintln(os.Stderr, "error: WAME_ALERT_DELAY must be a duration like 2m")
		os.Exit(2)
	}
	fs.DurationVar(&c.alertDelay, "alert-delay", alertDelay, "")
	fs.BoolVar(&c.typing, "typing", false, "")
	fs.BoolVar(&c.online, "online", false, "")
	fs.BoolVar(&c.debug, "debug", false, "")
	_ = fs.Parse(args)
	if c.rate < 0 {
		fmt.Fprintln(os.Stderr, "error: -rate must be 0 or more")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = run(ctx, cmd, fs.Args(), c)
	stop()
	exitOn(err)
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// secrets are replaced wherever they appear in the logs kept for GET /logs.
func (c config) secrets() []string {
	return []string{c.token, c.webhook, c.webhookSecret, c.alertGotify, c.alertGotifyToken}
}

func run(ctx context.Context, cmd string, args []string, c config) error {
	for _, f := range []struct {
		name string
		d    time.Duration
	}{{"-chat-gap", c.chatGap}, {"-max-wait", c.maxWait}, {"-queue-jitter", c.queueJitter}, {"-alert-delay", c.alertDelay}} {
		if f.d < 0 {
			return fmt.Errorf("%s must be 0 or more", f.name)
		}
	}
	level := "INFO"
	if c.debug {
		level = "DEBUG"
	}
	quiet := cmd == "tui" || cmd == "send"
	var waLogger waLog.Logger = waLog.Stdout("whatsmeow", level, true)
	// In mcp mode stdout carries the protocol, so whatsmeow stays silent and app logs go to stderr.
	if quiet || cmd == "mcp" {
		waLogger = waLog.Noop
	}
	logs := bot.NewLogBuffer(logBufferSize)
	logs.Scrub(c.secrets()...)
	log, closeLog, err := newLogger(c.logFormat, c.logFile, level, quiet, logs)
	if err != nil {
		return err
	}
	defer closeLog.Close()
	if dir := filepath.Dir(c.db); dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}

	b, err := bot.New(ctx, c.db, waLogger, log)
	if err != nil {
		return err
	}
	defer b.Close()
	b.Logs = logs
	b.Typing = c.typing
	b.Online = c.online
	if c.webhook != "" {
		if u, err := url.Parse(c.webhook); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("-webhook must be an http(s) URL")
		}
		if _, err := bot.ParseWebhookFilter(c.webhookFilter); err != nil {
			return fmt.Errorf("-webhook-filter: %w", err)
		}
		b.StartWebhook(c.webhook, c.webhookSecret)
	}
	quietHours, err := bot.ParseQuietHours(c.quietHours)
	if err != nil {
		return fmt.Errorf("-queue-quiet-hours: %w", err)
	}
	tz, err := bot.ParseTimezone(c.timezone)
	if err != nil {
		return fmt.Errorf("-queue-timezone: %w", err)
	}
	if c.alertGotify != "" {
		g, err := bot.NewGotify(c.alertGotify, c.alertGotifyToken)
		if err != nil {
			return fmt.Errorf("-alert-gotify: %w", err)
		}
		b.SetAlerts(g, c.alertDelay)
	}
	// Settings changed at runtime (GET/PATCH /settings) override these flags.
	if _, err := b.InitSettings(bot.Settings{Rate: c.rate, ChatGap: c.chatGap, MaxWait: c.maxWait, AutoReply: c.autoreply,
		WebhookFilter: c.webhookFilter, QueueJitter: c.queueJitter, QuietHours: quietHours, Timezone: tz}); err != nil {
		return err
	}

	switch cmd {
	case "login":
		return login(ctx, b, c.phone)
	case "serve":
		return serveCommand(ctx, b, c)
	case "tui":
		return tuiCommand(ctx, b, c)
	case "send":
		b.SetAutoReply(false)
		return sendCommand(ctx, b, args)
	case "token":
		return tokenCommand(b, args)
	case "mcp":
		return mcpCommand(ctx, b, args)
	case "logout":
		return logoutCommand(ctx, b)
	}
	return fmt.Errorf("unknown command %q (try 'wame help')", cmd)
}

func serveCommand(ctx context.Context, b *bot.Bot, c config) error {
	if c.addr == "" {
		c.addr = "127.0.0.1:8080"
	}
	if err := guardPublicAddr(b, c); err != nil {
		return err
	}
	if err := b.Start(ctx); err != nil {
		return err
	}
	go printQRUpdates(ctx, b)
	b.Go(b.RunOutbox)
	b.Go(b.RunAlerts)
	return serve(ctx, b, c)
}

// tuiCommand runs the terminal UI, plus the HTTP API when -addr is set; quitting stops both.
func tuiCommand(ctx context.Context, b *bot.Bot, c config) error {
	if c.addr != "" {
		if err := guardPublicAddr(b, c); err != nil {
			return err
		}
	}
	if err := b.Start(ctx); err != nil {
		return err
	}
	b.Go(b.RunOutbox)
	b.Go(b.RunAlerts)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var served sync.WaitGroup
	if c.addr != "" {
		served.Go(func() {
			if err := serve(ctx, b, c); err != nil {
				b.Log.Error("HTTP API stopped", "err", err)
			}
		})
	}
	err := tui.Run(b)
	cancel()
	served.Wait()
	return err
}

func sendCommand(ctx context.Context, b *bot.Bot, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: wame send NUMBER|JID TEXT")
	}
	if b.Client.Store.ID == nil {
		return errors.New("not logged in, run 'wame login' first")
	}
	if err := b.Start(ctx); err != nil {
		return err
	}
	if err := waitLoggedIn(ctx, b, 20*time.Second); err != nil {
		return err
	}
	m, err := b.SendText(ctx, "cli", args[0], strings.Join(args[1:], " "), bot.SendOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("sent %s to %s\n", m.ID, m.Chat)
	return nil
}

// scheduledSend reports whether wame send was given a time: it then queues the message on a
// running server instead of sending it from this process.
func scheduledSend(args []string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && (name == "at" || name == "before") {
			return true
		}
	}
	return false
}

// sendAtCommand is wame send -at: queues one message over the HTTP API (POST /send with send_at).
func sendAtCommand(ctx context.Context, args []string, out io.Writer) error {
	const use = "usage: wame send -at WHEN [-before WHEN] [-server URL] [-profile NAME] NUMBER|JID TEXT\n" +
		"  WHEN: 2026-10-21T09:00:00-03:00, 2026-10-21T09:00 (their clock), 2h, 09:00 or \"tomorrow 09:00\""
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sf serverFlags
	sf.register(fs)
	at := fs.String("at", "", "")
	before := fs.String("before", "", "")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, use)
	}
	if fs.NArg() < 2 || *at == "" {
		return errors.New(use)
	}
	when, err := bot.ParseSendTime(*at, time.Now(), true)
	if err != nil {
		return fmt.Errorf("-at %w", err)
	}
	deadline, err := bot.ParseDeadline(*before)
	if err != nil {
		return fmt.Errorf("-before %w", err)
	}
	target, _, err := sf.resolve()
	if err != nil {
		return err
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	res, err := c.SendAt(ctx, fs.Arg(0), strings.Join(fs.Args()[1:], " "), "", when.String(), deadline.String())
	if err != nil {
		return err
	}
	if len(res.Items) == 0 {
		return errors.New("nothing was queued")
	}
	it := res.Items[0]
	fmt.Fprintf(out, "queued #%d for %s\n", it.ID, it.Due.Local().Format("Mon 02 Jan 15:04 (MST)"))
	return nil
}

func logoutCommand(ctx context.Context, b *bot.Bot) error {
	if b.Client.Store.ID == nil {
		return errors.New("not logged in")
	}
	if err := b.Start(ctx); err != nil {
		return err
	}
	_ = waitLoggedIn(ctx, b, 20*time.Second)
	if err := b.Logout(ctx, "cli"); err != nil {
		return err
	}
	fmt.Println("logged out")
	return nil
}

func login(ctx context.Context, b *bot.Bot, phone string) error {
	if b.Client.Store.ID != nil {
		fmt.Println("already logged in as", b.Client.Store.ID.ToNonAD())
		return nil
	}
	if err := b.Start(ctx); err != nil {
		return err
	}
	if phone != "" {
		deadline := time.Now().Add(30 * time.Second)
		for b.Status().QR == "" {
			if time.Now().After(deadline) {
				return fmt.Errorf("WhatsApp didn't start the login (state: %s)", b.Status().State)
			}
			if err := bot.SleepUntil(ctx, time.Now().Add(200*time.Millisecond)); err != nil {
				return err
			}
		}
		code, err := b.PairPhone(ctx, "cli", phone)
		if err != nil {
			return err
		}
		fmt.Println("Pairing code:", code)
		fmt.Println("On your phone: WhatsApp → Linked devices → Link a device → Link with phone number instead")
	} else {
		go printQRUpdates(ctx, b)
	}
	if err := waitLoggedIn(ctx, b, 5*time.Minute); err != nil {
		return err
	}
	fmt.Println("✅ logged in as", b.Status().JID)
	fmt.Println("finishing initial sync…")
	select {
	case <-ctx.Done():
	case <-time.After(8 * time.Second):
	}
	return nil
}

func printQRUpdates(ctx context.Context, b *bot.Bot) {
	ch, unsub := b.Subscribe()
	defer unsub()
	last := ""
	show := func(s bot.Status) {
		if s.QR != "" && s.QR != last {
			last = s.QR
			fmt.Println("\nScan this QR with WhatsApp → Linked devices → Link a device:")
			qrterminal.GenerateHalfBlock(s.QR, qrterminal.L, os.Stdout)
		}
	}
	show(b.Status())
	for {
		select {
		case <-ctx.Done():
			return
		case v := <-ch:
			if s, ok := v.(bot.Status); ok {
				show(s)
			}
		}
	}
}

func waitLoggedIn(ctx context.Context, b *bot.Bot, timeout time.Duration) error {
	deadline := time.After(timeout)
	for !b.Client.IsLoggedIn() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return errors.New("timed out waiting for WhatsApp login")
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

// serve runs the HTTP API until ctx is done, then gives requests 5 seconds to finish.
func serve(ctx context.Context, b *bot.Bot, c config) error {
	h := api.NewHandler(b, &auth.Authenticator{Store: b.Store, Owner: c.token}, version)
	srv := &http.Server{
		Addr:              c.addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	srv.RegisterOnShutdown(h.StopStreams)
	if c.token == "" {
		b.Log.Warn("API has no token: only local, same-origin requests are accepted", "addr", c.addr)
	}
	b.Log.Info("HTTP API listening", "addr", c.addr, "docs", "/docs")
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return errors.Join(fmt.Errorf("shut down HTTP API: %w", err), srv.Close())
	}
	return nil
}

// guardPublicAddr refuses a non-loopback listener unless tokens are required (an owner secret, or any
// token ever created), and rejects owner secrets short enough to guess.
func guardPublicAddr(b *bot.Bot, c config) error {
	if err := auth.CheckOwnerSecret(c.token); err != nil {
		return err
	}
	if isLoopbackAddr(c.addr) || c.token != "" {
		return nil
	}
	has, err := b.Store.HasTokens()
	if err != nil {
		return fmt.Errorf("check tokens: %w", err)
	}
	if !has {
		return fmt.Errorf("refusing to serve on %s without a token: anyone on the network could send messages as you (create one with 'wame token create', set WAME_TOKEN, or bind to 127.0.0.1)", c.addr)
	}
	return nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// envOr reads the environment variable k, or def when it is unset or empty.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// defaultDB is ~/.config/wame/wame.db (or $XDG_CONFIG_HOME/wame/wame.db) on macOS and Linux alike.
func defaultDB() string {
	dir := configDir()
	if dir == "" {
		return "wame.db"
	}
	return filepath.Join(dir, "wame", "wame.db")
}

func configDir() string {
	if runtime.GOOS == "windows" {
		dir, _ := os.UserConfigDir()
		return dir
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return ""
}

// logBufferSize is how many app log records GET /logs can return.
const logBufferSize = 1000

// newLogger builds the app logger. format is "text" or "json"; file "" means stderr. Records also go
// to logs (for GET /logs) when it's set, whatever the output.
func newLogger(format, file, level string, quiet bool, logs *bot.LogBuffer) (*slog.Logger, io.Closer, error) {
	var w io.Writer = os.Stderr
	var closer io.Closer = io.NopCloser(nil)
	switch {
	case file != "":
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, nil, err
		}
		w, closer = f, f
	case quiet:
		w = io.Discard
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(w, opts)
	if format == "json" {
		h = slog.NewJSONHandler(w, opts)
	}
	if logs != nil {
		h = logs.Wrap(h)
	}
	return slog.New(h), closer, nil
}

// tokenCommand manages API tokens. It only touches the database, so it's safe to run while serve is up.
func tokenCommand(b *bot.Bot, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: wame token create NAME [flags] | list | revoke NAME")
	}
	switch args[0] {
	case "list":
		tokens, err := b.Store.Tokens()
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			fmt.Println("no tokens yet; create one with 'wame token create NAME'")
			return nil
		}
		fmt.Printf("%-20s %-16s %-6s %-10s %-17s %-17s %-20s %s\n", "NAME", "SCOPES", "RATE", "STATUS", "EXPIRES", "LAST USED", "CREATED BY", "CHATS")
		for _, t := range tokens {
			status := "active"
			switch {
			case t.Revoked:
				status = "revoked"
			case !t.Expires.IsZero() && time.Now().After(t.Expires):
				status = "expired"
			}
			fmt.Printf("%-20s %-16s %-6s %-10s %-17s %-17s %-20s %s\n", t.Name, strings.Join(t.Scopes, ","), rateLabel(t.Rate), status,
				timeLabel(t.Expires, "never"), timeLabel(t.LastUsed, "never"), cmp.Or(t.CreatedBy, "-"), cmp.Or(strings.Join(t.Chats, ","), "all"))
		}
		return nil

	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: wame token revoke NAME")
		}
		n, err := b.Store.RevokeToken(args[1])
		b.Audit("cli", "token_revoke", args[1], "", err)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("no active token named %q", args[1])
		}
		fmt.Printf("revoked %s (%d token(s), including any it created)\n", args[1], n)
		return nil

	case "create":
		rest := args[1:]
		name := ""
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name, rest = rest[0], rest[1:]
		}
		fs := flag.NewFlagSet("token create", flag.ContinueOnError)
		scopes := fs.String("scopes", "read", "comma-separated: read, send, admin")
		chats := fs.String("chats", "", "comma-separated phone numbers or JIDs this token may access (default all)")
		rate := fs.Int("rate", auth.DefaultRate, "max sends per minute for this token, on top of the global limit (0 = global only)")
		expires := fs.Duration("expires", 0, "lifetime, e.g. 720h (0 = never)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if name == "" && fs.NArg() == 1 {
			name = fs.Arg(0)
		}
		if name == "" {
			return errors.New("usage: wame token create NAME [-scopes read,send] [-chats ...] [-rate N] [-expires 720h]")
		}
		sc, err := auth.ParseScopes(strings.Split(*scopes, ","))
		if err != nil {
			return err
		}
		list, err := api.NormalizeChats(strings.Split(*chats, ","))
		if err != nil {
			return err
		}
		plain, t, err := auth.Issue(b.Store, auth.Request{Name: name, Scopes: sc, Chats: list, Rate: *rate, TTL: *expires})
		b.Audit("cli", "token_create", name, *scopes, err)
		if err != nil {
			return err
		}
		fmt.Printf("Created token %q (scopes: %s, chats: %s)\n\n  %s\n\nStore it now: it can't be shown again. Use it as 'Authorization: Bearer <token>'.\n",
			t.Name, strings.Join(t.Scopes, ","), cmp.Or(strings.Join(t.Chats, ","), "all"), plain)
		fmt.Println("From now on the API requires a token on every request, including from localhost, even after tokens are revoked.")
		return nil
	}
	return fmt.Errorf("unknown token command %q (use create, list or revoke)", args[0])
}

func rateLabel(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

func timeLabel(t time.Time, zero string) string {
	if t.IsZero() {
		return zero
	}
	return t.Local().Format("2006-01-02 15:04")
}

// mcpCommand serves MCP over stdio. It's read-only unless -allow-send is given, or acts as an existing
// token (its scopes and chat allowlist) with -mcp-token. It holds its own WhatsApp connection, so it
// can't run alongside serve or tui on the same database; use http://.../mcp on a running server instead.
func mcpCommand(ctx context.Context, b *bot.Bot, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	allowSend := fs.Bool("allow-send", false, "let the agent send messages, react and mark chats read")
	token := fs.String("mcp-token", envOr("WAME_MCP_TOKEN", ""), "act as this API token (its scopes and chat allowlist)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if b.Client.Store.ID == nil {
		return errors.New("not logged in: run 'wame login' first")
	}
	p := auth.Principal{Name: "mcp", Scopes: []auth.Scope{auth.Read}}
	if *allowSend {
		p.Scopes = append(p.Scopes, auth.Send)
	}
	if *token != "" {
		var err error
		if p, err = (&auth.Authenticator{Store: b.Store}).Authenticate(*token); err != nil {
			return fmt.Errorf("-mcp-token: %w", err)
		}
	}
	if err := b.Start(ctx); err != nil {
		return err
	}
	if err := waitLoggedIn(ctx, b, 30*time.Second); err != nil {
		return err
	}
	b.Log.Info("MCP server on stdio", "as", p.Name, "scopes", p.Scopes)
	return mcpserver.New(b, version, &p).Run(ctx, &mcp.StdioTransport{})
}

// backupCommand copies the database with VACUUM INTO over a read-only connection. It never calls
// bot.New, which would upgrade the schema and connect to WhatsApp.
func backupCommand(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(usage) }
	db := fs.String("db", envOr("WAME_DB", defaultDB()), "")
	keep := fs.Int("keep", 0, "")
	_ = fs.Parse(args)
	if fs.NArg() > 1 || *keep < 0 {
		return errors.New("usage: wame backup [-db PATH] [-keep N] [PATH]")
	}
	dest, err := backupDest(*db, fs.Arg(0), time.Now())
	if err != nil {
		return err
	}
	size, err := store.Backup(context.Background(), *db, dest)
	if err != nil {
		return err
	}
	fmt.Printf("backed up %s to %s (%s)\n", *db, dest, byteSize(size))
	if *keep == 0 {
		return nil
	}
	removed, err := store.PruneBackups(filepath.Dir(dest), *keep)
	for _, p := range removed {
		fmt.Println("removed old backup", p)
	}
	return err
}

// backupDest is where a backup goes: path itself, a new file inside path when it's a directory (or
// ends in a separator, which creates it), or next to the database when path is empty.
func backupDest(db, path string, now time.Time) (string, error) {
	name := store.BackupName(now)
	if path == "" {
		return filepath.Join(filepath.Dir(db), name), nil
	}
	if strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(filepath.Separator)) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", err
		}
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return filepath.Join(path, name), nil
	}
	return path, nil
}

func byteSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// adminCommand opens the admin panel for a server over its HTTP API; it never touches the local database.
// With no token set anywhere, it offers the saved profiles.
func adminCommand(args []string) error {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(usage) }
	var sf serverFlags
	sf.register(fs)
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (flags go right after 'admin')", fs.Arg(0))
	}
	target, profiles, err := sf.resolve()
	if err != nil {
		return err
	}
	o := admin.Options{Profiles: profiles, Insecure: sf.insecure, Copy: tui.CopyToClipboard, Bell: func() { _, _ = os.Stderr.WriteString("\a") }}
	if target.Token == "" && len(profiles) > 0 {
		return admin.Run(o)
	}
	if o.Client, err = target.client(); err != nil {
		return err
	}
	o.Profile = target.Name
	return admin.Run(o)
}

// serverFlags are the flags of the commands that talk to a running server over its HTTP API.
type serverFlags struct {
	server, token, profile string
	insecure               bool
}

func (s *serverFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&s.server, "server", "", "")
	fs.StringVar(&s.token, "token", "", "")
	fs.StringVar(&s.profile, "profile", "", "")
	fs.BoolVar(&s.insecure, "insecure", false, "")
}

// serverTarget is the server and token to use; Name is the profile's, when one was picked.
type serverTarget admin.Profile

func (t serverTarget) client() (*admin.Client, error) {
	c, err := admin.NewClient(t.Server, t.Token, t.Insecure)
	if err != nil && t.Name != "" {
		return nil, fmt.Errorf("%s: %w", t.Name, err)
	}
	return c, err
}

// resolve picks the server and token from -profile, or else flags, the environment, ./.env or
// ~/.config/wame/admin.env, in that order (admin.Resolve), and also returns the saved profiles.
func (s serverFlags) resolve() (serverTarget, []admin.Profile, error) {
	var profiles []admin.Profile
	dir := configDir()
	if dir != "" {
		var err error
		if profiles, err = admin.Profiles(filepath.Join(dir, "wame", "servers")); err != nil {
			return serverTarget{}, nil, err
		}
	}
	if s.profile != "" {
		i := slices.IndexFunc(profiles, func(p admin.Profile) bool { return p.Name == s.profile })
		if i < 0 {
			return serverTarget{}, nil, fmt.Errorf("no saved server %q in %s", s.profile, filepath.Join(dir, "wame", "servers"))
		}
		t := serverTarget(profiles[i])
		t.Insecure = t.Insecure || s.insecure
		return t, profiles, nil
	}
	sources := []admin.Source{
		{Name: "flags", Server: s.server, Token: s.token},
		{Name: "environment", Server: envOr("WAME_SERVER", ""), Token: envOr("WAME_TOKEN", "")},
	}
	files := []string{".env"}
	if dir != "" {
		files = append(files, filepath.Join(dir, "wame", "admin.env"))
	}
	for _, f := range files {
		src, err := admin.ReadEnvFile(f)
		if err != nil {
			return serverTarget{}, nil, err
		}
		sources = append(sources, src)
	}
	srv, tok, fileInsecure, err := admin.Resolve(sources)
	if err != nil {
		return serverTarget{}, nil, err
	}
	return serverTarget{Source: admin.Source{Server: srv, Token: tok, Insecure: s.insecure || fileInsecure}}, profiles, nil
}
