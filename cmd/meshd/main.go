// meshd — AmneziaWG mesh node daemon + CLI.
//
// Subcommand'ы:
//   meshd init    — инициализирует новую mesh-сеть (первая нода = seed)
//   meshd join    — подключается к существующей сети через seed
//   meshd status  — печатает текущее состояние (peers, wg-handshakes)
//   meshd version — версия бинарника
package main

import (
	"fmt"
	"os"
)

var (
	// version — выставляется через -ldflags="-X main.version=..." при сборке.
	version = "dev"

	// Глобальный --state-file флаг, по умолчанию /etc/meshd/state.json.
	stateFile string
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "version", "-v", "--version":
		fmt.Println(version)
		return
	case "init":
		err = cmdInit(args)
	case "join":
		err = cmdJoin(args)
	case "run":
		err = cmdRun(args)
	case "serve":
		err = cmdServe(args)
	case "status":
		err = cmdStatus(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "meshd %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `meshd %s — AmneziaWG mesh node

Commands:
  init     Initialize a new mesh network (first node, becomes seed)
  join     Join an existing mesh network via a seed
  run      Run meshd daemon: bring up wg-interface + (if seed) bootstrap listener
  serve    Run only bootstrap-listener (without wg-device; for debug/testing)
  status   Print current state and peer info
  version  Print version

Run 'meshd <command> -h' for command-specific flags.
`, version)
}
