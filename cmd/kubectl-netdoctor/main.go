// Command kubectl-netdoctor is a kubectl plugin that diagnoses KubeVirt VM
// networking. Install it on PATH and run "kubectl netdoctor".
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/cli-runtime/pkg/genericiooptions"

	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/cli"
	"github.com/sudharsanomprakash/kubevirt-netdoctor/internal/kube"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	streams := genericiooptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	cmd := cli.NewRootCommand(streams, kube.NewClients)
	err := cmd.ExecuteContext(ctx)
	code := cli.ExitCode(err)
	switch code {
	case cli.ExitOK:
	case cli.ExitFindings:
		fmt.Fprintln(os.Stderr, err) // the report itself was written
	default:
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	return code
}
