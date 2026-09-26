package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// writeZshCompletion derives the zsh completion from the registered flags,
// so descriptions always match -help.
func writeZshCompletion(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, "#compdef starawesome\n\n_starawesome() {\n  _arguments \\\n")
	fs.VisitAll(func(f *flag.Flag) {
		spec := "-" + f.Name + "[" + zshEscapeDesc(f.Usage) + "]"
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !b.IsBoolFlag() {
			spec += ":" + f.Name + ":"
		}
		fmt.Fprintf(w, "    %s \\\n", shellQuote(spec))
	})
	fmt.Fprint(w, `    '1:source:_files'
}

if [ "$funcstack[1]" = "_starawesome" ]; then
  _starawesome "$@"
else
  compdef _starawesome starawesome
fi
`)
}

func zshEscapeDesc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`, `:`, `\:`).Replace(s)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
