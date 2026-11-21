package frontend

import (
	"sort"
	"strings"
)

// Store is one store view with what the storefront shows for it, resolved
// through the config scopes (store → website → default) the way Magento does.
type Store struct {
	ID, Code, Name string
	WebsiteID      string
	Website        string // website code
	Active         bool
	Default        bool   // the default store view of the default website
	Theme          string // Vendor/theme, "" if unknown
	ThemeHyva      bool
	Locale         string
	URL            string // storefront URL, "" if no base URL is configured
}

// storeCfgPaths are the config paths StoresQuery reads.
var storeCfgPaths = []string{
	"web/secure/base_url", "web/unsecure/base_url", "web/secure/use_in_frontend",
	"web/url/use_store", "design/theme/theme_id", "general/locale/code",
}

// StoresQuery returns, in one round trip, the store views, websites, store
// groups, physical themes and the config that decides each store's URL,
// theme and locale.
func StoresQuery(prefix string) string {
	t := func(n string) string { return "`" + strings.ReplaceAll(prefix+n, "`", "``") + "`" }
	return "SELECT 'store', store_id, CONCAT(website_id, ':', group_id, ':', is_active), CONCAT(code, ':', name) FROM " + t("store") + " WHERE store_id > 0" +
		" UNION ALL SELECT 'website', website_id, CONCAT(code, ':', default_group_id, ':', is_default), name FROM " + t("store_website") + " WHERE website_id > 0" +
		" UNION ALL SELECT 'group', group_id, default_store_id, name FROM " + t("store_group") + " WHERE group_id > 0" +
		" UNION ALL SELECT 'theme', theme_id, theme_path, CONCAT(area, ':', IFNULL(parent_id, '')) FROM " + t("theme") + " WHERE type = 0" +
		" UNION ALL SELECT 'cfg', CONCAT(scope, ':', scope_id), path, value FROM " + t("core_config_data") +
		" WHERE path IN ('" + strings.Join(storeCfgPaths, "', '") + "')"
}

// ParseStores reads StoresQuery's tab-separated output.
func ParseStores(out string) []Store {
	type website struct {
		code, defGroup string
		isDefault      bool
	}
	var (
		stores   []Store
		groups   = map[string]string{} // group_id → default store_id
		websites = map[string]website{}
		themes   = map[string]Theme{}
		cfg      = map[string]map[string]string{}
		groupOf  = map[string]string{} // store_id → group_id
	)
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) != 4 {
			continue
		}
		switch f[0] {
		case "store":
			parts := strings.SplitN(f[2], ":", 3)
			code, name, _ := strings.Cut(f[3], ":")
			if len(parts) != 3 {
				continue
			}
			stores = append(stores, Store{ID: f[1], Code: code, Name: unescape(name), WebsiteID: parts[0], Active: parts[2] == "1"})
			groupOf[f[1]] = parts[1]
		case "website":
			parts := strings.SplitN(f[2], ":", 3)
			if len(parts) == 3 {
				websites[f[1]] = website{code: parts[0], defGroup: parts[1], isDefault: parts[2] == "1"}
			}
		case "group":
			groups[f[1]] = f[2]
		case "theme":
			area, parent, _ := strings.Cut(f[3], ":")
			if parent == "NULL" {
				parent = ""
			}
			themes[f[1]] = Theme{ID: f[1], Path: f[2], Area: area, Parent: parent}
		case "cfg":
			if cfg[f[2]] == nil {
				cfg[f[2]] = map[string]string{}
			}
			cfg[f[2]][f[1]] = unescape(f[3])
		}
	}
	hyva := func(id string) bool {
		for t, n := themes[id], 0; t.ID != "" && n < 20; t, n = themes[t.Parent], n+1 {
			if strings.HasPrefix(t.Path, "Hyva/") {
				return true
			}
		}
		return false
	}
	for i := range stores {
		s := &stores[i]
		resolve := func(path string) string {
			for _, k := range []string{"stores:" + s.ID, "websites:" + s.WebsiteID, "default:0"} {
				if v, ok := cfg[path][k]; ok && v != "NULL" {
					return v
				}
			}
			return ""
		}
		w := websites[s.WebsiteID]
		s.Website = w.code
		s.Default = w.isDefault && groups[w.defGroup] == s.ID
		if t, ok := themes[resolve("design/theme/theme_id")]; ok {
			s.Theme, s.ThemeHyva = t.Path, hyva(t.ID)
		}
		if s.Locale = resolve("general/locale/code"); s.Locale == "" {
			s.Locale = "en_US"
		}
		unsecure := resolve("web/unsecure/base_url")
		base := unsecure
		if resolve("web/secure/use_in_frontend") == "1" {
			if sec := resolve("web/secure/base_url"); sec != "" {
				base = strings.ReplaceAll(sec, "{{unsecure_base_url}}", unsecure)
			}
		}
		if base != "" && !strings.Contains(base, "{{") {
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			if resolve("web/url/use_store") == "1" {
				base += s.Code + "/"
			}
			s.URL = base
		}
	}
	sort.SliceStable(stores, func(i, j int) bool {
		if stores[i].Default != stores[j].Default {
			return stores[i].Default
		}
		if stores[i].WebsiteID != stores[j].WebsiteID {
			return numLess(stores[i].WebsiteID, stores[j].WebsiteID)
		}
		return numLess(stores[i].ID, stores[j].ID)
	})
	return stores
}

// unescape undoes mysql --batch escaping of special characters.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\t`, "\t", `\n`, "\n", `\\`, `\`, `\0`, "\x00").Replace(s)
}

// Host is the host name of a storefront URL ("" if it has none).
func Host(url string) string {
	_, rest, ok := strings.Cut(url, "://")
	if !ok {
		return ""
	}
	h, _, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}

// numLess orders numeric IDs as numbers ("2" before "10").
func numLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
