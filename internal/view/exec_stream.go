// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	dockerterm "github.com/moby/term"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/cmd/util/podcmd"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/kubectl/pkg/util/interrupt"
	"k8s.io/kubectl/pkg/util/term"
	"k8s.io/streaming/pkg/httpstream"
)

// remoteSession streams an interactive exec/attach session in-process over
// client-go remotecommand, mirroring `kubectl exec|attach -it` behavior.
type remoteSession struct {
	a          *App
	cfg        *rest.Config
	ns, po, co string
	attach     bool
	cmd        []string
	tty        bool
}

func newRemoteSession(a *App, po *v1.Pod, co string, attach bool, cmd []string) (*remoteSession, error) {
	cfg, err := a.Conn().RestConfig()
	if err != nil {
		return nil, err
	}
	// A request timeout would cut long-lived shell streams.
	cfg.Timeout = 0

	s := remoteSession{
		a: a, cfg: cfg,
		ns: po.Namespace, po: po.Name, co: co,
		attach: attach, cmd: cmd, tty: true,
	}
	if attach {
		s.matchContainerTTY(po)
	}

	return &s, nil
}

// matchContainerTTY downgrades the session tty when the container was started
// without one, like kubectl attach does.
func (s *remoteSession) matchContainerTTY(po *v1.Pod) {
	co, _ := podcmd.FindContainerByName(po, s.co)
	if co == nil || co.TTY {
		return
	}

	s.tty = false
}

// runRemote suspends the k9s UI, hands the terminal to the remote session
// and blocks until the session ends.
func runRemote(a *App, s *remoteSession, banner string) error {
	errCh := make(chan error, 1)
	a.Halt()
	defer a.Resume()

	if !a.Suspend(func() {
		clearScreen()
		fmt.Fprint(os.Stdout, banner)
		err := s.run()
		clearScreen()
		errCh <- err
	}) {
		return errors.New("unable to run command")
	}

	return <-errCh
}

func (s *remoteSession) run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// TTY.Safe chains its own interrupt handler: on a signal it restores the
	// terminal but the chained parent's final never runs (Chain closes the
	// parent before signaling it), and a nil parent makes Safe os.Exit(1).
	// So the parent is only an exit guard; cancel via a private channel.
	interrupted := false
	parent := interrupt.New(func(os.Signal) {})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		select {
		case <-sigChan:
			interrupted = true
			cancel()
		case <-ctx.Done():
		}
	}()

	t := term.TTY{In: os.Stdin, Out: os.Stdout, Parent: parent}
	raw := s.tty && t.IsTerminalIn()
	if s.tty && !raw {
		fmt.Fprintln(os.Stderr, "Unable to use a TTY - input is not a terminal or the right kind of file")
	}
	if raw {
		// dockerterm returns plain std streams on unix and fixes windows consoles.
		t.In, t.Out, _ = dockerterm.StdStreams()
	}
	t.Raw = raw

	err := s.stream(ctx, &t, raw)
	if interrupted {
		return nil
	}

	return err
}

func (s *remoteSession) stream(ctx context.Context, t *term.TTY, raw bool) error {
	s.printHints()

	var sizeQueue remotecommand.TerminalSizeQueue
	if raw {
		sizeQueue = &resizeQueue{delegate: t.MonitorSize(s.initialSizes(t)...)}
	}

	exec, err := s.executor(raw)
	if err != nil {
		return err
	}

	stdin := t.In
	if s.attach && raw {
		if stdin, err = term.NewDetachableReader(stdin, "ctrl-p,ctrl-q"); err != nil {
			return err
		}
	}
	var stderr io.Writer
	if !raw {
		stderr = os.Stderr
	}

	err = t.Safe(func() error {
		return exec.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             stdin,
			Stdout:            t.Out,
			Stderr:            stderr,
			Tty:               raw,
			TerminalSizeQueue: sizeQueue,
		})
	})
	if s.attach && raw && err == nil {
		fmt.Fprintf(t.Out, "Session ended, resume using 'kubectl attach %s -c %s -n %s -i -t' command\n", s.po, s.co, s.ns)
	}

	return err
}

// printHints mirrors the messages kubectl attach prints around a session.
func (s *remoteSession) printHints() {
	if !s.attach {
		return
	}
	if !s.tty {
		fmt.Fprintf(os.Stderr, "error: Unable to use a TTY - container %s did not allocate one\n", s.co)
	}

	hint := "All commands and output from this session will be recorded in container logs, " +
		"including credentials and sensitive information passed through the command prompt."
	fmt.Fprintln(os.Stderr, hint)
	fmt.Fprintln(os.Stderr, "If you don't see a command prompt, try pressing enter.")
}

// initialSizes seeds the resize queue; attach fakes a resize to force a
// remote redraw, like kubectl.
func (s *remoteSession) initialSizes(t *term.TTY) []*term.TerminalSize {
	size := t.GetSize()
	if size == nil || !s.attach {
		return []*term.TerminalSize{size}
	}

	plusOne := *size
	plusOne.Width++
	plusOne.Height++

	return []*term.TerminalSize{&plusOne, size}
}

func (s *remoteSession) executor(raw bool) (remotecommand.Executor, error) {
	rc, err := s.restClient()
	if err != nil {
		return nil, err
	}

	req := rc.Post().
		Resource("pods").
		Name(s.po).
		Namespace(s.ns).
		SubResource(s.subResource()).
		VersionedParams(s.params(raw), scheme.ParameterCodec)
	u := req.URL()

	spdyExec, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", u)
	if err != nil {
		return nil, err
	}
	if cmdutil.RemoteCommandWebsockets.IsDisabled() {
		return spdyExec, nil
	}

	wsExec, err := remotecommand.NewWebSocketExecutor(s.cfg, "GET", u.String())
	if err != nil {
		return nil, err
	}

	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, isUpgradeErr)
}

func isUpgradeErr(err error) bool {
	return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
}

func (s *remoteSession) subResource() string {
	if s.attach {
		return "attach"
	}

	return "exec"
}

func (s *remoteSession) params(raw bool) runtime.Object {
	if s.attach {
		return &v1.PodAttachOptions{
			Container: s.co,
			Stdin:     true,
			Stdout:    true,
			Stderr:    !raw,
			TTY:       raw,
		}
	}

	return &v1.PodExecOptions{
		Container: s.co,
		Command:   s.cmd,
		Stdin:     true,
		Stdout:    true,
		Stderr:    !raw,
		TTY:       raw,
	}
}

// restClient builds a rest client solely to shape the exec/attach request URL
// like kubectl; the streams travel on their own connection.
func (s *remoteSession) restClient() (*rest.RESTClient, error) {
	cfg := s.cfg
	cfg.GroupVersion = &v1.SchemeGroupVersion
	cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	cfg.APIPath = "/api"

	return rest.RESTClientFor(cfg)
}

// resizeQueue adapts kubectl's terminal size queue to the remotecommand one.
type resizeQueue struct {
	delegate term.TerminalSizeQueue
}

func (q *resizeQueue) Next() *remotecommand.TerminalSize {
	next := q.delegate.Next()
	if next == nil {
		return nil
	}

	return &remotecommand.TerminalSize{Width: next.Width, Height: next.Height}
}
