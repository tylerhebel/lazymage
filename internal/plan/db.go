package plan

import (
	"fmt"
	"strings"

	"lazymage/internal/config"
)

// Import, queries and the client shell use docker-magento's bin/mysql where
// the project has it (Hybrid and bin-only modes): `bin/mysql < dump.sql` and
// `bin/mysql -N -B -e "SQL"` both take its non-TTY branch (bin/clinotty).
//
// Dumps never use bin/mysqldump: it runs n98-magerun2 through bin/cli, which
// allocates a TTY (CRLF line endings in the output) and prints a download
// notice on stdout the first time — both would end up inside the .sql file.
//
// Otherwise, database commands run inside the database container with `sh -c`. The
// script reads the credentials from the container's own environment (set by
// docker-magento's env/db.env), so the password never reaches lazymage, the
// displayed command, or history. It is passed to the client through
// MYSQL_PWD rather than -p, so it is not in the container's process list
// either. MariaDB images ship mariadb / mariadb-dump; MySQL images ship
// mysql / mysqldump — the script uses whichever exists.
const dbCreds = `u="${MYSQL_USER:-${MARIADB_USER:-root}}"; d="${MYSQL_DATABASE:-$MARIADB_DATABASE}"; ` +
	`if [ "$u" = root ]; then p="${MYSQL_ROOT_PASSWORD:-$MARIADB_ROOT_PASSWORD}"; else p="${MYSQL_PASSWORD:-$MARIADB_PASSWORD}"; fi; ` +
	`[ -n "$d" ] || { echo "lazymage: MYSQL_DATABASE is not set in the database container" >&2; exit 2; }; export MYSQL_PWD="$p"; `

const (
	dbClient = `c=$(command -v mariadb || command -v mysql) || { echo "lazymage: no mariadb or mysql client in the database container" >&2; exit 127; }; `
	dbDumper = `c=$(command -v mariadb-dump || command -v mysqldump) || { echo "lazymage: no mariadb-dump or mysqldump in the database container" >&2; exit 127; }; `
)

// DBDefaultService is the docker-magento database service name.
const DBDefaultService = "db"

// DBServiceFrom picks the database service from the compose service list.
func DBServiceFrom(services []string) string {
	for _, want := range []string{"db", "mysql", "mariadb", "database"} {
		for _, s := range services {
			if s == want {
				return s
			}
		}
	}
	return ""
}

func (b Builder) dbExec(tty bool, script string, args ...string) []string {
	svc := b.DBService
	if svc == "" {
		svc = DBDefaultService
	}
	argv := []string{"exec"}
	if !tty {
		argv = append(argv, "-T")
	}
	argv = append(argv, svc, "sh", "-c", script, "lazymage")
	return b.Compose(append(argv, args...)...)
}

// DBDump writes the whole database as SQL to stdout.
func (b Builder) DBDump() []string {
	return b.dbExec(false, dbCreds+dbDumper+
		`exec "$c" -u"$u" --single-transaction --quick --routines --triggers --no-tablespaces "$d"`)
}

// useBinMysql reports whether to go through the project's bin/mysql.
func (b Builder) useBinMysql() bool { return b.Mode != config.ModeDockerOnly && b.has("mysql") }

// DBImport reads SQL from stdin into the database (`bin/mysql < file`).
func (b Builder) DBImport() []string {
	if b.useBinMysql() {
		return []string{"bin/mysql"}
	}
	return b.dbExec(false, dbCreds+dbClient+`exec "$c" -u"$u" "$d"`)
}

// DBQuery runs one SQL statement, printing tab-separated rows without headers.
func (b Builder) DBQuery(sql string) []string {
	if b.useBinMysql() {
		return []string{"bin/mysql", "-N", "-B", "-e", sql}
	}
	return b.dbExec(false, dbCreds+dbClient+`exec "$c" -u"$u" -N -B -e "$1" "$d"`, sql)
}

// DBShell opens an interactive client; bin/mysql where the project has it.
func (b Builder) DBShell() []string {
	if b.useBinMysql() {
		return []string{"bin/mysql"}
	}
	return b.dbExec(true, dbCreds+dbClient+`exec "$c" -u"$u" "$d"`)
}

// SQLString quotes s as a MySQL string literal.
func SQLString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\x1a", `\Z`)
	return "'" + r.Replace(s) + "'"
}

// sqlLike quotes s as a LIKE pattern matching s anywhere in the value.
func sqlLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return SQLString("%" + r.Replace(s) + "%")
}

// SQLIdent quotes a table name.
func SQLIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// BaseURLRows lists the store base URLs (scope, scope_id, path, value).
func BaseURLRows(prefix string) string {
	return fmt.Sprintf("SELECT scope, scope_id, path, value FROM %s WHERE path LIKE 'web/%%/base%%url' ORDER BY scope, scope_id, path",
		SQLIdent(prefix+"core_config_data"))
}

// URLReplacePreview lists the config rows whose value contains from.
func URLReplacePreview(prefix, from string) string {
	return fmt.Sprintf("SELECT scope, scope_id, path, value FROM %s WHERE value LIKE %s ORDER BY path, scope, scope_id",
		SQLIdent(prefix+"core_config_data"), sqlLike(from))
}

// URLReplace rewrites from to to in every config value that contains it.
func URLReplace(prefix, from, to string) string {
	return fmt.Sprintf("UPDATE %s SET value = REPLACE(value, %s, %s) WHERE value LIKE %s; SELECT ROW_COUNT()",
		SQLIdent(prefix+"core_config_data"), SQLString(from), SQLString(to), sqlLike(from))
}
