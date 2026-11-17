package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/config"
	"github.com/horus14-wq/microagentIRC/internal/identity"
)

// subcommandUsage is the usage line of `mairc token`.
const subcommandUsage = "usage: mairc token rotate [--now] | add <machine> | list | revoke <machine>"

// previousTokenGrace is how long a rotated hub token keeps working for the hub's own agents.
const previousTokenGrace = 24 * time.Hour

// runToken implements `mairc token`: the hub's own token and the tokens of the other machines that join it.
//
//	rotate            replace the hub token; the running daemon and the agents on this machine pick it up at once
//	add <machine>     make a token for one other machine and print it once (the hub keeps only its hash)
//	list              the machines that have a token, and since when
//	revoke <machine>  that machine's token stops working at once
//
// Every change is put in force by asking the running daemon to reload; with no daemon running it takes effect when
// one starts.
func runToken(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	p, err := config.ResolvePaths(getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	machines := config.MachinesPath(p.Token)
	fail := func(err error) int {
		_, _ = fmt.Fprintln(stderr, "mairc token:", err)
		return 1
	}
	switch {
	case (len(args) == 1 || len(args) == 2 && args[1] == "--now") && args[0] == "rotate":
		atOnce := len(args) == 2
		old, _ := config.ReadToken(p.Token) // "" when there was none yet
		if err := config.EnsureHome(p); err != nil {
			return fail(err)
		}
		fresh, err := config.NewToken()
		if err != nil {
			return fail(err)
		}
		// Agents already running on this machine hold the old token in memory, and a shim started by an older
		// mairc cannot read the new one: unless told to cut it off now, the old token keeps working for this
		// machine's own agents (and for nothing else) for a day, so that nobody is locked out mid-task.
		here := identity.HostName(os.Hostname)
		now := time.Now()
		switch {
		case atOnce:
			if _, err := config.RevokeMachine(machines, here); err != nil {
				return fail(err)
			}
		case old != "":
			if err := config.KeepPrevious(machines, here, old, now, now.Add(previousTokenGrace)); err != nil {
				return fail(err)
			}
		}
		if err := config.WriteToken(p.Token, fresh); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintln(stdout, "new hub token written to", p.Token)
		if atOnce {
			_, _ = fmt.Fprintln(stdout, "the old token is refused from now on: agents on this machine that were started by an older mairc cannot send until their CLI is restarted")
		} else if old != "" {
			_, _ = fmt.Fprintf(stdout, "the old token keeps working until %s, for agents on this machine only (%s); `mairc token rotate --now` cuts it off at once\n",
				now.Add(previousTokenGrace).UTC().Format(time.RFC3339), here)
		}
		_, _ = fmt.Fprintln(stdout, "other machines that hold a copy of the old hub token must be given their own: mairc token add <machine>")
		return applied(p, old, stdout, stderr)
	case len(args) == 2 && args[0] == "add":
		if err := config.EnsureHome(p); err != nil {
			return fail(err)
		}
		token, err := config.AddMachine(machines, args[1], time.Now())
		if err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "token for machine %s (shown once; the hub keeps only its hash):\n%s\n", args[1], token)
		_, _ = fmt.Fprintf(stdout, "put it in the file `token` in mairc's data directory on %s; it works only for agents on that machine\n", args[1])
		return applied(p, "", stdout, stderr)
	case len(args) == 1 && args[0] == "list":
		listed, err := config.LoadMachines(machines)
		if err != nil {
			return fail(err)
		}
		if len(listed) == 0 {
			_, _ = fmt.Fprintln(stdout, "no machine tokens")
			return 0
		}
		names := make([]string, 0, len(listed))
		for name := range listed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if until := listed[name].Until; until != "" {
				_, _ = fmt.Fprintf(stdout, "%s  since %s  (the previous hub token, until %s)\n", name, listed[name].Created, until)
				continue
			}
			_, _ = fmt.Fprintf(stdout, "%s  since %s\n", name, listed[name].Created)
		}
		return 0
	case len(args) == 2 && args[0] == "revoke":
		had, err := config.RevokeMachine(machines, args[1])
		if err != nil {
			return fail(err)
		}
		if !had {
			return fail(errors.New("no token for machine " + args[1]))
		}
		_, _ = fmt.Fprintln(stdout, "revoked the token of machine", args[1])
		return applied(p, "", stdout, stderr)
	}
	_, _ = fmt.Fprintln(stderr, subcommandUsage)
	return 2
}

// applied asks the running daemon to read the tokens again and says whether the change is in force. present is the
// token to ask with: the old hub token after a rotation (the daemon does not know the new one yet), or "" for the
// current one.
func applied(p config.Paths, present string, stdout, stderr io.Writer) int {
	var err error
	if present != "" {
		err = reloadWith(p, present)
	} else {
		err = reload(p)
	}
	switch {
	case err == nil:
		_, _ = fmt.Fprintln(stdout, "in force: the running daemon has reloaded its tokens")
		return 0
	case errors.Is(err, errNotRunning):
		_, _ = fmt.Fprintln(stdout, "no daemon is running: the change takes effect when one starts")
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "mairc token: written, but the daemon did not reload:", err)
	return 1
}
