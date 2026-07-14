// Tests for transport argv construction. These pin the exact command
// dotduffel runs, because a stray flag or a mis-quoted script is the
// difference between "shell with my aliases" and "ssh error spew".
package remote

import (
	"reflect"
	"strings"
	"testing"
)

const script = "set -u\necho payload\n"

func TestSSHInteractiveForcesTTY(t *testing.T) {
	argv, err := SSH("devbox", nil, script, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-t", "devbox", "sh -c 'set -u\necho payload\n'"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q", argv)
	}
}

func TestSSHCommandModeSkipsTTY(t *testing.T) {
	// Forcing a tty for a one-off command would mangle piped output
	// with CRLFs.
	argv, err := SSH("devbox", nil, script, false)
	if err != nil {
		t.Fatal(err)
	}
	if argv[1] == "-t" {
		t.Fatalf("command mode must not force a tty: %q", argv)
	}
}

func TestSSHPassesExtraArgsBeforeHost(t *testing.T) {
	argv, err := SSH("devbox", []string{"-p", "2222", "-J", "jump.example.test"}, script, true)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-p 2222 -J jump.example.test devbox") {
		t.Fatalf("ssh args misplaced: %q", argv)
	}
}

func TestSSHWrapsScriptForForeignLoginShells(t *testing.T) {
	// The remote side may run csh or fish as the login shell; wrapping
	// in `sh -c '…'` keeps the bootstrap on POSIX ground.
	argv, _ := SSH("devbox", nil, "echo 'quoted'", true)
	last := argv[len(argv)-1]
	if !strings.HasPrefix(last, "sh -c '") {
		t.Fatalf("remote command not wrapped: %q", last)
	}
	if !strings.Contains(last, `'\''quoted'\''`) {
		t.Fatalf("script quotes not escaped for the remote shell: %q", last)
	}
}

func TestSSHRejectsBadHosts(t *testing.T) {
	// Blank hosts are user error; a "host" like -oProxyCommand=… is
	// option injection and must never reach ssh's parser.
	for _, host := range []string{"", "  ", "-oProxyCommand=payload"} {
		if _, err := SSH(host, nil, script, true); err == nil {
			t.Errorf("host %q accepted", host)
		}
	}
}

func TestContainerInteractiveArgv(t *testing.T) {
	argv, err := Container("docker", "mybox", script, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docker", "exec", "-it", "mybox", "sh", "-c", script}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q", argv)
	}
}

func TestContainerCommandModeDropsTTY(t *testing.T) {
	argv, err := Container("podman", "mybox", script, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"podman", "exec", "-i", "mybox", "sh", "-c", script}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q", argv)
	}
}

func TestContainerRejectsDashPrefixedName(t *testing.T) {
	if _, err := Container("docker", "--privileged", script, true); err == nil {
		t.Fatal("dash-prefixed container name accepted")
	}
}

func TestExecAppendsToAnyTransport(t *testing.T) {
	base := []string{"kubectl", "exec", "-it", "mypod", "--"}
	argv, err := Exec(base, script)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, base...), "sh", "-c", script)
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q", argv)
	}
	// The caller's slice must not be mutated.
	if len(base) != 5 {
		t.Fatal("transport prefix mutated")
	}
}

func TestExecRejectsEmptyTransport(t *testing.T) {
	if _, err := Exec(nil, script); err == nil {
		t.Fatal("empty transport accepted")
	}
	if _, err := Exec([]string{" "}, script); err == nil {
		t.Fatal("blank transport accepted")
	}
}

func TestLocalRunsPlainSh(t *testing.T) {
	want := []string{"sh", "-c", script}
	if got := Local(script); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q", got)
	}
}
