package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"lazymage/internal/config"
	"lazymage/internal/update"
)

// Self-update: a daily background check (on by default, see Settings) shows
// a notice in the header; U (or the palette) downloads the new release,
// verifies its checksum, replaces the binary, and offers to restart.

type updateCheckedMsg struct {
	rel    update.Release
	err    error
	manual bool
}

type updateInstalledMsg struct {
	tag string
	err error
}

func updateCachePath() string { return filepath.Join(config.Dir(), "update-check.json") }

// autoUpdateCheck runs at startup: from the cache if it is under a day old,
// otherwise from GitHub in the background. Failures are silent.
func (a *App) autoUpdateCheck() tea.Cmd {
	if a.cfg == nil || !a.cfg.CheckUpdates() || a.opts.Version == "test" {
		return nil
	}
	if c := update.ReadCache(updateCachePath()); time.Since(c.Checked) < 24*time.Hour {
		if update.Newer(c.Latest, a.opts.Version) {
			a.updateAvail = update.Release{Tag: c.Latest, URL: c.URL}
		}
		return nil
	}
	return checkUpdateCmd(false)
}

func checkUpdateCmd(manual bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rel, err := update.Latest(ctx)
		if err == nil {
			update.WriteCache(updateCachePath(), update.Cache{Checked: time.Now(), Latest: rel.Tag, URL: rel.URL})
		}
		return updateCheckedMsg{rel: rel, err: err, manual: manual}
	}
}

func (a *App) startUpdate() tea.Cmd {
	if a.updating {
		return a.setStatus("an update is already in progress", false)
	}
	return tea.Batch(a.setStatus("checking for a new lazymage release…", false), checkUpdateCmd(true))
}

func (a *App) onUpdateChecked(m updateCheckedMsg) tea.Cmd {
	if m.err != nil {
		if m.manual {
			return a.setStatus("update check failed: "+m.err.Error(), true)
		}
		a.log.printf("update check: %v", m.err)
		return nil
	}
	newer := update.Newer(m.rel.Tag, a.opts.Version)
	if newer {
		a.updateAvail = m.rel
	} else {
		a.updateAvail = update.Release{}
	}
	if !m.manual {
		return nil
	}
	exe, err := update.Executable()
	if err != nil {
		return a.setStatus("cannot find the lazymage binary: "+err.Error(), true)
	}
	body := fmt.Sprintf("Installs %s for %s over\n%s\n\nThe download is checked against the release's checksums.txt first.\nRelease notes: %s",
		m.rel.Tag, update.Asset(), exe, m.rel.URL)
	switch {
	case newer:
		a.push(newConfirm(fmt.Sprintf("Update lazymage %s → %s?", a.opts.Version, m.rel.Tag), body, "Update",
			func() tea.Cmd { return a.installUpdate(m.rel.Tag, exe) }))
	case a.opts.Version == "dev":
		a.push(newConfirm("Replace this source build?", "This lazymage was built from source (version dev).\n\n"+body, "Install "+m.rel.Tag,
			func() tea.Cmd { return a.installUpdate(m.rel.Tag, exe) }))
	default:
		return a.setStatus("lazymage "+a.opts.Version+" is up to date", false)
	}
	return nil
}

func (a *App) installUpdate(tag, exe string) tea.Cmd {
	a.updating = true
	status := a.setStatus("downloading lazymage "+tag+"…", false)
	return tea.Batch(status, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return updateInstalledMsg{tag: tag, err: update.Install(ctx, tag, exe)}
	})
}

func (a *App) onUpdateInstalled(m updateInstalledMsg) tea.Cmd {
	a.updating = false
	if m.err != nil {
		a.push(&messageOverlay{title: "Update failed", body: m.err.Error() + "\n\n" + sFaint.Render("enter / esc to close")})
		return a.setStatus("update failed", true)
	}
	a.updateAvail = update.Release{}
	a.log.printf("updated to %s", m.tag)
	exe, _ := update.Executable()
	a.push(newChoice("lazymage "+m.tag+" installed", "The new version starts the next time you open lazymage.", []choice{
		{label: "Restart now", key: "r", run: func() tea.Cmd {
			a.restartExe = exe
			return a.quit()
		}},
		{label: "Later", key: "l"},
	}))
	return a.setStatus("installed lazymage "+m.tag, false)
}

// RestartPath is set when the user chose "Restart now" after an update; main
// re-executes it once the TUI has shut down.
func (a *App) RestartPath() string { return a.restartExe }
