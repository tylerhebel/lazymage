package ui

import (
	"sort"
	"strings"

	"lazymage/internal/config"
)

// Action IDs shared by the keymap, the palette, and the help overlay.
const (
	aStackStart      = "stack.start"
	aStackStop       = "stack.stop"
	aStackRestart    = "stack.restart"
	aStackRestartSvc = "stack.restartservice"
	aEngineStart     = "engine.start"
	aRefresh         = "refresh"

	aLogsAll    = "logs.all"
	aLogsPick   = "logs.pick"
	aLogsPause  = "logs.pause"
	aLogsFollow = "logs.follow"
	aLogsSearch = "logs.search"
	aLogsNext   = "logs.next"
	aLogsPrev   = "logs.prev"
	aLogsFilter = "logs.filter"
	aLogsClear  = "logs.clear"
	aLogsSave   = "logs.save"

	aMageFree     = "magento.freeform"
	aComposerFree = "composer.freeform"

	aShell      = "project.shell"
	aStorefront = "project.storefront"
	aAdmin      = "project.admin"
	aCopyURL    = "project.copyurl"
	aEditEnv    = "project.editenv"
	aEditConfig = "project.editconfig"

	aFEThemes       = "frontend.themes"
	aFEEditThemes   = "frontend.editthemes"
	aFESetupGrunt   = "frontend.setupgrunt"
	aFENpmInstall   = "frontend.npminstall"
	aFEGruntBuild   = "frontend.gruntbuild"
	aFEGruntLess    = "frontend.gruntless"
	aFEGruntWatch   = "frontend.gruntwatch"
	aFEGruntFree    = "frontend.grunt"
	aFEStaticDeploy = "frontend.staticdeploy"
	aFECleanStatic  = "frontend.cleanstatic"

	aCronStart    = "cron.start"
	aCronStop     = "cron.stop"
	aCronStatus   = "cron.status"
	aXdebugSetup  = "xdebug.setup"
	aXdebugCLI    = "xdebug.cli"
	aXdebugHelp   = "xdebug.help"
	aXdebugOn     = "xdebug.enable"
	aXdebugOff    = "xdebug.disable"
	aXdebugStatus = "xdebug.status"

	aDoctor     = "env.doctor"
	aPHPSwitch  = "env.phpswitch"
	aDMUpdate   = "env.dmupdate"
	aMultistore = "frontend.multistore"

	aDBDump       = "db.dump"
	aDBImport     = "db.import"
	aDBShell      = "db.shell"
	aDBReplaceURL = "db.replaceurl"

	aSettings   = "app.settings"
	aSetup      = "app.setup"
	aHelp       = "app.help"
	aNewCustom  = "app.newcustom"
	aManage     = "app.manage"
	aQuit       = "app.quit"
	aSwitchProj = "app.switchproject"
	aUpdate     = "app.update"
	aPalette    = "app.palette"
	aNextTab    = "nav.nexttab"
	aPrevTab    = "nav.prevtab"
	aTab1       = "nav.tab1"
	aTab2       = "nav.tab2"
	aTab3       = "nav.tab3"
	aTab4       = "nav.tab4"
	aTab5       = "nav.tab5"
	aCancelRun  = "run.cancel"
	aBgList     = "run.background"
	aViewOutput = "run.view"
)

// Binding is one entry of the keymap. Context scopes where it is active:
// "global" everywhere on the main screen, "logs" on the Logs tab, "commands"
// on the Magento / Composer tabs, or an overlay name. Display-only bindings
// (Action "") document keys handled inside a view, so help stays complete.
type Binding struct {
	Keys     []string
	Action   string
	Help     string
	Category string
	Context  string
}

