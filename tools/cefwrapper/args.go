package main

// Arguments returns a Windows command line without its program name,
// following the same rule as Windows: a space ends the program name unless it
// is inside double quotes.
func Arguments(cmd string) string {
	quoted := false
	i := 0
	for ; i < len(cmd); i++ {
		if cmd[i] == '"' {
			quoted = !quoted
		} else if cmd[i] == ' ' && !quoted {
			break
		}
	}
	for i < len(cmd) && cmd[i] == ' ' {
		i++
	}
	return cmd[i:]
}
