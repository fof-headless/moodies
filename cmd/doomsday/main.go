package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomsday/agent/internal/config"
	"github.com/doomsday/agent/internal/foreign"
	"github.com/doomsday/agent/internal/proxy"
	"github.com/doomsday/agent/internal/state"
	"github.com/doomsday/agent/internal/store"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{Use: "doomsday", Short: "Doomsday agent CLI"}

	root.AddCommand(installCmd(), uninstallCmd(), startCmd(), stopCmd(),
		statusCmd(), doctorCmd(), logsCmd(), configCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ---- install ----

func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install agent (cert, PAC, launchd)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Foreign proxy check
			hits := foreign.Scan()
			blocking := filterUs(hits, true)
			if len(blocking) > 0 {
				fmt.Fprintln(os.Stderr, foreign.FormatWarning(blocking))
				return fmt.Errorf("installation blocked: remove existing proxy config first")
			}
			infos := filterUs(hits, false)
			for _, p := range infos {
				fmt.Printf("[info] foreign proxy found: %s=%s at %s\n", p.Variable, p.Value, p.Source)
			}

			st, _ := state.Load()
			if st.InstallCompletedAt != nil {
				return fmt.Errorf("already installed; run 'doomsday uninstall' first")
			}

			now := time.Now()
			if st.InstallStartedAt == nil {
				st.InstallStartedAt = &now
				_ = st.Save()
			} else {
				fmt.Println("[install] Resuming partial installation...")
			}

			home, _ := os.UserHomeDir()

			if !st.Components.CACertTrusted {
				alreadyTrusted := func() bool {
					out, err := exec.Command("security", "find-certificate", "-c", "mitmproxy").Output()
					return err == nil && len(out) > 0
				}()
				if alreadyTrusted {
					fmt.Println("[install] CA cert already trusted, skipping...")
				} else {
					fmt.Println("[install] Generating and installing CA cert...")
					if err := proxy.GenerateCA(); err != nil {
						return fmt.Errorf("generate CA: %w", err)
					}
					if err := proxy.InstallCA(); err != nil {
						return fmt.Errorf("install CA: %w", err)
					}
				}
				_ = st.MarkComponent("ca_cert_trusted", true)
			}

			if !st.Components.PACFileWritten {
				fmt.Println("[install] Writing PAC file...")
				if _, err := proxy.WritePAC(8080); err != nil {
					return fmt.Errorf("write PAC: %w", err)
				}
				_ = st.MarkComponent("pac_file_written", true)
			}

			if len(st.Components.PACActiveOnServices) == 0 {
				fmt.Println("[install] Activating PAC on network services...")
				services := listNetworkServices()
				pacURL := "file://" + filepath.Join(home, ".doomsday", "proxy.pac")
				var active []string
				for _, svc := range services {
					if err := exec.Command("networksetup", "-setautoproxyurl", svc, pacURL).Run(); err == nil {
						active = append(active, svc)
					}
				}
				_ = st.MarkComponent("pac_active_on_services", active)
			}

			if !st.Components.SQLiteInitialized {
				fmt.Println("[install] Initializing SQLite buffer...")
				dbPath := filepath.Join(home, ".doomsday", "buffer.db")
				schemaPath := filepath.Join(filepath.Dir(os.Args[0]), "schema.sql")
				s, err := store.OpenWithSchema(dbPath, schemaPath)
				if err != nil {
					return fmt.Errorf("init sqlite: %w", err)
				}
				s.Close()
				_ = st.MarkComponent("sqlite_initialized", true)
			}

			// Copy sanitizer.py next to the binary into ~/.doomsday/
			sanitizerSrc := filepath.Join(filepath.Dir(os.Args[0]), "sanitizer", "sanitizer.py")
			sanitizerDst := proxy.SanitizerPath()
			if _, err := os.Stat(sanitizerSrc); err == nil {
				if data, err := os.ReadFile(sanitizerSrc); err == nil {
					_ = os.WriteFile(sanitizerDst, data, 0755)
				}
			}

			if !st.Components.LaunchdLoaded {
				fmt.Println("[install] Installing launchd service...")
				if err := writeLaunchdPlist(home); err != nil {
					return fmt.Errorf("write plist: %w", err)
				}
				plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.doomsday.agent.plist")
				if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
					return fmt.Errorf("launchctl load: %w", err)
				}
				_ = st.MarkComponent("launchd_loaded", true)
			}

			now2 := time.Now()
			st.InstallCompletedAt = &now2
			_ = st.Save()

			fmt.Println("\n✓ Doomsday installed. Run 'doomsday doctor' to verify.")
			return nil
		},
	}
}

