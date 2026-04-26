package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/doomsday/agent/internal/state"
)

func main() {
	st, err := state.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "state load:", err)
		os.Exit(1)
	}

	for _, svc := range st.Components.PACActiveOnServices {
		cmd := exec.Command("networksetup", "-setautoproxystate", svc, "off")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	}

	_ = st.MarkComponent("pac_active_on_services", []string{})

	// Send SIGTERM to daemon by unloading launchd
	_ = exec.Command("launchctl", "unload",
		os.ExpandEnv("$HOME/Library/LaunchAgents/com.doomsday.agent.plist"),
	).Run()

	fmt.Println("Doomsday disabled. Re-enable with: rm ~/.doomsday/disable_marker && launchctl kickstart -k com.doomsday.agent")
}
