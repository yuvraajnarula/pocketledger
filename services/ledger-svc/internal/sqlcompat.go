package internal

import (
	"database/sql"
	"fmt"
	"strings"
)

func queryForDriver(db *sql.DB, query string) string {
	if isPostgresDriver(db) {
		return replaceQuestionMarks(query)
	}
	return query
}

func isPostgresDriver(db *sql.DB) bool {
	if db == nil {
		return false
	}
	name := strings.ToLower(fmt.Sprintf("%T", db.Driver()))
	return strings.Contains(name, "postgres") || strings.Contains(name, "pq")
}

func replaceQuestionMarks(query string) string {
	var b strings.Builder
	count := 0
	for _, r := range query {
		if r == '?' {
			count++
			b.WriteString(fmt.Sprintf("$%d", count))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func currentTimestampExpr(db *sql.DB) string {
	if isPostgresDriver(db) {
		return "NOW()"
	}
	return "datetime('now')"
}