// ---- uninstall ----

func uninstallCmd() *cobra.Command {
	force := false
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall agent",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, _ := os.UserHomeDir()
			st, _ := state.Load()

			now := time.Now()
			if st.LastUninstallAttemptedAt == nil || force {
				st.LastUninstallAttemptedAt = &now
				_ = st.Save()
			}

			// Reverse order
			plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.doomsday.agent.plist")
			_ = exec.Command("launchctl", "unload", plistPath).Run()
			_ = os.Remove(plistPath)
			_ = st.MarkComponent("launchd_loaded", false)

			for _, svc := range st.Components.PACActiveOnServices {
				_ = exec.Command("networksetup", "-setautoproxystate", svc, "off").Run()
			}
			_ = st.MarkComponent("pac_active_on_services", []string{})

			_ = os.Remove(filepath.Join(home, ".doomsday", "proxy.pac"))
			_ = st.MarkComponent("pac_file_written", false)

			_ = proxy.UninstallCA()
			_ = st.MarkComponent("ca_cert_trusted", false)

			_ = st.MarkComponent("sqlite_initialized", false)

			now2 := time.Now()
			st.LastUninstallCompletedAt = &now2
			_ = st.Delete()

			fmt.Println("✓ Doomsday uninstalled.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Force uninstall even if state is inconsistent")
	return cmd
}

// ---- start / stop ----

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start daemon via launchd",
		RunE: func(_ *cobra.Command, _ []string) error {
			return exec.Command("launchctl", "kickstart", "-k", "gui/"+uidStr()+"/com.doomsday.agent").Run()
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop daemon via launchd",
		RunE: func(_ *cobra.Command, _ []string) error {
			home, _ := os.UserHomeDir()
			return exec.Command("launchctl", "unload", filepath.Join(home, "Library", "LaunchAgents", "com.doomsday.agent.plist")).Run()
		},
	}
}

// ---- status ----

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show agent status",
		RunE: func(_ *cobra.Command, _ []string) error {
			home, _ := os.UserHomeDir()
			st, _ := state.Load()
			if st.InstallCompletedAt == nil {
				fmt.Println("Status: not installed")
				return nil
			}
			if st.DisabledAt != nil {
				fmt.Printf("Status: disabled at %s\n", st.DisabledAt.Format(time.RFC3339))
				return nil
			}

			dbPath := filepath.Join(home, ".doomsday", "buffer.db")
			s, err := store.OpenWithSchema(dbPath, "")
			queued := 0
			if err == nil {
				queued, _ = s.UnsyncedCount()
				s.Close()
			}

			hb := readHeartbeat(home)
			fmt.Printf("Status: running\nQueued events: %d\nLast heartbeat: %s\n", queued, hb)
			return nil
		},
	}
}

// ---- doctor ----

