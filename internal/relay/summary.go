// SPDX-License-Identifier: Apache-2.0

package relay

import (
	"fmt"
	"strings"
)

// RenderMarkdown formats a run summary as a GitHub-flavored markdown table,
// suitable for $GITHUB_STEP_SUMMARY or console output.
func RenderMarkdown(sum Summary) string {
	var b strings.Builder
	b.WriteString("### Ech0-Relay run\n\n")
	if len(sum.Results) == 0 {
		b.WriteString("_No syncs ran._\n")
		return b.String()
	}
	b.WriteString("| Sync | Channel | Found | Posted | Failed | Deleted | Cursor | Status |\n")
	b.WriteString("|------|---------|------:|-------:|-------:|--------:|--------|--------|\n")
	for _, r := range sum.Results {
		cursor := fmt.Sprintf("%d → %d", r.OldCursor, r.NewCursor)
		if r.Seeded {
			cursor += " (seed)"
		}
		status := "✅ ok"
		if r.Err != nil {
			status = "❌ " + oneLine(r.Err.Error())
		} else if r.Failed > 0 {
			status = "⚠️ partial"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %s | %s |\n",
			r.Name, r.Channel, r.Found, r.Posted, r.Failed, r.Deleted, cursor, status)
	}
	if sum.HardError {
		b.WriteString("\n> ⚠️ One or more syncs failed — see logs.\n")
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