// Keymap is the single source for dispatch, palette shortcut hints and help.
var Keymap = []Binding{
	{[]string{"j", "k", "↑", "↓"}, "", "Move within the focused view", "Navigation", "global"},
	{[]string{"tab"}, aNextTab, "Next tab", "Navigation", "global"},
	{[]string{"shift+tab"}, aPrevTab, "Previous tab", "Navigation", "global"},
	{[]string{"1"}, aTab1, "Stack tab (start / restart the environment)", "Navigation", "global"},
	{[]string{"2"}, aTab2, "Magento tab", "Navigation", "global"},
	{[]string{"3"}, aTab3, "Composer tab", "Navigation", "global"},
	{[]string{"4"}, aTab4, "Git tab (status of the repository holding src/)", "Navigation", "global"},
	{[]string{"5"}, aTab5, "Frontend tab (stores, themes, grunt, Tailwind)", "Navigation", "global"},
	{[]string{"enter"}, "", "Run / open the selected item", "Navigation", "global"},
	{[]string{"esc"}, "", "Back / close overlay", "Navigation", "global"},
	{[]string{"pgup", "pgdown"}, "", "Scroll output a page", "Navigation", "global"},

	{[]string{"x", "ctrl+p"}, aPalette, "Command palette", "App", "global"},
	{[]string{"?"}, aHelp, "Help (this screen)", "App", "global"},
	{[]string{"S"}, aSettings, "Settings", "App", "global"},
	{[]string{"P"}, aSwitchProj, "Switch to another project", "App", "global"},
	{[]string{"U"}, aUpdate, "Update lazymage to the newest release", "App", "global"},
	{[]string{"q"}, aQuit, "Quit (confirms if a command is running)", "App", "global"},
	{[]string{"ctrl+n"}, aNewCustom, "Create a custom command (in the palette)", "App", "palette"},

	{[]string{"D"}, aDoctor, "Doctor: check the environment and fix problems", "Stack", "global"},
	{[]string{"r"}, aRefresh, "Refresh header health and project facts", "Stack", "global"},
	{[]string{"ctrl+c"}, aCancelRun, "Cancel the running command (not the app)", "Stack", "global"},
	{[]string{"v"}, aViewOutput, "Show the last command's output", "Stack", "global"},
	{[]string{"B"}, aBgList, "Background tasks: view output, stop", "Stack", "global"},
	{[]string{"ctrl+b"}, "", "Move the running command to the background (in its output)", "Stack", "output"},

	{[]string{"a"}, aLogsAll, "Logs — all services", "Logs", "logs"},
	{[]string{"f"}, aLogsFollow, "Toggle log follow", "Logs", "logs"},
	{[]string{"space"}, aLogsPause, "Pause / resume the stream", "Logs", "logs"},
	{[]string{"/"}, aLogsSearch, "Search in the current view", "Logs", "logs"},
	{[]string{"n"}, aLogsNext, "Next match", "Logs", "logs"},
	{[]string{"N"}, aLogsPrev, "Previous match", "Logs", "logs"},

	{[]string{"i", ":"}, "", "Type a command (free-form) in the Magento / Composer tab", "Commands", "commands"},
	{[]string{"/"}, "", "Filter the command list", "Commands", "commands"},
	{[]string{"space"}, "", "Toggle favorite (Magento tab)", "Commands", "commands"},
	{[]string{"↑", "↓"}, "", "Command history (while typing a command)", "Commands", "commands"},

	{[]string{"s"}, aShell, "Shell into the PHP container", "Project", "global"},
	{[]string{"o"}, aStorefront, "Open storefront", "Project", "global"},
	{[]string{"O"}, aAdmin, "Open admin", "Project", "global"},
	{[]string{"y"}, aCopyURL, "Copy storefront URL", "Project", "global"},

	{[]string{"b"}, aFEGruntBuild, "Grunt: build a theme (clean, exec, less)", "Frontend", "global"},
	{[]string{"M"}, aMultistore, "Multistore setup: store domains, nginx run codes, hosts, SSL", "Frontend", "global"},
	{[]string{"enter", "o"}, "", "Open the selected store (y copies its URL)", "Frontend", "frontend"},
	{[]string{"enter"}, "", "Build the selected grunt theme (clean, exec, less) / Tailwind theme", "Frontend", "frontend"},
	{[]string{"l"}, "", "Compile LESS only for the selected theme", "Frontend", "frontend"},
	{[]string{"d"}, "", "Deploy static content for the selected theme", "Frontend", "frontend"},
	{[]string{"w"}, "", "Start / stop grunt watch (Tailwind watch on a Hyvä row)", "Frontend", "frontend"},
	{[]string{"i"}, "", "npm install for the selected Hyvä theme", "Frontend", "frontend"},
	{[]string{"R"}, "", "Reload stores from the database", "Frontend", "frontend"},
	{[]string{"m"}, aDBShell, "MySQL shell in the database container", "Database", "global"},

	{[]string{"enter", "d"}, "", "Show the selected file's diff", "Git", "git"},
	{[]string{"e"}, "", "Open the selected file in your editor", "Git", "git"},
	{[]string{"L"}, "", "Open lazygit (or tig) in the repository", "Git", "git"},

	{[]string{"enter"}, "", "Run the selected custom command", "Custom commands", "manage"},
	{[]string{"e"}, "", "Edit custom command", "Custom commands", "manage"},
	{[]string{"d"}, "", "Delete custom command (confirms)", "Custom commands", "manage"},
	{[]string{"space"}, "", "Pin / unpin custom command", "Custom commands", "manage"},
	{[]string{"c"}, "", "Duplicate custom command", "Custom commands", "manage"},
	{[]string{"n"}, "", "New custom command", "Custom commands", "manage"},
}