func doctorCmd() *cobra.Command {
	var fix bool
	var showForeign bool
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostics",
		RunE: func(_ *cobra.Command, _ []string) error {
			home, _ := os.UserHomeDir()

			type Check struct {
				Name           string
				Status         string
				Detail         string
				AutoFixable    bool
				RemediationCmd string
			}

			var checks []Check

			// 1. CA cert
			caOk := false
			out, err := exec.Command("security", "find-certificate", "-c", "mitmproxy").Output()
			if err == nil && len(out) > 0 {
				caOk = true
			}
			checks = append(checks, Check{"CA cert in keychain", boolStatus(caOk), proxy.MitmproxyCACertPath(), false, "doomsday install"})

			// 2. PAC file
			pacPath := filepath.Join(home, ".doomsday", "proxy.pac")
			_, pacErr := os.Stat(pacPath)
			checks = append(checks, Check{"PAC file exists", boolStatus(pacErr == nil), pacPath, false, "doomsday install"})

			// 3. PAC active
			proxyOut, _ := exec.Command("scutil", "--proxy").Output()
			pacActive := strings.Contains(string(proxyOut), "ProxyAutoConfigEnable") && strings.Contains(string(proxyOut), ": 1")
			checks = append(checks, Check{"PAC active", boolStatus(pacActive), "", false, "doomsday install"})

			// 4. launchd loaded
			launchOut, _ := exec.Command("launchctl", "list").Output()
			launchOk := strings.Contains(string(launchOut), "com.doomsday")
			checks = append(checks, Check{"launchd loaded", boolStatus(launchOk), "", false, "doomsday start"})

			// 5. mitmdump port listening
			conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:8080", time.Second)
			mitmOk := dialErr == nil
			if mitmOk { conn.Close() }
			checks = append(checks, Check{"mitmdump port 8080", boolStatus(mitmOk), "", true, "launchctl kickstart -k gui/" + uidStr() + "/com.doomsday.agent"})

			// 6. SQLite writable
			dbPath := filepath.Join(home, ".doomsday", "buffer.db")
			_, dbErr := os.Stat(dbPath)
			dbOk := dbErr == nil
			checks = append(checks, Check{"SQLite buffer exists", boolStatus(dbOk), dbPath, false, "doomsday install"})

			// 7. Daemon heartbeat freshness
			hbTime := readHeartbeatTime(home)
			hbFresh := hbTime != nil && time.Since(*hbTime) < 90*time.Second
			hbDetail := "no heartbeat file"
			if hbTime != nil {
				hbDetail = fmt.Sprintf("last: %s ago", time.Since(*hbTime).Truncate(time.Second))
			}
			checks = append(checks, Check{"Daemon heartbeat fresh", boolStatus(hbFresh), hbDetail, true, "launchctl kickstart -k gui/" + uidStr() + "/com.doomsday.agent"})

			// 8. Phantom proxy detection
			if pacActive && !mitmOk {
				checks = append(checks, Check{"Phantom proxy", "fail", "PAC active but port 8080 not listening — proxy traffic will fail", true, "doomsday doctor --fix"})
			}

			// 9. Foreign proxy detection
			foreignHits := foreign.Scan()
			for _, fp := range foreignHits {
				if fp.PointsToUs {
					checks = append(checks, Check{
						"Foreign proxy (ours)",
						"warn",
						fmt.Sprintf("Found %s=%s in %s\nThis was NOT set by Doomsday. If mitmproxy stops, tools will break.", fp.Variable, fp.Value, fp.Source),
						false,
						fmt.Sprintf("Manually remove %s from %s", fp.Variable, fp.Source),
					})
				}
			}

			// Print results
			if jsonOut {
				out2, _ := marshalChecks(checks)
				fmt.Println(string(out2))
				return nil
			}

			for _, c := range checks {
				icon := "✓"
				if c.Status == "fail" { icon = "✗" }
				if c.Status == "warn" { icon = "⚠" }
				fmt.Printf("%s %s", icon, c.Name)
				if c.Detail != "" { fmt.Printf(" — %s", c.Detail) }
				fmt.Println()
				if c.Status != "ok" && c.RemediationCmd != "" {
					fmt.Printf("  → Fix: %s\n", c.RemediationCmd)
				}
			}

			if showForeign {
				fmt.Println("\n--- Foreign proxy scan ---")
				if len(foreignHits) == 0 {
					fmt.Println("No foreign proxy config found.")
				}
				for _, fp := range foreignHits {
					fmt.Printf("  %s: %s=%s (ours=%v)\n", fp.Source, fp.Variable, fp.Value, fp.PointsToUs)
				}
			}

			if fix {
				for _, c := range checks {
					if c.AutoFixable && c.Status != "ok" {
						fmt.Printf("[fix] Running: %s\n", c.RemediationCmd)
						parts := strings.Fields(c.RemediationCmd)
						_ = exec.Command(parts[0], parts[1:]...).Run()
					}
				}
			}

			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Auto-apply safe fixes")
	cmd.Flags().BoolVar(&showForeign, "show-foreign-proxies", false, "Show foreign proxy scan output")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Machine-readable JSON output")
	return cmd
}

// ---- logs ----

func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Tail audit log",
		RunE: func(_ *cobra.Command, _ []string) error {
			home, _ := os.UserHomeDir()
			path := filepath.Join(home, ".doomsday", "audit.jsonl")
			cmd := exec.Command("tail", "-f", path)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
	}
}

