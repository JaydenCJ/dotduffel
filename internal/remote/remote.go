// Package remote builds the argument vectors that carry a bootstrap
// script to its target: ssh, docker/podman exec, a generic transport,
// or the local shell.
//
// Everything here is a pure function from (target, script) to argv, so
// the exact command dotduffel is about to run can be asserted in tests
// and shown verbatim with --print — no surprises, no hidden flags.
package remote

import (
	"fmt"
	"strings"

	"github.com/JaydenCJ/dotduffel/internal/bootstrap"
)

// SSH builds the ssh invocation. The script travels as a single remote
// argument wrapped in `sh -c '…'`, so it runs identically whatever the
// remote user's login shell is. Interactive sessions force a tty.
func SSH(host string, sshArgs []string, script string, interactive bool) ([]string, error) {
	if err := checkTarget("host", host); err != nil {
		return nil, err
	}
	argv := []string{"ssh"}
	if interactive {
		argv = append(argv, "-t")
	}
	argv = append(argv, sshArgs...)
	argv = append(argv, host, "sh -c "+bootstrap.ShellQuote(script))
	return argv, nil
}

// Container builds a `docker exec` / `podman exec` invocation. Unlike
// ssh, exec passes argv through verbatim, so the script needs no extra
// quoting layer.
func Container(tool, container, script string, interactive bool) ([]string, error) {
	if err := checkTarget("container", container); err != nil {
		return nil, err
	}
	argv := []string{tool, "exec"}
	if interactive {
		argv = append(argv, "-it")
	} else {
		argv = append(argv, "-i")
	}
	argv = append(argv, container, "sh", "-c", script)
	return argv, nil
}

// Exec appends the script to an arbitrary transport prefix — anything
// that accepts a trailing `sh -c <script>`, e.g.
// `kubectl exec -it mypod --` or `lxc exec mybox --`.
func Exec(base []string, script string) ([]string, error) {
	if len(base) == 0 {
		return nil, fmt.Errorf("exec: transport command is required")
	}
	if strings.TrimSpace(base[0]) == "" {
		return nil, fmt.Errorf("exec: transport command is required")
	}
	argv := append([]string{}, base...)
	argv = append(argv, "sh", "-c", script)
	return argv, nil
}

// Local runs the bootstrap on this machine — the zero-risk way to
// test-drive a duffel before pointing it at a real host.
func Local(script string) []string {
	return []string{"sh", "-c", script}
}

// checkTarget refuses empty targets and anything that starts with "-",
// which would otherwise be parsed as an option by ssh/docker
// (argv-injection via a hostile hostname).
func checkTarget(kind, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if strings.HasPrefix(v, "-") {
		return fmt.Errorf("%s %q must not start with \"-\"", kind, v)
	}
	return nil
}