// reservedKeys cannot be assigned to custom commands: they are needed to
// operate the app itself.
var reservedKeys = map[string]bool{
	"esc": true, "enter": true, "ctrl+c": true, "q": true, "x": true, "ctrl+p": true, "?": true,
	"tab": true, "shift+tab": true, "j": true, "k": true, "up": true, "down": true, "ctrl+n": true,
	"backspace": true, "space": true, "pgup": true, "pgdown": true,
}

// actionForKey finds the built-in action bound to key in a context.
func actionForKey(key, ctx string) string {
	for _, b := range Keymap {
		if b.Action == "" || (b.Context != "global" && b.Context != ctx) {
			continue
		}
		for _, k := range b.Keys {
			if k == key {
				return b.Action
			}
		}
	}
	return ""
}

// shortcutFor returns the display keys for an action, e.g. "x / ctrl+p".
func shortcutFor(action string) string {
	for _, b := range Keymap {
		if b.Action == action {
			return strings.Join(b.Keys, " / ")
		}
	}
	return ""
}

// builtinUsing returns the help text of a built-in binding that uses key
// (in any context), or "".
func builtinUsing(key string) string {
	for _, b := range Keymap {
		if b.Action == "" {
			continue
		}
		for _, k := range b.Keys {
			if k == key {
				return b.Help
			}
		}
	}
	return ""
}

// helpSection is one group in the help overlay.
type helpSection struct {
	Title string
	Rows  [][2]string
}

// helpSections builds the help overlay from the live keymap plus custom
// command shortcuts — never a hand-maintained list.
func helpSections(customs []config.Scoped) []helpSection {
	order := []string{"Navigation", "App", "Stack", "Logs", "Commands", "Project", "Frontend", "Database", "Git", "Custom commands"}
	byCat := map[string]*helpSection{}
	for _, b := range Keymap {
		s := byCat[b.Category]
		if s == nil {
			s = &helpSection{Title: b.Category}
			byCat[b.Category] = s
		}
		s.Rows = append(s.Rows, [2]string{strings.Join(b.Keys, " / "), b.Help})
	}
	var out []helpSection
	for _, c := range order {
		if s := byCat[c]; s != nil {
			out = append(out, *s)
		}
	}
	var user helpSection
	user.Title = "Your shortcuts"
	for _, sc := range customs {
		if sc.Cmd.Shortcut == "" || sc.Overridden {
			continue
		}
		desc := sc.Cmd.Name + "  (" + scopeLabel(sc.Project) + ")"
		if b := builtinUsing(sc.Cmd.Shortcut); b != "" {
			desc += " — overrides: " + b
		}
		user.Rows = append(user.Rows, [2]string{sc.Cmd.Shortcut, desc})
	}
	sort.Slice(user.Rows, func(i, j int) bool { return user.Rows[i][0] < user.Rows[j][0] })
	if len(user.Rows) == 0 {
		user.Rows = [][2]string{{"", "none yet — assign one when creating a custom command (x, then ctrl+n)"}}
	}
	return append(out, user)
}

func scopeLabel(project bool) string {
	if project {
		return "project"
	}
	return "global"
}
