package netdiagram

import (
	"regexp"
	"strings"
)

// table is one CLI table: the header line just above its dashes, and its rows.
type table struct {
	header string
	rows   [][]string
}

// columnTables splits CLI output into its tables, using the dashed line under each header for the
// column positions ("---------  ------  ---"). Each cell runs from its dashes' start to the next
// column's start, so a cell may be empty; the last column takes the rest of the line. A blank
// line ends a table.
func columnTables(out string) []table {
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	var tables []table
	var starts []int
	var cur *table
	for i, line := range lines {
		if isDashLine(line) {
			starts = dashStarts(line)
			header := ""
			if i > 0 {
				header = strings.TrimSpace(lines[i-1])
			}
			tables = append(tables, table{header: header})
			cur = &tables[len(tables)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if strings.TrimSpace(line) == "" {
			if len(cur.rows) > 0 {
				cur = nil
			}
			continue
		}
		row := make([]string, len(starts))
		for c, st := range starts {
			end := len(line)
			if c+1 < len(starts) && starts[c+1] < end {
				end = starts[c+1]
			}
			if st < len(line) && st < end {
				row[c] = strings.TrimSpace(line[st:end])
			}
		}
		cur.rows = append(cur.rows, row)
	}
	return tables
}

// columnTable is the rows of the first table in out.
func columnTable(out string) [][]string {
	if t := columnTables(out); len(t) > 0 {
		return t[0].rows
	}
	return nil
}

// tableUnder is the rows of the first table whose header line starts with word.
func tableUnder(out, word string) [][]string {
	for _, t := range columnTables(out) {
		if strings.HasPrefix(strings.ToLower(t.header), strings.ToLower(word)) {
			return t.rows
		}
	}
	return nil
}

var dashLine = regexp.MustCompile(`^\s*-{2,}(\s+-{2,})*\s*$`)

func isDashLine(s string) bool { return dashLine.MatchString(s) }

func dashStarts(s string) []int {
	var st []int
	for i := 0; i < len(s); i++ {
		if s[i] == '-' && (i == 0 || s[i-1] == ' ') {
			st = append(st, i)
		}
	}
	return st
}

// dotted reads "Key.......... value" lines into a map keyed by the key text.
func dotted(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		i := strings.Index(line, "..")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(strings.TrimLeft(line[i:], ". …"))
		if key != "" {
			m[key] = val
		}
	}
	return m
}

// colonFields reads "Key: value" lines into a map (first occurrence wins).
func colonFields(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		i := strings.Index(line, ":")
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		if _, seen := m[key]; !seen {
			m[key] = strings.TrimSpace(line[i+1:])
		}
	}
	return m
}
