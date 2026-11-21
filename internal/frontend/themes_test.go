package frontend

import (
	"strings"
	"testing"
	"time"
)

// Two websites: the default store uses Acme/default in en_US, the French
// store (website 2) uses it in fr_FR, a Hyvä child theme is installed but
// unused, and the admin theme is Magento/backend.
const dbOut = "theme\t1\tMagento/backend\tadminhtml:NULL\n" +
	"theme\t2\tMagento/blank\tfrontend:NULL\n" +
	"theme\t3\tMagento/luma\tfrontend:2\n" +
	"theme\t4\tAcme/default\tfrontend:3\n" +
	"theme\t5\tHyva/default\tfrontend:NULL\n" +
	"theme\t6\tAcme/hyva\tfrontend:5\n" +
	"cfg\tdefault:0\tdesign/theme/theme_id\t4\n" +
	"cfg\tdefault:0\tgeneral/locale/code\ten_US\n" +
	"cfg\twebsites:2\tgeneral/locale/code\tfr_FR\n" +
	"store\t1\t1\tdefault\n" +
	"store\t2\t2\tfrench\n"

func TestParseResolvesScopes(t *testing.T) {
	d := Parse(dbOut)
	var acme, hyva Theme
	for _, th := range d.Themes {
		switch th.Path {
		case "Acme/default":
			acme = th
		case "Acme/hyva":
			hyva = th
		}
	}
	if strings.Join(acme.UsedBy["en_US"], ",") != "store default" || strings.Join(acme.UsedBy["fr_FR"], ",") != "store french" {
		t.Fatalf("UsedBy: %v", acme.UsedBy)
	}
	if !hyva.Hyva {
		t.Fatal("a child of Hyva/default is a Hyvä theme")
	}
	if d.Themes[len(d.Themes)-1].Area != "adminhtml" {
		t.Fatal("frontend themes sort first")
	}
}

func TestCandidatesKeepExistingAndKeysUnique(t *testing.T) {
	current := ParseLocalThemes(`module.exports = {
    acme: { area: 'frontend', name: 'Acme/default', locale: 'en_US', files: ['css/styles-m', 'css/custom'], dsl: 'less' },
    legacy: { area: 'frontend', name: 'Old/theme', locale: 'en_US', files: ['css/styles-m'], dsl: 'less' }
};`)
	cs := Candidates(Parse(dbOut), current)
	by := map[string]Candidate{}
	keys := map[string]bool{}
	for _, c := range cs {
		by[c.Name+"|"+c.Locale] = c
		if keys[c.Key] {
			t.Fatalf("duplicate key %q", c.Key)
		}
		keys[c.Key] = true
	}
	en, fr := by["Acme/default|en_US"], by["Acme/default|fr_FR"]
	if en.Key != "acme" || strings.Join(en.Files, ",") != "css/styles-m,css/custom" || !en.Selected {
		t.Fatalf("existing entry not kept: %+v", en.Entry)
	}
	if fr.Key != "default" || !fr.Selected {
		t.Fatalf("fr store entry: %+v", fr)
	}
	if by["Acme/hyva|en_US"].Selected {
		t.Fatal("unused Hyvä theme must not be preselected")
	}
	if by["Magento/luma|en_US"].Selected || len(by["Magento/luma|en_US"].Files) != 4 {
		t.Fatal("unused stock theme: unselected, with stock files")
	}
	if l := by["Old/theme|en_US"]; !l.Selected || !l.InFile {
		t.Fatal("entries not in the database are kept")
	}
}

func TestRenderRoundTrip(t *testing.T) {
	es := []Entry{
		{Key: "acme", Area: "frontend", Name: "Acme/default", Locale: "en_US", Files: []string{"css/styles-m", "css/styles-l"}, DSL: "less"},
		{Key: "acme-fr", Area: "frontend", Name: "Acme/default", Locale: "fr_FR", Files: []string{"css/styles-m"}},
	}
	js := Render(es, time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	if !strings.Contains(js, "'acme-fr': {") || !strings.Contains(js, "module.exports = {") {
		t.Fatal(js)
	}
	back := ParseLocalThemes(js)
	if len(back) != 2 || back[1].Key != "acme-fr" || back[1].DSL != "less" || back[0].Files[1] != "css/styles-l" {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestParseSetupGruntOutput(t *testing.T) {
	// What bin/setup-grunt writes (util.inspect, single line).
	js := `"use strict"; module.exports = { blank: { area: 'frontend', name: 'Magento/blank', locale: 'en_US', files: [ 'css/styles-m', 'css/styles-l', 'css/email', 'css/email-inline' ], dsl: 'less' }, backend: { area: 'adminhtml', name: 'Magento/backend', locale: 'en_US', files: [ 'css/styles-old', 'css/styles' ], dsl: 'less' }, mytheme: { area: 'frontend', name: 'Acme/default', locale: 'en_US', files: [ 'css/styles-m', 'css/styles-l' ], dsl: 'less' } }`
	es := ParseLocalThemes(js)
	if strings.Join(Keys(es), ",") != "blank,backend,mytheme" {
		t.Fatalf("keys %v", Keys(es))
	}
}
