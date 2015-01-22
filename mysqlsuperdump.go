// Copyright 2012-2013 Herbert G. Fischer. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// mysqlsuperdump is a program that generates MySQL partial and secure dumps
// With it you can specify the WHERE clause for each table being dumped and
// also value replacements for each table.column.
package main

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"github.com/hgfischer/goconf"
	_ "github.com/hgfischer/mysql"
	"io"
	"os"
	"strings"
)

var (
	configFile         string
	dsn                string
	extendedInsertRows int
	tableMap 		   = make(map[string]string, 0)
	whereMap           = make(map[string]string, 0)
	selectMap          = make(map[string]map[string]string, 0)
	filterMap          = make(map[string]string, 0)
	output             = flag.String("o", "", "Output path. Default is stdout")
	verboseFlag        = flag.Bool("v", false, "Enable printing status information")
	debugFlag          = flag.Bool("d", false, "Enable printing of debug information")
	verbose            Bool
	debug              Bool
	useTableLock       bool
)

type Bool bool

func (b Bool) Printf(s string, a ...interface{}) {
	if b {
		fmt.Fprintf(os.Stderr, s, a...)
	}
}

func Query(db *sql.DB, q string) (*sql.Rows, error) {
	debug.Printf("Query: %s\n", q)
	return db.Query(q)
}

func QueryRow(db *sql.DB, q string) *sql.Row {
	debug.Printf("QueryRow: %s\n", q)
	return db.QueryRow(q)
}

func ExecQuery(db *sql.DB, q string) (sql.Result, error) {
	debug.Printf("ExecQuery: %s\n", q)
	return db.Exec(q)
}

// MAIN
func main() {
	var err error
	var w io.Writer

	parseCommandLine()
	readConfigFile()

	verbose.Printf("> Using table locks: %t\n", useTableLock)

	verbose.Printf("> Connecting to MySQL database at %s\n", dsn)
	db, err := sql.Open("mysql", dsn)
	checkError(err)
	defer db.Close()

	if *output == "" {
		w = os.Stdout
	} else {
		w, err = os.Create(*output)
		checkError(err)
	}

	fmt.Fprintf(w, "SET NAMES utf8;\n")
	fmt.Fprintf(w, "SET FOREIGN_KEY_CHECKS = 0;\n")

	verbose.Printf("> Getting table list...\n")
	tables := getTables(db)

	for _, table := range tables {
		if filterMap[table] != "ignore" {
			skipData := filterMap[table] == "nodata"
			if ! skipData && useTableLock {
				verbose.Printf("> Locking table %s...\n", tableMap[table])
				lockTable(db, table)
				flushTable(db, table)
			}
			verbose.Printf("> Dumping structure for table %s...\n", tableMap[table])
			dumpCreateTable(w, db, table)
			if ! skipData {
				verbose.Printf("> Dumping data for table %s...\n", tableMap[table])
				dumpTableData(w, db, table)
				if useTableLock {
					verbose.Printf("> Unlocking table %s...\n", tableMap[table])
					unlockTables(db)
				}
			}
		}
	}

	fmt.Fprintf(w, "SET FOREIGN_KEY_CHECKS = 1;\n")
}

// Check if err is not nil. If it's not, prints error and exit program
func checkError(err error) {
	if err != nil {
		panic(err)
	}
}

// Print command line help and exit application
func printUsage() {
	fmt.Fprintf(os.Stderr,
		"Usage: mysqlsuperdump [flags] [path to config file]\n")
	fmt.Fprintf(os.Stderr, "\nFlags:\n")
	flag.PrintDefaults()
	os.Exit(1)
}

// Parse command line options and parameters
func parseCommandLine() {
	flag.Usage = printUsage
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Error: Missing parameters\n")
		flag.Usage()
	}
	configFile = flag.Arg(0)
	verbose = Bool(*verboseFlag)
	debug = Bool(*debugFlag)
	return
}

// Read config file, inclusing wheres and selects maps
func readConfigFile() {
	cfg, err := conf.ReadConfigFile(configFile)
	checkError(err)
	dsn, err = cfg.GetString("mysql", "dsn")
	checkError(err)
	extendedInsertRows, err = cfg.GetInt("mysql", "extended_insert_rows")
	checkError(err)
	useTableLock, err = cfg.GetBool("mysql", "use_table_lock") // return false on error

	selects, err := cfg.GetOptions("select")
	checkError(err)
	for _, tablecol := range selects {
		split := strings.Split(tablecol, ".")
		table := split[0]
		column := split[1]
		if selectMap[table] == nil {
			selectMap[table] = make(map[string]string, 0)
		}
		selectMap[table][column], err = cfg.GetString("select", tablecol)
		checkError(err)
	}

	wheres, err := cfg.GetOptions("where")
	checkError(err)
	for _, table := range wheres {
		whereMap[table], err = cfg.GetString("where", table)
		checkError(err)
	}

	filters, err := cfg.GetOptions("filter")
	checkError(err)
	for _, table := range filters {
		filterMap[table], err = cfg.GetString("filter", table)
		checkError(err)
	}
}

// Lock the table (read only)
func lockTable(db *sql.DB, table string) (sql.Result, error) {
	return ExecQuery(db, fmt.Sprintf("LOCK TABLES `%s` READ", tableMap[table]))
}

