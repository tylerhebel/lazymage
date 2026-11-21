package ui

import (
	"sort"
	"strings"
	"unicode"
)

// mageCommand is a built-in Magento CLI entry.
type mageCommand struct {
	Cmd      string // e.g. "cache:flush"
	Args     []string
	Category string
	Desc     string
	Keywords string
	Confirm  bool
	Prompt   *argPrompt // ask for one argument before running
	Refresh  bool       // refresh cached facts afterwards
}

type argPrompt struct {
	Label    string
	Default  string
	Required bool
	Choices  []string
}

const (
	catStack    = "Stack"
	catLogs     = "Logs"
	catCache    = "Magento — cache & index"
	catSetup    = "Magento — setup"
	catComposer = "Composer"
	catFrontend = "Frontend"
	catDB       = "Database"
	catProject  = "Project"
	catApp      = "App"
)

var categoryOrder = []string{catStack, catLogs, catCache, catSetup, catComposer, catFrontend, catDB, catProject, catApp}

var mageCatalog = []mageCommand{
	{Cmd: "cache:flush", Category: catCache, Desc: "Flush all cache storage", Keywords: "flush clear cache purge"},
	{Cmd: "cache:clean", Category: catCache, Desc: "Clean cache types", Keywords: "clean clear cache"},
	{Cmd: "cache:status", Category: catCache, Desc: "Show cache type status", Keywords: "cache status"},
	{Cmd: "indexer:reindex", Category: catCache, Desc: "Reindex all indexers", Keywords: "reindex index indexer"},
	{Cmd: "indexer:status", Category: catCache, Desc: "Show indexer status", Keywords: "index indexer status reindex"},
	{Cmd: "setup:upgrade", Category: catSetup, Desc: "Upgrade schema and data", Keywords: "upgrade setup install modules", Refresh: true},
	{Cmd: "setup:di:compile", Category: catSetup, Desc: "Generate DI configuration", Keywords: "compile di generated"},
	{Cmd: "setup:static-content:deploy", Args: []string{"-f"}, Category: catSetup, Desc: "Deploy static view files", Keywords: "deploy static content scd assets"},
	{Cmd: "deploy:mode:show", Category: catSetup, Desc: "Show the application mode", Keywords: "deploy mode show developer production", Refresh: true},
	{Cmd: "deploy:mode:set", Category: catSetup, Desc: "Change the application mode", Keywords: "deploy mode set developer production default", Confirm: true, Refresh: true,
		Prompt: &argPrompt{Label: "Mode (developer, production, default)", Default: "developer", Required: true, Choices: []string{"developer", "production", "default"}}},
	{Cmd: "config:show", Category: catSetup, Desc: "Show configuration values", Keywords: "config show settings"},
	{Cmd: "module:status", Category: catSetup, Desc: "List enabled / disabled modules", Keywords: "module status modules extensions"},
	{Cmd: "cron:run", Category: catSetup, Desc: "Run cron jobs once", Keywords: "cron run jobs schedule"},
}

func findMage(cmd string) (mageCommand, bool) {
	for _, m := range mageCatalog {
		if m.Cmd == cmd {
			return m, true
		}
	}
	return mageCommand{}, false
}

// composerCommand is a built-in Composer entry.
type composerCommand struct {
	ID       string
	Title    string
	Args     []string
	Desc     string
	Keywords string
	Prompt   *argPrompt
}

var composerCatalog = []composerCommand{
	{ID: "composer.install", Title: "composer install", Args: []string{"install"}, Desc: "Install from composer.lock", Keywords: "install dependencies vendor"},
	{ID: "composer.update", Title: "composer update", Args: []string{"update"}, Desc: "Update dependencies", Keywords: "update upgrade dependencies"},
	{ID: "composer.require", Title: "composer require…", Args: []string{"require"}, Desc: "Add a package", Keywords: "require add package install",
		Prompt: &argPrompt{Label: "Package (vendor/name[:constraint])", Required: true}},
	{ID: "composer.outdated", Title: "composer show --outdated", Args: []string{"show", "--outdated"}, Desc: "List outdated packages", Keywords: "show outdated packages versions"},
	{ID: "composer.dumpautoload", Title: "composer dump-autoload", Args: []string{"dump-autoload"}, Desc: "Regenerate the autoloader", Keywords: "dump autoload dumpautoload classmap"},
}

// fuzzyScore returns a score > 0 when every rune of q appears in s in order.
// Contiguous runs, word starts, and a plain substring hit score higher.
func fuzzyScore(q, s string) int {
	if q == "" {
		return 1
	}
	ls, lq := strings.ToLower(s), strings.ToLower(q)
	score := 0
	if i := strings.Index(ls, lq); i >= 0 {
		score += 100 + 4*len(lq)
		if i == 0 || !isWordRune(rune(ls[i-1])) {
			score += 50
		}
		return score
	}
	sr, qr := []rune(ls), []rune(lq)
	qi, run, last := 0, 0, -2
	for i := 0; i < len(sr) && qi < len(qr); i++ {
		if sr[i] != qr[qi] {
			continue
		}
		score += 1
		if i == last+1 {
			run++
			score += 2 * run
		} else {
			run = 0
		}
		if i == 0 || !isWordRune(sr[i-1]) {
			score += 5
		}
		last = i
		qi++
	}
	if qi < len(qr) {
		return 0
	}
	return score
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// paletteEntry is one row of the palette.
type paletteEntry struct {
	ID       string
	Title    string
	Category string
	Command  string // exact command or action description
	Keywords string
	Shortcut string
	Disabled string // reason; empty means enabled
	Custom   bool
	HostSh   bool
	Pinned   bool
	order    int
	score    int
}

// rankEntries filters by query and sorts: (score when filtering), pinned /
// favorites, most recently used, category, catalog order.
func rankEntries(entries []paletteEntry, q string, recent []string) []paletteEntry {
	rank := map[string]int{}
	for i, id := range recent {
		rank[id] = len(recent) - i
	}
	catIdx := func(c string) int {
		for i, x := range categoryOrder {
			if x == c {
				return i
			}
		}
		return len(categoryOrder)
	}
	var out []paletteEntry
	q = strings.TrimSpace(q)
	for _, e := range entries {
		if q != "" {
			s := max(fuzzyScore(q, e.Title)*3, fuzzyScore(q, e.Keywords)*2, fuzzyScore(q, e.Command)*2, fuzzyScore(q, e.Category))
			if s == 0 {
				continue
			}
			e.score = s
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if q != "" && a.score != b.score {
			return a.score > b.score
		}
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if rank[a.ID] != rank[b.ID] {
			return rank[a.ID] > rank[b.ID]
		}
		if ca, cb := catIdx(a.Category), catIdx(b.Category); ca != cb {
			return ca < cb
		}
		return a.order < b.order
	})
	return out
}
