package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mobile-next/mobilecli/devices/devicekit"
)

// ── agent lifecycle ─────────────────────────────────────────

// probeInterval bounds how long the cached path trusts a bare TCP connect
// instead of proving the agent actually answers.
const probeInterval = 30 * time.Second

// probedFile's mtime records the last full liveness probe. Each iph call is a
// new process, so the timestamp lives on disk.
var probedFile = filepath.Join(devDir, "agent-probed")

func probedRecently() bool {
	fi, err := os.Stat(probedFile)
	if err != nil {
		return false
	}
	age := time.Since(fi.ModTime())
	return age >= 0 && age < probeInterval
}

func markProbed() {
	_ = os.MkdirAll(devDir, 0o755)
	_ = os.WriteFile(probedFile, nil, 0o644)
}

// alive uses a short timeout: a wedged agent keeps its port open but stops
// answering, and the client's 10 s default would stall every recovery step.
// It asks for the orientation: like every handler it runs on the runner's main
// actor, so it proves the agent answers, in ~25 ms; device.info queries
// SpringBoard (~120 ms) and every iph command pays the probe.
func alive(addr string) bool {
	_, err := devicekit.NewDeviceKitClient(addr).CallRPCWithTimeout("device.io.orientation.get", nil, probeTimeout)
	return err == nil
}

// cachedAgent answers from the address file. Until recovery is proven safe after
// ambiguous timeouts, always perform the full liveness probe. TCP-only checking
// cannot distinguish a wedged agent from a healthy one.
func cachedAgent() (string, bool) {
	b, err := os.ReadFile(ipFile)
	if err != nil {
		return "", false
	}
	addr := strings.TrimSpace(string(b))
	if addr == "" {
		return "", false
	}
	if !alive(addr) {
		return "", false
	}
	markProbed()
	return addr, true
}

// known lists the config's fixed routes to this device's agent, tried before a
// LAN scan: a VPN address, or a loopback forward where the binary may not open
// local-network sockets.
func known() []string {
	var out []string
	for _, h := range deviceEntry(deviceUUID).Addrs {
		if !strings.Contains(h, ":") {
			h = net.JoinHostPort(h, port)
		}
		out = append(out, h)
	}
	return out
}

// findAgent tries the cached address, the device's known routes, then scans the local /24 subnets for the port.
func findAgent() (string, bool) {
	if addr := os.Getenv("IPH_AGENT_ADDR"); addr != "" {
		return addr, alive(addr)
	}
	if addr, ok := cachedAgent(); ok {
		return addr, true
	}
	for _, addr := range known() {
		if alive(addr) {
			markProbed()
			_ = os.MkdirAll(devDir, 0o755)
			_ = os.WriteFile(ipFile, []byte(addr), 0o644)
			return addr, true
		}
	}
	return scanAgent()
}

// scanAgent finds a live agent on the LAN (the phone's IP may have changed) and
// caches its address.
func scanAgent() (string, bool) {
	for _, addr := range scan() {
		if alive(addr) {
			markProbed()
			_ = os.MkdirAll(devDir, 0o755)
			_ = os.WriteFile(ipFile, []byte(addr), 0o644)
			return addr, true
		}
	}
	return "", false
}

func scan() []string {
	var hits []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagPointToPoint != 0 {
			continue // skip VPN tunnels
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			base := ipn.IP.To4().Mask(net.CIDRMask(24, 32))
			for i := 1; i < 255; i++ {
				addr := net.JoinHostPort(fmt.Sprintf("%d.%d.%d.%d", base[0], base[1], base[2], i), port)
				wg.Add(1)
				go func() {
					defer wg.Done()
					if conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond); err == nil {
						conn.Close()
						mu.Lock()
						hits = append(hits, addr)
						mu.Unlock()
					}
				}()
			}
		}
	}
	wg.Wait()
	return hits
}

// up starts the agent through xcodebuild, detached, and waits for it to answer.
func up() (string, error) {
	if addr := os.Getenv("IPH_AGENT_ADDR"); addr != "" {
		if alive(addr) {
			return addr, nil
		}
		return "", fmt.Errorf("explicit agent %s is not answering; start the runner with iph remote run; no local fallback attempted", addr)
	}
	// 'up' promises a working agent, so it always proves one answers rather
	// than accepting the cheaper cached check for a merely-listening port.
	if b, err := os.ReadFile(ipFile); err == nil {
		if addr := strings.TrimSpace(string(b)); addr != "" && alive(addr) {
			markProbed()
			return addr, nil
		}
	}
	if addr, ok := findAgent(); ok {
		return addr, nil
	}
	if err := ensureSigned(); err != nil {
		return "", err
	}
	// A crashed agent can leave its xcodebuild alive holding the test session;
	// stop the one iph recorded so a restart never stacks a second runner.
	if pid := recordedPID(); pid > 0 {
		_ = down()
		until(10*time.Second, 250*time.Millisecond, func() bool { return syscall.Kill(pid, 0) != nil })
	}
	_ = os.MkdirAll(devDir, 0o755)
	log, err := os.Create(logFile)
	if err != nil {
		return "", err
	}
	cmd, err := runnerCmd("run-dd")
	if err != nil {
		return "", err
	}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // outlive this call; 'iph down' stops it
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	_ = cmd.Process.Release()

	var addr string
	var failed error
	until(60*time.Second, time.Second, func() bool {
		b, _ := os.ReadFile(logFile)
		txt := string(b)
		// xcodebuild waits silently for a locked phone; say so instead of timing out.
		if strings.Contains(txt, "** TEST EXECUTE FAILED **") || strings.Contains(txt, "because the device is locked") {
			failed = launchFailure(txt)
			return true
		}
		var ok bool
		addr, ok = "", false
		if strings.Contains(txt, "Server is ready") {
			addr, ok = findAgent()
		}
		return ok
	})
	if addr != "" {
		return addr, nil
	}
	_ = down() // xcodebuild has exited or is stuck; leave no pid file behind
	if failed != nil {
		return "", failed
	}
	return "", fmt.Errorf("agent did not come up in 60s; see %s", logFile)
}

