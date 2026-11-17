package daemon

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Prime Agent has no hooks (docs/compat.md), so the daemon delivers to it from outside (mairc-byd): a shim that runs
// under Prime Agent reports its agent's id as its CLI session, "prime-agent:<id>" (identity.PrimeAgentSession), and
// every bridgeEvery the daemon takes what is due for each named such session, exactly as a hook event would, and
// hands it to the agent with `prime-agent send --follow-up <id> <lines>`, which queues it after the current turn.
const (
	primeAgentPrefix = "prime-agent:"
	bridgeEvery      = 5 * time.Second
	bridgeSendLimit  = 10 * time.Second
)

// sendFunc hands text to one Prime Agent agent.
type sendFunc func(ctx context.Context, agent, text string) error

// primeAgentSender sends with the prime-agent binary at bin.
func primeAgentSender(bin string) sendFunc {
	return func(ctx context.Context, agent, text string) error {
		ctx, cancel := context.WithTimeout(ctx, bridgeSendLimit)
		defer cancel()
		// #nosec G204 -- bin is the prime-agent found on PATH at start; agent is an id the shim reported
		return exec.CommandContext(ctx, bin, "send", "--follow-up", "--", agent, text).Run()
	}
}

// bridgeTargets returns, by name, the Prime Agent agent id of each live named session a shim reported from under
// Prime Agent.
func (s *server) bridgeTargets() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	targets := make(map[string]string)
	for _, sess := range s.sessions {
		agent, ok := strings.CutPrefix(sess.hookSession, primeAgentPrefix)
		if ok && agent != "" && sess.Name != "" {
			targets[sess.Name] = agent
		}
	}
	return targets
}

// bridgeOnce delivers what is due to each Prime Agent agent: one counted event per agent, as for a hook. Lines taken
// are delivered at most once: a failed send is logged, not retried.
func (s *server) bridgeOnce(ctx context.Context, send sendFunc) {
	targets := s.bridgeTargets()
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		msgs, err := s.chat.HookEvent(ctx, name)
		if err != nil {
			s.logf("error: bridge %s: %v", name, err)
			continue
		}
		if len(msgs) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString("irc:")
		for _, m := range msgs {
			b.WriteByte('\n')
			b.WriteString(m.Line())
		}
		if err := send(ctx, targets[name], b.String()); err != nil {
			s.logf("error: bridge %s: prime-agent send: %v", name, err)
		}
	}
}

// runBridge delivers to Prime Agent agents every bridgeEvery until ctx ends.
func (s *server) runBridge(ctx context.Context, send sendFunc) {
	ticker := time.NewTicker(bridgeEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.bridgeOnce(ctx, send)
		}
	}
}
