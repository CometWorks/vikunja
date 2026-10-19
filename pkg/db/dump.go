// Vikunja is a to-do list application to facilitate your life.
// Copyright 2018-present Vikunja and contributors. All rights reserved.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package db

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"code.vikunja.io/api/pkg/log"

	"xorm.io/xorm/schemas"
)

var validTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Formats a dumped time value may be in, depending on the database and driver the dump was created with.
var dumpTimeFormats = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}

// parseDumpTime parses a time string from a dump. Values without a timezone are
// interpreted as UTC since that's what dumps are written in.
func parseDumpTime(value string) (t time.Time, err error) {
	for _, format := range dumpTimeFormats {
		t, err = time.ParseInLocation(format, value, time.UTC)
		if err == nil {
			return t, nil
		}
	}
	return t, err
}

func validateTableName(table string) error {
	if !validTableName.MatchString(table) {
		return fmt.Errorf("invalid table name: %q", table)
	}
	return nil
}

// tableMetas returns the schema of every table in the database, keyed by table name.
func tableMetas() (map[string]*schemas.Table, error) {
	tables, err := x.DBMetas()
	if err != nil {
		return nil, err
	}
	metas := make(map[string]*schemas.Table, len(tables))
	for _, table := range tables {
		metas[table.Name] = table
	}
	return metas, nil
}

// Dump dumps all Vikunja database tables
func Dump() (data map[string][]byte, err error) {
	tableNames := RegisteredTableNames()

	metas, err := tableMetas()
	if err != nil {
		return nil, err
	}

	data = make(map[string][]byte, len(tableNames))
	for _, name := range tableNames {
		data[name], err = dumpTable(name, metas[name])
		if err != nil {
			return nil, err
		}
	}

	return
}

func dumpTable(name string, meta *schemas.Table) ([]byte, error) {
	// xorm scans nullable int and text columns into sql.NullInt64 and sql.NullString and
	// returns 0 or "" for NULL, so every nullable column gets an IS NULL flag next to it.
	nullFlags := map[string]string{}
	selects := []string{"*"}
	for _, col := range meta.Columns() {
		if !col.Nullable {
			continue
		}
		flag := fmt.Sprintf("vikunja_dump_is_null_%d", len(nullFlags))
		nullFlags[flag] = col.Name
		selects = append(selects, x.Quote(col.Name)+" IS NULL AS "+flag)
	}

	entries := []map[string]interface{}{}
	if err := x.Table(name).Select(strings.Join(selects, ", ")).Find(&entries); err != nil {
		return nil, err
	}

	for _, entry := range entries {
		for flag, colName := range nullFlags {
			// The flag comes back as a bool (postgres), an int (mysql) or a string (sqlite).
			if isNull := fmt.Sprint(entry[flag]); isNull == "true" || isNull == "1" {
				entry[colName] = nil
			}
			delete(entry, flag)
		}
	}

	// xorm hands back column types it has no scan type for as raw bytes, which json.Marshal
	// would write as base64. On Postgres that hits every double precision and json column.
	for _, entry := range entries {
		for colName, value := range entry {
			raw, is := value.([]byte)
			if !is {
				continue
			}
			col := meta.GetColumn(colName)
			if col == nil || col.SQLType.IsBlob() {
				continue
			}
			if col.SQLType.IsNumeric() {
				entry[colName] = json.Number(raw)
				continue
			}
			entry[colName] = string(raw)
		}
	}

	return json.Marshal(entries)
}

// Restore restores a table with all its entries
func Restore(table string, contents []map[string]interface{}) (err error) {
	if err := validateTableName(table); err != nil {
		return err
	}

	if _, err := x.IsTableExist(table); err != nil {
		return err
	}

	metas, err := tableMetas()
	if err != nil {
		return err
	}

	metaForCurrentTable := metas[table]
	if metaForCurrentTable == nil {
		log.Fatalf("Could not find table definition for table %s", table)
	}

	for _, content := range contents {
		for colName, value := range content {
			col := metaForCurrentTable.GetColumn(colName)
			if col == nil {
				log.Warningf("Column %s does not exist in table %s, dropping it from the restored data", colName, table)
				delete(content, colName)
				continue
			}

			// SQLite and MySQL dump bools as 0/1, which pgx refuses to bind to a bool column.
			if col.SQLType.IsBool() {
				if num, is := value.(float64); is {
					content[colName] = num != 0
				}
				continue
			}

			strVal, is := value.(string)
			if !is || !col.SQLType.IsTime() {
				continue
			}

			// Date fields might get restored as 0001-01-01 from null dates. This can have unintended side-effects like
			// users being scheduled for deletion after a restore.
			// To avoid this, we set these dates to nil so that they'll end up as null in the db.
			if strVal == "" || strings.HasPrefix(strVal, "0001-") {
				content[colName] = nil
				continue
			}

			// Dumps contain dates as RFC3339 strings, which MySQL and MariaDB reject ("Incorrect
			// datetime value"). Convert to time.Time and let the driver format it for the target db.
			t, err := parseDumpTime(strVal)
			if err != nil {
				return fmt.Errorf("could not parse time value %q for column %s in table %s: %w", strVal, colName, table, err)
			}
			content[colName] = t
		}

		if _, err := x.Table(table).Insert(content); err != nil {
			return err
		}
	}

	// Tables with a text id (sessions) or without an id column have no sequence to reset.
	if autoIncr := metaForCurrentTable.AutoIncrement; Type() == schemas.POSTGRES && autoIncr != "" {
		_, err = x.Query(`SELECT setval(pg_get_serial_sequence(?, ?), COALESCE((SELECT MAX(`+x.Quote(autoIncr)+`) FROM `+x.Quote(table)+`), 1))`, table, autoIncr)
		if err != nil {
			log.Warningf("Could not reset the %s sequence of table %s: %s", autoIncr, table, err)
			err = nil
		}
	}

	return
}

// RestoreAndTruncate removes all content from the table before restoring it from the contents map
func RestoreAndTruncate(table string, contents []map[string]interface{}) (err error) {
	if err := validateTableName(table); err != nil {
		return err
	}

	if _, err := x.IsTableExist(table); err != nil {
		return err
	}

	if x.Dialect().URI().DBType == schemas.SQLITE {
		if _, err := x.Query(`DELETE FROM "` + table + `"`); err != nil {
			return err
		}
	} else {
		if _, err := x.Query("TRUNCATE TABLE " + x.Quote(table)); err != nil {
			return err
		}
	}

	return Restore(table, contents)
}

// TruncateAllTables deletes all data from every registered Vikunja table.
// Used by e2e tests to ensure a clean database state before each test.
func TruncateAllTables() error {
	for _, name := range RegisteredTableNames() {
		if err := validateTableName(name); err != nil {
			return err
		}

		if x.Dialect().URI().DBType == schemas.SQLITE {
			if _, err := x.Query(`DELETE FROM "` + name + `"`); err != nil {
				return err
			}
		} else {
			if _, err := x.Query("TRUNCATE TABLE " + x.Quote(name)); err != nil {
				return err
			}
		}
	}
	return nil
}
