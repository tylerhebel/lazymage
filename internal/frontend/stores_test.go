package frontend

import (
	"strings"
	"testing"
)

func TestParseStores(t *testing.T) {
	rows := [][]string{
		{"store", "1", "1:1:1", "default:Default Store View"},
		{"store", "2", "1:1:1", "fr:Fran\\tçais"},
		{"store", "3", "2:2:0", "b2b:B2B"},
		{"website", "1", "base:1:1", "Main"},
		{"website", "2", "b2b:2:0", "B2B"},
		{"group", "1", "1", "Main"},
		{"group", "2", "3", "B2B"},
		{"theme", "5", "Hyva/default", "frontend:NULL"},
		{"theme", "6", "Acme/hyva", "frontend:5"},
		{"theme", "3", "Magento/luma", "frontend:NULL"},
		{"cfg", "default:0", "web/unsecure/base_url", "http://shop.test/"},
		{"cfg", "default:0", "web/secure/base_url", "{{unsecure_base_url}}"},
		{"cfg", "default:0", "web/secure/use_in_frontend", "1"},
		{"cfg", "default:0", "design/theme/theme_id", "6"},
		{"cfg", "stores:2", "general/locale/code", "fr_FR"},
		{"cfg", "stores:2", "web/url/use_store", "1"},
		{"cfg", "websites:2", "web/secure/base_url", "https://b2b.test"},
		{"cfg", "websites:2", "design/theme/theme_id", "3"},
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	ss := ParseStores(b.String())
	if len(ss) != 3 {
		t.Fatalf("got %d stores", len(ss))
	}
	want := []struct {
		code, url, theme, locale string
		hyva, def, active        bool
	}{
		{"default", "http://shop.test/", "Acme/hyva", "en_US", true, true, true},
		{"fr", "http://shop.test/fr/", "Acme/hyva", "fr_FR", true, false, true},
		{"b2b", "https://b2b.test/", "Magento/luma", "en_US", false, false, false},
	}
	for i, w := range want {
		s := ss[i]
		if s.Code != w.code || s.URL != w.url || s.Theme != w.theme || s.Locale != w.locale || s.ThemeHyva != w.hyva || s.Default != w.def || s.Active != w.active {
			t.Errorf("store %d = %+v, want %+v", i, s, w)
		}
	}
	if ss[1].Name != "Fran\tçais" {
		t.Errorf("name not unescaped: %q", ss[1].Name)
	}
	if h := Host("https://b2b.test:8443/x/"); h != "b2b.test" {
		t.Errorf("Host = %q", h)
	}
}
