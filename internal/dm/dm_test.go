package dm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		mode := int64(0o644)
		if strings.Contains(name, "/bin/") {
			mode = 0o755
		}
		tw.WriteHeader(&tar.Header{Name: "docker-magento-53.3.0/" + name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestFetchPlanApply(t *testing.T) {
	tgz := tarball(t, map[string]string{
		"compose/compose.yaml":        "## Version 53.3.0\nservices: {}\n",
		"compose/bin/start":           "#!/bin/sh\necho new\n",
		"compose/bin/brand-new":       "#!/bin/sh\n",
		"compose/env/db.env":          "MYSQL_PASSWORD=magento\nMYSQL_NEW=1\n",
		"compose/.vscode/launch.json": "{}",
		"compose/../evil":             "x",
		"CHANGELOG.md":                "# Changelog\n\n## [Unreleased]\n\n## [53.3.0] - 2026-11-20\n- new thing\n\n## [53.2.0] - 2026-10-28\n- old thing\n",
		"images/nginx/Dockerfile":     "ignored",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/markshust/docker-magento/releases/latest":
			w.Write([]byte(`{"tag_name":"53.3.0"}`))
		case "/markshust/docker-magento/tar.gz/refs/tags/53.3.0":
			w.Write(tgz)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	APIBase, CodeloadURL = srv.URL, srv.URL

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "bin"), 0o755)
	os.MkdirAll(filepath.Join(root, "env"), 0o755)
	os.MkdirAll(filepath.Join(root, ".vscode"), 0o755)
	os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("## Version 53.2.0\nservices: {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "bin/start"), []byte("#!/bin/sh\necho old\n"), 0o755)
	os.WriteFile(filepath.Join(root, "env/db.env"), []byte("MYSQL_PASSWORD=secret\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".vscode/launch.json"), []byte(`{"mine":1}`), 0o644)

	if v := LocalVersion(root); v != "53.2.0" {
		t.Fatalf("local version %q", v)
	}
	tag, err := LatestTag(context.Background())
	if err != nil || tag != "53.3.0" {
		t.Fatal(tag, err)
	}
	u, err := Fetch(context.Background(), tag)
	if err != nil {
		t.Fatal(err)
	}
	if u.Version != "53.3.0" {
		t.Fatalf("upstream version %q", u.Version)
	}
	if _, ok := u.Files["../evil"]; ok {
		t.Fatal("path traversal accepted")
	}
	if cl := u.ChangesSince("53.2.0", 0); !strings.Contains(cl, "new thing") || strings.Contains(cl, "old thing") {
		t.Fatalf("changelog: %q", cl)
	}
	kinds := map[string]ChangeKind{}
	for _, c := range u.Plan(root) {
		kinds[c.Path] = c.Kind
	}
	want := map[string]ChangeKind{"compose.yaml": Changed, "bin/start": Changed, "bin/brand-new": New, "env/db.env": Merged, ".vscode/launch.json": Kept}
	for p, k := range want {
		if kinds[p] != k {
			t.Errorf("%s: kind %d, want %d", p, kinds[p], k)
		}
	}
	bak := filepath.Join(root, "backups", "dm")
	if err := u.Apply(root, bak, u.Plan(root)); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(root, "env/db.env"))
	if !strings.Contains(string(env), "MYSQL_PASSWORD=secret") || !strings.Contains(string(env), "MYSQL_NEW=1") || strings.Contains(string(env), "=magento") {
		t.Fatalf("env merge: %s", env)
	}
	if b, _ := os.ReadFile(filepath.Join(bak, "bin/start")); !strings.Contains(string(b), "old") {
		t.Fatal("old bin/start not backed up")
	}
	if st, _ := os.Stat(filepath.Join(root, "bin/brand-new")); st == nil || st.Mode()&0o111 == 0 {
		t.Fatal("new bin script must be executable")
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".vscode/launch.json")); string(b) != `{"mine":1}` {
		t.Fatal(".vscode must be kept")
	}
	if LocalVersion(root) != "53.3.0" {
		t.Fatal("version after apply")
	}
}

func TestPHPTags(t *testing.T) {
	rows := ParseVersions("# c\nmagento\t2.4.9\t8.5-fpm-1\nmagento\t2.4.8\t8.4-fpm-4\nmagento\t2.4.7\t8.3-fpm-8\nmageos\t1\t8.3-fpm-8\n")
	tags := PHPTags(rows)
	if strings.Join(tags, ",") != "8.2-fpm-10,8.3-fpm-8,8.4-fpm-4,8.5-fpm-1" {
		t.Fatalf("tags %v", tags)
	}
	if r := Recommended(rows, "magento", "2.4.8-p2"); r != "8.4-fpm-4" {
		t.Fatalf("recommended %q", r)
	}
	if r := Recommended(rows, "magento", "2.4.81"); r != "" {
		t.Fatalf("prefix rule: %q", r)
	}
	if XdebugTag("8.4-fpm-3") != "8.4-fpm-xdebug-3" || PHPMinor("8.4-fpm-xdebug-3") != "8.4" {
		t.Fatal("tag helpers")
	}
	if r, tg := SplitImage("localhost:5000/markoshust/magento-php:8.4-fpm-3"); r != "localhost:5000/markoshust/magento-php" || tg != "8.4-fpm-3" {
		t.Fatal(r, tg)
	}
	imgs, err := ServiceImages(`{"services":{"phpfpm":{"image":"markoshust/magento-php:8.3-fpm-8"}}}`)
	if err != nil || imgs["phpfpm"] != "markoshust/magento-php:8.3-fpm-8" {
		t.Fatal(imgs, err)
	}
}

func TestOverride(t *testing.T) {
	root := t.TempDir()
	orig := "# mine\nservices:\n  db:\n    ports: !override\n      - \"3307:3306\"\n"
	os.WriteFile(filepath.Join(root, OverrideFile), []byte(orig), 0o644)
	o, err := LoadOverride(root)
	if err != nil {
		t.Fatal(err)
	}
	o.SetImage("phpfpm", "markoshust/magento-php:8.3-fpm-8")
	o.AddVolume("app", StoreMapVolume)
	o.AddVolume("app", StoreMapVolume)
	if err := o.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, OverrideFile))
	s := string(b)
	for _, want := range []string{"# mine", "!override", "3307:3306", "image: markoshust/magento-php:8.3-fpm-8", StoreMapVolume} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Count(s, StoreMapVolume) != 1 {
		t.Error("volume added twice")
	}
	o2, _ := LoadOverride(root)
	if o2.Image("phpfpm") != "markoshust/magento-php:8.3-fpm-8" || !o2.HasVolume("app", StoreMapVolume) {
		t.Error("reload")
	}
	RestoreOverride(root, o.Original())
	if b, _ := os.ReadFile(filepath.Join(root, OverrideFile)); string(b) != orig {
		t.Error("restore")
	}
	// No file before: restore removes it.
	root2 := t.TempDir()
	o3, _ := LoadOverride(root2)
	o3.SetImage("phpfpm", "x:1")
	o3.Save()
	RestoreOverride(root2, o3.Original())
	if _, err := os.Stat(filepath.Join(root2, OverrideFile)); err == nil {
		t.Error("restore of a missing file must remove it")
	}
}

func TestMultistore(t *testing.T) {
	sites := []Site{
		{StoreCode: "default", WebsiteCode: "base", Default: true, Host: "shop.test"},
		{StoreCode: "fr", WebsiteCode: "base", Host: "shop.test"},
		{StoreCode: "de", WebsiteCode: "base", Host: "de.shop.test"},
		{StoreCode: "b2b_en", WebsiteCode: "b2b", Host: "b2b.test"},
		{StoreCode: "b2b_fr", WebsiteCode: "b2b", Host: "b2b.test"},
		{StoreCode: "x", WebsiteCode: "other", Host: "shop.test"},
	}
	ms, problems := Mappings(sites)
	got := []string{}
	for _, m := range ms {
		got = append(got, m.Host+"="+m.Code+"/"+m.Type)
	}
	if strings.Join(got, " ") != "b2b.test=b2b/website de.shop.test=de/store" {
		t.Fatalf("mappings %v", got)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "store x") {
		t.Fatalf("problems %v", problems)
	}
	conf := StoreMapConf(ms)
	for _, want := range []string{"map $host $MAGE_RUN_CODE", "de.shop.test de;", "b2b.test website;", `default "";`, "default store;"} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf missing %q:\n%s", want, conf)
		}
	}

	sample := "location ~ ^/(index|get|static|errors/report|errors/404|errors/503|health_check)\\.php$ {\n    try_files $uri =404;\n    fastcgi_pass   fastcgi_backend;\n    fastcgi_param  SCRIPT_FILENAME  $document_root$fastcgi_script_name;\n    include        fastcgi_params;\n}\n"
	out, err := AddRunParams(sample)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "include        fastcgi_params;\n    "+runParamsMarker+"\n    fastcgi_param  MAGE_RUN_CODE $MAGE_RUN_CODE;") {
		t.Fatalf("patched:\n%s", out)
	}
	again, _ := AddRunParams(out)
	if again != out {
		t.Fatal("not idempotent")
	}
	if _, err := AddRunParams("server {}"); err == nil {
		t.Fatal("expected an error without the PHP location")
	}

	hosts := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(hosts, []byte("127.0.0.1 localhost\n127.0.0.1 ::1 shop.test # x\n#127.0.0.1 b2b.test\n"), 0o644)
	if !HostsHas(hosts, "shop.test") || HostsHas(hosts, "b2b.test") || HostsHas(hosts, "127.0.0.1") {
		t.Fatal("HostsHas")
	}
	if !CertCovers([]string{"*.shop.test"}, "de.shop.test") || CertCovers([]string{"shop.test"}, "de.shop.test") {
		t.Fatal("CertCovers")
	}
}
