package tui

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/williamtcastro/wame/internal/bot"
	"github.com/williamtcastro/wame/internal/store"
	"github.com/williamtcastro/wame/internal/termui"
)

// suggest refreshes the input's completions for the command being typed and resizes the layout
// when the hint bar appears or disappears.
func (m *model) suggest() tea.Cmd {
	v := m.input.Value()
	var list []string
	var load tea.Cmd
	cmd, arg, hasArg := strings.Cut(v, " ")
	switch {
	case !strings.HasPrefix(v, "/"):
	case !hasArg:
		for _, c := range tuiCommands {
			if c.args != "" {
				c.name += " "
			}
			list = append(list, c.name)
		}
	case cmd == "/to":
		seen := map[string]bool{}
		add := func(target string) {
			if target != "" && !seen[target] {
				seen[target] = true
				list = append(list, "/to "+target)
			}
		}
		for _, c := range m.chats {
			add(chatTarget(c))
		}
		for _, name := range m.contactNames {
			add(name)
		}
		if m.contactNames == nil && !m.loadingNames {
			m.loadingNames = true
			load = func() tea.Msg {
				contacts, _ := m.bot.Contacts(context.Background(), bot.ContactQuery{})
				names := make([]string, 0, len(contacts))
				for _, c := range contacts {
					names = append(names, safeLine(c.Display))
				}
				return contactNamesMsg(names)
			}
		}
	case cmd == "/name" && termui.IsPersonChat(m.active) && ownableName(m.activeName):
		list = []string{"/name " + m.activeName}
	case (cmd == "/send" || cmd == "/sendat") && !strings.Contains(arg, " "):
		for _, c := range m.chats {
			target := userOf(c.JID)
			if c.IsGroup {
				target = c.JID
			}
			list = append(list, cmd+" "+target+" ")
		}
	case cmd == "/file" && !strings.Contains(arg, " "):
		dir, _ := filepath.Split(arg)
		names, ok := m.dirCache[dir]
		if ok {
			list = pathSuggestions(arg, names)
		} else if m.dirLoading != dir {
			m.dirLoading = dir
			load = func() tea.Msg { return dirMsg{dir, readDirNames(dir)} }
		}
	}
	m.input.ShowSuggestions = true // SetSuggestions only refreshes the matches while this is on
	m.input.SetSuggestions(list)
	m.input.ShowSuggestions = len(list) > 0
	if shown := m.contextBar() != ""; shown != m.barShown {
		m.barShown = shown
		m.layout()
		m.render(m.sel < 0)
	}
	return load
}

// chatTarget is what /to needs to reopen a chat: its contact name if it has one, else its number or group JID.
func chatTarget(c store.Chat) string {
	switch {
	case c.IsGroup:
		return c.JID
	case strings.HasPrefix(c.Name, "~"), strings.HasPrefix(c.Name, "+"), c.Name == "Me":
		return userOf(c.JID)
	}
	return c.Name
}

// ownableName reports whether a chat's name is worth offering to /name: not a push name, a number or a bare JID.
func ownableName(name string) bool {
	return name != "" && !strings.HasPrefix(name, "~") && !strings.HasPrefix(name, "+") && !strings.Contains(name, "@")
}

// readDirNames lists a directory for /file completion (directories end in "/"); it runs off the UI loop.
func readDirNames(dir string) []string {
	entries, _ := os.ReadDir(cmp.Or(termui.ExpandHome(dir), "."))
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name()+"/")
		} else {
			names = append(names, e.Name())
		}
	}
	return names
}

func pathSuggestions(typed string, names []string) []string {
	dir, base := filepath.Split(typed)
	var out []string
	for _, name := range names {
		if strings.Contains(name, " ") || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) || !strings.HasPrefix(name, base) {
			continue
		}
		out = append(out, "/file "+dir+name)
		if len(out) == 200 {
			break
		}
	}
	return out
}