// ---- config ----

func configCmd() *cobra.Command {
	cfg := &cobra.Command{Use: "config", Short: "Manage config"}
	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := config.Load()
			if err != nil { return err }
			if err := c.Set(args[0], args[1]); err != nil { return err }
			fmt.Printf("Set %s = %s\n", args[0], args[1])
			return nil
		},
	}
	cfg.AddCommand(set)
	return cfg
}

// ---- helpers ----

func boolStatus(ok bool) string {
	if ok { return "ok" }
	return "fail"
}

func uidStr() string {
	return fmt.Sprint(os.Getuid())
}

func listNetworkServices() []string {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil { return nil }
	var services []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "*") && !strings.HasPrefix(line, "An asterisk") {
			services = append(services, line)
		}
	}
	return services
}

func writeLaunchdPlist(home string) error {
	// Resolve the absolute path of the doomsday-daemon binary.
	// os.Args[0] may be relative ("./doomsday"), so we make it absolute first.
	selfAbs, _ := filepath.Abs(os.Args[0])
	daemonPath := filepath.Join(filepath.Dir(selfAbs), "doomsday-daemon")
	if _, err := os.Stat(daemonPath); err != nil {
		// Fall back to PATH lookup
		if p, err := exec.LookPath("doomsday-daemon"); err == nil {
			daemonPath = p
		}
	}
	// Inherit the current PATH so launchd can find mitmdump in /opt/homebrew/bin etc.
	currentPath := os.Getenv("PATH")
	if currentPath == "" {
		currentPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.doomsday.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>KeepAlive</key>
  <true/>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardErrorPath</key>
  <string>%s/.doomsday/daemon.log</string>
  <key>StandardOutPath</key>
  <string>%s/.doomsday/daemon.log</string>
</dict>
</plist>`, daemonPath, currentPath, home, home)

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0755); err != nil { return err }
	return os.WriteFile(filepath.Join(plistDir, "com.doomsday.agent.plist"), []byte(plist), 0644)
}

func readHeartbeat(home string) string {
	data, err := os.ReadFile(filepath.Join(home, ".doomsday", "heartbeat"))
	if err != nil { return "none" }
	return strings.TrimSpace(string(data))
}

func readHeartbeatTime(home string) *time.Time {
	data, err := os.ReadFile(filepath.Join(home, ".doomsday", "heartbeat"))
	if err != nil { return nil }
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil { return nil }
	return &t
}

func filterUs(hits []foreign.ForeignProxy, onlyUs bool) []foreign.ForeignProxy {
	var out []foreign.ForeignProxy
	for _, h := range hits {
		if h.PointsToUs == onlyUs {
			out = append(out, h)
		}
	}
	return out
}

func marshalChecks(checks any) ([]byte, error) {
	return json.MarshalIndent(checks, "", "  ")
}