// Flush table to ensure that the all active index pages are written to disk
func flushTable(db *sql.DB, table string) (sql.Result, error) {
	return ExecQuery(db, fmt.Sprintf("FLUSH TABLES `%s`", tableMap[table]))
}

// Release the global read locks
func unlockTables(db *sql.DB) (sql.Result, error) {
	return ExecQuery(db, fmt.Sprintf("UNLOCK TABLES"))
}

// Get list of existing tables in database
func getTables(db *sql.DB) (tables []string) {
	tables = make([]string, 0)
	rows, err := Query(db, "SHOW FULL TABLES")
	checkError(err)
	for rows.Next() {
		var tableName string
		var tableType string
		err = rows.Scan(&tableName, &tableType)
		checkError(err)
		if tableType == "BASE TABLE" {
			tableMap[strings.ToLower(tableName)] = tableName;
			tables = append(tables, strings.ToLower(tableName))
		}
		// TODO feature to export views as well
	}
	checkError(rows.Err())
	return
}

// Dump the script to create the table
func dumpCreateTable(w io.Writer, db *sql.DB, table string) {
	fmt.Fprintf(w, "\n--\n")
	fmt.Fprintf(w, "-- Structure for table `%s`\n", tableMap[table])
	fmt.Fprintf(w, "--\n\n")
	fmt.Fprintf(w, "DROP TABLE IF EXISTS `%s`;\n", tableMap[table])
	row := QueryRow(db, fmt.Sprintf("SHOW CREATE TABLE `%s`", tableMap[table]))
	
	var tname, ddl string
	err := row.Scan(&tname, &ddl)
	checkError(err)
	fmt.Fprintf(w, "%s;\n", ddl)
}

// Get the column list for the SELECT, applying the select map
// from config file.
func getColumnListForSelect(db *sql.DB, table string) string {
	rows, err := Query(db, fmt.Sprintf("SELECT * FROM `%s` LIMIT 1", tableMap[table]))
	checkError(err)
	columns, err := rows.Columns()
	checkError(err)
	for k, column := range columns {
		replacement, ok := selectMap[table][column]
		if ok {
			columns[k] = fmt.Sprintf("%s AS `%s`", replacement, column)
		} else {
			columns[k] = fmt.Sprintf("`%s`", column)
		}
	}
	return strings.Join(columns, ", ")
}

// Get the complete SELECT query to fetch data from database
func getSelectQueryFor(db *sql.DB, table string) (query string) {
	columns := getColumnListForSelect(db, table)
	query = fmt.Sprintf("SELECT %s FROM `%s`", columns, tableMap[table])
	where, ok := whereMap[table]
	if ok {
		query = fmt.Sprintf("%s WHERE %s", query, where)
	}
	return
}

// Get the number of rows the select will return
func getSelectCountQueryFor(db *sql.DB, table string) (query string) {
	query = fmt.Sprintf("SELECT COUNT(*) FROM `%s`", tableMap[table])
	where, ok := whereMap[table]
	if ok {
		query = fmt.Sprintf("%s WHERE %s", query, where)
	}
	return
}

// Get the table data
func dumpTableData(w io.Writer, db *sql.DB, table string) {
	fmt.Fprintf(w, "\n--\n-- Data for table `%s`", tableMap[table])

	var count uint64
	row := QueryRow(db, getSelectCountQueryFor(db, table))
	err := row.Scan(&count)
	checkError(err)
	fmt.Fprintf(w, " -- %d rows\n--\n\n", count)

	if count == 0 {
		return // Avoid table lock if empty
	}

	fmt.Fprintf(w, "LOCK TABLES `%s` WRITE;\n", tableMap[table])
	query := fmt.Sprintf("INSERT INTO `%s` VALUES", tableMap[table])
	data := make([]string, 0)

	selectQuery := getSelectQueryFor(db, table)
	rows, err := Query(db, selectQuery)
	checkError(err)
	columns, err := rows.Columns()
	checkError(err)

	values := make([]*sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	for rows.Next() {
		err = rows.Scan(scanArgs...)
		checkError(err)

		vals := make([]string, 0)
		for _, col := range values {
			val := "NULL"
			if col != nil {
				val = fmt.Sprintf("'%s'", escape(string(*col)))
			}
			vals = append(vals, val)
		}

		data = append(data, fmt.Sprintf("( %s )", strings.Join(vals, ", ")))
		if len(data) >= 100 {
			fmt.Fprintf(w, "%s\n%s;\n", query, strings.Join(data, ",\n"))
			data = make([]string, 0)
		}
	}

	if len(data) > 0 {
		fmt.Fprintf(w, "%s\n%s;\n", query, strings.Join(data, ",\n"))
	}

	fmt.Fprintf(w, "\nUNLOCK TABLES;\n")
}

func escape(str string) string {
	var esc string
	var buf bytes.Buffer
	last := 0
	for i, c := range str {
		switch c {
		case 0:
			esc = `\0`
		case '\n':
			esc = `\n`
		case '\r':
			esc = `\r`
		case '\\':
			esc = `\\`
		case '\'':
			esc = `\'`
		case '"':
			esc = `\"`
		case '\032':
			esc = `\Z`
		default:
			continue
		}
		io.WriteString(&buf, str[last:i])
		io.WriteString(&buf, esc)
		last = i + 1
	}
	io.WriteString(&buf, str[last:])
	return buf.String()
}
