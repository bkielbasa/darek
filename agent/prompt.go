package agent

import (
	"fmt"
	"strings"
	"time"
)

func BuildSystemPrompt(today time.Time, toolNames []string) string {
	var sb strings.Builder
	sb.WriteString("You are darek, a personal assistant CLI.\n")
	fmt.Fprintf(&sb, "Today is %s.\n\n", today.Format("2006-01-02 (Monday)"))
	sb.WriteString("Be concise. Prefer plain prose to bullet lists unless listing items.\n")
	sb.WriteString("When the user shares a fact you should remember across sessions, call memory.save.\n")
	sb.WriteString("When recalling personal context, call memory.recall.\n")
	sb.WriteString("\nAvailable tools: ")
	sb.WriteString(strings.Join(toolNames, ", "))
	sb.WriteString(".\n")
	return sb.String()
}