// launchFailure turns xcodebuild's failure into the one action that fixes it.
func launchFailure(log string) error {
	switch {
	case strings.Contains(log, "because the device is locked"):
		return fmt.Errorf("the phone is locked: unlock it, then rerun")
	case strings.Contains(log, "Developer App Certificate is not trusted"),
		strings.Contains(log, "has not been explicitly trusted"):
		return fmt.Errorf("the phone does not trust this developer certificate yet: on the phone open " +
			"Settings > General > VPN & Device Management > Apple Development: <your Apple ID> > Trust, then rerun")
	case strings.Contains(log, "invalid code signature"), strings.Contains(log, "profile has expired"):
		return fmt.Errorf("agent signing is invalid on this phone; use ios-deploy to repair DeviceKit for IPH_DEVICE=%s", deviceUUID)
	}
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "Failure Reason:") {
			return fmt.Errorf("agent failed to start: %s (log: %s)", strings.TrimSpace(line), logFile)
		}
	}
	return fmt.Errorf("agent failed to start; see %s", logFile)
}

// ensureSigned checks only. Signing and installation belong to ios-deploy;
// a read/tap command must never silently replace the installed helper.
func ensureSigned() error {
	profile := filepath.Join(products, "Debug-iphoneos", "devicekit-iosUITests-Runner.app", "embedded.mobileprovision")
	if exp, err := profileExpiry(profile); err == nil && time.Now().Before(exp) {
		return nil
	}
	// The runner is an XCTest bundle: iOS runs it only with Apple Development
	// signing (get-task-allow). A purchased distribution profile installs it
	// but iOS refuses the test session (2026-10-04), so do not send agents there.
	return fmt.Errorf("DeviceKit runner profile is missing or expired; it needs Apple Development signing " +
		"(an Apple ID in Xcode > Settings > Accounts), not the distribution certificate; no installation attempted")
}

func profileExpiry(path string) (time.Time, error) {
	plist, err := exec.Command("security", "cms", "-D", "-i", path).Output()
	if err != nil {
		return time.Time{}, err
	}
	cmd := exec.Command("plutil", "-extract", "ExpirationDate", "raw", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(plist)
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, strings.TrimSpace(string(out)))
}

func recordedPID() int {
	b, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func down() error {
	if _, err := os.Stat(pidFile); err != nil {
		return fmt.Errorf("no agent started by iph")
	}
	pid := recordedPID()
	_ = os.Remove(pidFile)
	_ = os.Remove(ipFile)
	_ = os.Remove(probedFile)
	if pid <= 0 {
		return nil
	}
	// Only signal the PID iph recorded, and only if it is still that xcodebuild.
	out, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if !strings.Contains(string(out), "xcodebuild") {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// runnerCmd is the one xcodebuild invocation that starts this device's
// DeviceKit runner, for `iph up` and `iph keep` alike. A fixed DerivedData per
// device (ddName under devDir), emptied first: without one every launch left a
// new ~750 MB folder in ~/Library/Developer/Xcode/DerivedData (2026-10-09).
func runnerCmd(ddName string) (*exec.Cmd, error) {
	xctestrun, err := filepath.Glob(filepath.Join(products, "*.xctestrun"))
	if err != nil || len(xctestrun) == 0 {
		return nil, fmt.Errorf("agent not built; use ios-deploy DeviceKit build for IPH_DEVICE=%s", deviceUUID)
	}
	dd := filepath.Join(devDir, ddName)
	_ = os.RemoveAll(dd)
	cmd := exec.Command("xcodebuild", "test-without-building", "-xctestrun", xctestrun[0],
		"-destination", "platform=iOS,id="+runnerDest(), "-derivedDataPath", dd)
	cmd.Env = append(os.Environ(), "TEST_RUNNER_DEVICEKIT_LISTEN_HOST=0.0.0.0", "TEST_RUNNER_DEVICEKIT_LISTEN_PORT="+port)
	return cmd, nil
}

// runnerDest is the xcodebuild destination for this device: the hardware UDID
// where xcodebuild rejects the CoreDevice UUID, else the UUID.
func runnerDest() string {
	if d := deviceEntry(deviceUUID).UDID; d != "" {
		return d
	}
	return deviceUUID
}
